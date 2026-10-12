package backupapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type multipartJournal struct {
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	UploadID  string    `json:"upload_id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func saveMultipartJournal(path string, cfg config, key, id string) error {
	b, e := json.Marshal(multipartJournal{cfg.S3Bucket, key, id, os.Getpid(), time.Now().UTC()})
	if e != nil {
		return e
	}
	return atomicWriteFile(path, append(b, '\n'), 0600)
}
func recoverMultipartJournal(ctx context.Context, cfg config, path, key string) error {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var state multipartJournal
	if e = json.Unmarshal(b, &state); e != nil {
		return fmt.Errorf("invalid multipart journal; preserve for operator review: %w", e)
	}
	if state.Bucket != cfg.S3Bucket || state.Key != key || state.UploadID == "" {
		return fmt.Errorf("multipart journal destination mismatch; refusing unsafe abort")
	}
	if state.PID != os.Getpid() {
		if stale, _ := isLockStale(fmt.Sprintf("pid=%d", state.PID)); !stale {
			return fmt.Errorf("multipart journal belongs to an active process")
		}
	}
	// Abort affects this uncommitted upload ID, never an existing object.
	if e = abortS3MultipartUpload(ctx, cfg, key, state.UploadID); e != nil {
		if status, ok := e.(*s3HTTPStatusError); !ok || status.status != 404 {
			return e
		}
	}
	return os.Remove(path)
}
