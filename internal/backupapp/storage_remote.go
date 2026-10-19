package backupapp

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type StorageRemoteObject struct {
	Key      string `json:"key"`
	Size     int64  `json:"size_bytes"`
	SHA256   string `json:"sha256"`
	ETag     string `json:"etag"`
	Version  string `json:"version_id,omitempty"`
	Modified string `json:"last_modified"`
}
type StorageRemoteProof struct {
	RunID          string                `json:"run_id"`
	Destination    string                `json:"destination"`
	VerifiedAt     time.Time             `json:"verified_at"`
	State          string                `json:"state"`
	RestoreState   string                `json:"restore_state"`
	LogicalOnly    bool                  `json:"logical_only,omitempty"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	Objects        []StorageRemoteObject `json:"objects"`
	Manifest       runRecord             `json:"-"`
}

func storageGet(ctx context.Context, cfg config, key string) (*http.Response, error) {
	empty := sha256.Sum256(nil)
	response, err := doS3Request(ctx, cfg, http.MethodGet, key, nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
	if err != nil {
		return nil, err
	}
	if err = checkS3Success(response, "storage verify "+key); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}
func storageJSON(ctx context.Context, cfg config, key string) ([]byte, error) {
	var data []byte
	err := withS3Retry(ctx, func() error {
		resp, err := storageGet(ctx, cfg, key)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 4<<20 {
			return fmt.Errorf("recovery metadata exceeds bound")
		}
		return nil
	})
	return data, err
}

// Independently downloads every byte. Completion markers and ETags alone are
// not verification. gzip/SQL checks do not constitute a restore test.
func verifyStorageRemote(ctx context.Context, cfg config, id string) (StorageRemoteProof, error) {
	return verifyStorageRemoteCapture(ctx, cfg, id, false)
}

func verifyStorageRemoteLogical(ctx context.Context, cfg config, id string) (StorageRemoteProof, error) {
	return verifyStorageRemoteCapture(ctx, cfg, id, true)
}

func verifyStorageRemoteCapture(ctx context.Context, cfg config, id string, allowPhysicalFailure bool) (StorageRemoteProof, error) {
	proof := StorageRemoteProof{RunID: id, State: "UNVERIFIED", RestoreState: "UNVERIFIED"}
	if _, err := time.Parse(runTimestampLayout, id); err != nil {
		return proof, fmt.Errorf("invalid recovery run id")
	}
	prefix := strings.Trim(cfg.S3LogicalPrefix, "/") + "/" + id
	proof.Destination = "s3://" + cfg.S3Bucket + "/" + prefix
	raw, err := storageJSON(ctx, cfg, prefix+"/COMPLETED.json")
	partial := false
	if err != nil && allowPhysicalFailure {
		raw, err = storageJSON(ctx, cfg, prefix+"/PARTIAL.json")
		partial = true
	}
	if err != nil {
		return proof, err
	}
	var marker logicalCompletionManifest
	if err = json.Unmarshal(raw, &marker); err != nil {
		return proof, err
	}
	if ((!partial && marker.Status != "COMPLETE") || (partial && marker.Status != "PARTIAL")) || marker.FormatVersion < 2 || marker.RunID != id || marker.Bucket != cfg.S3Bucket || marker.Prefix != prefix {
		return proof, fmt.Errorf("untrusted or incomplete remote recovery marker")
	}
	expected := append([]string(nil), cfg.LogicalDatabases...)
	slices.Sort(expected)
	got := append([]string(nil), marker.ExpectedDatabases...)
	slices.Sort(got)
	if len(expected) == 0 || !slices.Equal(expected, got) {
		return proof, fmt.Errorf("remote database requirements disagree")
	}
	objects := map[string]remoteArtifact{}
	for _, a := range marker.Artifacts {
		if filepath.Base(a.Key) != a.Key || strings.Contains(a.Key, "..") || a.SizeBytes <= 0 || len(a.SHA256) != 64 {
			return proof, fmt.Errorf("unsafe remote artifact inventory")
		}
		if _, e := hex.DecodeString(a.SHA256); e != nil {
			return proof, e
		}
		if _, ok := objects[a.Key]; ok {
			return proof, fmt.Errorf("duplicate remote artifact")
		}
		objects[a.Key] = a
	}
	meta, ok := objects["manifest.json"]
	if !ok {
		return proof, fmt.Errorf("remote manifest missing")
	}
	raw, err = storageJSON(ctx, cfg, prefix+"/manifest.json")
	if err != nil {
		return proof, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != meta.SizeBytes || hex.EncodeToString(sum[:]) != meta.SHA256 {
		return proof, fmt.Errorf("remote manifest checksum mismatch")
	}
	var record runRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return proof, err
	}
	persistedRecord := record
	if partial {
		record = logicalStorageRecord(record)
	}
	if record.RunID != id || record.Status != "success" || record.RecoveryState != "COMPLETE" || record.DatabasesFailed != 0 || record.FailureReason != "" || record.DatabasesSucceeded != len(expected) || record.DatabasesTotal != len(expected) || len(record.Results) != len(expected) || len(record.LogicalOmissions) > 0 || record.LogicalBackupScope != "" {
		return proof, fmt.Errorf("remote recovery point is partial")
	}
	if cfg.LogicalConsistency == "global" && !strings.HasPrefix(record.ConsistencyMethod, "single mysqldump transaction across all selected InnoDB") {
		return proof, fmt.Errorf("remote consistency requirement not met")
	}
	seen := map[string]bool{}
	required := map[string]string{"manifest.json": ""}
	for _, d := range record.Results {
		name := filepath.Base(d.OutputPath)
		a, exists := objects[name]
		if !slices.Contains(expected, d.Name) || seen[d.Name] || d.Status != "success" || len(d.OmittedObjects) > 0 || len(d.SelectedTables) > 0 || len(d.ExpectedObjects) == 0 || !exists || a.SizeBytes != d.SizeBytes || a.SHA256 != d.ArtifactSHA256 || len(d.SQLSHA256) != 64 || !isStoragePayload(name) {
			return proof, fmt.Errorf("remote required artifact missing or inconsistent")
		}
		if old, exists := required[name]; exists && old != d.SQLSHA256 {
			return proof, fmt.Errorf("shared SQL checksum disagreement")
		}
		seen[d.Name] = true
		required[name] = d.SQLSHA256
	}
	if cfg.LogicalConsistency == "global" && len(required) != 2 {
		return proof, fmt.Errorf("global point does not contain exactly one shared dump")
	}
	if len(required) != len(objects) {
		return proof, fmt.Errorf("unexpected remote artifact")
	}
	listing, err := listS3Prefix(ctx, cfg, prefix)
	if err != nil {
		return proof, err
	}
	sizes := map[string]int64{}
	for _, object := range listing.Objects {
		sizes[strings.TrimPrefix(object.Key, prefix+"/")] = object.SizeBytes
	}
	for name, sqlHash := range required {
		a := objects[name]
		if sizes[name] != a.SizeBytes {
			return proof, fmt.Errorf("remote inventory size mismatch")
		}
		var observed StorageRemoteObject
		err = withS3Retry(ctx, func() error {
			resp, e := storageGet(ctx, cfg, prefix+"/"+name)
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			h := sha256.New()
			counter := &storageCountReader{reader: io.TeeReader(resp.Body, h)}
			if name == "manifest.json" {
				_, e = io.Copy(io.Discard, counter)
			} else {
				var source io.Reader = counter
				if strings.HasSuffix(name, ".enc") {
					source, e = DecryptReader(source, cfg.EncryptionKey)
					if e != nil {
						return e
					}
				}
				gz, e := gzip.NewReader(source)
				if e != nil {
					return e
				}
				plain := sha256.New()
				tail := &storageTailWriter{}
				parsed, parseErr := readArtifactObjectInventory(io.TeeReader(gz, io.MultiWriter(plain, tail)), storageArtifactDatabase(record, name))
				e = parseErr
				if e == nil {
					for _, d := range record.Results {
						if filepath.Base(d.OutputPath) == name {
							if compareErr := compareObjectInventory(d.Name, d.ExpectedObjects, parsed[d.Name]); compareErr != nil {
								e = compareErr
								break
							}
						}
					}
				}
				closeErr := gz.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					return closeErr
				}
				if _, e = io.Copy(io.Discard, counter); e != nil {
					return e
				}
				if hex.EncodeToString(plain.Sum(nil)) != sqlHash || !strings.Contains(string(tail.tail), "-- Dump completed on ") {
					return fmt.Errorf("remote SQL integrity mismatch")
				}
			}
			if e != nil {
				return e
			}
			if counter.n != a.SizeBytes || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
				return fmt.Errorf("remote payload checksum mismatch")
			}
			observed = StorageRemoteObject{Key: prefix + "/" + name, Size: a.SizeBytes, SHA256: a.SHA256, ETag: resp.Header.Get("ETag"), Version: resp.Header.Get("x-amz-version-id"), Modified: resp.Header.Get("Last-Modified")}
			return nil
		})
		if err != nil {
			return proof, err
		}
		proof.Objects = append(proof.Objects, observed)
	}
	proof.Manifest = persistedRecord
	proof.LogicalOnly = partial
	proof.ManifestSHA256 = meta.SHA256
	proof.State = "VERIFIED"
	proof.VerifiedAt = time.Now().UTC()
	return proof, nil
}

type storageCountReader struct {
	reader io.Reader
	n      int64
}

func (r *storageCountReader) Read(p []byte) (int, error) {
	n, e := r.reader.Read(p)
	r.n += int64(n)
	return n, e
}

type storageTailWriter struct{ tail []byte }

func (w *storageTailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= 4096 {
		w.tail = append(w.tail[:0], p[n-4096:]...)
	} else {
		w.tail = append(w.tail, p...)
		if len(w.tail) > 4096 {
			w.tail = w.tail[len(w.tail)-4096:]
		}
	}
	return n, nil
}
func checkStorageRemoteIdentity(ctx context.Context, cfg config, p StorageRemoteProof) error {
	empty := sha256.Sum256(nil)
	if p.State != "VERIFIED" || time.Since(p.VerifiedAt) > time.Hour {
		return fmt.Errorf("remote verification is absent or expired")
	}
	for _, o := range p.Objects {
		err := withS3Retry(ctx, func() error {
			resp, e := doS3Request(ctx, cfg, http.MethodHead, o.Key, nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if e = checkS3Success(resp, "recheck recovery object"); e != nil {
				return e
			}
			if o.ETag == "" || resp.Header.Get("ETag") != o.ETag || resp.ContentLength != o.Size || resp.Header.Get("x-amz-version-id") != o.Version || resp.Header.Get("Last-Modified") != o.Modified {
				return fmt.Errorf("remote object identity changed after full SHA256 verification")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Re-fetch the same kind of commitment that was fully verified. A PARTIAL
	// marker proves only the independently checked logical component.
	markerName, expectedStatus := "COMPLETED.json", "COMPLETE"
	if p.LogicalOnly {
		markerName, expectedStatus = "PARTIAL.json", "PARTIAL"
	}
	raw, err := storageJSON(ctx, cfg, strings.Trim(cfg.S3LogicalPrefix, "/")+"/"+p.RunID+"/"+markerName)
	if err != nil {
		return err
	}
	var marker logicalCompletionManifest
	if err = json.Unmarshal(raw, &marker); err != nil {
		return err
	}
	if marker.Status != expectedStatus || marker.RunID != p.RunID {
		return fmt.Errorf("remote commitment changed")
	}
	for _, a := range marker.Artifacts {
		if a.Key == "manifest.json" && a.SHA256 == p.ManifestSHA256 {
			return nil
		}
	}
	return fmt.Errorf("remote manifest commitment changed")
}

func storageArtifactDatabase(r runRecord, name string) string {
	for _, d := range r.Results {
		if filepath.Base(d.OutputPath) == name {
			return d.Name
		}
	}
	return ""
}
