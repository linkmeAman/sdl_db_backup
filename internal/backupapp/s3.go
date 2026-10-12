package backupapp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const logicalCompletionObject = "COMPLETED.json"

const (
	// AWS requires every multipart part except the last to be at least 5 MiB.
	// 16 MiB strikes a useful balance between request count and retry cost.
	s3MultipartThreshold = int64(64 * 1024 * 1024)
	s3MultipartPartSize  = int64(16 * 1024 * 1024)
	s3MultipartMaxParts  = int64(10000)
	s3RequestAttempts    = 4
)

var s3EndpointForConfig = func(cfg config) string {
	if cfg.S3Endpoint != "" {
		return strings.TrimRight(cfg.S3Endpoint, "/") + "/" + url.PathEscape(cfg.S3Bucket)
	}
	return "https://" + cfg.S3Bucket + ".s3." + cfg.S3Region + ".amazonaws.com"
}

var s3RequestRetryDelay = func(attempt int) time.Duration {
	base := time.Duration(1<<(attempt-1)) * 250 * time.Millisecond
	return base/2 + time.Duration(rand.Int64N(int64(base)))
}

type remoteArtifact struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ETag      string `json:"etag,omitempty"`
}

type logicalCompletionManifest struct {
	Status            string           `json:"status"`
	ConsistencyMethod string           `json:"consistency_method,omitempty"`
	ExpectedDatabases []string         `json:"expected_databases,omitempty"`
	FormatVersion     int              `json:"format_version"`
	CompletedAt       time.Time        `json:"completed_at"`
	RunID             string           `json:"run_id"`
	Bucket            string           `json:"bucket"`
	Prefix            string           `json:"prefix"`
	Artifacts         []remoteArtifact `json:"artifacts"`
}

func logS3Result(result map[string]interface{}) {
	errFlag, _ := result["error"].(float64)
	status, _ := result["status"].(string)
	succeeded, _ := result["succeeded"].(float64)
	failedCount, _ := result["failed_count"].(float64)
	s3Prefix, _ := result["s3_prefix"].(string)
	if errFlag != 0 {
		log.Printf("s3 upload completed with errors: status=%s succeeded=%d failed=%d prefix=%s", status, int(succeeded), int(failedCount), s3Prefix)
	} else {
		log.Printf("s3 upload successful: status=%s succeeded=%d prefix=%s", status, int(succeeded), s3Prefix)
	}
}

func uploadBackupToS3(cfg config, runFolder string) error {
	log.Printf("s3 upload: starting for folder %s", runFolder)
	if !verifiedUploadMode(cfg) {
		if cfg.RequireRemoteCompletion {
			return fmt.Errorf("remote completion requires the verified direct upload path; legacy helpers cannot prove full completion")
		}
		capture, e := readManifest(runFolder)
		if e != nil {
			return e
		}
		if e = verifyLogicalCapture(cfg, capture, runFolder); e != nil {
			return fmt.Errorf("partial captures must not be sent to unverifiable legacy helpers: %w", e)
		}
	}
	switch cfg.S3UploadMode {
	case "", "direct":
		if err := uploadBackupDirectToS3(cfg, runFolder); err != nil {
			log.Printf("s3 upload direct error: %v", err)
			return err
		}
		return nil
	case "php", "cli":
		if cfg.S3UploadScript == "" {
			err := fmt.Errorf("s3 upload skipped: BACKUP_S3_UPLOAD_MODE=%s requires BACKUP_S3_UPLOAD_SCRIPT", cfg.S3UploadMode)
			log.Printf("%v", err)
			return err
		}
		return uploadBackupViaCLI(cfg, runFolder)
	case "http":
		if cfg.S3UploadURL == "" {
			err := fmt.Errorf("s3 upload skipped: BACKUP_S3_UPLOAD_MODE=http requires BACKUP_S3_UPLOAD_URL")
			log.Printf("%v", err)
			return err
		}
		return uploadBackupViaHTTP(cfg, runFolder)
	case "auto":
		if cfg.S3KeyID != "" && cfg.S3KeySecret != "" && cfg.S3Bucket != "" {
			if err := uploadBackupDirectToS3(cfg, runFolder); err != nil {
				log.Printf("s3 upload direct error: %v", err)
				return err
			}
			return nil
		}
		if cfg.S3UploadScript != "" {
			return uploadBackupViaCLI(cfg, runFolder)
		}
		if cfg.S3UploadURL != "" {
			return uploadBackupViaHTTP(cfg, runFolder)
		}
	default:
		err := fmt.Errorf("s3 upload skipped: unsupported BACKUP_S3_UPLOAD_MODE=%q", cfg.S3UploadMode)
		log.Printf("%v", err)
		return err
	}
	err := errors.New("s3 upload skipped: no direct credentials, PHP script, or HTTP endpoint configured")
	log.Printf("%v", err)
	return err
}

