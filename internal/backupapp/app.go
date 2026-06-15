package backupapp

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

const runTimestampLayout = "2006-01-02_15-04-05"

type Config = config

type config struct {
	DBUser                  string
	DBPass                  string
	DBHost                  string
	DBPort                  string
	BackupDir               string
	LogDir                  string
	RunLogPath              string
	MetricsFile             string
	LockFile                string
	ServiceUnitName         string
	TimerUnitName           string
	MySQLBin                string
	MySQLDumpBin            string
	RetryCount              int
	RetentionDays           int
	LogicalEnabled          bool
	LogicalTimeoutPerDB     time.Duration
	LogicalS3UploadEnabled  bool
	LogicalSchedule         string
	LogicalDatabases        []string
	LogicalTables           map[string][]string
	PhysicalSchedule        string
	PhysicalTimeout         time.Duration
	DiscoveryTimeout        time.Duration
	PreflightTimeout        time.Duration
	RetryBaseDelay          time.Duration
	RetryMaxDelay           time.Duration
	CleanupFailFatal        bool
	S3UploadURL             string
	S3UploadTimeout         time.Duration
	S3UploadMode            string
	S3PHPBin                string
	S3UploadScript          string
	S3LogicalPrefix         string
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
	APIEnabled              bool
	APIListenAddr           string
	APIBasePath             string
	APIAuthEnabled          bool
	APIBearerToken          string
	ExecutionSource         string
}

type PhysicalBackupResult = physicalBackupResult

type physicalBackupResult struct {
	Status    string `json:"status"`
	TargetDir string `json:"target_dir,omitempty"`
	Duration  string `json:"duration"`
	Error     string `json:"error,omitempty"`
}

type DatabaseResult = databaseResult

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

type RunResult = runRecord

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
	ExitCode           int                   `json:"-"`
	LogicalUploadRun   bool                  `json:"logical_upload_run,omitempty"`
	LogicalUploadNote  string                `json:"logical_upload_note,omitempty"`
	OSUser             string                `json:"os_user,omitempty"`
	ExecutionSource    string                `json:"execution_source,omitempty"`
	Hostname           string                `json:"hostname,omitempty"`
	PID                int                   `json:"pid,omitempty"`
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
	Mode       ManualRunMode
	UploadMode ManualUploadMode
	ForceNow   bool
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

type LatestRunInfo struct {
	Timestamp          time.Time
	RunID              string
	Status             string
	RunFolder          string
	LogFile            string
	FailureReason      string
	CleanupError       string
	Duration           string
	DatabasesTotal     int
	DatabasesSucceeded int
	DatabasesFailed    int
	OSUser             string
	ExecutionSource    string
	Hostname           string
	PID                int
}

type HealthReport struct {
	ConfigPath   string
	RunLogPath   string
	DailyLogPath string
	LatestRun    *LatestRunInfo
	Logical      HealthCheck
	Physical     HealthCheck
	Runtime      RuntimeProfile
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

func defaultBackupDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "backups"
	}
	return filepath.Join(wd, "backups")
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

func envCandidates(explicit string) []string {
	if strings.TrimSpace(explicit) != "" {
		return []string{strings.TrimSpace(explicit)}
	}
	return []string{
		".env",
		filepath.Join("sdl_db_backup", ".env"),
	}
}

func ResolveEnvFilePath(explicit string) string {
	for _, path := range envCandidates(explicit) {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return ".env"
}

func loadEnvFile() string {
	path := ResolveEnvFilePath(os.Getenv("BACKUP_ENV_FILE"))
	if err := godotenv.Load(path); err != nil {
		log.Printf("warning: could not load env file %s: %v", path, err)
		return ""
	}
	log.Printf("loaded env from %s", path)
	return path
}

func loadEnvMap(path string) (map[string]string, error) {
	values := map[string]string{}
	if path == "" {
		path = ResolveEnvFilePath(os.Getenv("BACKUP_ENV_FILE"))
	}
	loaded, err := godotenv.Read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil
		}
		return nil, err
	}
	for key, value := range loaded {
		values[key] = value
	}
	return values, nil
}

func getMapValue(values map[string]string, key, fallback string) string {
	if values == nil {
		return fallback
	}
	if raw, ok := values[key]; ok {
		if v := strings.TrimSpace(raw); v != "" {
			return v
		}
	}
	return fallback
}

func getMapValueAny(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values == nil {
			break
		}
		if raw, ok := values[key]; ok {
			if v := strings.TrimSpace(raw); v != "" {
				return v
			}
		}
	}
	return ""
}

