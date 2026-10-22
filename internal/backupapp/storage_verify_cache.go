package backupapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"
)

// The storage verification cache lets the pre-capture storage manager skip
// re-reading artifacts that were fully verified recently and have not changed
// since. A local entry is reused only while the file's size, mtime, ctime,
// inode and device, and the manifest's integrity claims, are identical; a
// remote entry only while a HEAD shows the same ETag, size, version and
// Last-Modified. Entries expire after BACKUP_STORAGE_VERIFY_CACHE_TTL (0
// disables the cache). Only successful full verifications are recorded.

const storageVerifyCacheVersion = 1

var storageVerifyNow = time.Now

var storageVerifyCacheMu sync.Mutex

type storageLocalVerifyEntry struct {
	Size           int64                          `json:"size_bytes"`
	ModTimeNs      int64                          `json:"mtime_ns"`
	ChangeTimeNs   int64                          `json:"ctime_ns"`
	Inode          uint64                         `json:"inode"`
	Device         uint64                         `json:"device"`
	ArtifactSHA256 string                         `json:"artifact_sha256"`
	SQLSHA256      string                         `json:"sql_sha256"`
	Engine         string                         `json:"engine"`
	DefaultDB      string                         `json:"default_db"`
	KeyID          string                         `json:"key_id,omitempty"`
	Inventory      map[string]map[string][]string `json:"inventory"`
	VerifiedAt     time.Time                      `json:"verified_at"`
}