func uploadBackupDirectToS3(cfg config, runFolder string) error {
	if cfg.S3Bucket == "" {
		return errors.New("BACKUP_S3_BUCKET is required")
	}
	if cfg.S3Region == "" {
		return errors.New("BACKUP_S3_REGION is required")
	}
	if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		return errors.New("S3 credentials are required; set BACKUP_S3_KEY_ID and BACKUP_S3_KEY_SECRET, or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
	}

	runID := filepath.Base(runFolder)
	prefix := strings.Trim(strings.TrimSpace(cfg.S3LogicalPrefix), "/")
	baseKey := runID
	if prefix != "" {
		baseKey = prefix + "/" + runID
	}
	log.Printf("s3 upload: using direct S3 upload to s3://%s/%s", cfg.S3Bucket, baseKey)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.S3UploadTimeout)
	defer cancel()
	if _, e := headS3Object(ctx, cfg, baseKey+"/"+logicalCompletionObject); e == nil {
		return fmt.Errorf("refusing to overwrite finalized recovery point %s", runID)
	} else {
		var statusErr *s3HTTPStatusError
		if !errors.As(e, &statusErr) || statusErr.status != 404 {
			return fmt.Errorf("check immutable recovery point: %w", e)
		}
	}

	type localArtifact struct {
		path string
		rel  string
		size int64
	}
	localArtifacts := []localArtifact{}
	capture, manifestErr := readManifest(runFolder)
	if manifestErr != nil {
		return fmt.Errorf("required capture manifest: %w", manifestErr)
	}
	captureErr := verifyLogicalCapture(cfg, capture, runFolder)
	allowed := map[string]bool{"manifest.json": true}
	for _, d := range capture.Results {
		if d.Status == "success" {
			allowed[filepath.Base(d.OutputPath)] = true
		}
	}
	err := filepath.WalkDir(runFolder, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(runFolder, path)
		if err != nil {
			return err
		}
		if !allowed[filepath.ToSlash(rel)] {
			return nil
		}
		localArtifacts = append(localArtifacts, localArtifact{path: path, rel: filepath.ToSlash(rel), size: info.Size()})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(localArtifacts, func(i, j int) bool { return localArtifacts[i].rel < localArtifacts[j].rel })
	if len(localArtifacts) == 0 {
		return errors.New("logical backup upload has no artifacts to commit")
	}

	artifacts := make([]remoteArtifact, 0, len(localArtifacts))
	var uploadedBytes int64
	for _, artifact := range localArtifacts {
		key := baseKey + "/" + artifact.rel
		remote, err := uploadFileDirectToS3(ctx, cfg, artifact.path, key, artifact.size)
		if err != nil {
			return err
		}
		if err := verifyRemotePayload(ctx, cfg, key, artifact.size, remote.SHA256); err != nil {
			return err
		}
		artifacts = append(artifacts, remoteArtifact{Key: artifact.rel, SizeBytes: artifact.size, SHA256: remote.SHA256, ETag: remote.ETag})
		uploadedBytes += artifact.size
		log.Printf("s3 upload: uploaded and verified %s to s3://%s/%s", artifact.rel, cfg.S3Bucket, key)
	}

	// Publish the manifest with verified remote-transfer evidence before the
	// final marker. No marker exists until this final inventory is verified.
	markLogicalUploadSuccess(&capture)
	capture.Duration = time.Since(capture.Timestamp).Round(time.Millisecond).String()
	setManifestMetadata(cfg, &capture)
	if err := writeManifest(runFolder, capture); err != nil {
		return err
	}
	manifestPath := filepath.Join(runFolder, "manifest.json")
	stat, err := os.Stat(manifestPath)
	if err != nil {
		return err
	}
	manifestRemote, err := uploadFileDirectToS3(ctx, cfg, manifestPath, baseKey+"/manifest.json", stat.Size())
	if err != nil {
		return err
	}
	if err := verifyRemotePayload(ctx, cfg, baseKey+"/manifest.json", stat.Size(), manifestRemote.SHA256); err != nil {
		return err
	}
	for i := range artifacts {
		if artifacts[i].Key == "manifest.json" {
			artifacts[i] = remoteArtifact{Key: "manifest.json", SizeBytes: stat.Size(), SHA256: manifestRemote.SHA256, ETag: manifestRemote.ETag}
		}
	}
	inventory, err := listS3Prefix(ctx, cfg, baseKey)
	if err != nil {
		return err
	}
	actual := map[string]int64{}
	for _, o := range inventory.Objects {
		actual[strings.TrimPrefix(o.Key, baseKey+"/")] = o.SizeBytes
	}
	for _, a := range artifacts {
		if actual[a.Key] != a.SizeBytes {
			return fmt.Errorf("remote inventory missing or mismatched artifact %s", a.Key)
		}
	}
	marker := logicalCompletionObject
	status := "COMPLETE"
	if captureErr != nil {
		marker = "PARTIAL.json"
		status = "PARTIAL"
		if capture.DatabasesSucceeded == 0 {
			marker = "FAILED.json"
			status = "FAILED"
		}
		log.Printf("logical completion withheld: %v", captureErr)
	}
	completion := logicalCompletionManifest{
		Status: status, ConsistencyMethod: capture.ConsistencyMethod, ExpectedDatabases: capture.ExpectedDatabases,
		FormatVersion: 2,
		CompletedAt:   time.Now().UTC(),
		RunID:         runID,
		Bucket:        cfg.S3Bucket,
		Prefix:        baseKey,
		Artifacts:     artifacts,
	}
	completionBody, err := json.MarshalIndent(completion, "", "  ")
	if err != nil {
		return fmt.Errorf("encode logical completion manifest: %w", err)
	}
	completionBody = append(completionBody, '\n')
	completionKey := baseKey + "/" + marker
	completionHash := sha256.Sum256(completionBody)
	if _, err := putS3Object(ctx, cfg, completionKey, bytes.NewReader(completionBody), int64(len(completionBody)), "application/json", hex.EncodeToString(completionHash[:])); err != nil {
		return fmt.Errorf("write logical completion manifest: %w", err)
	}
	completionRemote, err := headS3Object(ctx, cfg, completionKey)
	if err != nil {
		return fmt.Errorf("verify logical completion manifest: %w", err)
	}
	if completionRemote.SizeBytes != int64(len(completionBody)) {
		return fmt.Errorf("verify logical completion manifest: expected %d bytes, got %d", len(completionBody), completionRemote.SizeBytes)
	}
	if err := persistManifestCommit(runFolder, capture.RunID, manifestRemote.SHA256); err != nil {
		return fmt.Errorf("persist manifest commit evidence: %w", err)
	}
	log.Printf("s3 upload committed: artifacts=%d bytes=%d status=%s completion=s3://%s/%s", len(artifacts), uploadedBytes, status, cfg.S3Bucket, completionKey)
	return nil
}