func getMapInt(values map[string]string, key string, fallback int) int {
	raw := strings.TrimSpace(getMapValue(values, key, ""))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func getMapDuration(values map[string]string, key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(getMapValue(values, key, ""))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func getMapBool(values map[string]string, key string, fallback bool) bool {
	raw := strings.TrimSpace(getMapValue(values, key, ""))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
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

func parseCSVList(raw string) []string {
	items := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		items = append(items, part)
	}
	return items
}

func parseTableScope(raw string) map[string][]string {
	scope := map[string][]string{}
	for _, dbSpec := range strings.Split(raw, ";") {
		dbSpec = strings.TrimSpace(dbSpec)
		if dbSpec == "" {
			continue
		}
		dbName, tableSpec, ok := strings.Cut(dbSpec, ":")
		if !ok {
			continue
		}
		dbName = strings.TrimSpace(dbName)
		if dbName == "" {
			continue
		}
		scope[dbName] = parseCSVList(tableSpec)
	}
	return scope
}

func formatTableScope(scope map[string][]string) string {
	if len(scope) == 0 {
		return ""
	}
	dbs := make([]string, 0, len(scope))
	for db := range scope {
		dbs = append(dbs, db)
	}
	slices.Sort(dbs)
	parts := []string{}
	for _, db := range dbs {
		tables := append([]string{}, scope[db]...)
		slices.Sort(tables)
		parts = append(parts, db+":"+strings.Join(tables, ","))
	}
	return strings.Join(parts, ";")
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

func loadConfigFromValues(values map[string]string) config {
	dbUser := getMapValue(values, "DB_USER", "")
	dbPass := getMapValue(values, "DB_PASS", "")
	if dbPass == "" {
		dbPass = getMapValue(values, "DB_PASSWORD", "")
	}
	if dbPass == "" {
		dbPass = getMapValue(values, "MYSQL_PASS", "")
	}

	backupDir := getMapValue(values, "BACKUP_DIR", defaultBackupDir())
	logDir := getMapValue(values, "BACKUP_LOG_DIR", filepath.Join(backupDir, "logs"))
	logicalTimeoutPerDB := getMapDuration(values, "BACKUP_LOGICAL_TIMEOUT_PER_DB", 0)
	if logicalTimeoutPerDB <= 0 {
		logicalTimeoutPerDB = getMapDuration(values, "BACKUP_TIMEOUT_PER_DB", 30*time.Minute)
	}
	s3KeyID := getMapValueAny(values, "BACKUP_S3_KEY_ID", "BACKUP_S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
	s3KeySecret := getMapValueAny(values, "BACKUP_S3_KEY_SECRET", "BACKUP_S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")

	return config{
		DBUser:                  dbUser,
		DBPass:                  dbPass,
		DBHost:                  getMapValue(values, "DB_HOST", "127.0.0.1"),
		DBPort:                  getMapValue(values, "DB_PORT", "3306"),
		BackupDir:               backupDir,
		LogDir:                  logDir,
		RunLogPath:              filepath.Join(logDir, "backup-runs.jsonl"),
		MetricsFile:             getMapValue(values, "BACKUP_METRICS_FILE", defaultMetricsFilePath),
		LockFile:                getMapValue(values, "BACKUP_LOCK_FILE", filepath.Join(logDir, "backup.lock")),
		ServiceUnitName:         getMapValue(values, "BACKUP_SYSTEMD_SERVICE_NAME", "sdl-db-backup.service"),
		TimerUnitName:           getMapValue(values, "BACKUP_SYSTEMD_TIMER_NAME", "sdl-db-backup.timer"),
		MySQLBin:                getMapValue(values, "MYSQL_BIN", "mysql"),
		MySQLDumpBin:            getMapValue(values, "MYSQLDUMP_BIN", "mysqldump"),
		RetryCount:              getMapInt(values, "BACKUP_RETRY_COUNT", 3),
		RetentionDays:           getMapInt(values, "BACKUP_RETENTION_DAYS", 5),
		LogicalEnabled:          getMapBool(values, "BACKUP_LOGICAL_ENABLED", true),
		LogicalTimeoutPerDB:     logicalTimeoutPerDB,
		LogicalS3UploadEnabled:  getMapBool(values, "BACKUP_LOGICAL_S3_UPLOAD_ENABLED", true),
		LogicalSchedule:         getMapValue(values, "BACKUP_LOGICAL_SCHEDULE", "always"),
		LogicalDatabases:        parseCSVList(getMapValue(values, "BACKUP_LOGICAL_DATABASES", "")),
		LogicalTables:           parseTableScope(getMapValue(values, "BACKUP_LOGICAL_TABLES", "")),
		PhysicalSchedule:        getMapValue(values, "BACKUP_PHYSICAL_SCHEDULE", "always"),
		PhysicalTimeout:         getMapDuration(values, "BACKUP_PHYSICAL_TIMEOUT", 6*time.Hour),
		DiscoveryTimeout:        getMapDuration(values, "BACKUP_DISCOVERY_TIMEOUT", 30*time.Second),
		PreflightTimeout:        getMapDuration(values, "BACKUP_PREFLIGHT_TIMEOUT", 15*time.Second),
		RetryBaseDelay:          getMapDuration(values, "BACKUP_RETRY_BASE_DELAY", 2*time.Second),
		RetryMaxDelay:           getMapDuration(values, "BACKUP_RETRY_MAX_DELAY", 20*time.Second),
		CleanupFailFatal:        getMapBool(values, "BACKUP_CLEANUP_FAIL_FATAL", false),
		S3UploadURL:             getMapValue(values, "BACKUP_S3_UPLOAD_URL", ""),
		S3UploadTimeout:         getMapDuration(values, "BACKUP_S3_UPLOAD_TIMEOUT", 2*time.Hour),
		S3UploadMode:            strings.ToLower(getMapValue(values, "BACKUP_S3_UPLOAD_MODE", "direct")),
		S3PHPBin:                getMapValue(values, "BACKUP_S3_PHP_BIN", "php"),
		S3UploadScript:          getMapValue(values, "BACKUP_S3_UPLOAD_SCRIPT", ""),
		S3LogicalPrefix:         getMapValue(values, "BACKUP_S3_LOGICAL_PREFIX", "logical"),
		S3PhysicalPrefix:        getMapValue(values, "BACKUP_S3_PHYSICAL_PREFIX", "backup-as-it-is"),
		S3Bucket:                getMapValue(values, "BACKUP_S3_BUCKET", "ticklerightbackups"),
		S3Region:                getMapValue(values, "BACKUP_S3_REGION", "ap-south-1"),
		S3KeyID:                 s3KeyID,
		S3KeySecret:             s3KeySecret,
		XbcloudBin:              getMapValue(values, "BACKUP_XBCLOUD_BIN", "xbcloud"),
		PhysicalEnabled:         getMapBool(values, "BACKUP_PHYSICAL_ENABLED", true),
		PhysicalS3UploadEnabled: getMapBool(values, "BACKUP_PHYSICAL_S3_UPLOAD_ENABLED", true),
		XtrabackupBin:           getMapValue(values, "BACKUP_XTRABACKUP_BIN", "xtrabackup"),
		XtrabackupParallel:      getMapInt(values, "BACKUP_XTRABACKUP_PARALLEL", 4),
		XtrabackupUser:          getMapValue(values, "BACKUP_XTRABACKUP_USER", dbUser),
		XtrabackupPass:          getMapValue(values, "BACKUP_XTRABACKUP_PASS", dbPass),
		XtrabackupSocket:        getMapValue(values, "BACKUP_XTRABACKUP_SOCKET", ""),
		XtrabackupRunAsUser:     getMapValue(values, "BACKUP_XTRABACKUP_RUN_AS_USER", ""),
		XtrabackupWorkDir:       getMapValue(values, "BACKUP_XTRABACKUP_WORK_DIR", "/tmp"),
		APIEnabled:              getMapBool(values, "BACKUP_API_ENABLED", false),
		APIListenAddr:           getMapValue(values, "BACKUP_API_LISTEN_ADDR", "127.0.0.1:8086"),
		APIBasePath:             getMapValue(values, "BACKUP_API_BASE_PATH", "/api/v1"),
		APIAuthEnabled:          getMapBool(values, "BACKUP_API_AUTH_ENABLED", false),
		APIBearerToken:          getMapValue(values, "BACKUP_API_BEARER_TOKEN", ""),
		ExecutionSource:         getMapValue(values, "BACKUP_EXECUTION_SOURCE", "runner"),
	}
}

func loadConfig() config {
	cfg, err := loadConfigWithOverrides(ResolveEnvFilePath(os.Getenv("BACKUP_ENV_FILE")))
	if err != nil {
		log.Printf("warning: could not read env file: %v", err)
	}
	return cfg
}

func loadConfigWithOverrides(envPath string) (config, error) {
	values, err := loadEnvMap(ResolveEnvFilePath(envPath))
	if err != nil {
		return config{}, err
	}
	for _, key := range managedEnvKeys() {
		if v, ok := os.LookupEnv(key); ok {
			values[key] = v
		}
	}
	if overrides, ok := loadActiveTemporaryOverrides(values); ok {
		for key, value := range overrides.Values {
			if _, fromProcess := os.LookupEnv(key); !fromProcess && slices.Contains(managedEnvKeys(), key) {
				values[key] = value
			}
		}
	}
	return loadConfigFromValues(values), nil
}

func LoadConfig(envPath string) (Config, error) {
	values, err := loadEnvMap(ResolveEnvFilePath(envPath))
	if err != nil {
		return config{}, err
	}
	return loadConfigFromValues(values), nil
}

func temporaryOverridePathFromValues(values map[string]string) string {
	cfg := loadConfigFromValues(values)
	return TemporaryOverridePath(cfg)
}

func readTemporaryOverridesFile(path string) (TemporaryOverrides, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TemporaryOverrides{}, false, nil
		}
		return TemporaryOverrides{}, false, err
	}
	var overrides TemporaryOverrides
	if err := json.Unmarshal(data, &overrides); err != nil {
		return TemporaryOverrides{}, false, err
	}
	return overrides, true, nil
}

func loadActiveTemporaryOverrides(values map[string]string) (TemporaryOverrides, bool) {
	path := temporaryOverridePathFromValues(values)
	overrides, ok, err := readTemporaryOverridesFile(path)
	if err != nil || !ok {
		return TemporaryOverrides{}, false
	}
	if time.Now().After(overrides.ExpiresAt) {
		_ = os.Remove(path)
		return TemporaryOverrides{}, false
	}
	return overrides, true
}

func LoadEffectiveConfig(envPath string) (Config, error) {
	return loadConfigWithOverrides(envPath)
}

func ListLogicalDatabases(cfg Config) ([]string, error) {
	return listDatabases(cfg)
}

func ListLogicalTables(cfg Config, dbName string) ([]string, error) {
	return listTables(cfg, dbName)
}

func TemporaryOverridePath(cfg Config) string {
	return filepath.Join(cfg.LogDir, "backup-temporary-overrides.json")
}

func LoadTemporaryOverrides(envPath string) (TemporaryOverrides, bool, error) {
	cfg, err := LoadConfig(envPath)
	if err != nil {
		return TemporaryOverrides{}, false, err
	}
	overrides, ok, err := readTemporaryOverridesFile(TemporaryOverridePath(cfg))
	if err != nil || !ok {
		return overrides, ok, err
	}
	if time.Now().After(overrides.ExpiresAt) {
		_ = os.Remove(TemporaryOverridePath(cfg))
		return TemporaryOverrides{}, false, nil
	}
	return overrides, true, nil
}

func SaveTemporaryOverrides(envPath string, overrides TemporaryOverrides) error {
	base, err := LoadConfig(envPath)
	if err != nil {
		return err
	}
	if overrides.CreatedAt.IsZero() {
		overrides.CreatedAt = time.Now()
	}
	if overrides.ExpiresAt.IsZero() {
		return fmt.Errorf("temporary override expiry is required")
	}
	if !overrides.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("temporary override expiry must be in the future")
	}
	if len(overrides.Values) == 0 {
		return fmt.Errorf("temporary override values are required")
	}
	cleanValues := map[string]string{}
	for key, value := range overrides.Values {
		if !slices.Contains(managedEnvKeys(), key) {
			return fmt.Errorf("temporary override key %s is not managed", key)
		}
		cleanValues[key] = value
	}
	overrides.Values = cleanValues
	if err := os.MkdirAll(base.LogDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(overrides, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(TemporaryOverridePath(base), append(data, '\n'), 0o640)
}

func ClearTemporaryOverrides(envPath string) error {
	base, err := LoadConfig(envPath)
	if err != nil {
		return err
	}
	err = os.Remove(TemporaryOverridePath(base))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func managedEnvKeys() []string {
	return []string{
		"DB_USER",
		"DB_PASS",
		"DB_HOST",
		"DB_PORT",
		"BACKUP_DIR",
		"BACKUP_LOG_DIR",
		"BACKUP_METRICS_FILE",
		"BACKUP_SYSTEMD_SERVICE_NAME",
		"BACKUP_SYSTEMD_TIMER_NAME",
		"MYSQL_BIN",
		"MYSQLDUMP_BIN",
		"BACKUP_RETRY_COUNT",
		"BACKUP_RETRY_BASE_DELAY",
		"BACKUP_RETRY_MAX_DELAY",
		"BACKUP_LOGICAL_ENABLED",
		"BACKUP_LOGICAL_SCHEDULE",
		"BACKUP_LOGICAL_DATABASES",
		"BACKUP_LOGICAL_TABLES",
		"BACKUP_LOGICAL_TIMEOUT_PER_DB",
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED",
		"BACKUP_PHYSICAL_ENABLED",
		"BACKUP_PHYSICAL_SCHEDULE",
		"BACKUP_PHYSICAL_TIMEOUT",
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED",
		"BACKUP_DISCOVERY_TIMEOUT",
		"BACKUP_PREFLIGHT_TIMEOUT",
		"BACKUP_RETENTION_DAYS",
		"BACKUP_CLEANUP_FAIL_FATAL",
		"BACKUP_LOCK_FILE",
		"BACKUP_S3_UPLOAD_URL",
		"BACKUP_S3_UPLOAD_TIMEOUT",
		"BACKUP_S3_UPLOAD_MODE",
		"BACKUP_S3_PHP_BIN",
		"BACKUP_S3_UPLOAD_SCRIPT",
		"BACKUP_S3_BUCKET",
		"BACKUP_S3_REGION",
		"BACKUP_S3_LOGICAL_PREFIX",
		"BACKUP_S3_PHYSICAL_PREFIX",
		"BACKUP_S3_KEY_ID",
		"BACKUP_S3_KEY_SECRET",
		"BACKUP_XBCLOUD_BIN",
		"BACKUP_XTRABACKUP_BIN",
		"BACKUP_XTRABACKUP_PARALLEL",
		"BACKUP_XTRABACKUP_USER",
		"BACKUP_XTRABACKUP_PASS",
		"BACKUP_XTRABACKUP_SOCKET",
		"BACKUP_XTRABACKUP_RUN_AS_USER",
		"BACKUP_XTRABACKUP_WORK_DIR",
		"BACKUP_API_ENABLED",
		"BACKUP_API_LISTEN_ADDR",
		"BACKUP_API_BASE_PATH",
		"BACKUP_API_AUTH_ENABLED",
		"BACKUP_API_BEARER_TOKEN",
	}
}

func envMapFromConfig(cfg Config) map[string]string {
	values := map[string]string{
		"DB_USER":                           cfg.DBUser,
		"DB_PASS":                           cfg.DBPass,
		"DB_HOST":                           cfg.DBHost,
		"DB_PORT":                           cfg.DBPort,
		"BACKUP_DIR":                        cfg.BackupDir,
		"BACKUP_LOG_DIR":                    cfg.LogDir,
		"BACKUP_METRICS_FILE":               cfg.MetricsFile,
		"BACKUP_SYSTEMD_SERVICE_NAME":       cfg.ServiceUnitName,
		"BACKUP_SYSTEMD_TIMER_NAME":         cfg.TimerUnitName,
		"MYSQL_BIN":                         cfg.MySQLBin,
		"MYSQLDUMP_BIN":                     cfg.MySQLDumpBin,
		"BACKUP_RETRY_COUNT":                strconv.Itoa(cfg.RetryCount),
		"BACKUP_RETRY_BASE_DELAY":           cfg.RetryBaseDelay.String(),
		"BACKUP_RETRY_MAX_DELAY":            cfg.RetryMaxDelay.String(),
		"BACKUP_LOGICAL_ENABLED":            strconv.FormatBool(cfg.LogicalEnabled),
		"BACKUP_LOGICAL_SCHEDULE":           cfg.LogicalSchedule,
		"BACKUP_LOGICAL_DATABASES":          strings.Join(cfg.LogicalDatabases, ","),
		"BACKUP_LOGICAL_TABLES":             formatTableScope(cfg.LogicalTables),
		"BACKUP_LOGICAL_TIMEOUT_PER_DB":     cfg.LogicalTimeoutPerDB.String(),
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED":  strconv.FormatBool(cfg.LogicalS3UploadEnabled),
		"BACKUP_PHYSICAL_ENABLED":           strconv.FormatBool(cfg.PhysicalEnabled),
		"BACKUP_PHYSICAL_SCHEDULE":          cfg.PhysicalSchedule,
		"BACKUP_PHYSICAL_TIMEOUT":           cfg.PhysicalTimeout.String(),
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED": strconv.FormatBool(cfg.PhysicalS3UploadEnabled),
		"BACKUP_DISCOVERY_TIMEOUT":          cfg.DiscoveryTimeout.String(),
		"BACKUP_PREFLIGHT_TIMEOUT":          cfg.PreflightTimeout.String(),
		"BACKUP_RETENTION_DAYS":             strconv.Itoa(cfg.RetentionDays),
		"BACKUP_CLEANUP_FAIL_FATAL":         strconv.FormatBool(cfg.CleanupFailFatal),
		"BACKUP_LOCK_FILE":                  cfg.LockFile,
		"BACKUP_S3_UPLOAD_URL":              cfg.S3UploadURL,
		"BACKUP_S3_UPLOAD_TIMEOUT":          cfg.S3UploadTimeout.String(),
		"BACKUP_S3_UPLOAD_MODE":             cfg.S3UploadMode,
		"BACKUP_S3_PHP_BIN":                 cfg.S3PHPBin,
		"BACKUP_S3_UPLOAD_SCRIPT":           cfg.S3UploadScript,
		"BACKUP_S3_BUCKET":                  cfg.S3Bucket,
		"BACKUP_S3_REGION":                  cfg.S3Region,
		"BACKUP_S3_LOGICAL_PREFIX":          cfg.S3LogicalPrefix,
		"BACKUP_S3_PHYSICAL_PREFIX":         cfg.S3PhysicalPrefix,
		"BACKUP_S3_KEY_ID":                  cfg.S3KeyID,
		"BACKUP_S3_KEY_SECRET":              cfg.S3KeySecret,
		"BACKUP_XBCLOUD_BIN":                cfg.XbcloudBin,
		"BACKUP_XTRABACKUP_BIN":             cfg.XtrabackupBin,
		"BACKUP_XTRABACKUP_PARALLEL":        strconv.Itoa(cfg.XtrabackupParallel),
		"BACKUP_XTRABACKUP_USER":            cfg.XtrabackupUser,
		"BACKUP_XTRABACKUP_PASS":            cfg.XtrabackupPass,
		"BACKUP_XTRABACKUP_SOCKET":          cfg.XtrabackupSocket,
		"BACKUP_XTRABACKUP_RUN_AS_USER":     cfg.XtrabackupRunAsUser,
		"BACKUP_XTRABACKUP_WORK_DIR":        cfg.XtrabackupWorkDir,
		"BACKUP_API_ENABLED":                strconv.FormatBool(cfg.APIEnabled),
		"BACKUP_API_LISTEN_ADDR":            cfg.APIListenAddr,
		"BACKUP_API_BASE_PATH":              cfg.APIBasePath,
		"BACKUP_API_AUTH_ENABLED":           strconv.FormatBool(cfg.APIAuthEnabled),
		"BACKUP_API_BEARER_TOKEN":           cfg.APIBearerToken,
	}
	return values
}

func quoteEnvValue(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " #'\"\t") {
		return strconv.Quote(value)
	}
	return value
}

func SaveConfig(envPath string, cfg Config) error {
	path := ResolveEnvFilePath(envPath)
	lines := []string{}
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	values := envMapFromConfig(cfg)
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value, ok := values[key]
		if !ok {
			continue
		}
		lines[i] = key + "=" + quoteEnvValue(value)
		seen[key] = true
	}

	for _, key := range managedEnvKeys() {
		if seen[key] {
			continue
		}
		value, ok := values[key]
		if !ok {
			continue
		}
		lines = append(lines, key+"="+quoteEnvValue(value))
	}

	output := strings.Join(lines, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return os.WriteFile(path, []byte(output), 0o640)
}

func BuildManualRunConfig(base Config, opts ManualRunOptions) (Config, Preview, error) {
	cfg := base
	preview := Preview{}

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

	preview.Lines = append(preview.Lines, "Config file will not be rewritten unless you explicitly save from the TUI.")
	return cfg, preview, nil
}

func parseSystemdShow(output string, unitName string) UnitState {
	state := UnitState{Name: unitName}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.Active = value
		case "SubState":
			state.SubState = value
		case "UnitFileState":
			state.Enabled = value
		}
	}
	return state
}

func GetSystemdStatus(ctx context.Context, envPath string) (UnitStatus, error) {
	status := UnitStatus{}
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return status, err
	}
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	readUnit := func(name string) (UnitState, error) {
		cmd := exec.CommandContext(
			ctx,
			"systemctl",
			"--user",
			"show",
			name,
			"--property=LoadState,ActiveState,SubState,UnitFileState",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return UnitState{Name: name}, fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)))
		}
		return parseSystemdShow(string(out), name), nil
	}

	status.Service, err = readUnit(serviceName)
	if err != nil {
		return status, err
	}
	status.Timer, err = readUnit(timerName)
	if err != nil {
		return status, err
	}
	return status, nil
}

