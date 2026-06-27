package backupapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const runTimestampLayout = "2006-01-02_15-04-05"

type PhysicalBackupResult = physicalBackupResult

type physicalBackupResult struct {
	Status             string `json:"status"`
	TargetDir          string `json:"target_dir,omitempty"`
	Duration           string `json:"duration"`
	Error              string `json:"error,omitempty"`
	Attempts           int    `json:"attempts,omitempty"`
	RateLimitRetries   int    `json:"rate_limit_retries,omitempty"`
	XtrabackupParallel int    `json:"xtrabackup_parallel,omitempty"`
	XbcloudParallel    int    `json:"xbcloud_parallel,omitempty"`
}

type DatabaseResult = databaseResult

type databaseResult struct {
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	Attempts          int               `json:"attempts"`
	Duration          string            `json:"duration"`
	OutputPath        string            `json:"output_path,omitempty"`
	SizeBytes         int64             `json:"size_bytes,omitempty"`
	ArtifactSHA256    string            `json:"artifact_sha256,omitempty"`
	SQLSHA256         string            `json:"sql_sha256,omitempty"`
	TableCount        int64             `json:"table_count,omitempty"`
	ViewCount         int64             `json:"view_count,omitempty"`
	TriggerCount      int64             `json:"trigger_count,omitempty"`
	RoutineCount      int64             `json:"routine_count,omitempty"`
	EventCount        int64             `json:"event_count,omitempty"`
	SchemaFingerprint string            `json:"schema_fingerprint,omitempty"`
	RowCounts         int64             `json:"row_counts,omitempty"`
	RowCountMode      string            `json:"row_count_mode,omitempty"`
	TableRowCounts    map[string]int64  `json:"table_row_counts,omitempty"`
	SampleRowHashes   map[string]string `json:"sample_row_hashes,omitempty"`
	SampleRowCount    int               `json:"sample_row_count,omitempty"`
	ErrorCategory     string            `json:"error_category,omitempty"`
	Error             string            `json:"error,omitempty"`
}

type RunResult = runRecord

type runRecord struct {
	Timestamp                time.Time                  `json:"timestamp"`
	RunID                    string                     `json:"run_id"`
	Status                   string                     `json:"status"`
	BackupDir                string                     `json:"backup_dir"`
	RunFolder                string                     `json:"run_folder,omitempty"`
	LogFile                  string                     `json:"log_file,omitempty"`
	FailureReason            string                     `json:"failure_reason,omitempty"`
	CleanupError             string                     `json:"cleanup_error,omitempty"`
	Duration                 string                     `json:"duration"`
	DatabasesTotal           int                        `json:"databases_total"`
	DatabasesSucceeded       int                        `json:"databases_succeeded"`
	DatabasesFailed          int                        `json:"databases_failed"`
	Results                  []databaseResult           `json:"results,omitempty"`
	PhysicalBackup           *physicalBackupResult      `json:"physical_backup,omitempty"`
	ExitCode                 int                        `json:"-"`
	LogicalUploadRun         bool                       `json:"logical_upload_run,omitempty"`
	LogicalUploadStatus      string                     `json:"logical_upload_status,omitempty"`
	LogicalUploadNote        string                     `json:"logical_upload_note,omitempty"`
	LogicalUploadError       string                     `json:"logical_upload_error,omitempty"`
	AdaptiveLoadPerCPU       float64                    `json:"adaptive_load_per_cpu,omitempty"`
	AdaptiveLogicalParallel  int                        `json:"adaptive_logical_parallel,omitempty"`
	AdaptivePhysicalParallel int                        `json:"adaptive_physical_parallel,omitempty"`
	AdaptiveXbcloudParallel  int                        `json:"adaptive_xbcloud_parallel,omitempty"`
	AdaptiveTuningReason     string                     `json:"adaptive_tuning_reason,omitempty"`
	ValidationCheckedAt      time.Time                  `json:"validation_checked_at,omitempty"`
	ValidationMode           string                     `json:"validation_mode,omitempty"`
	ValidationStatus         string                     `json:"validation_status,omitempty"`
	ValidationError          string                     `json:"validation_error,omitempty"`
	ValidationDatabases      []DatabaseValidationResult `json:"validation_databases,omitempty"`
	OSUser                   string                     `json:"os_user,omitempty"`
	ExecutionSource          string                     `json:"execution_source,omitempty"`
	Hostname                 string                     `json:"hostname,omitempty"`
	PID                      int                        `json:"pid,omitempty"`
}

type ManualRunMode string