type s3ObjectInfo struct {
	SizeBytes int64
	SHA256    string
	ETag      string
}

func uploadFileDirectToS3(ctx context.Context, cfg config, filePath, objectKey string, size int64) (s3ObjectInfo, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return s3ObjectInfo{}, err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return s3ObjectInfo{}, err
	}
	payloadHash := hex.EncodeToString(hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return s3ObjectInfo{}, err
	}
	if size >= s3MultipartThreshold {
		if err := uploadFileMultipartToS3(ctx, cfg, file, objectKey, size, detectContentType(filePath)); err != nil {
			return s3ObjectInfo{}, err
		}
	} else if _, err := putS3Object(ctx, cfg, objectKey, file, size, detectContentType(filePath), payloadHash); err != nil {
		return s3ObjectInfo{}, err
	}
	remote, err := headS3Object(ctx, cfg, objectKey)
	if err != nil {
		return s3ObjectInfo{}, err
	}
	if remote.SizeBytes != size {
		return s3ObjectInfo{}, fmt.Errorf("verify upload %s: expected %d bytes, got %d", objectKey, size, remote.SizeBytes)
	}
	remote.SHA256 = payloadHash
	return remote, nil
}

func putS3Object(ctx context.Context, cfg config, objectKey string, body io.ReadSeeker, size int64, contentType string, payloadHash string) (s3ObjectInfo, error) {
	return putS3ObjectWithQuery(ctx, cfg, objectKey, nil, body, size, contentType, payloadHash)
}