func RunSystemdAction(ctx context.Context, envPath string, action SystemdAction) error {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return err
	}
	serviceName := cfg.ServiceUnitName
	timerName := cfg.TimerUnitName
	args := []string{"--user"}
	switch action {
	case SystemdDaemonReload:
		args = append(args, "daemon-reload")
	case SystemdEnableTimer:
		args = append(args, "enable", "--now", timerName)
	case SystemdDisableTimer:
		args = append(args, "disable", "--now", timerName)
	case SystemdStartTimer:
		args = append(args, "start", timerName)
	case SystemdStopTimer:
		args = append(args, "stop", timerName)
	case SystemdRestartSvc:
		args = append(args, "restart", serviceName)
	case SystemdStartSvc:
		args = append(args, "start", serviceName)
	case SystemdStopSvc:
		args = append(args, "stop", serviceName)
	default:
		return fmt.Errorf("unsupported systemd action %q", action)
	}
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func LoadRecentJournal(ctx context.Context, unit string, lines int) (string, error) {
	if lines <= 0 {
		lines = 50
	}
	cmd := exec.CommandContext(
		ctx,
		"journalctl",
		"--user",
		"-u",
		unit,
		"-n",
		strconv.Itoa(lines),
		"--no-pager",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func readLatestRunInfo(path string) (*LatestRunInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var record runRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		return &LatestRunInfo{
			Timestamp:          record.Timestamp,
			RunID:              record.RunID,
			Status:             record.Status,
			RunFolder:          record.RunFolder,
			LogFile:            record.LogFile,
			FailureReason:      record.FailureReason,
			CleanupError:       record.CleanupError,
			Duration:           record.Duration,
			DatabasesTotal:     record.DatabasesTotal,
			DatabasesSucceeded: record.DatabasesSucceeded,
			DatabasesFailed:    record.DatabasesFailed,
			OSUser:             record.OSUser,
			ExecutionSource:    record.ExecutionSource,
			Hostname:           record.Hostname,
			PID:                record.PID,
		}, nil
	}
	return nil, nil
}

func checkLogicalHealth(cfg config) HealthCheck {
	check := HealthCheck{Name: "logical", Status: "ok", Message: "logical backup prerequisites look valid"}
	if !cfg.LogicalEnabled {
		check.Status = "disabled"
		check.Message = "logical backup is disabled in config"
		return check
	}
	if err := validatePrerequisites(cfg, true); err != nil {
		check.Status = "error"
		check.Message = err.Error()
	}
	return check
}

func checkPhysicalHealth(cfg config) HealthCheck {
	check := HealthCheck{Name: "physical", Status: "ok", Message: "physical backup prerequisites look valid"}
	if !cfg.PhysicalEnabled {
		check.Status = "disabled"
		check.Message = "physical backup is disabled in config"
		return check
	}
	if !cfg.PhysicalS3UploadEnabled {
		check.Status = "disabled"
		check.Message = "physical backup is skipped because BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false"
		return check
	}
	if err := validateWritableDir(cfg.BackupDir); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	if err := validateWritableDir(cfg.LogDir); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	if _, err := exec.LookPath(cfg.MySQLBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("mysql binary %q not found in PATH: %v", cfg.MySQLBin, err)
		return check
	}
	if _, err := exec.LookPath(cfg.XtrabackupBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("xtrabackup binary %q not found in PATH: %v", cfg.XtrabackupBin, err)
		return check
	}
	if _, err := exec.LookPath(cfg.XbcloudBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("xbcloud binary %q not found in PATH: %v", cfg.XbcloudBin, err)
		return check
	}
	if cfg.S3Bucket == "" {
		check.Status = "error"
		check.Message = "physical backup requires BACKUP_S3_BUCKET"
		return check
	}
	if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		check.Status = "error"
		check.Message = "physical backup requires S3 credentials"
		return check
	}
	if err := checkXtrabackupPrivileges(cfg); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	return check
}