const (
	ManualRunBoth         ManualRunMode = "both"
	ManualRunLogicalOnly  ManualRunMode = "logical_only"
	ManualRunPhysicalOnly ManualRunMode = "physical_only"
)

func shouldBlockScheduledRootRun(osUser, executionSource string) bool {
	return normalizedExecutionSource(executionSource) == "runner" && strings.TrimSpace(osUser) == "root"
}

type ManualUploadMode string

const (
	ManualUploadNormal    ManualUploadMode = "normal"
	ManualUploadLocalOnly ManualUploadMode = "local_only"
)

type ManualRunOptions struct {
	Mode          ManualRunMode
	UploadMode    ManualUploadMode
	ForceNow      bool
	PreflightOnly bool
}

type Preview struct {
	Lines    []string
	Warnings []string
}

func (p Preview) String() string {
	parts := append([]string{}, p.Lines...)
	if len(p.Warnings) > 0 {
		parts = append(parts, "Warnings:")
		for _, warning := range p.Warnings {
			parts = append(parts, "- "+warning)
		}
	}
	return strings.Join(parts, "\n")
}

type RunSinks struct {
	Console io.Writer
}

type TemporaryOverrides struct {
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
	Values    map[string]string `json:"values"`
	Note      string            `json:"note,omitempty"`
}

type SystemdAction string

const (
	SystemdDaemonReload SystemdAction = "daemon-reload"
	SystemdEnableTimer  SystemdAction = "enable-timer"
	SystemdDisableTimer SystemdAction = "disable-timer"
	SystemdStartTimer   SystemdAction = "start-timer"
	SystemdStopTimer    SystemdAction = "stop-timer"
	SystemdRestartSvc   SystemdAction = "restart-service"
	SystemdStartSvc     SystemdAction = "start-service"
	SystemdStopSvc      SystemdAction = "stop-service"
)

type UnitState struct {
	Name      string
	LoadState string
	Active    string
	SubState  string
	Enabled   string
}

type UnitStatus struct {
	Service UnitState
	Timer   UnitState
}

type HealthCheck struct {
	Name    string
	Status  string
	Message string
}

type RestoreVerificationProfile struct {
	RestoreTestEnabled bool
	ExactRowCounts     bool
	SampleDataChecks   bool
	SampleDataRows     int
}

type LatestRunInfo struct {
	Timestamp                time.Time
	RunID                    string
	Status                   string
	RunFolder                string
	LogFile                  string
	FinalOutcome             string
	FailureReason            string
	CleanupError             string
	Duration                 string
	DatabasesTotal           int
	DatabasesSucceeded       int
	DatabasesFailed          int
	LogicalUploadRun         bool
	LogicalUploadStatus      string
	LogicalUploadNote        string
	LogicalUploadError       string
	AdaptiveLoadPerCPU       float64
	AdaptiveLogicalParallel  int
	AdaptivePhysicalParallel int
	AdaptiveXbcloudParallel  int
	AdaptiveTuningReason     string
	ValidationCheckedAt      time.Time
	ValidationMode           string
	ValidationStatus         string
	ValidationError          string
	OSUser                   string
	ExecutionSource          string
	Hostname                 string
	PID                      int
}

func DeriveFinalOutcome(status string, databasesTotal int, databasesFailed int, failureReason string, cleanupError string, logicalUploadError string) string {
	if strings.TrimSpace(failureReason) != "" {
		return failureReason
	}
	if strings.TrimSpace(logicalUploadError) != "" {
		return "logical upload failed: " + logicalUploadError
	}
	if strings.TrimSpace(cleanupError) != "" {
		if status == "success" {
			return "backup completed with cleanup issue: " + cleanupError
		}
		return "cleanup issue: " + cleanupError
	}
	switch status {
	case "partial":
		if databasesFailed > 0 && databasesTotal > 0 {
			return fmt.Sprintf("%d of %d database backups failed", databasesFailed, databasesTotal)
		}
		return "backup completed partially"
	case "failed":
		if databasesFailed > 0 && databasesTotal > 0 {
			return fmt.Sprintf("%d of %d database backups failed", databasesFailed, databasesTotal)
		}
		if databasesTotal == 0 {
			return "backup failed before any database finished"
		}
		return "backup failed"
	default:
		return ""
	}
}

type APIRunResult struct {
	RunResult
	FinalOutcome string `json:"final_outcome,omitempty"`
}

func BuildAPIRunResult(run RunResult) APIRunResult {
	return APIRunResult{
		RunResult:    run,
		FinalOutcome: DeriveFinalOutcome(run.Status, run.DatabasesTotal, run.DatabasesFailed, run.FailureReason, run.CleanupError, run.LogicalUploadError),
	}
}