func putS3ObjectWithQuery(ctx context.Context, cfg config, objectKey string, query url.Values, body io.ReadSeeker, size int64, contentType string, payloadHash string) (s3ObjectInfo, error) {
	var etag string
	err := withS3Retry(ctx, func() error {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind upload body: %w", err)
		}
		resp, err := doS3Request(ctx, cfg, http.MethodPut, objectKey, query, body, size, contentType, payloadHash)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := checkS3Success(resp, "upload "+objectKey); err != nil {
			return err
		}
		etag = strings.Trim(resp.Header.Get("ETag"), "\"")
		return nil
	})
	if err != nil {
		return s3ObjectInfo{}, err
	}
	return s3ObjectInfo{SizeBytes: size, ETag: etag}, nil
}

func headS3Object(ctx context.Context, cfg config, objectKey string) (s3ObjectInfo, error) {
	emptyHash := sha256.Sum256(nil)
	payloadHash := hex.EncodeToString(emptyHash[:])
	var remote s3ObjectInfo
	err := withS3Retry(ctx, func() error {
		resp, err := doS3Request(ctx, cfg, http.MethodHead, objectKey, nil, nil, 0, "application/octet-stream", payloadHash)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := checkS3Success(resp, "head "+objectKey); err != nil {
			return err
		}
		remote = s3ObjectInfo{SizeBytes: resp.ContentLength, ETag: strings.Trim(resp.Header.Get("ETag"), "\"")}
		return nil
	})
	return remote, err
}

// s3PrefixInfo summarises objects found under a given S3 key prefix.
type s3PrefixInfo struct {
	ObjectCount int64
	TotalBytes  int64
	Objects     []remoteArtifact
}

// listS3Prefix lists objects whose key begins with prefix (plus a trailing "/").
// It performs a single ListObjectsV2 request with max-keys=1000 and returns
// aggregate count and total byte size. A non-zero ObjectCount confirms the
// prefix has at least one uploaded object.
func listS3Prefix(ctx context.Context, cfg config, prefix string) (s3PrefixInfo, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var info s3PrefixInfo
	token := ""
	seenTokens := map[string]bool{}
	seen := map[string]bool{}
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		var page struct {
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
			Contents  []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
		}
		empty := sha256.Sum256(nil)
		err := withS3Retry(ctx, func() error {
			resp, e := doS3Request(ctx, cfg, http.MethodGet, "", query, nil, 0, "application/xml", hex.EncodeToString(empty[:]))
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if e = checkS3Success(resp, "list prefix "+prefix); e != nil {
				return e
			}
			page.Contents = nil
			page.Truncated = false
			page.Next = ""
			return xml.NewDecoder(resp.Body).Decode(&page)
		})
		if err != nil {
			return info, err
		}
		for _, obj := range page.Contents {
			if !strings.HasPrefix(obj.Key, prefix) || obj.Size < 0 || seen[obj.Key] {
				return info, fmt.Errorf("invalid or duplicate S3 inventory object")
			}
			seen[obj.Key] = true
			info.ObjectCount++
			info.TotalBytes += obj.Size
			info.Objects = append(info.Objects, remoteArtifact{Key: obj.Key, SizeBytes: obj.Size})
		}
		if !page.Truncated {
			return info, nil
		}
		if page.Next == "" || page.Next == token || seenTokens[page.Next] {
			return info, fmt.Errorf("truncated S3 inventory omitted a progressing continuation token")
		}
		seenTokens[page.Next] = true
		token = page.Next
	}
}

