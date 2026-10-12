package backupapp

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// xbcloud --md5 records source chunk checksums. Verify every actual remote
// payload against that source inventory; never infer checksums from ETags.
func verifyPhysicalInventory(ctx context.Context, cfg config, key string, inventory s3PrefixInfo) ([]remoteArtifact, error) {
	empty := sha256.Sum256(nil)
	var sums map[string]string
	err := withS3Retry(ctx, func() error {
		resp, e := doS3Request(ctx, cfg, http.MethodGet, key+".md5", nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if e = checkS3Success(resp, "read physical source checksums"); e != nil {
			return e
		}
		sums = map[string]string{}
		scan := bufio.NewScanner(io.LimitReader(resp.Body, 64<<20))
		for scan.Scan() {
			parts := strings.Fields(scan.Text())
			if len(parts) != 2 || len(parts[0]) != 32 || strings.HasPrefix(parts[1], "/") || strings.Contains(parts[1], "..") {
				return fmt.Errorf("invalid physical source checksum inventory")
			}
			if _, e := hex.DecodeString(parts[0]); e != nil {
				return e
			}
			object := key + "/" + parts[1]
			if _, exists := sums[object]; exists {
				return fmt.Errorf("duplicate physical source chunk")
			}
			sums[object] = parts[0]
		}
		return scan.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(sums) != len(inventory.Objects) || len(sums) == 0 {
		return nil, fmt.Errorf("physical source and remote object counts disagree")
	}
	for _, required := range []string{"xtrabackup_checkpoints.", "xtrabackup_info.", "backup-my.cnf.", "xtrabackup_tablespaces."} {
		found := false
		for name := range sums {
			if strings.HasPrefix(name, key+"/"+required) {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("physical backup lacks required metadata %s", required)
		}
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
	result := make([]remoteArtifact, len(inventory.Objects))
	workers := min(4, max(1, cfg.XbcloudParallel))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				object := inventory.Objects[i]
				expected, ok := sums[object.Key]
				if !ok {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("unexpected physical object %s", object.Key)
					}
					mu.Unlock()
					cancel()
					continue
				}
				var sha string
				e := withS3Retry(child, func() error {
					resp, e := doS3Request(child, cfg, http.MethodGet, object.Key, nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
					if e != nil {
						return e
					}
					defer resp.Body.Close()
					if e = checkS3Success(resp, "verify physical chunk"); e != nil {
						return e
					}
					md := md5.New()
					h := sha256.New()
					n, e := io.Copy(io.MultiWriter(md, h), resp.Body)
					if e != nil {
						return e
					}
					if n != object.SizeBytes || hex.EncodeToString(md.Sum(nil)) != expected {
						return fmt.Errorf("physical chunk checksum or size mismatch: %s", object.Key)
					}
					sha = hex.EncodeToString(h.Sum(nil))
					return nil
				})
				if e != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = e
					}
					mu.Unlock()
					cancel()
				} else {
					object.SHA256 = sha
					result[i] = object
				}
			}
		}()
	}
loop:
	for i := range inventory.Objects {
		select {
		case jobs <- i:
		case <-child.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, nil
}