type HealthReport struct {
	ConfigPath          string
	RunLogPath          string
	DailyLogPath        string
	LatestRun           *LatestRunInfo
	Logical             HealthCheck
	Physical            HealthCheck
	Metrics             HealthCheck
	Directories         []HealthCheck
	Observability       ObservabilityReport
	Runtime             RuntimeProfile
	RestoreVerification RestoreVerificationProfile
}

type scheduleState struct {
	LogicalLastSuccess  string `json:"logical_last_success,omitempty"`
	PhysicalLastSuccess string `json:"physical_last_success,omitempty"`
}

type countingWriter struct {
	writer io.Writer
	bytes  int64
}

type logicalBackupTask struct {
	index           int
	dbName          string
	outputPath      string
	tables          []string
	immediateResult *databaseResult
}

type logicalBackupOutcome struct {
	index  int
	result databaseResult
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.writer.Write(p)
	cw.bytes += int64(n)
	return n, err
}

func markLogicalUploadSuccess(record *runRecord) {
	record.LogicalUploadRun = true
	record.LogicalUploadStatus = "success"
	record.LogicalUploadNote = ""
	record.LogicalUploadError = ""
}

func markLogicalUploadSkipped(record *runRecord, note string) {
	record.LogicalUploadRun = false
	record.LogicalUploadStatus = "skipped"
	record.LogicalUploadNote = note
	record.LogicalUploadError = ""
}

func markLogicalUploadFailure(record *runRecord, err error) {
	record.LogicalUploadRun = true
	record.LogicalUploadStatus = "failed"
	record.LogicalUploadNote = ""
	record.LogicalUploadError = err.Error()
	if record.FailureReason == "" {
		record.FailureReason = "logical backup upload failed: " + err.Error()
	}
	if record.Status == "success" {
		record.Status = "failed"
	}
}

func prepareLogicalBackupTasks(cfg config, runFolder string, databases []string) ([]logicalBackupTask, error) {
	tasks := make([]logicalBackupTask, 0, len(databases))
	for index, dbName := range databases {
		ext := ".sql.gz"
		if cfg.EncryptionKey != "" {
			ext = ".sql.gz.enc"
		}
		outputPath := filepath.Join(runFolder, dbName+ext)
		log.Printf("[%d/%d] processing %s", index+1, len(databases), dbName)
		requestedTables := selectedTablesForDatabase(cfg, dbName)
		tables := requestedTables
		if len(requestedTables) > 0 {
			availableTables, missingTables, err := resolveSelectedTablesForDatabase(cfg, dbName)
			if err != nil {
				log.Printf("warning: could not validate selected tables for database=%s: %v", dbName, err)
				log.Printf("database=%s: skipping table-scoped dump so the backup can continue without errors", dbName)
				tables = nil
			} else {
				tables = availableTables
				if len(missingTables) > 0 {
					log.Printf("database=%s: skipping missing tables from logical backup: %s", dbName, strings.Join(missingTables, ", "))
				}
				if len(tables) == 0 {
					log.Printf("database=%s: no requested tables remain after validation; skipping this database", dbName)
					immediate := databaseResult{
						Name:     dbName,
						Status:   "success",
						Duration: "0s",
					}
					tasks = append(tasks, logicalBackupTask{
						index:           index,
						dbName:          dbName,
						outputPath:      outputPath,
						immediateResult: &immediate,
					})
					continue
				}
			}
		}
		tasks = append(tasks, logicalBackupTask{
			index:      index,
			dbName:     dbName,
			outputPath: outputPath,
			tables:     tables,
		})
	}
	return tasks, nil
}

func executeLogicalBackupTasks(cfg config, tasks []logicalBackupTask) []databaseResult {
	results := make([]databaseResult, len(tasks))
	if len(tasks) == 0 {
		return results
	}

	workers := normalizedLogicalParallelism(cfg.LogicalParallel)
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers == 1 {
		for _, task := range tasks {
			if task.immediateResult != nil {
				results[task.index] = *task.immediateResult
				continue
			}
			results[task.index] = dumpWithRetry(cfg, task.dbName, task.outputPath, task.tables)
		}
		return results
	}

	jobs := make(chan logicalBackupTask)
	outcomes := make(chan logicalBackupOutcome, len(tasks))
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if task.immediateResult != nil {
					outcomes <- logicalBackupOutcome{index: task.index, result: *task.immediateResult}
					continue
				}
				outcomes <- logicalBackupOutcome{
					index:  task.index,
					result: dumpWithRetry(cfg, task.dbName, task.outputPath, task.tables),
				}
			}
		}()
	}

	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)
	wg.Wait()
	close(outcomes)

	for outcome := range outcomes {
		results[outcome.index] = outcome.result
	}
	return results
}