type storageRemoteVerifyEntry struct {
	Object         StorageRemoteObject `json:"object"`
	SQLSHA256      string              `json:"sql_sha256"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	KeyID          string              `json:"key_id,omitempty"`
	VerifiedAt     time.Time           `json:"verified_at"`
}

type storageVerifyCache struct {
	Version int                                 `json:"version"`
	Local   map[string]storageLocalVerifyEntry  `json:"local"`
	Remote  map[string]storageRemoteVerifyEntry `json:"remote"`

	path        string
	ttl         time.Duration
	addedLocal  map[string]storageLocalVerifyEntry
	addedRemote map[string]storageRemoteVerifyEntry
}

func storageVerifyCachePath(cfg config) string {
	return filepath.Join(cfg.LogDir, "storage-verify-cache.json")
}

// storageVerifyKeyID identifies the encryption key without storing it.
func storageVerifyKeyID(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("sdl-db-backup storage-verify-cache\x00" + key))
	return hex.EncodeToString(sum[:8])
}

// openStorageVerifyCache returns nil when the cache is disabled. A missing or
// unreadable state file yields an empty cache; it never fails verification.
func openStorageVerifyCache(cfg config) *storageVerifyCache {
	if cfg.StorageVerifyCacheTTL <= 0 || cfg.LogDir == "" {
		return nil
	}
	c := &storageVerifyCache{path: storageVerifyCachePath(cfg), ttl: cfg.StorageVerifyCacheTTL, addedLocal: map[string]storageLocalVerifyEntry{}, addedRemote: map[string]storageRemoteVerifyEntry{}}
	storageVerifyCacheMu.Lock()
	defer storageVerifyCacheMu.Unlock()
	c.load()
	return c
}

func (c *storageVerifyCache) load() {
	c.Version, c.Local, c.Remote = 0, nil, nil
	if raw, err := os.ReadFile(c.path); err == nil {
		if json.Unmarshal(raw, c) != nil || c.Version != storageVerifyCacheVersion {
			c.Local, c.Remote = nil, nil
		}
	}
	if c.Local == nil {
		c.Local = map[string]storageLocalVerifyEntry{}
	}
	if c.Remote == nil {
		c.Remote = map[string]storageRemoteVerifyEntry{}
	}
}

func (c *storageVerifyCache) fresh(at time.Time) bool {
	age := storageVerifyNow().Sub(at)
	return age >= 0 && age < c.ttl
}

// save merges this session's new entries into the current state file, so
// concurrent verifications do not discard each other's results.
func (c *storageVerifyCache) save() {
	if c == nil || len(c.addedLocal)+len(c.addedRemote) == 0 {
		return
	}
	storageVerifyCacheMu.Lock()
	defer storageVerifyCacheMu.Unlock()
	c.load()
	for path, e := range c.addedLocal {
		c.Local[path] = e
	}
	for key, e := range c.addedRemote {
		c.Remote[key] = e
	}
	for path, e := range c.Local {
		if !c.fresh(e.VerifiedAt) || !fileExists(path) {
			delete(c.Local, path)
		}
	}
	for key, e := range c.Remote {
		if !c.fresh(e.VerifiedAt) {
			delete(c.Remote, key)
		}
	}
	c.Version = storageVerifyCacheVersion
	raw, err := json.Marshal(c)
	if err == nil {
		err = atomicWriteFile(c.path, raw, 0o600)
	}
	if err != nil {
		log.Printf("warning: could not persist storage verification cache: %v", err)
	}
}

func localVerifyFingerprint(cfg config, path string, d databaseResult) (storageLocalVerifyEntry, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return storageLocalVerifyEntry{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return storageLocalVerifyEntry{}, false
	}
	return storageLocalVerifyEntry{
		Size:           info.Size(),
		ModTimeNs:      info.ModTime().UnixNano(),
		ChangeTimeNs:   st.Ctim.Nano(),
		Inode:          st.Ino,
		Device:         uint64(st.Dev),
		ArtifactSHA256: d.ArtifactSHA256,
		SQLSHA256:      d.SQLSHA256,
		Engine:         cfg.DBEngine,
		DefaultDB:      d.Name,
		KeyID:          storageVerifyKeyID(cfg.EncryptionKey),
	}, true
}

func (c *storageVerifyCache) lookupLocal(cfg config, path string, d databaseResult) (map[string]map[string][]string, bool) {
	if c == nil {
		return nil, false
	}
	e, ok := c.Local[path]
	if !ok || !c.fresh(e.VerifiedAt) {
		return nil, false
	}
	want, ok := localVerifyFingerprint(cfg, path, d)
	if !ok {
		return nil, false
	}
	got := e
	got.Inventory, got.VerifiedAt = nil, time.Time{}
	if !reflect.DeepEqual(want, got) {
		return nil, false
	}
	return e.Inventory, true
}

func (c *storageVerifyCache) recordLocal(cfg config, path string, d databaseResult, inventory map[string]map[string][]string) {
	if c == nil {
		return
	}
	e, ok := localVerifyFingerprint(cfg, path, d)
	if !ok {
		return
	}
	e.Inventory = inventory
	e.VerifiedAt = storageVerifyNow().UTC()
	c.Local[path] = e
	c.addedLocal[path] = e
}

// verifyLogicalCaptureCached is verifyLogicalCapture for the storage manager:
// every manifest and inventory check still runs, but unchanged artifacts that
// were fully verified within the TTL are not re-read.
func verifyLogicalCaptureCached(cfg config, r runRecord, folder string) error {
	cache := openStorageVerifyCache(cfg)
	defer cache.save()
	return verifyLogicalCaptureWith(cfg, r, folder, func(path string, d databaseResult) (map[string]map[string][]string, error) {
		if inventory, ok := cache.lookupLocal(cfg, path, d); ok {
			return inventory, nil
		}
		inventory, err := logicalArtifactCheck(cfg, path, d)
		if err == nil {
			cache.recordLocal(cfg, path, d, inventory)
		}
		return inventory, err
	})
}

func remoteVerifyCacheKey(cfg config, objectKey string) string {
	return cfg.S3Endpoint + "|" + cfg.S3Bucket + "|" + objectKey
}

// lookupRemote returns the previously verified object when a HEAD shows the
// remote object is unchanged. Any HEAD failure means a full download instead.
func (c *storageVerifyCache) lookupRemote(ctx context.Context, cfg config, objectKey string, a remoteArtifact, sqlHash, manifestSHA256 string) (StorageRemoteObject, bool) {
	if c == nil {
		return StorageRemoteObject{}, false
	}
	e, ok := c.Remote[remoteVerifyCacheKey(cfg, objectKey)]
	if !ok || !c.fresh(e.VerifiedAt) || e.Object.ETag == "" || e.Object.Key != objectKey || e.Object.Size != a.SizeBytes || e.Object.SHA256 != a.SHA256 || e.SQLSHA256 != sqlHash || e.ManifestSHA256 != manifestSHA256 || e.KeyID != storageVerifyKeyID(cfg.EncryptionKey) {
		return StorageRemoteObject{}, false
	}
	empty := sha256.Sum256(nil)
	err := withS3Retry(ctx, func() error {
		resp, err := doS3Request(ctx, cfg, http.MethodHead, objectKey, nil, nil, 0, "application/octet-stream", hex.EncodeToString(empty[:]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err = checkS3Success(resp, "head cached recovery object"); err != nil {
			return err
		}
		if resp.Header.Get("ETag") != e.Object.ETag || resp.ContentLength != e.Object.Size || resp.Header.Get("x-amz-version-id") != e.Object.Version || resp.Header.Get("Last-Modified") != e.Object.Modified {
			return errStorageRemoteChanged
		}
		return nil
	})
	if err != nil {
		return StorageRemoteObject{}, false
	}
	return e.Object, true
}

var errStorageRemoteChanged = fmt.Errorf("remote object changed since cached verification")

func (c *storageVerifyCache) recordRemote(cfg config, o StorageRemoteObject, sqlHash, manifestSHA256 string) {
	if c == nil || o.ETag == "" {
		return
	}
	key := remoteVerifyCacheKey(cfg, o.Key)
	c.Remote[key] = storageRemoteVerifyEntry{Object: o, SQLSHA256: sqlHash, ManifestSHA256: manifestSHA256, KeyID: storageVerifyKeyID(cfg.EncryptionKey), VerifiedAt: storageVerifyNow().UTC()}
	c.addedRemote[key] = c.Remote[key]
}