// Independent streamed GET comparison verifies the actual payload, including
// multipart objects. ETags are opaque identifiers, never full-file MD5 hashes.
func verifyRemotePayload(ctx context.Context, cfg config, key string, size int64, expected string) error {
	empty := sha256.Sum256(nil)
	return withS3Retry(ctx, func() error {
		resp, e := doS3Request(ctx, cfg, http.MethodGet, key, nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if e = checkS3Success(resp, "verify payload "+key); e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, resp.Body)
		if e != nil {
			return e
		}
		if n != size || hex.EncodeToString(h.Sum(nil)) != expected {
			return fmt.Errorf("S3 payload size/checksum mismatch for %s", key)
		}
		return nil
	})
}

type s3HTTPStatusError struct {
	operation string
	status    int
	body      string
}

func (e *s3HTTPStatusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("%s failed: status=%d", e.operation, e.status)
	}
	var detail struct {
		Code      string `xml:"Code"`
		RequestID string `xml:"RequestId"`
	}
	_ = xml.Unmarshal([]byte(e.body), &detail)
	return fmt.Sprintf("%s failed: status=%d code=%s request_id=%s", e.operation, e.status, detail.Code, detail.RequestID)
}

func checkS3Success(resp *http.Response, operation string) error {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &s3HTTPStatusError{operation: operation, status: resp.StatusCode, body: strings.TrimSpace(string(body))}
}

