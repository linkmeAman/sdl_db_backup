package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

const runTimestampLayout = "2006-01-02_15-04-05"

type config struct {
	DBUser                  string
	DBPass                  string
	DBHost                  string
	DBPort                  string
	BackupDir               string
	LogDir                  string
	RunLogPath              string
	LockFile                string
	MySQLBin                string
	MySQLDumpBin            string
	RetryCount              int
	RetentionDays           int
	LogicalEnabled          bool
	LogicalTimeoutPerDB     time.Duration
	LogicalS3UploadEnabled  bool
	LogicalSchedule         string
	PhysicalSchedule        string
	PhysicalTimeout         time.Duration
	DiscoveryTimeout        time.Duration
	PreflightTimeout        time.Duration
	RetryBaseDelay          time.Duration
	RetryMaxDelay           time.Duration
	CleanupFailFatal        bool
	S3UploadURL             string
	S3UploadTimeout         time.Duration
	S3PHPBin                string
	S3UploadScript          string
	S3PhysicalPrefix        string
	S3Bucket                string
	S3Region                string
	S3KeyID                 string
	S3KeySecret             string
	XbcloudBin              string
	PhysicalEnabled         bool
	PhysicalS3UploadEnabled bool
	XtrabackupBin           string
	XtrabackupParallel      int
	XtrabackupUser          string
	XtrabackupPass          string
	XtrabackupSocket        string
	XtrabackupRunAsUser     string
	XtrabackupWorkDir       string
}

type physicalBackupResult struct {
	Status    string `json:"status"`
	TargetDir string `json:"target_dir,omitempty"`
	Duration  string `json:"duration"`
	Error     string `json:"error,omitempty"`
}

