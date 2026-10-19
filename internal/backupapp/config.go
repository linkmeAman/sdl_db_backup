package backupapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config = config

type config struct {
	StorageAutoCleanup     bool
	StorageMaxLocalBackups int
	StorageHighWatermark   int
	StorageLowWatermark    int
	StorageGrowthPercent   int
	StorageHistoryRuns     int
	StorageConcurrentBytes int
	StorageTemporaryBytes  int
	StorageMinFreeInodes   int
	StorageTempMaxAge      time.Duration
	StorageVerifyTimeout   time.Duration
	StorageSecondaryDir    string

	RetentionDeleteEnabled          bool
	RetentionMinComplete            int
	StorageReserveBytes             int
	StorageEstimateBytes            int
	SystemdScope                    string
	LogicalConsistency              string
	ScheduleTimezone                string
	MySQLDumpSessionTimeoutOverride bool
	DBUser                          string
	DBPass                          string
	DBEngine                        string
	DBHost                          string
	DBPort                          string
	BackupDir                       string
	LogDir                          string
	RunLogPath                      string
	MetricsFile                     string
	LockFile                        string
	ServiceUnitName                 string
	TimerUnitName                   string
	MySQLBin                        string
	MySQLDumpBin                    string
	RetryCount                      int
	RetentionDaily                  int
	RetentionWeekly                 int
	RetentionMonthly                int
	RetentionMinVerified            int
	LogicalEnabled                  bool
	LogicalTimeoutPerDB             time.Duration
	ExactRowCounts                  bool
	SampleDataChecks                bool
	SampleDataRows                  int
	LogicalParallel                 int
	LogicalGzipLevel                int
	LogicalS3UploadEnabled          bool
	RequireRemoteCompletion         bool
	LogicalSchedule                 string
	EncryptionKey                   string
	LogicalDatabases                []string
	LogicalTables                   map[string][]string
	PhysicalSchedule                string
	PhysicalTimeout                 time.Duration
	DiscoveryTimeout                time.Duration
	PreflightTimeout                time.Duration
	RetryBaseDelay                  time.Duration
	RetryMaxDelay                   time.Duration
	CleanupFailFatal                bool
	AllowObjectOmissions            bool
	MySQLPITRMetadataEnabled        bool
	MySQLPITRMetadataRequired       bool
	S3UploadURL                     string
	S3UploadTimeout                 time.Duration
	S3UploadMode                    string
	S3PHPBin                        string
	S3UploadScript                  string
	S3LogicalPrefix                 string
	S3PhysicalPrefix                string
	S3Bucket                        string
	S3Region                        string
	S3Endpoint                      string
	S3KeyID                         string
	S3KeySecret                     string
	XbcloudBin                      string
	XbcloudParallel                 int
	XbcloudFIFOStreams              int
	PhysicalEnabled                 bool
	PhysicalS3UploadEnabled         bool
	XtrabackupBin                   string
	XtrabackupParallel              int
	XtrabackupUser                  string
	XtrabackupPass                  string
	XtrabackupSocket                string
	XtrabackupRunAsUser             string
	XtrabackupWorkDir               string
	APIEnabled                      bool
	APIListenAddr                   string
	APIBasePath                     string
	APIAuthEnabled                  bool
	APIBearerToken                  string
	ExecutionSource                 string
	PreflightOnly                   bool
	MetricsJob                      string
	MetricsService                  string
	MetricsEnv                      string
	MetricsRegion                   string
	RestoreTestEnabled              bool
	RestoreTestHost                 string
	RestoreTestPort                 string
	RestoreTestUser                 string
	RestoreTestPass                 string
	AllowRootRunner                 bool
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
	dbEngine := strings.ToLower(getMapValue(values, "DB_ENGINE", "mysql"))
	dbPortDefault := "3306"
	restorePortDefault := "3306"
	restoreUserDefault := "root"
	if dbEngine == "postgres" {
		dbPortDefault = "5432"
		restorePortDefault = "5432"
		restoreUserDefault = "postgres"
	}

	return config{

		StorageAutoCleanup:              getMapBool(values, "BACKUP_STORAGE_AUTO_CLEANUP", false),
		StorageMaxLocalBackups:          getMapInt(values, "BACKUP_LOCAL_MAX_BACKUPS", 0),
		StorageHighWatermark:            getMapInt(values, "BACKUP_STORAGE_HIGH_WATERMARK", 92),
		StorageLowWatermark:             getMapInt(values, "BACKUP_STORAGE_LOW_WATERMARK", 90),
		StorageGrowthPercent:            getMapInt(values, "BACKUP_STORAGE_GROWTH_PERCENT", 50),
		StorageHistoryRuns:              getMapInt(values, "BACKUP_STORAGE_HISTORY_RUNS", 12),
		StorageConcurrentBytes:          getMapInt(values, "BACKUP_STORAGE_CONCURRENT_BYTES", 512<<20),
		StorageTemporaryBytes:           getMapInt(values, "BACKUP_STORAGE_TEMPORARY_BYTES", 256<<20),
		StorageMinFreeInodes:            getMapInt(values, "BACKUP_STORAGE_MIN_FREE_INODES", 128),
		StorageTempMaxAge:               getMapDuration(values, "BACKUP_STORAGE_TEMP_MAX_AGE", 24*time.Hour),
		StorageVerifyTimeout:            getMapDuration(values, "BACKUP_STORAGE_VERIFY_TIMEOUT", 20*time.Minute),
		StorageSecondaryDir:             getMapValue(values, "BACKUP_STORAGE_SECONDARY_DIR", ""),
		RetentionDeleteEnabled:          getMapBool(values, "BACKUP_RETENTION_DELETE_ENABLED", false),
		RetentionMinComplete:            max(1, getMapInt(values, "BACKUP_RETENTION_MIN_COMPLETE", 2)),
		StorageReserveBytes:             getMapInt(values, "BACKUP_STORAGE_RESERVE_BYTES", 2<<30),
		StorageEstimateBytes:            getMapInt(values, "BACKUP_STORAGE_ESTIMATE_BYTES", 0),
		SystemdScope:                    getMapValue(values, "BACKUP_SYSTEMD_SCOPE", "auto"),
		LogicalConsistency:              getMapValue(values, "BACKUP_LOGICAL_CONSISTENCY", "per_database"),
		ScheduleTimezone:                getMapValue(values, "BACKUP_SCHEDULE_TIMEZONE", "Local"),
		MySQLDumpSessionTimeoutOverride: getMapBool(values, "BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE", true),
		DBUser:                          dbUser,
		DBPass:                          dbPass,
		DBEngine:                        dbEngine,
		DBHost:                          getMapValue(values, "DB_HOST", "127.0.0.1"),
		DBPort:                          getMapValue(values, "DB_PORT", dbPortDefault),
		BackupDir:                       backupDir,
		LogDir:                          logDir,
		RunLogPath:                      filepath.Join(logDir, "backup-runs.jsonl"),
		MetricsFile:                     getMapValue(values, "BACKUP_METRICS_FILE", defaultMetricsFilePath),
		LockFile:                        getMapValue(values, "BACKUP_LOCK_FILE", filepath.Join(logDir, "backup.lock")),
		ServiceUnitName:                 getMapValue(values, "BACKUP_SYSTEMD_SERVICE_NAME", "sdl-db-backup.service"),
		TimerUnitName:                   getMapValue(values, "BACKUP_SYSTEMD_TIMER_NAME", "sdl-db-backup.timer"),
		MySQLBin:                        getMapValue(values, "MYSQL_BIN", "mysql"),
		MySQLDumpBin:                    getMapValue(values, "MYSQLDUMP_BIN", "mysqldump"),
		RetryCount:                      getMapInt(values, "BACKUP_RETRY_COUNT", 3),
		RetentionDaily:                  getMapInt(values, "BACKUP_RETENTION_DAILY", 7),
		RetentionWeekly:                 getMapInt(values, "BACKUP_RETENTION_WEEKLY", 4),
		RetentionMonthly:                getMapInt(values, "BACKUP_RETENTION_MONTHLY", 12),
		RetentionMinVerified:            getMapInt(values, "BACKUP_RETENTION_MIN_VERIFIED", 1),
		LogicalEnabled:                  getMapBool(values, "BACKUP_LOGICAL_ENABLED", true),
		LogicalTimeoutPerDB:             logicalTimeoutPerDB,
		ExactRowCounts:                  getMapBool(values, "BACKUP_EXACT_ROW_COUNTS", false),
		SampleDataChecks:                getMapBool(values, "BACKUP_SAMPLE_DATA_CHECKS", false),
		SampleDataRows:                  max(1, getMapInt(values, "BACKUP_SAMPLE_DATA_ROWS", 50)),
		LogicalParallel:                 getMapInt(values, "BACKUP_LOGICAL_PARALLEL", 2),
		LogicalGzipLevel:                getMapInt(values, "BACKUP_LOGICAL_GZIP_LEVEL", 1),
		LogicalS3UploadEnabled:          getMapBool(values, "BACKUP_LOGICAL_S3_UPLOAD_ENABLED", false),
		RequireRemoteCompletion:         getMapBool(values, "BACKUP_REQUIRE_REMOTE_COMPLETION", true),
		LogicalSchedule:                 getMapValue(values, "BACKUP_LOGICAL_SCHEDULE", "always"),
		LogicalDatabases:                parseCSVList(getMapValue(values, "BACKUP_LOGICAL_DATABASES", "")),
		LogicalTables:                   parseTableScope(getMapValue(values, "BACKUP_LOGICAL_TABLES", "")),
		PhysicalSchedule:                getMapValue(values, "BACKUP_PHYSICAL_SCHEDULE", "always"),
		PhysicalTimeout:                 getMapDuration(values, "BACKUP_PHYSICAL_TIMEOUT", 6*time.Hour),
		DiscoveryTimeout:                getMapDuration(values, "BACKUP_DISCOVERY_TIMEOUT", 30*time.Second),
		PreflightTimeout:                getMapDuration(values, "BACKUP_PREFLIGHT_TIMEOUT", 15*time.Second),
		RetryBaseDelay:                  getMapDuration(values, "BACKUP_RETRY_BASE_DELAY", 2*time.Second),
		RetryMaxDelay:                   getMapDuration(values, "BACKUP_RETRY_MAX_DELAY", 20*time.Second),
		CleanupFailFatal:                getMapBool(values, "BACKUP_CLEANUP_FAIL_FATAL", false),
		AllowObjectOmissions:            getMapBool(values, "BACKUP_ALLOW_OBJECT_OMISSIONS", false),
		MySQLPITRMetadataEnabled:        getMapBool(values, "BACKUP_MYSQL_PITR_METADATA_ENABLED", false) || getMapBool(values, "BACKUP_MYSQL_PITR_METADATA_REQUIRED", false),
		MySQLPITRMetadataRequired:       getMapBool(values, "BACKUP_MYSQL_PITR_METADATA_REQUIRED", false),
		S3UploadURL:                     getMapValue(values, "BACKUP_S3_UPLOAD_URL", ""),
		S3UploadTimeout:                 getMapDuration(values, "BACKUP_S3_UPLOAD_TIMEOUT", 2*time.Hour),
		S3UploadMode:                    strings.ToLower(getMapValue(values, "BACKUP_S3_UPLOAD_MODE", "direct")),
		S3PHPBin:                        getMapValue(values, "BACKUP_S3_PHP_BIN", "php"),
		S3UploadScript:                  getMapValue(values, "BACKUP_S3_UPLOAD_SCRIPT", ""),
		S3LogicalPrefix:                 getMapValue(values, "BACKUP_S3_LOGICAL_PREFIX", "logical"),
		S3PhysicalPrefix:                getMapValue(values, "BACKUP_S3_PHYSICAL_PREFIX", "backup-as-it-is"),
		S3Bucket:                        getMapValue(values, "BACKUP_S3_BUCKET", ""),
		S3Region:                        getMapValue(values, "BACKUP_S3_REGION", "ap-south-1"),
		S3Endpoint:                      getMapValue(values, "BACKUP_S3_ENDPOINT", ""),
		S3KeyID:                         s3KeyID,
		S3KeySecret:                     s3KeySecret,
		EncryptionKey:                   getMapValue(values, "BACKUP_ENCRYPTION_KEY", ""),
		XbcloudBin:                      getMapValue(values, "BACKUP_XBCLOUD_BIN", "xbcloud"),
		XbcloudParallel:                 getMapInt(values, "BACKUP_XBCLOUD_PARALLEL", 2),
		XbcloudFIFOStreams:              getMapInt(values, "BACKUP_XBCLOUD_FIFO_STREAMS", 1),
		PhysicalEnabled:                 getMapBool(values, "BACKUP_PHYSICAL_ENABLED", false),
		PhysicalS3UploadEnabled:         getMapBool(values, "BACKUP_PHYSICAL_S3_UPLOAD_ENABLED", false),
		XtrabackupBin:                   getMapValue(values, "BACKUP_XTRABACKUP_BIN", "xtrabackup"),
		XtrabackupParallel:              getMapInt(values, "BACKUP_XTRABACKUP_PARALLEL", 4),
		XtrabackupUser:                  getMapValue(values, "BACKUP_XTRABACKUP_USER", dbUser),
		XtrabackupPass:                  getMapValue(values, "BACKUP_XTRABACKUP_PASS", dbPass),
		XtrabackupSocket:                getMapValue(values, "BACKUP_XTRABACKUP_SOCKET", ""),
		XtrabackupRunAsUser:             getMapValue(values, "BACKUP_XTRABACKUP_RUN_AS_USER", ""),
		XtrabackupWorkDir:               getMapValue(values, "BACKUP_XTRABACKUP_WORK_DIR", filepath.Join(backupDir, "xtrabackup-work")),
		APIEnabled:                      getMapBool(values, "BACKUP_API_ENABLED", false),
		APIListenAddr:                   getMapValue(values, "BACKUP_API_LISTEN_ADDR", "127.0.0.1:8086"),
		APIBasePath:                     getMapValue(values, "BACKUP_API_BASE_PATH", "/api/v1"),
		APIAuthEnabled:                  getMapBool(values, "BACKUP_API_AUTH_ENABLED", false),
		APIBearerToken:                  getMapValue(values, "BACKUP_API_BEARER_TOKEN", ""),
		ExecutionSource:                 getMapValue(values, "BACKUP_EXECUTION_SOURCE", "runner"),
		MetricsJob:                      getMapValue(values, "BACKUP_METRICS_JOB", "sdl_db_backup"),
		MetricsService:                  getMapValue(values, "BACKUP_METRICS_SERVICE", "mysql"),
		MetricsEnv:                      getMapValue(values, "BACKUP_METRICS_ENV", "pilot"),
		MetricsRegion:                   getMapValue(values, "BACKUP_METRICS_REGION", ""),
		RestoreTestEnabled:              strings.ToLower(getMapValue(values, "RESTORE_TEST_ENABLED", "false")) == "true",
		RestoreTestHost:                 getMapValue(values, "RESTORE_TEST_HOST", ""),
		RestoreTestPort:                 getMapValue(values, "RESTORE_TEST_PORT", restorePortDefault),
		RestoreTestUser:                 getMapValue(values, "RESTORE_TEST_USER", restoreUserDefault),
		RestoreTestPass:                 getMapValue(values, "RESTORE_TEST_PASS", ""),
		AllowRootRunner:                 getMapBool(values, "BACKUP_ALLOW_ROOT_RUNNER", false),
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
	return atomicWriteFile(TemporaryOverridePath(base), append(data, '\n'), 0o640)
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
	return []string{"BACKUP_LOCAL_MAX_BACKUPS", "BACKUP_STORAGE_AUTO_CLEANUP", "BACKUP_STORAGE_HIGH_WATERMARK", "BACKUP_STORAGE_LOW_WATERMARK", "BACKUP_STORAGE_GROWTH_PERCENT", "BACKUP_STORAGE_HISTORY_RUNS", "BACKUP_STORAGE_CONCURRENT_BYTES", "BACKUP_STORAGE_TEMPORARY_BYTES", "BACKUP_STORAGE_MIN_FREE_INODES", "BACKUP_STORAGE_TEMP_MAX_AGE", "BACKUP_STORAGE_VERIFY_TIMEOUT", "BACKUP_STORAGE_SECONDARY_DIR", "BACKUP_RETENTION_DELETE_ENABLED", "BACKUP_RETENTION_MIN_COMPLETE", "BACKUP_STORAGE_RESERVE_BYTES", "BACKUP_STORAGE_ESTIMATE_BYTES", "BACKUP_SYSTEMD_SCOPE", "BACKUP_LOGICAL_CONSISTENCY", "BACKUP_SCHEDULE_TIMEZONE", "BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE",
		"DB_USER",
		"DB_PASS",
		"DB_ENGINE",
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
		"BACKUP_EXACT_ROW_COUNTS",
		"BACKUP_SAMPLE_DATA_CHECKS",
		"BACKUP_SAMPLE_DATA_ROWS",
		"BACKUP_LOGICAL_SCHEDULE",
		"BACKUP_LOGICAL_DATABASES",
		"BACKUP_LOGICAL_TABLES",
		"BACKUP_LOGICAL_TIMEOUT_PER_DB",
		"BACKUP_LOGICAL_PARALLEL",
		"BACKUP_LOGICAL_GZIP_LEVEL",
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED",
		"BACKUP_REQUIRE_REMOTE_COMPLETION",
		"BACKUP_PHYSICAL_ENABLED",
		"BACKUP_PHYSICAL_SCHEDULE",
		"BACKUP_PHYSICAL_TIMEOUT",
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED",
		"BACKUP_ENCRYPTION_KEY",
		"BACKUP_DISCOVERY_TIMEOUT",
		"BACKUP_PREFLIGHT_TIMEOUT",
		"BACKUP_RETENTION_DAILY",
		"BACKUP_RETENTION_WEEKLY",
		"BACKUP_RETENTION_MONTHLY",
		"BACKUP_RETENTION_MIN_VERIFIED",
		"BACKUP_CLEANUP_FAIL_FATAL",
		"BACKUP_ALLOW_OBJECT_OMISSIONS",
		"BACKUP_MYSQL_PITR_METADATA_ENABLED",
		"BACKUP_MYSQL_PITR_METADATA_REQUIRED",
		"BACKUP_LOCK_FILE",
		"BACKUP_S3_UPLOAD_URL",
		"BACKUP_S3_UPLOAD_TIMEOUT",
		"BACKUP_S3_UPLOAD_MODE",
		"BACKUP_S3_PHP_BIN",
		"BACKUP_S3_UPLOAD_SCRIPT",
		"BACKUP_S3_BUCKET",
		"BACKUP_S3_REGION",
		"BACKUP_S3_ENDPOINT",
		"BACKUP_S3_LOGICAL_PREFIX",
		"BACKUP_S3_PHYSICAL_PREFIX",
		"BACKUP_S3_KEY_ID",
		"BACKUP_S3_KEY_SECRET",
		"BACKUP_XBCLOUD_BIN",
		"BACKUP_XBCLOUD_PARALLEL",
		"BACKUP_XBCLOUD_FIFO_STREAMS",
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
		"BACKUP_METRICS_JOB",
		"BACKUP_METRICS_SERVICE",
		"BACKUP_METRICS_ENV",
		"BACKUP_METRICS_REGION",
		"BACKUP_ALLOW_ROOT_RUNNER",
		"RESTORE_TEST_ENABLED",
		"RESTORE_TEST_HOST",
		"RESTORE_TEST_PORT",
		"RESTORE_TEST_USER",
		"RESTORE_TEST_PASS",
	}
}

func envMapFromConfig(cfg Config) map[string]string {
	values := map[string]string{
		"BACKUP_STORAGE_AUTO_CLEANUP":               fmt.Sprint(cfg.StorageAutoCleanup),
		"BACKUP_LOCAL_MAX_BACKUPS":                  fmt.Sprint(cfg.StorageMaxLocalBackups),
		"BACKUP_STORAGE_HIGH_WATERMARK":             fmt.Sprint(cfg.StorageHighWatermark),
		"BACKUP_STORAGE_LOW_WATERMARK":              fmt.Sprint(cfg.StorageLowWatermark),
		"BACKUP_STORAGE_GROWTH_PERCENT":             fmt.Sprint(cfg.StorageGrowthPercent),
		"BACKUP_STORAGE_HISTORY_RUNS":               fmt.Sprint(cfg.StorageHistoryRuns),
		"BACKUP_STORAGE_CONCURRENT_BYTES":           fmt.Sprint(cfg.StorageConcurrentBytes),
		"BACKUP_STORAGE_TEMPORARY_BYTES":            fmt.Sprint(cfg.StorageTemporaryBytes),
		"BACKUP_STORAGE_MIN_FREE_INODES":            fmt.Sprint(cfg.StorageMinFreeInodes),
		"BACKUP_STORAGE_TEMP_MAX_AGE":               cfg.StorageTempMaxAge.String(),
		"BACKUP_STORAGE_VERIFY_TIMEOUT":             cfg.StorageVerifyTimeout.String(),
		"BACKUP_STORAGE_SECONDARY_DIR":              cfg.StorageSecondaryDir,
		"BACKUP_RETENTION_DELETE_ENABLED":           fmt.Sprint(cfg.RetentionDeleteEnabled),
		"BACKUP_RETENTION_MIN_COMPLETE":             fmt.Sprint(cfg.RetentionMinComplete),
		"BACKUP_STORAGE_RESERVE_BYTES":              fmt.Sprint(cfg.StorageReserveBytes),
		"BACKUP_STORAGE_ESTIMATE_BYTES":             fmt.Sprint(cfg.StorageEstimateBytes),
		"BACKUP_SYSTEMD_SCOPE":                      cfg.SystemdScope,
		"BACKUP_LOGICAL_CONSISTENCY":                cfg.LogicalConsistency,
		"BACKUP_SCHEDULE_TIMEZONE":                  cfg.ScheduleTimezone,
		"BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE": fmt.Sprint(cfg.MySQLDumpSessionTimeoutOverride),
		"DB_USER":                             cfg.DBUser,
		"DB_PASS":                             cfg.DBPass,
		"DB_ENGINE":                           cfg.DBEngine,
		"DB_HOST":                             cfg.DBHost,
		"DB_PORT":                             cfg.DBPort,
		"BACKUP_DIR":                          cfg.BackupDir,
		"BACKUP_LOG_DIR":                      cfg.LogDir,
		"BACKUP_METRICS_FILE":                 cfg.MetricsFile,
		"BACKUP_SYSTEMD_SERVICE_NAME":         cfg.ServiceUnitName,
		"BACKUP_SYSTEMD_TIMER_NAME":           cfg.TimerUnitName,
		"MYSQL_BIN":                           cfg.MySQLBin,
		"MYSQLDUMP_BIN":                       cfg.MySQLDumpBin,
		"BACKUP_RETRY_COUNT":                  strconv.Itoa(cfg.RetryCount),
		"BACKUP_RETRY_BASE_DELAY":             cfg.RetryBaseDelay.String(),
		"BACKUP_RETRY_MAX_DELAY":              cfg.RetryMaxDelay.String(),
		"BACKUP_LOGICAL_ENABLED":              strconv.FormatBool(cfg.LogicalEnabled),
		"BACKUP_EXACT_ROW_COUNTS":             strconv.FormatBool(cfg.ExactRowCounts),
		"BACKUP_SAMPLE_DATA_CHECKS":           strconv.FormatBool(cfg.SampleDataChecks),
		"BACKUP_SAMPLE_DATA_ROWS":             strconv.Itoa(cfg.SampleDataRows),
		"BACKUP_LOGICAL_SCHEDULE":             cfg.LogicalSchedule,
		"BACKUP_LOGICAL_DATABASES":            strings.Join(cfg.LogicalDatabases, ","),
		"BACKUP_LOGICAL_TABLES":               formatTableScope(cfg.LogicalTables),
		"BACKUP_LOGICAL_TIMEOUT_PER_DB":       cfg.LogicalTimeoutPerDB.String(),
		"BACKUP_LOGICAL_PARALLEL":             strconv.Itoa(cfg.LogicalParallel),
		"BACKUP_LOGICAL_GZIP_LEVEL":           strconv.Itoa(cfg.LogicalGzipLevel),
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED":    strconv.FormatBool(cfg.LogicalS3UploadEnabled),
		"BACKUP_REQUIRE_REMOTE_COMPLETION":    strconv.FormatBool(cfg.RequireRemoteCompletion),
		"BACKUP_PHYSICAL_ENABLED":             strconv.FormatBool(cfg.PhysicalEnabled),
		"BACKUP_PHYSICAL_SCHEDULE":            cfg.PhysicalSchedule,
		"BACKUP_PHYSICAL_TIMEOUT":             cfg.PhysicalTimeout.String(),
		"BACKUP_PHYSICAL_S3_UPLOAD_ENABLED":   strconv.FormatBool(cfg.PhysicalS3UploadEnabled),
		"BACKUP_ENCRYPTION_KEY":               cfg.EncryptionKey,
		"BACKUP_DISCOVERY_TIMEOUT":            cfg.DiscoveryTimeout.String(),
		"BACKUP_PREFLIGHT_TIMEOUT":            cfg.PreflightTimeout.String(),
		"BACKUP_RETENTION_DAILY":              strconv.Itoa(cfg.RetentionDaily),
		"BACKUP_RETENTION_WEEKLY":             strconv.Itoa(cfg.RetentionWeekly),
		"BACKUP_RETENTION_MONTHLY":            strconv.Itoa(cfg.RetentionMonthly),
		"BACKUP_RETENTION_MIN_VERIFIED":       strconv.Itoa(cfg.RetentionMinVerified),
		"BACKUP_CLEANUP_FAIL_FATAL":           strconv.FormatBool(cfg.CleanupFailFatal),
		"BACKUP_ALLOW_OBJECT_OMISSIONS":       strconv.FormatBool(cfg.AllowObjectOmissions),
		"BACKUP_MYSQL_PITR_METADATA_ENABLED":  strconv.FormatBool(cfg.MySQLPITRMetadataEnabled),
		"BACKUP_MYSQL_PITR_METADATA_REQUIRED": strconv.FormatBool(cfg.MySQLPITRMetadataRequired),
		"BACKUP_LOCK_FILE":                    cfg.LockFile,
		"BACKUP_S3_UPLOAD_URL":                cfg.S3UploadURL,
		"BACKUP_S3_UPLOAD_TIMEOUT":            cfg.S3UploadTimeout.String(),
		"BACKUP_S3_UPLOAD_MODE":               cfg.S3UploadMode,
		"BACKUP_S3_PHP_BIN":                   cfg.S3PHPBin,
		"BACKUP_S3_UPLOAD_SCRIPT":             cfg.S3UploadScript,
		"BACKUP_S3_BUCKET":                    cfg.S3Bucket,
		"BACKUP_S3_REGION":                    cfg.S3Region,
		"BACKUP_S3_ENDPOINT":                  cfg.S3Endpoint,
		"BACKUP_S3_LOGICAL_PREFIX":            cfg.S3LogicalPrefix,
		"BACKUP_S3_PHYSICAL_PREFIX":           cfg.S3PhysicalPrefix,
		"BACKUP_S3_KEY_ID":                    cfg.S3KeyID,
		"BACKUP_S3_KEY_SECRET":                cfg.S3KeySecret,
		"BACKUP_XBCLOUD_BIN":                  cfg.XbcloudBin,
		"BACKUP_XBCLOUD_PARALLEL":             strconv.Itoa(cfg.XbcloudParallel),
		"BACKUP_XBCLOUD_FIFO_STREAMS":         strconv.Itoa(cfg.XbcloudFIFOStreams),
		"BACKUP_XTRABACKUP_BIN":               cfg.XtrabackupBin,
		"BACKUP_XTRABACKUP_PARALLEL":          strconv.Itoa(cfg.XtrabackupParallel),
		"BACKUP_XTRABACKUP_USER":              cfg.XtrabackupUser,
		"BACKUP_XTRABACKUP_PASS":              cfg.XtrabackupPass,
		"BACKUP_XTRABACKUP_SOCKET":            cfg.XtrabackupSocket,
		"BACKUP_XTRABACKUP_RUN_AS_USER":       cfg.XtrabackupRunAsUser,
		"BACKUP_XTRABACKUP_WORK_DIR":          cfg.XtrabackupWorkDir,
		"BACKUP_API_ENABLED":                  strconv.FormatBool(cfg.APIEnabled),
		"BACKUP_API_LISTEN_ADDR":              cfg.APIListenAddr,
		"BACKUP_API_BASE_PATH":                cfg.APIBasePath,
		"BACKUP_API_AUTH_ENABLED":             strconv.FormatBool(cfg.APIAuthEnabled),
		"BACKUP_API_BEARER_TOKEN":             cfg.APIBearerToken,
		"BACKUP_METRICS_JOB":                  cfg.MetricsJob,
		"BACKUP_METRICS_SERVICE":              cfg.MetricsService,
		"BACKUP_METRICS_ENV":                  cfg.MetricsEnv,
		"BACKUP_METRICS_REGION":               cfg.MetricsRegion,
		"BACKUP_ALLOW_ROOT_RUNNER":            strconv.FormatBool(cfg.AllowRootRunner),
		"RESTORE_TEST_ENABLED":                fmt.Sprintf("%t", cfg.RestoreTestEnabled),
		"RESTORE_TEST_HOST":                   cfg.RestoreTestHost,
		"RESTORE_TEST_PORT":                   cfg.RestoreTestPort,
		"RESTORE_TEST_USER":                   cfg.RestoreTestUser,
		"RESTORE_TEST_PASS":                   cfg.RestoreTestPass,
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
	if cfg.StorageAutoCleanup {
		if err := storagePolicy(cfg); err != nil {
			return err
		}
	}
	path := ResolveEnvFilePath(envPath)
	if _, e := time.LoadLocation(getMapValue(map[string]string{"zone": cfg.ScheduleTimezone}, "zone", "Local")); e != nil {
		return fmt.Errorf("invalid schedule timezone: %w", e)
	}
	for _, schedule := range []string{cfg.LogicalSchedule, cfg.PhysicalSchedule} {
		if schedule != "" {
			if _, _, e := evaluateSchedule(time.Now(), schedule, time.Time{}); e != nil {
				return e
			}
		}
	}
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
	return atomicWriteFile(path, []byte(output), 0o600)
}