var systemDBs = []string{
	"information_schema",
	"performance_schema",
	"mysql",
	"sys",
}

func BuildManualRunConfig(base Config, opts ManualRunOptions) (Config, Preview, error) {
	cfg := base
	preview := Preview{}
	currentUser := currentOSUser()
	execSource := normalizedExecutionSource(cfg.ExecutionSource)

	preview.Lines = append(preview.Lines,
		fmt.Sprintf("Invocation context: %s", schedulerContext(currentUser, execSource)),
		fmt.Sprintf("Runtime user: %s", currentUser),
		fmt.Sprintf("Execution source: %s", execSource),
	)

	if opts.Mode == "" {
		opts.Mode = ManualRunBoth
	}
	if opts.UploadMode == "" {
		opts.UploadMode = ManualUploadNormal
	}

	switch opts.Mode {
	case ManualRunBoth:
		cfg.LogicalEnabled = true
		cfg.PhysicalEnabled = true
		preview.Lines = append(preview.Lines, "Run mode: logical + physical")
	case ManualRunLogicalOnly:
		cfg.LogicalEnabled = true
		cfg.PhysicalEnabled = false
		preview.Lines = append(preview.Lines, "Run mode: logical only")
	case ManualRunPhysicalOnly:
		cfg.LogicalEnabled = false
		cfg.PhysicalEnabled = true
		preview.Lines = append(preview.Lines, "Run mode: physical only")
	default:
		return cfg, preview, fmt.Errorf("unsupported manual run mode %q", opts.Mode)
	}

	if opts.ForceNow {
		if cfg.LogicalEnabled {
			cfg.LogicalSchedule = "always"
			preview.Lines = append(preview.Lines, "Logical schedule override: always")
		}
		if cfg.PhysicalEnabled {
			cfg.PhysicalSchedule = "always"
			preview.Lines = append(preview.Lines, "Physical schedule override: always")
		}
	}

	switch opts.UploadMode {
	case ManualUploadNormal:
		preview.Lines = append(preview.Lines, "Upload mode: normal")
	case ManualUploadLocalOnly:
		cfg.LogicalS3UploadEnabled = false
		cfg.PhysicalS3UploadEnabled = false
		preview.Lines = append(preview.Lines, "Upload mode: local only")
		if cfg.PhysicalEnabled {
			cfg.PhysicalEnabled = false
			preview.Warnings = append(preview.Warnings, "Physical backup was disabled because this project only supports physical backups when streaming directly to S3.")
		}
	default:
		return cfg, preview, fmt.Errorf("unsupported upload mode %q", opts.UploadMode)
	}

	if opts.PreflightOnly {
		cfg.PreflightOnly = true
		preview.Lines = append(preview.Lines, "Run type: preflight only")
		preview.Warnings = append(preview.Warnings, "Preflight-only mode validates MySQL, directory permissions, metrics path, and configured upload prerequisites without creating backup artifacts.")
	}
	if shouldBlockScheduledRootRun(currentUser, execSource) {
		preview.Warnings = append(preview.Warnings, "Current runtime looks like a root scheduled runner. Scheduled execution should use the developer user-level systemd service/timer instead.")
	}

	preview.Lines = append(preview.Lines, "Config file will not be rewritten unless you explicitly save from the TUI.")
	return cfg, preview, nil
}

func (mc multiCloser) Close() error {
	var firstErr error
	for _, closer := range mc.closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func initRunLogger(logDir, runID string, console io.Writer) (io.Closer, string, string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, "", "", fmt.Errorf("create log dir %s: %w", logDir, err)
	}
	logFilePath := filepath.Join(logDir, runID+".log")
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, "", "", fmt.Errorf("open run log %s: %w", logFilePath, err)
	}
	dailyLogPath := filepath.Join(logDir, time.Now().Format("2006-01-02")+".log")
	dailyLog, err := os.OpenFile(dailyLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		_ = logFile.Close()
		return nil, "", "", fmt.Errorf("open daily log %s: %w", dailyLogPath, err)
	}
	if console == nil {
		console = io.Discard
	}
	log.SetOutput(io.MultiWriter(console, logFile, dailyLog))
	return multiCloser{closers: []io.Closer{logFile, dailyLog}}, logFilePath, dailyLogPath, nil
}