func GetHealthReport(ctx context.Context, envPath string) (HealthReport, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return HealthReport{}, err
	}
	resolvedPath := ResolveEnvFilePath(envPath)
	report := HealthReport{
		ConfigPath:   resolvedPath,
		RunLogPath:   cfg.RunLogPath,
		DailyLogPath: filepath.Join(cfg.LogDir, time.Now().Format("2006-01-02")+".log"),
		Logical:      checkLogicalHealth(cfg),
		Physical:     checkPhysicalHealth(cfg),
	}
	runtime, runtimeErr := GetRuntimeProfile(envPath)
	if runtimeErr == nil {
		report.Runtime = runtime
	}
	latestRun, err := readLatestRunInfo(cfg.RunLogPath)
	if err != nil {
		return report, err
	}
	report.LatestRun = latestRun
	return report, nil
}

type multiCloser struct {
	closers []io.Closer
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

func filterDatabases(cfg config, discovered []string) []string {
	if len(cfg.LogicalDatabases) == 0 {
		return discovered
	}
	allowed := map[string]bool{}
	for _, db := range cfg.LogicalDatabases {
		allowed[db] = true
	}
	filtered := []string{}
	for _, db := range discovered {
		if allowed[db] {
			filtered = append(filtered, db)
		}
	}
	return filtered
}

func listTables(cfg config, dbName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DiscoveryTimeout)
	defer cancel()
	query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=%q AND TABLE_TYPE IN ('BASE TABLE','VIEW') ORDER BY TABLE_NAME", dbName)
	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("mysql table discovery failed database=%s (%s): %s", dbName, classifyFailure(err, message), chooseFailureMessage(err, message))
	}
	tables := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		tableName := strings.TrimSpace(scanner.Text())
		if tableName != "" {
			tables = append(tables, tableName)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	brokenViews, err := discoverBrokenViews(cfg, dbName)
	if err != nil {
		log.Printf("warning: could not precheck views for database=%s while listing scope objects: %v", dbName, err)
		return tables, nil
	}
	if len(brokenViews) == 0 {
		return tables, nil
	}

	filtered := filterBrokenViewsFromObjects(tables, brokenViews)
	log.Printf("database=%s: excluded %d broken view(s) from discovery: %s", dbName, len(brokenViews), strings.Join(brokenViews, ", "))
	return filtered, nil
}

