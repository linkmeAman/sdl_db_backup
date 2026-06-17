package backupapp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

	var uploaded int
	var uploadedBytes int64
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
		key := baseKey + "/" + filepath.ToSlash(rel)
		if err := uploadFileDirectToS3(ctx, cfg, path, key, info.Size()); err != nil {
			return err
		}
		uploaded++
		uploadedBytes += info.Size()
		log.Printf("s3 upload: uploaded %s to s3://%s/%s", rel, cfg.S3Bucket, key)
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("s3 upload successful: uploaded=%d bytes=%d prefix=%s", uploaded, uploadedBytes, baseKey)
	return nil
}

func uploadFileDirectToS3(ctx context.Context, cfg config, filePath, objectKey string, size int64) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	payloadHash := hex.EncodeToString(hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	host := cfg.S3Bucket + ".s3." + cfg.S3Region + ".amazonaws.com"
	escapedKey := s3EscapeObjectKey(objectKey)
	endpoint := "https://" + host + "/" + escapedKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, file)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", detectContentType(filePath))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signS3Request(req, cfg, payloadHash)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upload %s failed: status=%d body=%s", objectKey, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
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
		"",
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