func acquireLock(lockFile string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create lock file %s: %w", lockFile, err)
		}
		// Lock file exists — check whether the owning process is still alive.
		state, _ := os.ReadFile(lockFile)
		if stale, pid := isLockStale(string(state)); stale {
			log.Printf("removing stale lock file (pid=%d is no longer running)", pid)
			if rmErr := os.Remove(lockFile); rmErr != nil && !os.IsNotExist(rmErr) {
				return nil, fmt.Errorf("remove stale lock: %w", rmErr)
			}
			file, err = os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
			if err != nil {
				return nil, fmt.Errorf("create lock file after stale removal: %w", err)
			}
		} else {
			info := strings.TrimSpace(string(state))
			if info != "" {
				return nil, fmt.Errorf("another backup run is already active: %s", info)
			}
			return nil, errors.New("another backup run is already active")
		}
	}

	_, _ = fmt.Fprintf(file, "pid=%d started=%s", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = file.Close()

	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = os.Remove(lockFile)
	}, nil
}

// isLockStale parses the PID from a lock file and returns true if that
// process is no longer running. Also returns the parsed PID for logging.
func isLockStale(content string) (bool, int) {
	for _, field := range strings.Fields(content) {
		after, ok := strings.CutPrefix(field, "pid=")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(after)
		if err != nil || pid <= 0 {
			return true, 0 // malformed — treat as stale
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return true, pid
		}
		// Signal 0 tests process existence without delivering a real signal.
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return true, pid // process is dead
		}
		return false, pid // process is alive
	}
	return true, 0 // no PID found — treat as stale
}

func validateWritableDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", path, err)
	}
	tempFile, err := os.CreateTemp(path, ".sdl-backup-check-*")
	if err != nil {
		return fmt.Errorf("write test in %s: %w", path, err)
	}
	name := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close write test file %s: %w", name, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove write test file %s: %w", name, err)
	}
	return nil
}

func validatePrerequisites(cfg config, requireLogicalDump bool) error {
	if cfg.DBUser == "" {
		return errors.New("DB_USER is required")
	}
	if err := validateWritableDir(cfg.BackupDir); err != nil {
		return err
	}
	if err := validateWritableDir(cfg.LogDir); err != nil {
		return err
	}
	if _, err := exec.LookPath(cfg.MySQLBin); err != nil {
		return fmt.Errorf("mysql binary %q not found in PATH: %w", cfg.MySQLBin, err)
	}
	if requireLogicalDump {
		if _, err := exec.LookPath(cfg.MySQLDumpBin); err != nil {
			return fmt.Errorf("mysqldump binary %q not found in PATH: %w", cfg.MySQLDumpBin, err)
		}
	}
	if err := pingMySQL(cfg); err != nil {
		return err
	}
	return nil
}

func validateLogicalUploadPrerequisites(cfg config) error {
	if !cfg.LogicalS3UploadEnabled {
		return nil
	}
	switch cfg.S3UploadMode {
	case "", "direct":
		if cfg.S3Bucket == "" {
			return errors.New("logical backup upload requires BACKUP_S3_BUCKET")
		}
		if cfg.S3Region == "" {
			return errors.New("logical backup upload requires BACKUP_S3_REGION")
		}
		if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
			return errors.New("logical backup upload requires S3 credentials")
		}
	case "php", "cli":
		if cfg.S3UploadScript == "" {
			return fmt.Errorf("logical backup upload mode %q requires BACKUP_S3_UPLOAD_SCRIPT", cfg.S3UploadMode)
		}
		if _, err := exec.LookPath(cfg.S3PHPBin); err != nil {
			return fmt.Errorf("php binary %q not found in PATH: %w", cfg.S3PHPBin, err)
		}
	case "http":
		if strings.TrimSpace(cfg.S3UploadURL) == "" {
			return errors.New("logical backup upload mode http requires BACKUP_S3_UPLOAD_URL")
		}
	case "auto":
		if (cfg.S3KeyID != "" && cfg.S3KeySecret != "" && cfg.S3Bucket != "" && cfg.S3Region != "") || cfg.S3UploadScript != "" || cfg.S3UploadURL != "" {
			return nil
		}
		return errors.New("logical backup upload mode auto requires direct S3 credentials, BACKUP_S3_UPLOAD_SCRIPT, or BACKUP_S3_UPLOAD_URL")
	default:
		return fmt.Errorf("unsupported BACKUP_S3_UPLOAD_MODE=%q", cfg.S3UploadMode)
	}
	return nil
}

