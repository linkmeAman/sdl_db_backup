package backupapp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUploadFileDirectToS3UsesMultipartRetriesAndVerifies(t *testing.T) {
	var initiated, completed, headCalls, partCalls int
	var uploaded int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && query.Has("uploads"):
			initiated++
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && query.Get("uploadId") == "test-upload":
			partCalls++
			if partCalls == 1 {
				http.Error(w, "temporary outage", http.StatusServiceUnavailable)
				return
			}
			n, _ := io.Copy(io.Discard, r.Body)
			uploaded += n
			w.Header().Set("ETag", fmt.Sprintf(`"part-%s"`, query.Get("partNumber")))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && query.Get("uploadId") == "test-upload":
			completed++
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<PartNumber>1</PartNumber>") {
				t.Errorf("completion request is missing first part: %s", body)
			}
			w.Header().Set("ETag", `"complete"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead:
			headCalls++
			w.Header().Set("Content-Length", fmt.Sprintf("%d", s3MultipartThreshold+1))
			w.Header().Set("ETag", `"complete"`)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	originalEndpoint := s3EndpointForConfig
	originalDelay := s3RequestRetryDelay
	s3EndpointForConfig = func(config) string { return server.URL }
	s3RequestRetryDelay = func(int) time.Duration { return 0 }
	defer func() {
		s3EndpointForConfig = originalEndpoint
		s3RequestRetryDelay = originalDelay
	}()

	path := filepath.Join(t.TempDir(), "large.sql.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(s3MultipartThreshold + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := uploadFileDirectToS3(context.Background(), testS3Config(), path, "run/large.sql.gz", s3MultipartThreshold+1)
	if err != nil {
		t.Fatalf("uploadFileDirectToS3: %v", err)
	}
	if initiated != 1 || completed != 1 || headCalls != 1 {
		t.Fatalf("initiate=%d complete=%d head=%d, want one each", initiated, completed, headCalls)
	}
	if partCalls != 6 { // five parts plus the one retry.
		t.Fatalf("part requests=%d, want 6", partCalls)
	}
	if uploaded != s3MultipartThreshold+1 || got.SizeBytes != s3MultipartThreshold+1 {
		t.Fatalf("uploaded=%d remote=%d, want %d", uploaded, got.SizeBytes, s3MultipartThreshold+1)
	}
}

func TestMultipartUploadAbortsAfterPartFailure(t *testing.T) {
	var aborted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case r.Method == http.MethodPost && query.Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>abort-me</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			http.Error(w, "invalid part", http.StatusBadRequest)
		case r.Method == http.MethodDelete && query.Get("uploadId") == "abort-me":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	originalEndpoint := s3EndpointForConfig
	s3EndpointForConfig = func(config) string { return server.URL }
	defer func() { s3EndpointForConfig = originalEndpoint }()

	path := filepath.Join(t.TempDir(), "large.sql.gz")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := uploadFileMultipartToS3(context.Background(), testS3Config(), file, "run/large.sql.gz", 7, "application/gzip"); err == nil {
		t.Fatal("expected multipart upload failure")
	}
	if !aborted {
		t.Fatal("expected failed multipart upload to be aborted")
	}
}

func TestSignS3RequestIncludesCanonicalQuery(t *testing.T) {
	cfg := testS3Config()
	request, err := http.NewRequest(http.MethodPut, "https://bucket.s3.ap-south-1.amazonaws.com/a", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.URL.RawQuery = url.Values{"uploadId": []string{"z"}, "partNumber": []string{"1"}}.Encode()
	request.Header.Set("Content-Type", "application/octet-stream")
	signS3Request(request, cfg, strings.Repeat("0", 64))
	if !strings.Contains(request.Header.Get("Authorization"), "Signature=") {
		t.Fatalf("request was not signed: %q", request.Header.Get("Authorization"))
	}
}

func TestMultipartPartSizeKeepsPartCountWithinS3Limit(t *testing.T) {
	size := s3MultipartPartSize*s3MultipartMaxParts + 1
	partSize := multipartPartSize(size)
	if got := (size + partSize - 1) / partSize; got > s3MultipartMaxParts {
		t.Fatalf("part count %d exceeds S3 limit", got)
	}
}

func testS3Config() config {
	return config{S3Bucket: "bucket", S3Region: "ap-south-1", S3KeyID: "key", S3KeySecret: "secret"}
}

func TestCustomS3EndpointUsesSignedBucketPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/storage/test-bucket/logical/a%20b.sql.gz.enc" {
			t.Errorf("unexpected path: %s", r.URL.EscapedPath())
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("custom endpoint request is not signed")
		}
		w.Header().Set("Content-Length", "7")
	}))
	defer server.Close()
	cfg := loadConfigFromValues(map[string]string{
		"BACKUP_S3_ENDPOINT":   server.URL + "/storage/",
		"BACKUP_S3_BUCKET":     "test-bucket",
		"BACKUP_S3_KEY_ID":     "test-key",
		"BACKUP_S3_KEY_SECRET": "test-secret",
	})
	if envMapFromConfig(cfg)["BACKUP_S3_ENDPOINT"] != cfg.S3Endpoint {
		t.Fatal("custom endpoint lost during config round trip")
	}
	info, err := headS3Object(context.Background(), cfg, "logical/a b.sql.gz.enc")
	if err != nil || info.SizeBytes != 7 {
		t.Fatalf("head custom endpoint: info=%+v err=%v", info, err)
	}
	if got := s3EndpointForConfig(config{S3Bucket: "bucket", S3Region: "us-east-1"}); got != "https://bucket.s3.us-east-1.amazonaws.com" {
		t.Fatalf("default AWS endpoint changed: %s", got)
	}
}

func TestCustomS3EndpointRejectsInvalidURLs(t *testing.T) {
	for _, endpoint := range []string{"minio:9000", "file:///tmp/bucket", "http://user:pass@minio", "http://minio?x=y", "http://minio?", "http://minio#fragment", "http://minio#"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := doS3Request(context.Background(), config{S3Endpoint: endpoint}, http.MethodHead, "key", nil, nil, 0, "", "")
			if err == nil || !strings.Contains(err.Error(), "BACKUP_S3_ENDPOINT") {
				t.Fatalf("invalid endpoint accepted: %v", err)
			}
		})
	}
}

func TestListS3Prefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			t.Errorf("expected list-type=2, got %q", r.URL.Query().Get("list-type"))
		}
		if r.URL.Query().Get("prefix") != "backup-as-it-is/run-1/physical.xbstream/" {
			t.Errorf("expected trailing slash in prefix, got %q", r.URL.Query().Get("prefix"))
		}
		w.Header().Set("Content-Type", "application/xml")
		xml := `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
			<Contents><Key>backup-as-it-is/run-1/physical.xbstream/chunk1</Key><Size>1024</Size></Contents>
			<Contents><Key>backup-as-it-is/run-1/physical.xbstream/chunk2</Key><Size>2048</Size></Contents>
		</ListBucketResult>`
		w.Write([]byte(xml))
	}))
	defer server.Close()

	cfg := loadConfigFromValues(map[string]string{
		"BACKUP_S3_ENDPOINT":   server.URL + "/storage/",
		"BACKUP_S3_BUCKET":     "test-bucket",
		"BACKUP_S3_KEY_ID":     "test-key",
		"BACKUP_S3_KEY_SECRET": "test-secret",
	})

	info, err := listS3Prefix(context.Background(), cfg, "backup-as-it-is/run-1/physical.xbstream")
	if err != nil {
		t.Fatalf("listS3Prefix error: %v", err)
	}
	if info.ObjectCount != 2 {
		t.Fatalf("expected 2 objects, got %d", info.ObjectCount)
	}
	if info.TotalBytes != 3072 {
		t.Fatalf("expected 3072 bytes, got %d", info.TotalBytes)
	}
}

