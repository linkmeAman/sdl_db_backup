package backupapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetentionHoldPreservesCompleteAndPartial(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "2026-10-10_12-00-01")
	newer := filepath.Join(root, "2026-10-10_18-00-00")
	for path, status := range map[string]string{old: "success", newer: "partial"} {
		os.MkdirAll(path, 0700)
		b, _ := json.Marshal(runRecord{Status: status})
		os.WriteFile(filepath.Join(path, "manifest.json"), b, 0600)
	}
	if err := cleanupOldBackups(root, "", config{RetentionDaily: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("complete recovery point evicted")
	}
}
func TestPrefixFollowsSecondPage(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("continuation-token") == "next" {
			fmt.Fprint(w, `<ListBucketResult><Contents><Key>p/last</Key><Size>2</Size></Contents></ListBucketResult>`)
		} else {
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>p/first</Key><Size>1</Size></Contents></ListBucketResult>`)
		}
	}))
	defer s.Close()
	cfg := testS3Config()
	cfg.S3Endpoint = s.URL
	got, err := listS3Prefix(context.Background(), cfg, "p")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || got.TotalBytes != 3 {
		t.Fatal("pagination incomplete")
	}
}
func TestRequestTimeoutIsRetried(t *testing.T) {
	if !isRetriableS3Error(&s3HTTPStatusError{status: 400, body: `<Error><Code>RequestTimeout</Code></Error>`}) {
		t.Fatal("retryable timeout rejected")
	}
	if !strings.Contains(strings.Join(buildMySQLDumpArgs("d", nil, nil), " "), "--single-transaction") {
		t.Fatal("transaction option missing")
	}
}