func selectedTablesForDatabase(cfg config, dbName string) []string {
	if len(cfg.LogicalTables) == 0 {
		return nil
	}
	return cfg.LogicalTables[dbName]
}

func filterRequestedTables(requested, discovered []string) ([]string, []string) {
	if len(requested) == 0 {
		return nil, nil
	}
	available := make([]string, 0, len(requested))
	missing := []string{}
	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, table := range discovered {
		discoveredSet[table] = struct{}{}
	}
	for _, table := range requested {
		if _, ok := discoveredSet[table]; ok {
			available = append(available, table)
			continue
		}
		missing = append(missing, table)
	}
	return available, missing
}

func resolveSelectedTablesForDatabase(cfg config, dbName string) ([]string, []string, error) {
	requested := selectedTablesForDatabase(cfg, dbName)
	if len(requested) == 0 {
		return nil, nil, nil
	}

	discovered, err := listTables(cfg, dbName)
	if err != nil {
		return requested, nil, err
	}

	available, missing := filterRequestedTables(requested, discovered)
	return available, missing, nil
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

	query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA=%q ORDER BY TABLE_NAME", dbName)
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
		category := classifyFailure(checkErr, message)
		if category == "" {
			category = "view"
		}
		broken = append(broken, viewName)
		log.Printf("warning: skipping view database=%s view=%s category=%s error=%s", dbName, viewName, category, chooseFailureMessage(checkErr, message))
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan views output: %w", err)
	}
	return broken, nil
}