func withS3Retry(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 1; attempt <= s3RequestAttempts; attempt++ {
		if err = operation(); err == nil {
			return nil
		}
		if attempt == s3RequestAttempts || !isRetriableS3Error(err) {
			return err
		}
		delay := s3RequestRetryDelay(attempt)
		log.Printf("s3 request failed (attempt %d/%d): %v; retrying in %s", attempt, s3RequestAttempts, err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func isRetriableS3Error(err error) bool {
	var statusErr *s3HTTPStatusError
	if errors.As(err, &statusErr) {
		var response struct {
			Code string `xml:"Code"`
		}
		_ = xml.Unmarshal([]byte(statusErr.body), &response)
		return statusErr.status == 408 || statusErr.status == http.StatusTooManyRequests || statusErr.status >= 500 || response.Code == "RequestTimeout" || response.Code == "SlowDown" || response.Code == "InternalError"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) || errors.Is(err, io.ErrUnexpectedEOF)
}

func doS3Request(ctx context.Context, cfg config, method, objectKey string, query url.Values, body io.Reader, size int64, contentType, payloadHash string) (*http.Response, error) {
	if cfg.S3Endpoint != "" {
		u, err := url.Parse(cfg.S3Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.S3Endpoint, "#") {
			return nil, errors.New("BACKUP_S3_ENDPOINT must be an HTTP(S) URL without credentials, query, or fragment")
		}
	}
	endpoint := strings.TrimRight(s3EndpointForConfig(cfg), "/") + "/" + s3EscapeObjectKey(objectKey)
	if query != nil {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signS3Request(req, cfg, payloadHash)
	return (&http.Client{}).Do(req)
}

type multipartInitiateResult struct {
	UploadID string `xml:"UploadId"`
}

type multipartCompletedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type multipartCompleteRequest struct {
	XMLName xml.Name                 `xml:"CompleteMultipartUpload"`
	Parts   []multipartCompletedPart `xml:"Part"`
}

func uploadFileMultipartToS3(ctx context.Context, cfg config, file *os.File, objectKey string, size int64, contentType string) (resultErr error) {
	journalPath := file.Name() + ".multipart.json"
	if err := recoverMultipartJournal(ctx, cfg, journalPath, objectKey); err != nil {
		return err
	}
	uploadID, err := initiateS3MultipartUpload(ctx, cfg, objectKey, contentType)
	if err != nil {
		return fmt.Errorf("initiate multipart upload %s: %w", objectKey, err)
	}
	if err := saveMultipartJournal(journalPath, cfg, objectKey, uploadID); err != nil {
		abortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = abortS3MultipartUpload(abortCtx, cfg, objectKey, uploadID)
		return err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		abortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := abortS3MultipartUpload(abortCtx, cfg, objectKey, uploadID); err != nil {
			log.Printf("s3 upload: could not abort incomplete multipart upload %s: %v; recovery journal preserved", objectKey, err)
		} else {
			_ = os.Remove(journalPath)
		}
	}()

	partSize := multipartPartSize(size)
	partCount := (size + partSize - 1) / partSize
	if partCount > s3MultipartMaxParts {
		return fmt.Errorf("multipart upload %s requires %d parts, exceeding S3 limit of %d", objectKey, partCount, s3MultipartMaxParts)
	}
	parts := make([]multipartCompletedPart, 0, partCount)
	for partNumber, offset := int64(1), int64(0); offset < size; partNumber, offset = partNumber+1, offset+partSize {
		partLength := minInt64(partSize, size-offset)
		partHash, err := hashFilePart(file, offset, partLength)
		if err != nil {
			return fmt.Errorf("hash multipart upload %s part %d: %w", objectKey, partNumber, err)
		}
		remote, err := uploadS3MultipartPart(ctx, cfg, objectKey, url.Values{
			"partNumber": []string{fmt.Sprintf("%d", partNumber)},
			"uploadId":   []string{uploadID},
		}, file, offset, partLength, contentType, partHash)
		if err != nil {
			return fmt.Errorf("upload multipart %s part %d: %w", objectKey, partNumber, err)
		}
		if remote.ETag == "" {
			return fmt.Errorf("upload multipart %s part %d: S3 response omitted ETag", objectKey, partNumber)
		}
		parts = append(parts, multipartCompletedPart{PartNumber: int(partNumber), ETag: `"` + remote.ETag + `"`})
	}
	if err := completeS3MultipartUpload(ctx, cfg, objectKey, uploadID, parts); err != nil {
		return fmt.Errorf("complete multipart upload %s: %w", objectKey, err)
	}
	completed = true
	return os.Remove(journalPath)
}

func multipartPartSize(size int64) int64 {
	partSize := s3MultipartPartSize
	minimumForLimit := (size + s3MultipartMaxParts - 1) / s3MultipartMaxParts
	if minimumForLimit > partSize {
		partSize = minimumForLimit
	}
	return partSize
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func hashFilePart(file *os.File, offset, length int64) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, offset, length)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// uploadS3MultipartPart creates a new reader on every attempt. The transport
// may consume part of a body before reporting an error, so retries must not
// reuse a reader owned by the failed request.
func uploadS3MultipartPart(ctx context.Context, cfg config, objectKey string, query url.Values, file *os.File, offset, size int64, contentType, payloadHash string) (s3ObjectInfo, error) {
	var etag string
	err := withS3Retry(ctx, func() error {
		body := io.NewSectionReader(file, offset, size)
		resp, err := doS3Request(ctx, cfg, http.MethodPut, objectKey, query, body, size, contentType, payloadHash)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := checkS3Success(resp, "upload "+objectKey); err != nil {
			return err
		}
		etag = strings.Trim(resp.Header.Get("ETag"), "\"")
		return nil
	})
	if err != nil {
		return s3ObjectInfo{}, err
	}
	return s3ObjectInfo{SizeBytes: size, ETag: etag}, nil
}

func initiateS3MultipartUpload(ctx context.Context, cfg config, objectKey, contentType string) (string, error) {
	emptyHash := sha256.Sum256(nil)
	payloadHash := hex.EncodeToString(emptyHash[:])
	var uploadID string
	err := withS3Retry(ctx, func() error {
		resp, err := doS3Request(ctx, cfg, http.MethodPost, objectKey, url.Values{"uploads": []string{""}}, nil, 0, contentType, payloadHash)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := checkS3Success(resp, "initiate multipart upload "+objectKey); err != nil {
			return err
		}
		var result multipartInitiateResult
		if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("parse initiation response: %w", err)
		}
		if strings.TrimSpace(result.UploadID) == "" {
			return errors.New("initiation response omitted UploadId")
		}
		uploadID = result.UploadID
		return nil
	})
	return uploadID, err
}

func completeS3MultipartUpload(ctx context.Context, cfg config, objectKey, uploadID string, parts []multipartCompletedPart) error {
	data, err := xml.Marshal(multipartCompleteRequest{Parts: parts})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	return withS3Retry(ctx, func() error {
		body := bytes.NewReader(data)
		resp, err := doS3Request(ctx, cfg, http.MethodPost, objectKey, url.Values{"uploadId": []string{uploadID}}, body, int64(len(data)), "application/xml", hex.EncodeToString(hash[:]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return checkS3Success(resp, "complete multipart upload "+objectKey)
	})
}

func abortS3MultipartUpload(ctx context.Context, cfg config, objectKey, uploadID string) error {
	emptyHash := sha256.Sum256(nil)
	payloadHash := hex.EncodeToString(emptyHash[:])
	resp, err := doS3Request(ctx, cfg, http.MethodDelete, objectKey, url.Values{"uploadId": []string{uploadID}}, nil, 0, "application/octet-stream", payloadHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkS3Success(resp, "abort multipart upload "+objectKey)
}

func signS3Request(req *http.Request, cfg config, payloadHash string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credentialScope := dateStamp + "/" + cfg.S3Region + "/s3/aws4_request"
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	req.Header.Set("X-Amz-Date", amzDate)
	canonicalHeaders := "content-type:" + req.Header.Get("Content-Type") + "\n" +
		"host:" + req.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.Query().Encode(),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(cfg.S3KeySecret, dateStamp, cfg.S3Region), stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cfg.S3KeyID+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func signingKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "s3")
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func s3EscapeObjectKey(key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func detectContentType(filePath string) string {
	if contentType := mime.TypeByExtension(filepath.Ext(filePath)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func uploadBackupViaCLI(cfg config, runFolder string) error {
	log.Printf("s3 upload: using PHP CLI %s %s", cfg.S3PHPBin, cfg.S3UploadScript)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.S3UploadTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cfg.S3PHPBin, cfg.S3UploadScript, runFolder)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	// Stream PHP's progress (STDERR) directly to our logger line by line.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("s3 upload CLI error: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("s3 upload CLI error: start: %w", err)
	}
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		log.Printf("s3 upload: %s", scanner.Text())
	}
	if runErr := cmd.Wait(); runErr != nil {
		return fmt.Errorf("s3 upload CLI error: %w", runErr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		return fmt.Errorf("s3 upload CLI: could not parse output: %w | raw: %s", err, strings.TrimSpace(stdout.String()))
	}
	logS3Result(result)
	if errFlag, _ := result["error"].(float64); errFlag != 0 {
		status, _ := result["status"].(string)
		return fmt.Errorf("s3 upload CLI reported failure status=%s", status)
	}
	return nil
}

func uploadBackupViaHTTP(cfg config, runFolder string) error {
	log.Printf("s3 upload: using HTTP endpoint %s", cfg.S3UploadURL)

	payload, err := json.Marshal(map[string]string{
		"action":     "uploadBackupFolder",
		"backupPath": runFolder,
	})
	if err != nil {
		return fmt.Errorf("s3 upload HTTP error: marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.S3UploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.S3UploadURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("s3 upload HTTP error: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("s3 upload HTTP error: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("s3 upload HTTP: failed to parse response (status %d): %w", resp.StatusCode, err)
	}
	logS3Result(result)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("s3 upload HTTP failed with status %d", resp.StatusCode)
	}
	if errFlag, _ := result["error"].(float64); errFlag != 0 {
		status, _ := result["status"].(string)
		return fmt.Errorf("s3 upload HTTP reported failure status=%s", status)
	}
	return nil
}

// runXtrabackupCmd runs xtrabackup with the given args, streaming its stderr
// to the logger line by line. If runAsUser is set, it executes via
// "sudo -n -u <runAsUser>" so xtrabackup can read protected MySQL datadirs.

func verifiedUploadMode(cfg config) bool {
	switch cfg.S3UploadMode {
	case "", "direct":
		return true
	case "auto":
		return cfg.S3Bucket != "" && cfg.S3KeyID != "" && cfg.S3KeySecret != ""
	}
	return false
}