type databaseResult struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	Duration      string `json:"duration"`
	OutputPath    string `json:"output_path,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
	Error         string `json:"error,omitempty"`
}

type runRecord struct {
	Timestamp          time.Time             `json:"timestamp"`
	RunID              string                `json:"run_id"`
	Status             string                `json:"status"`
	BackupDir          string                `json:"backup_dir"`
	RunFolder          string                `json:"run_folder,omitempty"`
	LogFile            string                `json:"log_file,omitempty"`
	FailureReason      string                `json:"failure_reason,omitempty"`
	CleanupError       string                `json:"cleanup_error,omitempty"`
	Duration           string                `json:"duration"`
	DatabasesTotal     int                   `json:"databases_total"`
	DatabasesSucceeded int                   `json:"databases_succeeded"`
	DatabasesFailed    int                   `json:"databases_failed"`
	Results            []databaseResult      `json:"results,omitempty"`
	PhysicalBackup     *physicalBackupResult `json:"physical_backup,omitempty"`
}

type scheduleState struct {
	LogicalLastSuccess  string `json:"logical_last_success,omitempty"`
	PhysicalLastSuccess string `json:"physical_last_success,omitempty"`
}

type countingWriter struct {
	writer io.Writer
	bytes  int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.writer.Write(p)
	cw.bytes += int64(n)
	return n, err
}

var systemDBs = []string{
	"information_schema",
	"performance_schema",
	"mysql",
	"sys",
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getenvAny(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func getenvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		log.Printf("warning: invalid integer for %s=%q; using %d", key, raw, fallback)
		return fallback
	}
	return value
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		log.Printf("warning: invalid duration for %s=%q; using %s", key, raw, fallback)
		return fallback
	}
	return value
}

func getenvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("warning: invalid boolean for %s=%q; using %t", key, raw, fallback)
		return fallback
	}
	return value
}

func loadEnvFile() string {
	if explicit := strings.TrimSpace(os.Getenv("BACKUP_ENV_FILE")); explicit != "" {
		if err := godotenv.Load(explicit); err != nil {
			log.Printf("warning: could not load BACKUP_ENV_FILE=%s: %v", explicit, err)
			return ""
		}
		log.Printf("loaded env from %s", explicit)
		return explicit
	}

	candidates := []string{
		".env",
		filepath.Join("sdl_db_backup", ".env"),
	}
	for _, path := range candidates {
		if err := godotenv.Load(path); err == nil {
			log.Printf("loaded env from %s", path)
			return path
		}
	}

	log.Printf("warning: no .env file found in known locations; using process env")
	return ""
}

func parseScheduleTimestamp(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func loadScheduleState(path string) scheduleState {
	var state scheduleState
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("warning: could not parse schedule state %s: %v", path, err)
		return scheduleState{}
	}
	return state
}

func saveScheduleState(path string, state scheduleState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o640)
}

func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return hour, minute, nil
}

func parseWeekday(raw string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, fmt.Errorf("invalid weekday %q", raw)
	}
}

func parseDailyTimes(raw string) ([]time.Duration, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return nil, fmt.Errorf("expected at least one HH:MM time")
	}

	seen := make(map[time.Duration]struct{}, len(parts))
	times := make([]time.Duration, 0, len(parts))
	for _, part := range parts {
		hour, minute, err := parseClock(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		tod := time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute
		if _, ok := seen[tod]; ok {
			continue
		}
		seen[tod] = struct{}{}
		times = append(times, tod)
	}
	if len(times) == 0 {
		return nil, fmt.Errorf("expected at least one HH:MM time")
	}
	slices.Sort(times)
	return times, nil
}

func latestDueDailyTarget(now time.Time, times []time.Duration) (time.Time, bool) {
	if len(times) == 0 {
		return time.Time{}, false
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for i := len(times) - 1; i >= 0; i-- {
		target := today.Add(times[i])
		if !now.Before(target) {
			return target, true
		}
	}

	return time.Time{}, false
}

func evaluateSchedule(now time.Time, raw string, lastSuccess time.Time) (bool, string, error) {
	schedule := strings.ToLower(strings.TrimSpace(raw))
	if schedule == "" || schedule == "always" {
		return true, "schedule=always", nil
	}
	if schedule == "disabled" || schedule == "off" || schedule == "never" {
		return false, fmt.Sprintf("schedule=%s", raw), nil
	}

	if strings.HasPrefix(schedule, "interval@") {
		interval, err := time.ParseDuration(strings.TrimSpace(schedule[len("interval@"):]))
		if err != nil || interval <= 0 {
			return false, "", fmt.Errorf("invalid schedule %q: expected interval@<duration>", raw)
		}
		if lastSuccess.IsZero() {
			return true, fmt.Sprintf("schedule=%s first run", raw), nil
		}
		nextRun := lastSuccess.Add(interval)
		if !now.Before(nextRun) {
			return true, fmt.Sprintf("schedule=%s due since %s", raw, nextRun.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s next due at %s", raw, nextRun.Format(time.RFC3339)), nil
	}

	if strings.HasPrefix(schedule, "daily") {
		timeSpec := "00:00"
		if strings.Contains(schedule, "@") {
			timeSpec = strings.TrimSpace(strings.SplitN(schedule, "@", 2)[1])
		}
		times, err := parseDailyTimes(timeSpec)
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		target, ok := latestDueDailyTarget(now, times)
		if !ok {
			nextTarget := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(times[0])
			return false, fmt.Sprintf("schedule=%s waiting until %s", raw, nextTarget.Format(time.RFC3339)), nil
		}
		if lastSuccess.IsZero() || lastSuccess.Before(target) {
			return true, fmt.Sprintf("schedule=%s due at %s", raw, target.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s already satisfied for %s", raw, target.Format(time.RFC3339)), nil
	}

	if strings.HasPrefix(schedule, "weekly") {
		spec := "sun,00:00"
		if strings.Contains(schedule, "@") {
			spec = strings.TrimSpace(strings.SplitN(schedule, "@", 2)[1])
		}
		parts := strings.SplitN(spec, ",", 2)
		if len(parts) != 2 {
			return false, "", fmt.Errorf("invalid schedule %q: expected weekly@<weekday>,HH:MM", raw)
		}
		weekday, err := parseWeekday(parts[0])
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		hour, minute, err := parseClock(parts[1])
		if err != nil {
			return false, "", fmt.Errorf("invalid schedule %q: %v", raw, err)
		}
		daysBack := (7 + int(now.Weekday()) - int(weekday)) % 7
		targetDate := now.AddDate(0, 0, -daysBack)
		target := time.Date(targetDate.Year(), targetDate.Month(), targetDate.Day(), hour, minute, 0, 0, now.Location())
		if now.Before(target) {
			nextTarget := target
			return false, fmt.Sprintf("schedule=%s waiting until %s", raw, nextTarget.Format(time.RFC3339)), nil
		}
		if lastSuccess.IsZero() || lastSuccess.Before(target) {
			return true, fmt.Sprintf("schedule=%s due at %s", raw, target.Format(time.RFC3339)), nil
		}
		return false, fmt.Sprintf("schedule=%s already satisfied for %s", raw, target.Format(time.RFC3339)), nil
	}

	return false, "", fmt.Errorf("invalid schedule %q: supported values are always, disabled, daily@HH:MM[,HH:MM...], weekly@day,HH:MM, interval@24h", raw)
}

func loadConfig() config {
	_ = loadEnvFile()

	dbUser := getenv("DB_USER", "")
	dbPass := getenv("DB_PASS", "")
	if dbPass == "" {
		dbPass = getenv("DB_PASSWORD", "")
	}
	if dbPass == "" {
		dbPass = getenv("MYSQL_PASS", "")
	}

	backupDir := getenv("BACKUP_DIR", "/mnt/volume_1/backup/mysql_backup")
	logDir := getenv("BACKUP_LOG_DIR", filepath.Join(backupDir, "logs"))
	logicalTimeoutPerDB := getenvDuration("BACKUP_LOGICAL_TIMEOUT_PER_DB", 0)
	if logicalTimeoutPerDB <= 0 {
		logicalTimeoutPerDB = getenvDuration("BACKUP_TIMEOUT_PER_DB", 30*time.Minute)
	}
	s3KeyID := getenvAny("BACKUP_S3_KEY_ID", "BACKUP_S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
	s3KeySecret := getenvAny("BACKUP_S3_KEY_SECRET", "BACKUP_S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")

	return config{
		DBUser:                  dbUser,
		DBPass:                  dbPass,
		DBHost:                  getenv("DB_HOST", "127.0.0.1"),
		DBPort:                  getenv("DB_PORT", "3306"),
		BackupDir:               backupDir,
		LogDir:                  logDir,
		RunLogPath:              filepath.Join(logDir, "backup-runs.jsonl"),
		LockFile:                getenv("BACKUP_LOCK_FILE", filepath.Join(logDir, "backup.lock")),
		MySQLBin:                getenv("MYSQL_BIN", "mysql"),
		MySQLDumpBin:            getenv("MYSQLDUMP_BIN", "mysqldump"),
		RetryCount:              getenvInt("BACKUP_RETRY_COUNT", 3),
		RetentionDays:           getenvInt("BACKUP_RETENTION_DAYS", 5),
		LogicalEnabled:          getenvBool("BACKUP_LOGICAL_ENABLED", true),
		LogicalTimeoutPerDB:     logicalTimeoutPerDB,
		LogicalS3UploadEnabled:  getenvBool("BACKUP_LOGICAL_S3_UPLOAD_ENABLED", true),
		LogicalSchedule:         getenv("BACKUP_LOGICAL_SCHEDULE", "always"),
		PhysicalSchedule:        getenv("BACKUP_PHYSICAL_SCHEDULE", "always"),
		PhysicalTimeout:         getenvDuration("BACKUP_PHYSICAL_TIMEOUT", 6*time.Hour),
		DiscoveryTimeout:        getenvDuration("BACKUP_DISCOVERY_TIMEOUT", 30*time.Second),
		PreflightTimeout:        getenvDuration("BACKUP_PREFLIGHT_TIMEOUT", 15*time.Second),
		RetryBaseDelay:          getenvDuration("BACKUP_RETRY_BASE_DELAY", 2*time.Second),
		RetryMaxDelay:           getenvDuration("BACKUP_RETRY_MAX_DELAY", 20*time.Second),
		CleanupFailFatal:        getenvBool("BACKUP_CLEANUP_FAIL_FATAL", false),
		S3UploadURL:             getenv("BACKUP_S3_UPLOAD_URL", ""),
		S3UploadTimeout:         getenvDuration("BACKUP_S3_UPLOAD_TIMEOUT", 2*time.Hour),
		S3PHPBin:                getenv("BACKUP_S3_PHP_BIN", "php"),
		S3UploadScript:          getenv("BACKUP_S3_UPLOAD_SCRIPT", ""),
		S3PhysicalPrefix:        getenv("BACKUP_S3_PHYSICAL_PREFIX", "backup-as-it-is"),
		S3Bucket:                getenv("BACKUP_S3_BUCKET", "ticklerightbackups"),
		S3Region:                getenv("BACKUP_S3_REGION", "ap-south-1"),
		S3KeyID:                 s3KeyID,
		S3KeySecret:             s3KeySecret,
		XbcloudBin:              getenv("BACKUP_XBCLOUD_BIN", "xbcloud"),
		PhysicalEnabled:         getenvBool("BACKUP_PHYSICAL_ENABLED", true),
		PhysicalS3UploadEnabled: getenvBool("BACKUP_PHYSICAL_S3_UPLOAD_ENABLED", true),
		XtrabackupBin:           getenv("BACKUP_XTRABACKUP_BIN", "xtrabackup"),
		XtrabackupParallel:      getenvInt("BACKUP_XTRABACKUP_PARALLEL", 4),
		XtrabackupUser:          getenv("BACKUP_XTRABACKUP_USER", dbUser),
		XtrabackupPass:          getenv("BACKUP_XTRABACKUP_PASS", dbPass),
		XtrabackupSocket:        getenv("BACKUP_XTRABACKUP_SOCKET", ""),
		XtrabackupRunAsUser:     getenv("BACKUP_XTRABACKUP_RUN_AS_USER", ""),
		XtrabackupWorkDir:       getenv("BACKUP_XTRABACKUP_WORK_DIR", "/tmp"),
	}
}

func initRunLogger(logDir, runID string) (*os.File, string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create log dir %s: %w", logDir, err)
	}
	logFilePath := filepath.Join(logDir, runID+".log")
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, "", fmt.Errorf("open run log %s: %w", logFilePath, err)
	}
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	return logFile, logFilePath, nil
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

func pingMySQL(cfg config) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", "SELECT 1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		category := classifyFailure(err, message)
		return fmt.Errorf("mysql connectivity check failed (%s): %s", category, chooseFailureMessage(err, message))
	}
	return nil
}

func cleanupOldBackups(backupDir, currentRun string, retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	currentRun = filepath.Clean(currentRun)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		runTime, err := time.Parse(runTimestampLayout, entry.Name())
		if err != nil {
			// Not a timestamped backup folder (e.g. logs/); skip.
			continue
		}

		path := filepath.Clean(filepath.Join(backupDir, entry.Name()))
		if currentRun != "" && path == currentRun {
			continue
		}
		if runTime.Before(cutoff) {
			log.Printf("deleting old backup folder: %s", path)
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("delete old backup %s: %w", path, err)
			}
		}
	}
	return nil
}

func mysqlCmdContext(ctx context.Context, cfg config, bin string, args ...string) *exec.Cmd {
	base := []string{"-h", cfg.DBHost, "-P", cfg.DBPort, "-u", cfg.DBUser}
	all := append(base, args...)
	cmd := exec.CommandContext(ctx, bin, all...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPass)
	return cmd
}

func listDatabases(cfg config) ([]string, error) {
	log.Printf("discovering databases with %s", cfg.MySQLBin)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DiscoveryTimeout)
	defer cancel()

	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", "SHOW DATABASES")
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("mysql database discovery failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	var databases []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		dbName := strings.TrimSpace(scanner.Text())
		if dbName == "" || slices.Contains(systemDBs, dbName) {
			continue
		}
		databases = append(databases, dbName)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return databases, nil
}

func removePartialOutput(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not remove partial backup %s: %v", path, err)
	}
}

func discoverBrokenViews(cfg config, dbName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA=%q", dbName)
	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("list views failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	var broken []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		viewName := strings.TrimSpace(scanner.Text())
		if viewName == "" {
			continue
		}

		vCtx, vCancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
		check := mysqlCmdContext(vCtx, cfg, cfg.MySQLBin, "-D", dbName, "-N", "-e", fmt.Sprintf("SHOW FIELDS FROM `%s`", viewName))
		checkOut, checkErr := check.CombinedOutput()
		vCancel()
		if checkErr == nil {
			continue
		}
		message := strings.TrimSpace(string(checkOut))
		if classifyFailure(checkErr, message) == "view" {
			broken = append(broken, viewName)
			continue
		}

		log.Printf("warning: view precheck failed database=%s view=%s error=%s", dbName, viewName, chooseFailureMessage(checkErr, message))
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan views output: %w", err)
	}
	return broken, nil
}

func dumpDatabase(cfg config, dbName, outFile string, ignoreTables []string) (int64, error) {
	log.Printf("starting dump for database=%s", dbName)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.LogicalTimeoutPerDB)
	defer cancel()

	file, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}

	closeFile := func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("warning: could not close output file %s: %v", outFile, closeErr)
		}
	}

	counter := &countingWriter{writer: file}
	gz := gzip.NewWriter(counter)

	args := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--events",
		"--set-gtid-purged=OFF",
		"--force",                       // continue on SQL errors (required for --ignore-error to take effect)
		"--ignore-error=1356,1449,1227", // skip broken views, missing definers, definer-privilege errors
		"--databases", dbName,
	}
	for _, table := range ignoreTables {
		args = append(args, "--ignore-table="+dbName+"."+table)
	}
	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLDumpBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeFile()
		removePartialOutput(outFile)
		return 0, err
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		closeFile()
		removePartialOutput(outFile)
		return 0, err
	}

	if _, err := io.Copy(gz, stdout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = gz.Close()
		closeFile()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("stream mysqldump output: %w", err)
	}

	if err := gz.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		closeFile()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("finalize gzip: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("close gzip file: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		removePartialOutput(outFile)
		message := strings.TrimSpace(stderr.String())
		return 0, fmt.Errorf("mysqldump failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	// Log any warnings mysqldump emitted (e.g. objects skipped via --ignore-error).
	if warnings := strings.TrimSpace(stderr.String()); warnings != "" {
		for _, line := range strings.Split(warnings, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				log.Printf("mysqldump warning database=%s: %s", dbName, line)
			}
		}
	}

	if counter.bytes == 0 {
		removePartialOutput(outFile)
		return 0, fmt.Errorf("mysqldump produced empty output for database=%s", dbName)
	}

	log.Printf("completed dump for database=%s output=%s size_bytes=%d", dbName, outFile, counter.bytes)
	return counter.bytes, nil
}

func dumpWithRetry(cfg config, dbName, outFile string) databaseResult {
	started := time.Now()
	result := databaseResult{Name: dbName, Status: "failed"}
	brokenViews, precheckErr := discoverBrokenViews(cfg, dbName)
	if precheckErr != nil {
		log.Printf("warning: could not precheck views for database=%s: %v", dbName, precheckErr)
	}
	if len(brokenViews) > 0 {
		log.Printf("database=%s: precheck found %d broken view(s), excluding from dump: %s", dbName, len(brokenViews), strings.Join(brokenViews, ", "))
	}

	for attempt := 1; attempt <= cfg.RetryCount; attempt++ {
		result.Attempts = attempt
		sizeBytes, err := dumpDatabase(cfg, dbName, outFile, brokenViews)
		if err == nil {
			result.Status = "success"
			result.OutputPath = outFile
			result.SizeBytes = sizeBytes
			result.Duration = time.Since(started).Round(time.Millisecond).String()
			return result
		}

		result.Error = err.Error()
		result.ErrorCategory = classifyFailure(err, err.Error())
		log.Printf("attempt %d/%d failed for database=%s category=%s err=%v", attempt, cfg.RetryCount, dbName, result.ErrorCategory, err)

		if attempt == cfg.RetryCount || !shouldRetry(result.ErrorCategory) {
			break
		}

		delay := retryDelay(cfg, attempt)
		log.Printf("retrying database=%s in %s (attempt %d/%d)", dbName, delay, attempt+1, cfg.RetryCount)
		time.Sleep(delay)
	}

	result.Duration = time.Since(started).Round(time.Millisecond).String()
	return result
}

func retryDelay(cfg config, attempt int) time.Duration {
	delay := cfg.RetryBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= cfg.RetryMaxDelay {
			return cfg.RetryMaxDelay
		}
	}
	if delay > cfg.RetryMaxDelay {
		return cfg.RetryMaxDelay
	}
	return delay
}

func shouldRetry(category string) bool {
	switch category {
	case "auth", "permission", "disk", "view", "definer", "schema", "config", "binary":
		return false
	default:
		return true
	}
}

func classifyFailure(err error, detail string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	text := strings.ToLower(strings.TrimSpace(detail + " " + err.Error()))
	switch {
	case strings.Contains(text, "access denied"):
		return "auth"
	case strings.Contains(text, "permission denied"):
		return "permission"
	case strings.Contains(text, "no space left on device"), strings.Contains(text, "disk full"):
		return "disk"
	case strings.Contains(text, "references invalid table"), strings.Contains(text, "1356"):
		return "view"
	case strings.Contains(text, "can't connect"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "server has gone away"), strings.Contains(text, "connection reset"),
		strings.Contains(text, "no route to host"):
		return "connect"
	case strings.Contains(text, "deadline exceeded"), strings.Contains(text, "timed out"),
		strings.Contains(text, "error 3024"), strings.Contains(text, "max_execution_time"):
		return "timeout"
	case strings.Contains(text, "unknown variable"), strings.Contains(text, "unknown option"):
		return "config"
	case strings.Contains(text, "executable file not found"):
		return "binary"
	case strings.Contains(text, "user specified as a definer"), strings.Contains(text, "1449"):
		return "definer"
	case strings.Contains(text, "doesn't exist"), strings.Contains(text, "1146"):
		return "schema"
	case strings.Contains(text, "max_allowed_packet"), strings.Contains(text, "got a packet bigger"), strings.Contains(text, "1153"):
		return "config"
	default:
		return "command"
	}
}

func chooseFailureMessage(err error, detail string) string {
	trimmed := strings.TrimSpace(detail)
	if trimmed != "" {
		return trimmed
	}
	return err.Error()
}

func appendRunRecord(path string, record runRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func writeManifest(runFolder string, record runRecord) error {
	manifestPath := filepath.Join(runFolder, "manifest.json")
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(encoded, '\n'), 0o640)
}

func finalizeRun(cfg config, record *runRecord, startedAt time.Time) int {
	if record.Duration == "" {
		record.Duration = time.Since(startedAt).Round(time.Millisecond).String()
	}

	if record.RunFolder != "" {
		if err := writeManifest(record.RunFolder, *record); err != nil {
			log.Printf("warning: failed to write manifest for run=%s: %v", record.RunID, err)
		}
	}
	if err := appendRunRecord(cfg.RunLogPath, *record); err != nil {
		log.Printf("warning: failed to append run record %s: %v", cfg.RunLogPath, err)
	}

	log.Printf(
		"backup summary status=%s total=%d success=%d failed=%d duration=%s",
		record.Status,
		record.DatabasesTotal,
		record.DatabasesSucceeded,
		record.DatabasesFailed,
		record.Duration,
	)
	if record.FailureReason != "" {
		log.Printf("backup failure reason: %s", record.FailureReason)
	}
	if record.CleanupError != "" {
		log.Printf("backup cleanup issue: %s", record.CleanupError)
	}

	if record.Status == "success" {
		return 0
	}
	return 1
}

// tryDisableMaxExecutionTime attempts to set @@GLOBAL.max_execution_time=0 so
// mysqldump SELECT queries are not killed mid-table. Returns a restore function.
// If the user lacks SYSTEM_VARIABLES_ADMIN, logs a warning and returns a no-op.
func tryDisableMaxExecutionTime(cfg config) func() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-s", "-e", "SELECT @@GLOBAL.max_execution_time")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("max_execution_time: could not read current value: %v", strings.TrimSpace(string(out)))
		return func() {}
	}
	currentVal := strings.TrimSpace(string(out))
	if currentVal == "0" || currentVal == "" {
		log.Printf("max_execution_time: already 0, dumps will not be interrupted")
		return func() {}
	}

	log.Printf("max_execution_time: server value is %s ms — attempting to disable for this backup run", currentVal)

	setCtx, setCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setCancel()
	if setErr := mysqlCmdContext(setCtx, cfg, cfg.MySQLBin, "-N", "-e", "SET GLOBAL max_execution_time=0").Run(); setErr != nil {
		log.Printf("warning: cannot disable max_execution_time (user lacks SYSTEM_VARIABLES_ADMIN?): %v", setErr)
		log.Printf("warning: large-table dumps may still hit the %s ms server timeout", currentVal)
		return func() {}
	}
	log.Printf("max_execution_time: disabled (0) for backup run; will restore to %s ms afterward", currentVal)

	return func() {
		rCtx, rCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rCancel()
		restoreSQL := fmt.Sprintf("SET GLOBAL max_execution_time=%s", currentVal)
		if rErr := mysqlCmdContext(rCtx, cfg, cfg.MySQLBin, "-N", "-e", restoreSQL).Run(); rErr != nil {
			log.Printf("warning: could not restore max_execution_time to %s ms: %v", currentVal, rErr)
		} else {
			log.Printf("max_execution_time: restored to %s ms", currentVal)
		}
	}
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

func uploadBackupToS3(cfg config, runFolder string) {
	log.Printf("s3 upload: starting for folder %s", runFolder)
	if cfg.S3UploadScript != "" {
		uploadBackupViaCLI(cfg, runFolder)
		return
	}
	if cfg.S3UploadURL != "" {
		uploadBackupViaHTTP(cfg, runFolder)
		return
	}
	log.Printf("s3 upload skipped: neither BACKUP_S3_UPLOAD_SCRIPT nor BACKUP_S3_UPLOAD_URL is configured")
}

func uploadBackupViaCLI(cfg config, runFolder string) {
	log.Printf("s3 upload: using PHP CLI %s %s", cfg.S3PHPBin, cfg.S3UploadScript)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.S3UploadTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cfg.S3PHPBin, cfg.S3UploadScript, runFolder)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	// Stream PHP's progress (STDERR) directly to our logger line by line.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("s3 upload CLI error: stderr pipe: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("s3 upload CLI error: start: %v", err)
		return
	}
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		log.Printf("s3 upload: %s", scanner.Text())
	}
	if runErr := cmd.Wait(); runErr != nil {
		log.Printf("s3 upload CLI error: %v", runErr)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &result); err != nil {
		log.Printf("s3 upload CLI: could not parse output: %v | raw: %s", err, strings.TrimSpace(stdout.String()))
		return
	}
	logS3Result(result)
}

func uploadBackupViaHTTP(cfg config, runFolder string) {
	log.Printf("s3 upload: using HTTP endpoint %s", cfg.S3UploadURL)

	payload, err := json.Marshal(map[string]string{
		"action":     "uploadBackupFolder",
		"backupPath": runFolder,
	})
	if err != nil {
		log.Printf("s3 upload HTTP error: marshal payload: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.S3UploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.S3UploadURL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("s3 upload HTTP error: create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Printf("s3 upload HTTP error: %v", err)
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("s3 upload HTTP: failed to parse response (status %d): %v", resp.StatusCode, err)
		return
	}
	logS3Result(result)
}

// runXtrabackupCmd runs xtrabackup with the given args, streaming its stderr
// to the logger line by line. If runAsUser is set, it executes via
// "sudo -n -u <runAsUser>" so xtrabackup can read protected MySQL datadirs.
func runXtrabackupCmd(bin string, args []string, runAsUser string) error {
	var cmd *exec.Cmd
	if runAsUser != "" {
		sudoArgs := append([]string{"-n", "-u", runAsUser, bin}, args...)
		cmd = exec.Command("sudo", sudoArgs...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	var lastLine, diskErrLine string
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line != "" {
			lastLine = line
		}
		// Capture the most recent disk-full message so it surfaces in the error.
		lower := strings.ToLower(line)
		if strings.Contains(lower, "no space left") || strings.Contains(lower, "errno 28") {
			diskErrLine = line
		}
		log.Printf("xtrabackup: %s", raw)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return fmt.Errorf("read xtrabackup stderr: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		summary := lastLine
		if diskErrLine != "" {
			summary = diskErrLine
		}
		if summary != "" {
			return fmt.Errorf("%w (%s)", err, summary)
		}
		return err
	}
	return nil
}

// writeXtrabackupCredsFile writes a temporary MySQL defaults file containing
// the xtrabackup credentials. The caller must delete the file when done.
// When xtrabackup runs as another OS user (for example mysql via sudo),
// the file must be readable by that user.
func writeXtrabackupCredsFile(cfg config, runAsUser string) (string, error) {
	f, err := os.CreateTemp("", "xtrabackup-creds-*.cnf")
	if err != nil {
		return "", fmt.Errorf("create xtrabackup creds file: %w", err)
	}
	defer f.Close()
	mode := os.FileMode(0o600)
	if runAsUser != "" {
		// xtrabackup executes as another user (usually mysql), so the temporary
		// defaults file must be readable by that process.
		mode = 0o644
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod xtrabackup creds file: %w", err)
	}
	_, err = fmt.Fprintf(f, "[xtrabackup]\npassword=%s\n", cfg.XtrabackupPass)
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write xtrabackup creds file: %w", err)
	}
	return f.Name(), nil
}

// checkXtrabackupPrivileges verifies the xtrabackup user has the BACKUP_ADMIN
// privilege required by xtrabackup 8.0. Returns a descriptive error with the
// exact GRANT statement if the privilege is missing.
func checkXtrabackupPrivileges(cfg config) error {
	runSQL := func(query string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
		defer cancel()

		args := []string{"-u", cfg.XtrabackupUser, "-N", "-s", "-e", query}
		if cfg.XtrabackupSocket != "" {
			args = append([]string{"-S", cfg.XtrabackupSocket}, args...)
		} else {
			args = append([]string{"-h", cfg.DBHost, "-P", cfg.DBPort}, args...)
		}
		cmd := exec.CommandContext(ctx, cfg.MySQLBin, args...)
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.XtrabackupPass)
		out, err := cmd.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(out))
			return "", fmt.Errorf("%s", chooseFailureMessage(err, message))
		}
		return strings.TrimSpace(string(out)), nil
	}

	backupAdminCount, err := runSQL("SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES WHERE PRIVILEGE_TYPE='BACKUP_ADMIN'")
	if err != nil {
		return fmt.Errorf("xtrabackup privilege check: cannot connect as %q: %v", cfg.XtrabackupUser, err)
	}
	if backupAdminCount == "0" || backupAdminCount == "" {
		return fmt.Errorf(
			"xtrabackup user %q lacks BACKUP_ADMIN privilege; grant it with:\n"+
				"  GRANT BACKUP_ADMIN, PROCESS, RELOAD, LOCK TABLES, REPLICATION CLIENT ON *.* TO '%s'@'localhost';\n"+
				"  FLUSH PRIVILEGES;",
			cfg.XtrabackupUser, cfg.XtrabackupUser)
	}

	perfChecks := []struct {
		table string
		query string
	}{
		{table: "replication_group_members", query: "SELECT 1 FROM performance_schema.replication_group_members LIMIT 1"},
		{table: "keyring_component_status", query: "SELECT 1 FROM performance_schema.keyring_component_status LIMIT 1"},
	}
	for _, check := range perfChecks {
		_, err := runSQL(check.query)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "doesn't exist") || strings.Contains(msg, "unknown table") || strings.Contains(msg, "1146") {
			// Table not present on this server flavor/version; ignore.
			continue
		}
		if strings.Contains(msg, "1142") || strings.Contains(msg, "select command denied") {
			return fmt.Errorf(
				"xtrabackup user %q lacks SELECT on performance_schema.%s; grant it with:\n"+
					"  GRANT SELECT ON performance_schema.%s TO '%s'@'localhost';\n"+
					"  FLUSH PRIVILEGES;",
				cfg.XtrabackupUser, check.table, check.table, cfg.XtrabackupUser)
		}
		return fmt.Errorf("xtrabackup privilege check failed on performance_schema.%s: %v", check.table, err)
	}

	return nil
}

// runPhysicalBackup streams xtrabackup directly to S3 via xbcloud.
// No local physical directory is created.
func runPhysicalBackup(cfg config, runDir string) physicalBackupResult {
	started := time.Now()
	result := physicalBackupResult{Status: "failed"}
	runID := filepath.Base(runDir)
	objectKey := strings.Trim(strings.TrimSpace(cfg.S3PhysicalPrefix), "/") + "/" + runID + "/physical.xbstream"
	result.TargetDir = fmt.Sprintf("s3://%s/%s", cfg.S3Bucket, objectKey)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PhysicalTimeout)
	defer cancel()

	if cfg.S3Bucket == "" {
		result.Error = "physical backup requires BACKUP_S3_BUCKET"
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		result.Error = "physical backup requires S3 credentials (set BACKUP_S3_KEY_ID and BACKUP_S3_KEY_SECRET, or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)"
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	xtrabackupBin, err := exec.LookPath(cfg.XtrabackupBin)
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup binary %q not found in PATH: %v (install Percona XtraBackup or set BACKUP_XTRABACKUP_BIN)", cfg.XtrabackupBin, err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	xbcloudBin, err := exec.LookPath(cfg.XbcloudBin)
	if err != nil {
		result.Error = fmt.Sprintf("xbcloud binary %q not found in PATH: %v", cfg.XbcloudBin, err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	if err := checkXtrabackupPrivileges(cfg); err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		log.Printf("physical backup: privilege check failed: %s", result.Error)
		return result
	}

	credsFile, err := writeXtrabackupCredsFile(cfg, cfg.XtrabackupRunAsUser)
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	defer func() { _ = os.Remove(credsFile) }()

	backupArgs := []string{
		"--defaults-extra-file=" + credsFile,
		"--no-version-check",
		"--backup",
		"--stream=xbstream",
		"--user=" + cfg.XtrabackupUser,
		"--parallel=" + strconv.Itoa(cfg.XtrabackupParallel),
	}
	if cfg.XtrabackupSocket != "" {
		backupArgs = append(backupArgs, "--socket="+cfg.XtrabackupSocket)
	} else {
		backupArgs = append(backupArgs, "--host="+cfg.DBHost, "--port="+cfg.DBPort)
	}

	var xtrabackupCmd *exec.Cmd
	if cfg.XtrabackupRunAsUser != "" {
		sudoArgs := append([]string{"-n", "-u", cfg.XtrabackupRunAsUser, xtrabackupBin}, backupArgs...)
		xtrabackupCmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
		log.Printf("physical backup: running xtrabackup as OS user=%s", cfg.XtrabackupRunAsUser)
	} else {
		xtrabackupCmd = exec.CommandContext(ctx, xtrabackupBin, backupArgs...)
	}
	workDir := strings.TrimSpace(cfg.XtrabackupWorkDir)
	if workDir == "" {
		workDir = "/tmp"
	}
	xtrabackupCmd.Dir = workDir

	streamOut, err := xtrabackupCmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup stdout pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	xtrabackupErr, err := xtrabackupCmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup stderr pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	xbcloudArgs := []string{
		"put",
		"--storage=s3",
		"--s3-bucket=" + cfg.S3Bucket,
		"--s3-region=" + cfg.S3Region,
		objectKey,
	}
	xbcloudCmd := exec.CommandContext(ctx, xbcloudBin, xbcloudArgs...)
	xbcloudCmd.Dir = workDir
	xbcloudCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.S3KeyID,
		"AWS_SECRET_ACCESS_KEY="+cfg.S3KeySecret,
		"AWS_DEFAULT_REGION="+cfg.S3Region,
	)
	xbcloudCmd.Stdin = streamOut
	xbcloudErr, err := xbcloudCmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xbcloud stderr pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	log.Printf("physical backup: using working directory %s", workDir)
	log.Printf("physical backup: streaming directly to s3://%s/%s", cfg.S3Bucket, objectKey)

	if err := xtrabackupCmd.Start(); err != nil {
		result.Error = fmt.Sprintf("start xtrabackup: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	if err := xbcloudCmd.Start(); err != nil {
		_ = xtrabackupCmd.Process.Kill()
		_ = xtrabackupCmd.Wait()
		result.Error = fmt.Sprintf("start xbcloud: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	type loggedResult struct {
		lastLine string
		diskLine string
		err      error
	}
	logPipe := func(prefix string, reader io.Reader) <-chan loggedResult {
		ch := make(chan loggedResult, 1)
		go func() {
			defer close(ch)
			var res loggedResult
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				raw := scanner.Text()
				line := strings.TrimSpace(raw)
				if line != "" {
					res.lastLine = line
				}
				lower := strings.ToLower(line)
				if strings.Contains(lower, "no space left") || strings.Contains(lower, "errno 28") {
					res.diskLine = line
				}
				log.Printf("%s: %s", prefix, raw)
			}
			res.err = scanner.Err()
			ch <- res
		}()
		return ch
	}

	xtrabackupLogs := logPipe("xtrabackup", xtrabackupErr)
	xbcloudLogs := logPipe("xbcloud", xbcloudErr)

	xtrabackupWaitErr := xtrabackupCmd.Wait()
	xbcloudWaitErr := xbcloudCmd.Wait()
	xtrabackupScan := <-xtrabackupLogs
	xbcloudScan := <-xbcloudLogs

	result.Duration = time.Since(started).Round(time.Millisecond).String()

	if xtrabackupScan.err != nil {
		result.Error = fmt.Sprintf("read xtrabackup stderr: %v", xtrabackupScan.err)
		return result
	}
	if xbcloudScan.err != nil {
		result.Error = fmt.Sprintf("read xbcloud stderr: %v", xbcloudScan.err)
		return result
	}
	if xtrabackupWaitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("physical backup timed out after %s", cfg.PhysicalTimeout)
			log.Printf("physical backup: %s", result.Error)
			return result
		}
		summary := xtrabackupScan.lastLine
		if xtrabackupScan.diskLine != "" {
			summary = xtrabackupScan.diskLine
		}
		if summary != "" {
			result.Error = fmt.Sprintf("physical backup stream failed: %v (%s)", xtrabackupWaitErr, summary)
		} else {
			result.Error = fmt.Sprintf("physical backup stream failed: %v", xtrabackupWaitErr)
		}
		log.Printf("physical backup: %s", result.Error)
		return result
	}
	if xbcloudWaitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("physical backup timed out after %s", cfg.PhysicalTimeout)
			log.Printf("physical backup: %s", result.Error)
			return result
		}
		summary := xbcloudScan.lastLine
		if summary != "" {
			result.Error = fmt.Sprintf("physical backup upload failed: %v (%s)", xbcloudWaitErr, summary)
		} else {
			result.Error = fmt.Sprintf("physical backup upload failed: %v", xbcloudWaitErr)
		}
		log.Printf("physical backup: %s", result.Error)
		return result
	}

	result.Status = "success"
	log.Printf("physical backup: completed successfully target=%s duration=%s", result.TargetDir, result.Duration)
	return result
}

func run() (int, string, bool) {
	startedAt := time.Now()
	runID := startedAt.Format(runTimestampLayout)
	cfg := loadConfig()
	shouldUploadLogical := false
	record := runRecord{
		Timestamp: time.Now().UTC(),
		RunID:     runID,
		Status:    "failed",
		BackupDir: cfg.BackupDir,
	}

	logFile, logFilePath, err := initRunLogger(cfg.LogDir, runID)
	if err != nil {
		log.Printf("warning: persistent run logging is unavailable: %v", err)
	} else {
		defer logFile.Close()
		record.LogFile = logFilePath
	}

	log.Println("mysql full backup started")
	log.Printf(
		"using mysql target user=%s host=%s port=%s backup_dir=%s log_dir=%s",
		cfg.DBUser,
		cfg.DBHost,
		cfg.DBPort,
		cfg.BackupDir,
		cfg.LogDir,
	)

	releaseLock, err := acquireLock(cfg.LockFile)
	if err != nil {
		record.FailureReason = err.Error()
		return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
	}
	defer releaseLock()

	statePath := filepath.Join(cfg.LogDir, "backup-schedule-state.json")
	state := loadScheduleState(statePath)
	logicalDue := false
	physicalDue := false
	logicalLastSuccess := parseScheduleTimestamp(state.LogicalLastSuccess)
	physicalLastSuccess := parseScheduleTimestamp(state.PhysicalLastSuccess)

	if cfg.LogicalEnabled {
		logicalDue, _, err = evaluateSchedule(time.Now(), cfg.LogicalSchedule, logicalLastSuccess)
		if err != nil {
			record.FailureReason = fmt.Sprintf("logical backup schedule error: %v", err)
			return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
		}
		if logicalDue {
			log.Printf("logical backup: due now schedule=%s", cfg.LogicalSchedule)
		} else {
			log.Printf("logical backup: skipped by schedule=%s", cfg.LogicalSchedule)
		}
	} else {
		log.Printf("logical backup: disabled by BACKUP_LOGICAL_ENABLED=false")
	}

	if cfg.PhysicalEnabled {
		if !cfg.PhysicalS3UploadEnabled {
			log.Printf("physical backup: skipped because BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false and physical mode only supports direct S3 upload")
		} else {
			physicalDue, _, err = evaluateSchedule(time.Now(), cfg.PhysicalSchedule, physicalLastSuccess)
			if err != nil {
				record.FailureReason = fmt.Sprintf("physical backup schedule error: %v", err)
				return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
			}
			if physicalDue {
				log.Printf("physical backup: due now schedule=%s", cfg.PhysicalSchedule)
			} else {
				log.Printf("physical backup: skipped by schedule=%s", cfg.PhysicalSchedule)
			}
		}
	} else {
		log.Printf("physical backup: disabled by BACKUP_PHYSICAL_ENABLED=false")
	}

	if !logicalDue && !physicalDue {
		record.Status = "success"
		log.Printf("no backup tasks are due for this run")
		return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
	}

	if err := validatePrerequisites(cfg, logicalDue); err != nil {
		record.FailureReason = err.Error()
		return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
	}

	runFolder := filepath.Join(cfg.BackupDir, runID)
	if err := os.MkdirAll(runFolder, 0o755); err != nil {
		record.FailureReason = fmt.Sprintf("failed to create backup folder %s: %v", runFolder, err)
		return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
	}
	record.RunFolder = runFolder
	log.Printf("backup folder: %s", runFolder)
	stateDirty := false

	if logicalDue {
		shouldUploadLogical = cfg.LogicalS3UploadEnabled
		restoreMaxExecTime := tryDisableMaxExecutionTime(cfg)
		defer restoreMaxExecTime()

		databases, err := listDatabases(cfg)
		if err != nil {
			record.FailureReason = err.Error()
			return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
		}
		record.DatabasesTotal = len(databases)

		if len(databases) == 0 {
			log.Println("no user databases found for logical backup")
			state.LogicalLastSuccess = time.Now().UTC().Format(time.RFC3339)
			stateDirty = true
		} else {
			log.Printf("found %d databases: %s", len(databases), strings.Join(databases, ", "))
			for index, dbName := range databases {
				outputPath := filepath.Join(runFolder, dbName+".sql.gz")
				log.Printf("[%d/%d] processing %s", index+1, len(databases), dbName)
				result := dumpWithRetry(cfg, dbName, outputPath)
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
		log.Printf("physical backup: enabled, starting direct S3 stream")
		physResult := runPhysicalBackup(cfg, runFolder)
		record.PhysicalBackup = &physResult
		if physResult.Status != "success" {
			log.Printf("physical backup failed: %s", physResult.Error)
			if record.FailureReason == "" {
				record.FailureReason = physResult.Error
			}
		} else {
			state.PhysicalLastSuccess = time.Now().UTC().Format(time.RFC3339)
			stateDirty = true
		}
	}

	cleanupErr := cleanupOldBackups(cfg.BackupDir, runFolder, cfg.RetentionDays)
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

	return finalizeRun(cfg, &record, startedAt), record.RunFolder, shouldUploadLogical
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	exitCode, runFolder, shouldUploadLogical := run()
	if runFolder != "" && shouldUploadLogical {
		cfg := loadConfig()
		uploadBackupToS3(cfg, runFolder)
	} else if runFolder != "" {
		log.Printf("logical backup S3 upload skipped by BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false or logical backup not scheduled")
	}
	os.Exit(exitCode)
}