func filterBrokenViewsFromObjects(objects []string, brokenViews []string) []string {
	if len(objects) == 0 || len(brokenViews) == 0 {
		return objects
	}
	broken := make(map[string]struct{}, len(brokenViews))
	for _, viewName := range brokenViews {
		broken[viewName] = struct{}{}
	}
	filtered := make([]string, 0, len(objects))
	for _, objectName := range objects {
		if _, ok := broken[objectName]; ok {
			continue
		}
		filtered = append(filtered, objectName)
	}
	return filtered
}

func buildMySQLDumpArgs(dbName string, tables []string, ignoreTables []string) []string {
	args := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--events",
		"--no-tablespaces",
		"--set-gtid-purged=OFF",
		"--force",                       // continue on SQL errors (required for --ignore-error to take effect)
		"--ignore-error=1356,1449,1227", // skip broken views, missing definers, definer-privilege errors
		dbName,
	}
	args = append(args, tables...)
	for _, table := range ignoreTables {
		args = append(args, "--ignore-table="+dbName+"."+table)
	}
	return args
}

func logLogicalTableSummary(dbName string, tables []string) {
	if len(tables) == 0 {
		return
	}
	const maxPreview = 8
	if len(tables) <= maxPreview {
		log.Printf("database=%s: dumping selected tables only (%d): %s", dbName, len(tables), strings.Join(tables, ", "))
		return
	}
	log.Printf("database=%s: dumping selected tables only (%d tables)", dbName, len(tables))
}