func runPreflightChecks(cfg config, logicalPlanned bool, physicalPlanned bool) error {
	checks := []string{
		"backup directory writable",
		"log directory writable",
		"metrics path writable",
		"MySQL connectivity",
	}
	if logicalPlanned && cfg.LogicalS3UploadEnabled {
		checks = append(checks, "logical upload prerequisites")
	}
	if physicalPlanned {
		checks = append(checks, "physical backup prerequisites")
	}
	log.Printf("preflight-only run requested; validating %s", strings.Join(checks, ", "))

	if err := validatePrerequisites(cfg, logicalPlanned); err != nil {
		return err
	}
	if err := validateMetricsPathWritable(cfg.MetricsFile); err != nil {
		return fmt.Errorf("metrics path preflight failed: %w", err)
	}
	if logicalPlanned {
		if _, err := listDatabases(cfg); err != nil {
			return err
		}
		if err := validateLogicalUploadPrerequisites(cfg); err != nil {
			return err
		}
	}
	if physicalPlanned {
		check := checkPhysicalHealth(cfg)
		if check.Status == "error" {
			return errors.New(check.Message)
		}
	}
	log.Printf("preflight checks passed")
	return nil
}

func pingMySQL(cfg config) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	bin := cfg.MySQLBin
	if cfg.DBEngine == "postgres" {
		bin = "psql"
	}
	cmd := dbCmdContext(ctx, cfg, bin, "-A", "-t", "-c", "SELECT 1")
	if cfg.DBEngine == "mysql" {
		cmd = dbCmdContext(ctx, cfg, bin, "-N", "-e", "SELECT 1")
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	return nil
}

