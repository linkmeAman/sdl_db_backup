package backupapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type StorageDecision struct {
	RunID       string `json:"run_id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Type        string `json:"backup_type"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
	Size        int64  `json:"size_bytes"`
	SHA256      string `json:"sha256,omitempty"`
	RemoteRunID string `json:"remote_run_id,omitempty"`
	Remote      string `json:"remote_counterpart,omitempty"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	Modified    int64  `json:"modified_ns"`
	Changed     int64  `json:"changed_ns"`
}
type StorageReport struct {
	At                time.Time            `json:"at"`
	State             string               `json:"state"`
	DryRun            bool                 `json:"dry_run"`
	Initial           CapacityReport       `json:"initial"`
	Final             CapacityReport       `json:"final"`
	TargetBytes       uint64               `json:"target_bytes"`
	ReclaimedBytes    uint64               `json:"reclaimed_bytes"`
	RemovedBytes      uint64               `json:"removed_payload_bytes"`
	ProtectedLocal    []string             `json:"protected_local"`
	ProtectedRemote   []string             `json:"protected_remote"`
	RemoteProofs      []StorageRemoteProof `json:"remote_proofs,omitempty"`
	Decisions         []StorageDecision    `json:"decisions,omitempty"`
	Destination       string               `json:"destination"`
	Fallback          string               `json:"fallback"`
	Error             string               `json:"error,omitempty"`
	ForecastDays      float64              `json:"forecast_days"`
	ProtectionChecked bool                 `json:"protection_checked"`
	LocalBackupLimit  int                  `json:"local_backup_limit,omitempty"`
	LocalBackupCount  int                  `json:"local_backup_count"`
}
type storageRun struct {
	r      runRecord
	folder string
	at     time.Time
	files  []StorageDecision
	valid  bool
}
type storageManager struct {
	cfg            config
	capacity       func(config, bool) (CapacityReport, error)
	local          func(config, runRecord, string) error
	remote         func(context.Context, config, string) (StorageRemoteProof, error)
	remoteLogical  func(context.Context, config, string) (StorageRemoteProof, error)
	identity       func(context.Context, config, StorageRemoteProof) error
	remove         func(string, StorageDecision) (int64, error)
	now            func() time.Time
	protectedFiles map[string][]StorageDecision
}

var storageManagerFactory = func(cfg config) *storageManager {
	return &storageManager{cfg: cfg, capacity: preflightCapacity, local: verifyLogicalCapture, remote: verifyStorageRemote, remoteLogical: verifyStorageRemoteLogical, identity: checkStorageRemoteIdentity, remove: unlinkStoragePayload, now: time.Now}
}

func isStoragePayload(name string) bool {
	return filepath.Base(name) == name && !strings.Contains(name, "..") && (strings.HasSuffix(name, ".sql.gz") || strings.HasSuffix(name, ".sql.gz.enc"))
}
func storageTemporaryName(name string) bool {
	base, stamp, found := strings.Cut(name, ".incomplete-")
	if !found || !isStoragePayload(base) {
		return false
	}
	_, err := time.Parse("20060102T150405.000000000", stamp)
	return err == nil
}
func storagePolicy(cfg config) error {
	if cfg.StorageMaxLocalBackups != 0 && cfg.StorageMaxLocalBackups < 3 {
		return fmt.Errorf("BACKUP_LOCAL_MAX_BACKUPS must be zero (disabled) or at least three to preserve two points while capturing")
	}
	if cfg.StorageHighWatermark <= 0 || cfg.StorageHighWatermark > 99 || cfg.StorageLowWatermark <= 0 || cfg.StorageLowWatermark >= cfg.StorageHighWatermark {
		return fmt.Errorf("storage low/high watermarks must satisfy 0 < low < high < 100")
	}
	if cfg.StorageHistoryRuns < 1 || cfg.StorageHistoryRuns > 1000 || cfg.StorageGrowthPercent < 1 || cfg.StorageGrowthPercent > 500 || cfg.StorageConcurrentBytes < 0 || cfg.StorageTemporaryBytes < 0 || cfg.StorageReserveBytes < 0 || cfg.StorageMinFreeInodes < 1 || cfg.StorageTempMaxAge < time.Hour || cfg.StorageVerifyTimeout <= 0 {
		return fmt.Errorf("invalid storage safety settings")
	}
	return nil
}
func PlanStorageRecovery(ctx context.Context, cfg Config) (StorageReport, error) {
	release, err := acquireLock(cfg.LockFile)
	if err != nil {
		return StorageReport{}, err
	}
	defer release()
	return storageManagerFactory(cfg).ensure(ctx, "", cfg.LogicalEnabled, true)
}

// ApplyStorageRecovery enforces configured local retention without starting a capture.
func ApplyStorageRecovery(ctx context.Context, cfg Config) (StorageReport, error) {
	release, err := acquireLock(cfg.LockFile)
	if err != nil {
		return StorageReport{}, err
	}
	defer release()
	return storageManagerFactory(cfg).ensure(ctx, "", cfg.LogicalEnabled, false)
}

// logicalStorageRecord isolates a complete logical capture from a failed physical
// task. This copy is used only for independent verification and local eviction;
// the original PARTIAL manifest and remote marker remain immutable.
func logicalStorageRecord(r runRecord) runRecord {
	if r.Status == "partial" && r.PhysicalBackup != nil && r.PhysicalBackup.Status == "failed" && r.PhysicalBackup.Error != "" && r.FailureReason == r.PhysicalBackup.Error && r.DatabasesTotal > 0 && r.DatabasesSucceeded == r.DatabasesTotal && r.DatabasesFailed == 0 && r.LogicalBackupScope == "" && len(r.LogicalOmissions) == 0 {
		r.Status = "success"
		r.FailureReason = ""
		r.RecoveryState = "COMPLETE"
	}
	return r
}

func localPayloadRunCount(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if _, err := time.Parse(runTimestampLayout, entry.Name()); err != nil {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return count, err
		}
		for _, child := range children {
			if isStoragePayload(child.Name()) || storageTemporaryName(child.Name()) {
				count++
				break
			}
		}
	}
	return count, nil
}

// Called while the global backup lock is held. It additionally serializes all
// storage decisions with the existing post-run retention lock.
func (m *storageManager) ensure(ctx context.Context, current string, logical, dry bool) (report StorageReport, err error) {
	report = StorageReport{At: m.now().UTC(), State: "SAFE", DryRun: dry, Destination: m.cfg.BackupDir, Fallback: "DISABLED"}
	defer func() {
		if err != nil {
			report.Error = err.Error()
			report.State = "CRITICAL"
			log.Printf("CRITICAL backup storage: %v", err)
		}
		if e := persistStorageReport(m.cfg, report); e != nil {
			log.Printf("storage audit persistence failed: %v", e)
			if err == nil {
				err = e
				report.State = "CRITICAL"
				report.Error = e.Error()
			}
		}
	}()
	initial, capacityErr := m.capacity(m.cfg, logical)
	capacity := initial
	report.Initial = initial
	report.Final = initial
	report.TargetBytes = initial.RequiredBytes
	report.ForecastDays = storageForecast(m.cfg, initial)
	if !dry && !m.cfg.StorageAutoCleanup {
		if capacityErr != nil {
			return report, fmt.Errorf("automatic storage cleanup disabled: %w", capacityErr)
		}
		return report, nil
	}
	if err = storagePolicy(m.cfg); err != nil {
		return report, err
	}
	limit := m.cfg.StorageMaxLocalBackups
	if limit > 0 && current != "" && logical {
		limit--
	} // reserve one slot before creating a new dump
	report.LocalBackupLimit = limit
	count, countErr := localPayloadRunCount(m.cfg.BackupDir)
	if countErr != nil {
		return report, countErr
	}
	report.LocalBackupCount = count
	overLimit := limit > 0 && count > limit
	pressure := capacityErr != nil || initial.UsedPercent >= float64(m.cfg.StorageHighWatermark) || overLimit
	if !pressure && !dry {
		// Existing independently verified protection counts remain observable; they
		// are re-established from actual artifacts before any deletion.
		prior := readStorageReport(m.cfg)
		if len(prior.ProtectedRemote) < 2 || len(prior.ProtectedLocal) < 1 {
			checked, checkErr := m.ensure(ctx, current, logical, true)
			if checkErr != nil {
				return m.blocked(report, "initial recovery protection verification unavailable: "+checkErr.Error(), logical)
			}
			checked.DryRun = false
			if checked.State == "DRY_RUN" {
				checked.State = "SAFE"
			}
			return checked, nil
		}
		localIDs := fastStorageLocalProtection(m.cfg, current)
		report.ProtectedLocal = localIDs
		// Cached offsite verification is not used to authorize deletion.

		report.ProtectedRemote = prior.ProtectedRemote
		report.RemoteProofs = prior.RemoteProofs
		report.ProtectionChecked = false
		return report, nil
	}
	root, err := canonicalStorageRoot(m.cfg.BackupDir)
	if err != nil {
		return report, err
	}
	release, err := acquireLock(filepath.Join(root, ".retention.lock"))
	if err != nil {
		return report, err
	}
	defer release()
	timeout := m.cfg.StorageVerifyTimeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runs, held, err := scanStorageRuns(root, current)
	if err != nil {
		return report, err
	}
	report.Decisions = held
	localGood := []storageRun{}
	m.protectedFiles = map[string][]StorageDecision{}
	for i := range runs {
		r := &runs[i]
		if logicalStorageRecord(r.r).Status == "success" && len(r.r.Results) > 0 && r.r.MySQLPITR == nil && (len(localGood) < max(2, limit) || runHasSuccessfulRestoreValidation(r.folder) || fileExists(filepath.Join(r.folder, ".recovery-pin"))) {
			if e := m.local(m.cfg, logicalStorageRecord(r.r), r.folder); e == nil {
				log.Printf("storage local capture verified run=%s", r.r.RunID)
				r.valid = true
				localGood = append(localGood, *r)
				m.protectedFiles[r.r.RunID] = r.files
			}
		}
	}
	if len(localGood) == 0 {
		return m.blocked(report, "no independently valid complete local recovery point; deletion refused", logical)
	}
	preferred := min(max(2, limit), len(localGood))
	for i := 0; i < preferred; i++ {
		report.ProtectedLocal = append(report.ProtectedLocal, localGood[i].r.RunID)
	}
	for _, r := range localGood {
		if runHasSuccessfulRestoreValidation(r.folder) || fileExists(filepath.Join(r.folder, ".recovery-pin")) {
			if !slices.Contains(report.ProtectedLocal, r.r.RunID) {
				report.ProtectedLocal = append(report.ProtectedLocal, r.r.RunID)
			}
		}
	}
	// Protect at least two independently verified complete offsite points. Never
	// perform remote deletion, and never count a partial/legacy completion marker.
	listing, err := listS3Prefix(ctx, m.cfg, strings.Trim(m.cfg.S3LogicalPrefix, "/"))
	if err != nil {
		return m.blocked(report, "cannot establish offsite recovery coverage: "+err.Error(), logical)
	}
	ids := map[string]bool{}
	for _, o := range listing.Objects {
		rel := strings.TrimPrefix(o.Key, strings.Trim(m.cfg.S3LogicalPrefix, "/")+"/")
		parts := strings.Split(rel, "/")
		if len(parts) == 2 && parts[1] == "COMPLETED.json" {
			if _, e := time.Parse(runTimestampLayout, parts[0]); e == nil {
				ids[parts[0]] = true
			}
		}
	}
	remoteIDs := map[string]bool{}
	for id := range ids {
		remoteIDs[id] = true
	}
	for _, o := range listing.Objects {
		rel := strings.TrimPrefix(o.Key, strings.Trim(m.cfg.S3LogicalPrefix, "/")+"/")
		parts := strings.Split(rel, "/")
		if len(parts) == 2 && parts[1] == "PARTIAL.json" {
			remoteIDs[parts[0]] = true
		}
	}
	ordered := []string{}
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ordered)))
	proofs := map[string]StorageRemoteProof{}
	for _, id := range ordered {
		proof, e := m.remote(ctx, m.cfg, id)
		if e != nil {
			log.Printf("storage remote point held run=%s reason=%v", id, e)
			continue
		}
		proofs[id] = proof
		report.ProtectedRemote = append(report.ProtectedRemote, id)
		report.RemoteProofs = append(report.RemoteProofs, proof)
		if len(report.ProtectedRemote) == 2 {
			break
		}
	}
	if len(report.ProtectedRemote) < 2 {
		return m.blocked(report, "fewer than two independently verified complete offsite points; deletion refused", logical)
	}
	report.ProtectionChecked = true
	lowTarget := uint64(float64(initial.UsableBytes) * float64(100-m.cfg.StorageLowWatermark) / 100)
	report.TargetBytes = max(initial.RequiredBytes, lowTarget)
	candidates := []StorageDecision{}
	for _, r := range runs {
		if slices.Contains(report.ProtectedLocal, r.r.RunID) {
			for _, f := range r.files {
				f.Action = "KEEP"
				f.Reason = "protected complete local recovery point"
				report.Decisions = append(report.Decisions, f)
			}
			continue
		}
		if (len(r.r.Results) == 0 && r.r.PhysicalBackup != nil) || r.r.MySQLPITR != nil || runHasSuccessfulRestoreValidation(r.folder) || fileExists(filepath.Join(r.folder, ".recovery-pin")) {
			for _, f := range r.files {
				f.Action = "KEEP"
				f.Reason = "restore-tested, pinned, physical or binlog dependency"
				report.Decisions = append(report.Decisions, f)
			}
			continue
		}
		for _, f := range r.files {
			f.Action = "HOLD"
			f.Reason = "local artifact may contain uniquely valuable data"
			if storageTemporaryName(f.Name) {
				age := m.now().Sub(time.Unix(0, f.Modified))
				ttl := m.cfg.StorageTempMaxAge
				if r.r.Status == "failed" && r.r.DatabasesSucceeded == 0 && noStorageCapturedArtifacts(r.r) && !strings.Contains(f.Name, ".enc.incomplete-") && r.at.Before(localGood[0].at) && age >= ttl && !hasStorageMultipartJournal(r.folder) && !temporaryMayBeComplete(m.cfg, f.Path) {
					f.Action = "ELIGIBLE"
					f.Type = "disposable_failed_temporary"
					f.Reason = "expired application-labelled failed dump; superseded by verified complete capture"
					candidates = append(candidates, f)
				}
			} else if logicalStorageRecord(r.r).Status == "success" && remoteIDs[r.r.RunID] && referencedStoragePayload(r.r, f) {
				f.Action = "PENDING_REMOTE_VERIFICATION"
				f.Type = "complete_logical"
				f.Reason = "obsolete local duplicate with independently verified full logical S3 counterpart"
				f.RemoteRunID = r.r.RunID
				f.Remote = "s3://" + m.cfg.S3Bucket + "/" + strings.Trim(m.cfg.S3LogicalPrefix, "/") + "/" + r.r.RunID
				candidates = append(candidates, f)
			} else if r.r.Status == "failed" || r.r.Status == "partial" {
				for _, id := range report.ProtectedRemote {
					proof := proofs[id]
					if storageProofMatches(proof, r.r, f) && validateStoragePartialFile(m.cfg, r.r, f) == nil {
						f.Action = "ELIGIBLE"
						f.Type = "partial_duplicate"
						f.Reason = "exact payload duplicate in a protected complete verified S3 point"
						f.RemoteRunID = id
						f.Remote = proof.Destination
						candidates = append(candidates, f)
						break
					}
				}
			}
			report.Decisions = append(report.Decisions, f)
		}
	}
	// A failed candidate verification is a hold, not a global permission to purge.
	err = nil
	sort.Slice(candidates, func(i, j int) bool {
		rank := func(d StorageDecision) int {
			if d.Type == "disposable_failed_temporary" {
				return 0
			}
			if d.Type == "partial_duplicate" {
				return 1
			}
			return 2
		}
		a, b := rank(candidates[i]), rank(candidates[j])
		if a != b {
			return a < b
		}
		if candidates[i].RunID != candidates[j].RunID {
			return candidates[i].RunID < candidates[j].RunID
		}
		return candidates[i].Name < candidates[j].Name
	})
	if dry {
		for i, candidate := range candidates {
			if candidate.Type == "complete_logical" {
				if _, e := m.authorizeCandidate(ctx, candidate, proofs, &report); e != nil {
					updateStorageDecision(&report, candidate.Path, "HOLD", e.Error())
					continue
				}
			}
			candidates[i].Action = "ELIGIBLE"
			updateStorageDecision(&report, candidate.Path, "ELIGIBLE", candidate.Reason)
		}
		report.State = "DRY_RUN"
		return report, nil
	}
	// Recover committed deletion intents before making new ones. Their eligibility
	// is re-established from today's plan; unknown/replaced files remain held.
	if e := m.recoverIntents(ctx, root, candidates, proofs, &report, logical); e != nil {
		return report, e
	}
	for _, candidate := range candidates {
		capacity, e := m.capacity(m.cfg, logical)
		report.Final = capacity
		count, countErr := localPayloadRunCount(root)
		if countErr != nil {
			return report, countErr
		}
		if e == nil && capacity.AvailableBytes >= report.TargetBytes && (limit == 0 || count <= limit) {
			break
		}
		if fileExists(candidate.Path) {
			if _, e = m.authorizeCandidate(ctx, candidate, proofs, &report); e != nil {
				updateStorageDecision(&report, candidate.Path, "HOLD", e.Error())
				continue
			}
			if e = m.deleteCandidate(ctx, root, candidate, proofs, &report, logical); e != nil {
				return report, e
			}
		}
	}
	capacity, capacityErr = m.capacity(m.cfg, logical)
	report.Final = capacity
	// Prefer two local points. Only when the new capture cannot fit may the
	// second-newest lose its duplicate payload, after independent S3 verification.
	if capacityErr != nil && len(localGood) > 1 {
		second := localGood[1]
		potential := uint64(0)
		for _, file := range second.files {
			if isStoragePayload(file.Name) {
				potential += uint64(max(0, file.Size))
			}
		}
		if !fileExists(filepath.Join(second.folder, ".recovery-pin")) && !runHasSuccessfulRestoreValidation(second.folder) && capacity.AvailableBytes < capacity.RequiredBytes && potential >= capacity.RequiredBytes-capacity.AvailableBytes {
			proof, e := proofs[second.r.RunID], error(nil)
			if proof.State != "VERIFIED" {
				proof, e = m.remote(ctx, m.cfg, second.r.RunID)
			}
			if e == nil {
				proofs[proof.RunID] = proof
				report.RemoteProofs = append(report.RemoteProofs, proof)
				report.ProtectedLocal = slices.DeleteFunc(report.ProtectedLocal, func(id string) bool { return id == second.r.RunID })
				for _, f := range second.files {
					if !isStoragePayload(f.Name) || !storageProofMatches(proof, second.r, f) {
						continue
					}
					f.Type = "complete_logical"
					f.RemoteRunID = proof.RunID
					f.Remote = proof.Destination
					f.Reason = "capacity emergency; latest complete local and two verified offsite points remain protected"
					if e = m.deleteCandidate(ctx, root, f, proofs, &report, logical); e != nil {
						return report, e
					}
					capacity, capacityErr = m.capacity(m.cfg, logical)
					report.Final = capacity
					if capacityErr == nil {
						break
					}
				}
			}
		}
	}
	if capacityErr != nil {
		if m.cfg.StorageSecondaryDir != "" && logical {
			secondary := m.cfg
			secondary.BackupDir = m.cfg.StorageSecondaryDir
			if _, e := canonicalStorageRoot(secondary.BackupDir); e == nil {
				alternate, e := m.capacity(secondary, logical)
				if e == nil && alternate.FilesystemID != initial.FilesystemID {
					report.Final = alternate
					report.Destination = secondary.BackupDir
					report.Fallback = "APPROVED_SECONDARY"
					report.State = "FALLBACK"
					return report, nil
				}
			}
			report.Fallback = "SECONDARY_UNAVAILABLE"
		}
		return report, fmt.Errorf("safe cleanup cannot provide backup capacity; protected recovery points preserved; no usable approved fallback: %w", capacityErr)
	}
	if limit > 0 {
		count, e := localPayloadRunCount(root)
		if e != nil {
			return report, e
		}
		report.LocalBackupCount = count
		if count > limit {
			return report, fmt.Errorf("local backup limit unmet: %d payload runs remain, limit %d; unverified or protected artifacts held", count, limit)
		}
	}
	if report.RemovedBytes > 0 {
		report.State = "CLEANED"
	}
	report.ForecastDays = storageForecast(m.cfg, capacity)
	return report, nil
}
func storageProofMatches(p StorageRemoteProof, r runRecord, f StorageDecision) bool {
	for _, d := range r.Results {
		if filepath.Base(d.OutputPath) != f.Name || d.Status != "success" || d.ArtifactSHA256 == "" {
			continue
		}
		for _, remote := range p.Manifest.Results {
			if remote.Name == d.Name && remote.Status == "success" && remote.ArtifactSHA256 == d.ArtifactSHA256 && remote.SQLSHA256 == d.SQLSHA256 && remote.SizeBytes == f.Size {
				return true
			}
		}
	}
	return false
}
func validateStoragePartialFile(cfg config, r runRecord, f StorageDecision) error {
	for _, d := range r.Results {
		if filepath.Base(d.OutputPath) == f.Name && d.Status == "success" && d.SizeBytes == f.Size {
			actual, err := fileSHA256(f.Path)
			if err != nil {
				return err
			}
			if actual == d.ArtifactSHA256 && len(actual) == 64 {
				return nil
			}
		}
	}
	return fmt.Errorf("local payload is not an exact verified duplicate")
}
func hasStorageMultipartJournal(folder string) bool {
	files, _ := filepath.Glob(filepath.Join(folder, "*.multipart.json"))
	return len(files) > 0
}
func temporaryMayBeComplete(cfg config, path string) bool {
	ok, e := validateLogicalArtifactForEngineWithEncryption(cfg.DBEngine, path, "", "", cfg.EncryptionKey)
	return e == nil && ok
}
func storageForecast(cfg config, c CapacityReport) float64 {
	runs, e := ReadRunHistory(cfg.RunLogPath)
	if e != nil {
		return -1
	}
	var sizes []int64
	for i := len(runs) - 1; i >= 0 && len(sizes) < 12; i-- {
		if runs[i].Status == "success" && runs[i].DatabasesTotal > 0 {
			size := totalLogicalArtifactSize(runs[i].Results)
			if size > 0 {
				sizes = append(sizes, size)
			}
		}
	}
	if len(sizes) == 0 {
		return -1
	}
	var sum float64
	for _, size := range sizes {
		sum += float64(size)
	}
	captures := 0
	if strings.HasPrefix(cfg.LogicalSchedule, "daily@") {
		times, e := parseDailyTimes(strings.TrimPrefix(cfg.LogicalSchedule, "daily@"))
		if e == nil {
			captures = len(times)
		}
	}
	if captures == 0 {
		return -1
	}
	if c.AvailableBytes <= c.RequiredBytes {
		return 0
	}
	return float64(c.AvailableBytes-c.RequiredBytes) / (sum / float64(len(sizes)) * float64(captures))
}
func persistStorageReport(cfg config, r StorageReport) error {
	data, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return atomicWriteFile(filepath.Join(cfg.LogDir, "storage-status.json"), append(data, '\n'), 0600)
}
func readStorageReport(cfg config) StorageReport {
	var r StorageReport
	b, e := os.ReadFile(filepath.Join(cfg.LogDir, "storage-status.json"))
	if e == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func noStorageCapturedArtifacts(r runRecord) bool {
	for _, d := range r.Results {
		if d.Status == "success" || d.ArtifactSHA256 != "" {
			return false
		}
	}
	return true
}

func (m *storageManager) blocked(report StorageReport, reason string, logical bool) (StorageReport, error) {
	if m.cfg.StorageMaxLocalBackups > 0 {
		count, e := localPayloadRunCount(m.cfg.BackupDir)
		if e != nil {
			return report, e
		}
		limit := report.LocalBackupLimit
		if count > limit {
			return report, fmt.Errorf("%s; local count limit cannot be safely enforced", reason)
		}
	}
	if report.Initial.Sufficient {
		report.State = "CLEANUP_DEFERRED"
		report.Error = reason
		log.Printf("storage cleanup deferred: %s; capture capacity remains sufficient", reason)
		return report, nil
	}
	if m.cfg.StorageSecondaryDir != "" && logical {
		alternate := m.cfg
		alternate.BackupDir = m.cfg.StorageSecondaryDir
		if _, err := canonicalStorageRoot(alternate.BackupDir); err == nil {
			capacity, err := m.capacity(alternate, logical)
			if err == nil && capacity.FilesystemID != report.Initial.FilesystemID {
				report.Final = capacity
				report.Destination = alternate.BackupDir
				report.Fallback = "APPROVED_SECONDARY"
				report.State = "FALLBACK"
				return report, nil
			}
		}
		report.Fallback = "SECONDARY_UNAVAILABLE"
	}
	return report, fmt.Errorf("%s; insufficient capacity and no viable approved fallback", reason)
}

func fastStorageLocalProtection(cfg config, current string) []string {
	root, err := canonicalStorageRoot(cfg.BackupDir)
	if err != nil {
		return nil
	}
	runs, _, err := scanStorageRuns(root, current)
	if err != nil {
		return nil
	}
	var ids []string
	for _, run := range runs {
		if run.r.Status != "success" || run.r.RecoveryState != "COMPLETE" || run.r.LocalCaptureIntegrityState != "VERIFIED" || run.r.DatabasesFailed != 0 || len(run.r.Results) == 0 {
			continue
		}
		complete := true
		for _, result := range run.r.Results {
			info, e := os.Lstat(filepath.Join(run.folder, filepath.Base(result.OutputPath)))
			if e != nil || !info.Mode().IsRegular() || info.Size() != result.SizeBytes {
				complete = false
				break
			}
		}
		if complete && (len(ids) < 2 || fileExists(filepath.Join(run.folder, ".recovery-pin")) || runHasSuccessfulRestoreValidation(run.folder)) {
			ids = append(ids, run.r.RunID)
		}
	}
	return ids
}

func referencedStoragePayload(r runRecord, f StorageDecision) bool {
	for _, d := range r.Results {
		if filepath.Base(d.OutputPath) == f.Name && d.Status == "success" && d.SizeBytes == f.Size && len(d.ArtifactSHA256) == 64 && d.SQLSHA256 != "" {
			return true
		}
	}
	return false
}
func (m *storageManager) authorizeCandidate(ctx context.Context, d StorageDecision, proofs map[string]StorageRemoteProof, r *StorageReport) (StorageRemoteProof, error) {
	if d.RemoteRunID == "" {
		if d.Type == "disposable_failed_temporary" {
			return StorageRemoteProof{}, nil
		}
		return StorageRemoteProof{}, fmt.Errorf("no complete offsite counterpart")
	}
	proof, ok := proofs[d.RemoteRunID]
	if !ok {
		var err error
		verify := m.remote
		if m.remoteLogical != nil {
			verify = m.remoteLogical
		}
		proof, err = verify(ctx, m.cfg, d.RemoteRunID)
		if err != nil {
			return proof, err
		}
		proofs[d.RemoteRunID] = proof
		r.RemoteProofs = append(r.RemoteProofs, proof)
	}
	local, err := readManifest(filepath.Join(m.cfg.BackupDir, d.RunID))
	if err != nil {
		return proof, err
	}
	if proof.State != "VERIFIED" || !storageProofMatches(proof, local, d) || validateStoragePartialFile(m.cfg, local, d) != nil {
		return proof, fmt.Errorf("local/remote counterpart mismatch")
	}
	return proof, nil
}
func updateStorageDecision(r *StorageReport, path, action, reason string) {
	for i := range r.Decisions {
		if r.Decisions[i].Path == path {
			r.Decisions[i].Action = action
			r.Decisions[i].Reason = reason
		}
	}
}