func dumpDatabase(cfg config, dbName, outFile string, tables []string, ignoreTables []string) (int64, error) {
	log.Printf("starting dump for database=%s", dbName)
	logLogicalTableSummary(dbName, tables)

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

	args := buildMySQLDumpArgs(dbName, tables, ignoreTables)
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
				if shouldLogDumpLine(line) {
					log.Printf("mysqldump warning database=%s: %s", dbName, line)
				}
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

func shouldLogDumpLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	switch {
	case strings.Contains(lower, "error"),
		strings.Contains(lower, "warning"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "couldn't find table"),
		strings.Contains(lower, "could not"):
		return true
	default:
		return false
	}
}

func dumpWithRetry(cfg config, dbName, outFile string, tables []string) databaseResult {
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
		sizeBytes, err := dumpDatabase(cfg, dbName, outFile, tables, brokenViews)
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
		if shouldLogPhysicalLine("xtrabackup", line) {
			log.Printf("xtrabackup: %s", raw)
		}
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
				if shouldLogPhysicalLine(prefix, line) {
					log.Printf("%s: %s", prefix, raw)
				}
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

func shouldLogPhysicalLine(prefix, line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}

	switch {
	case strings.Contains(lower, "error"),
		strings.Contains(lower, "warning"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "fatal"),
		strings.Contains(lower, "no space left"),
		strings.Contains(lower, "errno 28"),
		strings.Contains(lower, "completed ok"),
		strings.Contains(lower, "upload completed"),
		strings.Contains(lower, "transaction log of lsn"),
		strings.Contains(lower, "backup created in directory"),
		strings.Contains(lower, "mysql binlog position"):
		return true
	}

	if prefix == "xbcloud" {
		return strings.Contains(lower, "upload completed")
	}

	if prefix == "xtrabackup" {
		return strings.Contains(lower, "mysql binlog position") ||
			strings.Contains(lower, "backup created in directory") ||
			strings.Contains(lower, "completed ok") ||
			strings.Contains(lower, "transaction log of lsn")
	}

	return false
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
	record := runRecord{
		Timestamp: time.Now().UTC(),
		RunID:     runID,
		Status:    "failed",
		BackupDir: cfg.BackupDir,
	}
	logicalUploadRequired := false
	logicalUploadSucceeded := false
	physicalUploadRequired := false
	physicalUploadSucceeded := false
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
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()
	defer func() {
		uploadRequired := logicalUploadRequired || physicalUploadRequired
		uploadSucceeded := true
		if logicalUploadRequired && !logicalUploadSucceeded {
			uploadSucceeded = false
		}
		if physicalUploadRequired && !physicalUploadSucceeded {
			uploadSucceeded = false
		}
		if uploadRequired || record.Status != "" {
			emitBackupMetrics(cfg, record, startedAt, uploadRequired, uploadSucceeded)
		}
	}()

	logCloser, logFilePath, dailyLogPath, err := initRunLogger(cfg.LogDir, runID, console)
	if err != nil {
		log.Printf("warning: persistent run logging is unavailable: %v", err)
	} else {
		defer logCloser.Close()
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
	logicalDue := false
	physicalDue := false
	logicalLastSuccess := parseScheduleTimestamp(state.LogicalLastSuccess)
	physicalLastSuccess := parseScheduleTimestamp(state.PhysicalLastSuccess)

	if cfg.LogicalEnabled {
		logicalDue, _, err = evaluateSchedule(time.Now(), cfg.LogicalSchedule, logicalLastSuccess)
		if err != nil {
			record.FailureReason = fmt.Sprintf("logical backup schedule error: %v", err)
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
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
				record.ExitCode = finalizeRun(cfg, &record, startedAt)
				return record, nil
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
		record.ExitCode = finalizeRun(cfg, &record, startedAt)
		return record, nil
	}

	if err := validatePrerequisites(cfg, logicalDue); err != nil {
		record.FailureReason = err.Error()
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

		discoveredDatabases, err := listDatabases(cfg)
		if err != nil {
			record.FailureReason = err.Error()
			record.ExitCode = finalizeRun(cfg, &record, startedAt)
			return record, nil
		}
		databases := filterDatabases(cfg, discoveredDatabases)
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
			for index, dbName := range databases {
				outputPath := filepath.Join(runFolder, dbName+".sql.gz")
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
							result := databaseResult{
								Name:     dbName,
								Status:   "success",
								Duration: "0s",
							}
							record.Results = append(record.Results, result)
							record.DatabasesSucceeded++
							continue
						}
					}
				}
				result := dumpWithRetry(cfg, dbName, outputPath, tables)
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

	record.ExitCode = finalizeRun(cfg, &record, startedAt)
	if record.RunFolder != "" && logicalDue {
		if cfg.LogicalS3UploadEnabled {
			record.LogicalUploadRun = true
			logicalUploadRequired = true
			if err := uploadBackupToS3(cfg, record.RunFolder); err != nil {
				log.Printf("s3 upload error: %v", err)
			} else {
				logicalUploadSucceeded = true
			}
		} else {
			record.LogicalUploadNote = "logical backup S3 upload skipped by BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false or logical backup not scheduled"
			log.Printf("%s", record.LogicalUploadNote)
		}
	}
	return record, nil
}

func RunFromEnvFile(ctx context.Context, envPath string, sinks RunSinks) (RunResult, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return RunResult{}, err
	}
	return RunBackup(ctx, cfg, sinks)
}