func RunBackup(ctx context.Context, cfg Config, sinks RunSinks) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	startedAt := time.Now()
	runID := startedAt.Format(runTimestampLayout)
	resourceProfile := buildAdaptiveResourceProfile(cfg, 0)
	logAdaptiveResourceProfile(resourceProfile)
	record := runRecord{
		Timestamp:                time.Now().UTC(),
		RunID:                    runID,
		Status:                   "failed",
		BackupDir:                cfg.BackupDir,
		AdaptiveLoadPerCPU:       resourceProfile.LoadPerCPU,
		AdaptiveLogicalParallel:  resourceProfile.LogicalParallel,
		AdaptivePhysicalParallel: resourceProfile.XtrabackupParallel,
		AdaptiveXbcloudParallel:  resourceProfile.XbcloudParallel,
		AdaptiveTuningReason:     resourceProfile.TuningReason,
	}
	logicalUploadRequired := false
	logicalUploadSucceeded := false
	physicalUploadRequired := false
	physicalUploadSucceeded := false
	logicalDue := false
	physicalDue := false
	logicalScheduleReason := ""
	physicalScheduleReason := ""
	var logCloser io.Closer
	stopRealtimeMetrics := func() {}
	record.OSUser = currentOSUser()
	record.ExecutionSource = normalizedExecutionSource(cfg.ExecutionSource)
	record.Hostname = currentHostname()
	record.PID = os.Getpid()
	console := sinks.Console
	if console == nil {
		console = os.Stdout
	}
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(console)
	defer func() {
		uploadRequired := logicalUploadRequired || physicalUploadRequired
		uploadSucceeded := true
		if logicalUploadRequired && !logicalUploadSucceeded {
			uploadSucceeded = false
		}
		if physicalUploadRequired && !physicalUploadSucceeded {
			uploadSucceeded = false
		}
		stopRealtimeMetrics()
		if uploadRequired || record.Status != "" {
			emitFinalBackupMetrics(cfg, record, startedAt, logicalDue, physicalDue, uploadRequired, uploadSucceeded)
		}
		if logCloser != nil {
			if err := logCloser.Close(); err != nil {
				log.Printf("warning: failed to close run logger: %v", err)
			}
		}
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()

	logCloser, logFilePath, dailyLogPath, err := initRunLogger(cfg.LogDir, runID, console)
	if err != nil {
		log.Printf("warning: persistent run logging is unavailable: %v", err)
	} else {
		record.LogFile = logFilePath
		log.Printf("run log file: %s", logFilePath)
		log.Printf("daily log file: %s", dailyLogPath)
	}

	log.Println("mysql full backup started")
	log.Printf(
		"runtime os_user=%s execution_source=%s hostname=%s pid=%d",
		record.OSUser,
		record.ExecutionSource,
		record.Hostname,
		record.PID,
	)
	log.Printf(
		"using mysql target user=%s host=%s port=%s backup_dir=%s log_dir=%s",
		cfg.DBUser,
		cfg.DBHost,
		cfg.DBPort,
		cfg.BackupDir,
		cfg.LogDir,
	)
	if err := validateMetricsPathWritable(cfg.MetricsFile); err != nil {
		log.Printf("metrics preflight warning path=%s err=%v", resolvedMetricsFilePath(cfg.MetricsFile), err)
	} else {
		stopRealtimeMetrics = startRealtimeBackupMetricsEmitter(cfg, startedAt)
	}

	if shouldBlockScheduledRootRun(record.OSUser, record.ExecutionSource) {
		record.FailureReason = "scheduled backup blocked for root user; use the user-level developer timer/service"
		log.Printf("refusing scheduled backup as root user: %s", record.FailureReason)
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}

	releaseLock, err := acquireLock(cfg.LockFile)
	if err != nil {
		record.FailureReason = err.Error()
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}
	defer releaseLock()

	statePath := filepath.Join(cfg.LogDir, "backup-schedule-state.json")
	state := loadScheduleState(statePath)
	logicalLastSuccess := parseScheduleTimestamp(state.LogicalLastSuccess)
	physicalLastSuccess := parseScheduleTimestamp(state.PhysicalLastSuccess)

	if cfg.LogicalEnabled {
		logicalDue, logicalScheduleReason, err = evaluateSchedule(time.Now(), cfg.LogicalSchedule, logicalLastSuccess)
		if err != nil {
			record.FailureReason = fmt.Sprintf("logical backup schedule error: %v", err)
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
		}
		if logicalDue {
			log.Printf("logical backup: due now %s", logicalScheduleReason)
		} else {
			log.Printf("logical backup: skipped %s", logicalScheduleReason)
		}
	} else {
		log.Printf("logical backup: disabled by BACKUP_LOGICAL_ENABLED=false")
	}

	if cfg.PhysicalEnabled {
		if !cfg.PhysicalS3UploadEnabled {
			log.Printf("physical backup: skipped because BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false and physical mode only supports direct S3 upload")
		} else {
			physicalDue, physicalScheduleReason, err = evaluateSchedule(time.Now(), cfg.PhysicalSchedule, physicalLastSuccess)
			if err != nil {
				record.FailureReason = fmt.Sprintf("physical backup schedule error: %v", err)
				record.ExitCode = finalizeRun(cfg, &record, startedAt)
				return record, nil
			}
			if physicalDue {
				log.Printf("physical backup: due now %s", physicalScheduleReason)
			} else {
				log.Printf("physical backup: skipped %s", physicalScheduleReason)
			}
		}
	} else {
		log.Printf("physical backup: disabled by BACKUP_PHYSICAL_ENABLED=false")
	}

	if !logicalDue && !physicalDue {
		if !cfg.PreflightOnly {
			record.Status = "skipped"
			log.Printf("no backup tasks are due for this run")
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
		}
		log.Printf("no backup tasks are due for this run; continuing with preflight-only validation")
	}

	logicalPlanned := logicalDue || (cfg.PreflightOnly && cfg.LogicalEnabled)
	physicalPlanned := physicalDue || (cfg.PreflightOnly && cfg.PhysicalEnabled && cfg.PhysicalS3UploadEnabled)

	if err := validatePrerequisites(cfg, logicalPlanned); err != nil {
		record.FailureReason = err.Error()
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}
	if cfg.PreflightOnly {
		if err := runPreflightChecks(cfg, logicalPlanned, physicalPlanned); err != nil {
			record.FailureReason = "preflight failed: " + err.Error()
			record.Status = "failed"
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
		}
		record.Status = "success"
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}

	runFolder := filepath.Join(cfg.BackupDir, runID)
	if err := os.MkdirAll(runFolder, 0o755); err != nil {
		record.FailureReason = fmt.Sprintf("failed to create backup folder %s: %v", runFolder, err)
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}
	record.RunFolder = runFolder
	log.Printf("backup folder: %s", runFolder)
	stateDirty := false

	if logicalDue {
		restoreMaxExecTime := tryDisableMaxExecutionTime(cfg)
		defer restoreMaxExecTime()
		logicalCfg := cfg
		logicalCfg.LogicalParallel = resourceProfile.LogicalParallel

		discoveredDatabases, err := listDatabases(logicalCfg)
		if err != nil {
			record.FailureReason = err.Error()
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
		}
		databases := filterDatabases(cfg, discoveredDatabases)
		if len(cfg.LogicalDatabases) == 0 && len(databases) != len(discoveredDatabases) {
			log.Printf("logical backup: excluded %d backup-prefixed database(s) matching bk_* from automatic discovery", len(discoveredDatabases)-len(databases))
		}
		if len(cfg.LogicalDatabases) > 0 {
			log.Printf("logical backup: selected databases=%s matched=%s", strings.Join(cfg.LogicalDatabases, ", "), strings.Join(databases, ", "))
		}
		record.DatabasesTotal = len(databases)

		if len(databases) == 0 {
			log.Println("no user databases found for logical backup")
			state.LogicalLastSuccess = time.Now().UTC().Format(time.RFC3339)
			stateDirty = true
		} else {
			log.Printf("found %d databases: %s", len(databases), strings.Join(databases, ", "))
			log.Printf(
				"logical backup tuning: parallel=%d gzip_level=%d",
				normalizedLogicalParallelism(logicalCfg.LogicalParallel),
				normalizedLogicalGzipLevel(logicalCfg.LogicalGzipLevel),
			)
			tasks, err := prepareLogicalBackupTasks(logicalCfg, runFolder, databases)
			if err != nil {
				record.FailureReason = err.Error()
				record.ExitCode = finalizeRun(cfg, &record, startedAt)
				return record, nil
			}
			for _, result := range executeLogicalBackupTasks(logicalCfg, tasks) {
				record.Results = append(record.Results, result)
				if result.Status == "success" {
					record.DatabasesSucceeded++
					continue
				}
				record.DatabasesFailed++
			}
			if record.DatabasesFailed == 0 {
				state.LogicalLastSuccess = time.Now().UTC().Format(time.RFC3339)
				stateDirty = true
			}
		}
	}

	if physicalDue {
		physicalUploadRequired = true
		log.Printf("physical backup: enabled, starting direct S3 stream")
		physResult := runPhysicalBackup(cfg, runFolder)
		record.PhysicalBackup = &physResult
		if physResult.XtrabackupParallel > 0 {
			record.AdaptivePhysicalParallel = physResult.XtrabackupParallel
		}
		if physResult.XbcloudParallel > 0 {
			record.AdaptiveXbcloudParallel = physResult.XbcloudParallel
		}
		if physResult.Status != "success" {
			log.Printf("physical backup failed: %s", physResult.Error)
			if record.FailureReason == "" {
				record.FailureReason = physResult.Error
			}
		} else {
			physicalUploadSucceeded = true
			state.PhysicalLastSuccess = time.Now().UTC().Format(time.RFC3339)
			stateDirty = true
		}
	}

	cleanupErr := cleanupOldBackups(cfg.BackupDir, runFolder, cfg)
	if cleanupErr != nil {
		record.CleanupError = cleanupErr.Error()
		if cfg.CleanupFailFatal {
			record.FailureReason = cleanupErr.Error()
		} else {
			log.Printf("cleanup warning: %v", cleanupErr)
		}
	}
	if stateDirty {
		if err := saveScheduleState(statePath, state); err != nil {
			log.Printf("warning: failed to save schedule state %s: %v", statePath, err)
		}
	}

	switch {
	case record.DatabasesFailed == 0 && record.FailureReason == "":
		record.Status = "success"
	case record.DatabasesSucceeded > 0:
		record.Status = "partial"
	default:
		record.Status = "failed"
	}
	if record.DatabasesFailed > 0 && record.FailureReason == "" {
		record.FailureReason = fmt.Sprintf("%d of %d database backups failed", record.DatabasesFailed, record.DatabasesTotal)
	}

	if record.RunFolder != "" && logicalDue {
		if err := writeManifest(record.RunFolder, record); err != nil {
			log.Printf("warning: failed to write pre-upload manifest for run=%s: %v", record.RunID, err)
		}
		if cfg.LogicalS3UploadEnabled {
			logicalUploadRequired = true
			if err := uploadBackupToS3(cfg, record.RunFolder); err != nil {
				log.Printf("s3 upload error: %v", err)
				markLogicalUploadFailure(&record, err)
			} else {
				logicalUploadSucceeded = true
				markLogicalUploadSuccess(&record)
			}
		} else {
			note := "logical backup S3 upload skipped by BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false or logical backup not scheduled"
			markLogicalUploadSkipped(&record, note)
			log.Printf("%s", note)
		}
	}
	record.ExitCode = finalizeRun(cfg, &record, startedAt)
	return record, nil
}

func RunFromEnvFile(ctx context.Context, envPath string, sinks RunSinks) (RunResult, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return RunResult{}, err
	}
	return RunBackup(ctx, cfg, sinks)
}
