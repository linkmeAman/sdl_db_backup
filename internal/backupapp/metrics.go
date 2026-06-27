package backupapp

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultMetricsFilePath = "/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom"
const backupMetricsFileMode = 0o644
const backupMetricsUpdateInterval = 5 * time.Second

// metricsLabelString builds the Prometheus label set for all metrics emitted
// by this tool. The default label set preserves the original monitoring
// contract: job="sdl_db_backup", service="mysql", and env="pilot". Region is
// optional and only emitted when configured.
func metricsLabelString(cfg config) string {
	job := cfg.MetricsJob
	if job == "" {
		job = "sdl_db_backup"
	}
	service := cfg.MetricsService
	if service == "" {
		service = "mysql"
	}
	env := strings.TrimSpace(cfg.MetricsEnv)
	if env == "" {
		env = "pilot"
	}
	region := strings.TrimSpace(cfg.MetricsRegion)

	labels := []string{fmt.Sprintf(`job=%q`, job), fmt.Sprintf(`service=%q`, service), fmt.Sprintf(`env=%q`, env)}
	if region != "" {
		labels = append(labels, fmt.Sprintf(`region=%q`, region))
	}
	return strings.Join(labels, ",")
}

type backupMetricsSnapshot struct {
	RunTimestamp                int64
	SuccessTimestamp            int64
	DurationSeconds             float64
	SizeBytes                   int64
	Status                      int
	UploadSuccess               int
	MetricsWriteSuccess         int
	CleanupSuccess              int
	CleanupTimestamp            int64
	LogicalAttempted            int
	LogicalStatus               int
	LogicalTotalDatabases       int
	LogicalSucceededDatabases   int
	LogicalFailedDatabases      int
	PhysicalAttempted           int
	PhysicalStatus              int
	PhysicalDurationSeconds     float64
	AdaptiveLoadPerCPU          float64
	AdaptiveLogicalParallel     int
	AdaptiveXtrabackupParallel  int
	AdaptiveXbcloudParallel     int
	PhysicalRetryCount          int
	PhysicalRateLimitRetryCount int
	RunInProgress               int
	CurrentRunStartTimestamp    int64
	CurrentRunDurationSeconds   float64
	MetricsLastUpdateTimestamp  int64
	LogicalValidationStatus     int
	DatabaseRowCounts           map[string]int64
}

type ObservabilityReport struct {
	MetricsPath       string
	MetricsFileExists bool
	MetricsFileSize   int64
	MetricsFileMode   string
	MetricsFileTime   time.Time
	MetricsWritable   bool
	MetricsStatus     string
	LastWriteResult   string
	LastUpdateTime    time.Time
	Snapshot          map[string]string
}

func resolvedMetricsFilePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return defaultMetricsFilePath
	}
	return path
}

func buildFinalBackupMetricsSnapshot(cfg config, record runRecord, startedAt time.Time, logicalDue bool, physicalDue bool, uploadRequired bool, uploadSucceeded bool) backupMetricsSnapshot {
	runEndedAt := time.Now().UTC()
	previous := readExistingBackupMetricsSnapshot(cfg.MetricsFile)
	if record.Status == "skipped" {
		previous.RunInProgress = 0
		previous.CurrentRunStartTimestamp = 0
		previous.CurrentRunDurationSeconds = 0
		previous.MetricsLastUpdateTimestamp = runEndedAt.Unix()
		return previous
	}

	successTimestamp := readLastSuccessTimestamp(cfg.MetricsFile)
	status := 0
	if record.Status == "success" && (!uploadRequired || uploadSucceeded) {
		status = 1
		successTimestamp = runEndedAt.Unix()
	}

	uploadSuccess := 0
	if uploadRequired && uploadSucceeded {
		uploadSuccess = 1
	}
	cleanupSuccess := 1
	if strings.TrimSpace(record.CleanupError) != "" {
		cleanupSuccess = 0
	}
	cleanupTimestamp := previous.CleanupTimestamp
	if strings.TrimSpace(record.RunFolder) != "" {
		cleanupTimestamp = runEndedAt.Unix()
	}

	logicalAttempted := 0
	logicalStatus := 0
	logicalTotalDatabases := 0
	logicalSucceededDatabases := 0
	logicalFailedDatabases := 0
	if logicalDue {
		logicalAttempted = 1
		logicalTotalDatabases = record.DatabasesTotal
		logicalSucceededDatabases = record.DatabasesSucceeded
		logicalFailedDatabases = record.DatabasesFailed
		if record.DatabasesFailed == 0 {
			logicalStatus = 1
		}
	}

	physicalAttempted := 0
	physicalStatus := 0
	physicalDurationSeconds := 0.0
	adaptivePhysicalParallel := record.AdaptivePhysicalParallel
	adaptiveXbcloudParallel := record.AdaptiveXbcloudParallel
	if physicalDue {
		physicalAttempted = 1
		if record.PhysicalBackup != nil && record.PhysicalBackup.Status == "success" {
			physicalStatus = 1
		}
		if record.PhysicalBackup != nil {
			physicalDurationSeconds = parseMetricDurationSeconds(record.PhysicalBackup.Duration)
			if record.PhysicalBackup.XtrabackupParallel > 0 {
				adaptivePhysicalParallel = record.PhysicalBackup.XtrabackupParallel
			}
			if record.PhysicalBackup.XbcloudParallel > 0 {
				adaptiveXbcloudParallel = record.PhysicalBackup.XbcloudParallel
			}
		}
	}

	dbRowCounts := make(map[string]int64)
	for _, res := range record.Results {
		if res.RowCounts > 0 {
			dbRowCounts[res.Name] = res.RowCounts
		}
	}

	return backupMetricsSnapshot{
		RunTimestamp:                runEndedAt.Unix(),
		SuccessTimestamp:            successTimestamp,
		DurationSeconds:             time.Since(startedAt).Seconds(),
		SizeBytes:                   totalLogicalArtifactSize(record.Results),
		Status:                      status,
		UploadSuccess:               uploadSuccess,
		MetricsWriteSuccess:         1,
		CleanupSuccess:              cleanupSuccess,
		CleanupTimestamp:            cleanupTimestamp,
		LogicalValidationStatus:     previous.LogicalValidationStatus,
		LogicalAttempted:            logicalAttempted,
		LogicalStatus:               logicalStatus,
		LogicalTotalDatabases:       logicalTotalDatabases,
		LogicalSucceededDatabases:   logicalSucceededDatabases,
		LogicalFailedDatabases:      logicalFailedDatabases,
		PhysicalAttempted:           physicalAttempted,
		PhysicalStatus:              physicalStatus,
		PhysicalDurationSeconds:     physicalDurationSeconds,
		AdaptiveLoadPerCPU:          record.AdaptiveLoadPerCPU,
		AdaptiveLogicalParallel:     record.AdaptiveLogicalParallel,
		AdaptiveXtrabackupParallel:  adaptivePhysicalParallel,
		AdaptiveXbcloudParallel:     adaptiveXbcloudParallel,
		PhysicalRetryCount:          physicalRetryCount(record.PhysicalBackup),
		PhysicalRateLimitRetryCount: physicalRateLimitRetryCount(record.PhysicalBackup),
		RunInProgress:               0,
		CurrentRunStartTimestamp:    0,
		CurrentRunDurationSeconds:   0,
		MetricsLastUpdateTimestamp:  runEndedAt.Unix(),
		DatabaseRowCounts:           dbRowCounts,
	}
}

func buildInProgressBackupMetricsSnapshot(cfg config, startedAt time.Time) backupMetricsSnapshot {
	now := time.Now().UTC()
	previous := readExistingBackupMetricsSnapshot(cfg.MetricsFile)
	previous.MetricsWriteSuccess = 1
	previous.RunInProgress = 1
	previous.CurrentRunStartTimestamp = startedAt.UTC().Unix()
	previous.CurrentRunDurationSeconds = time.Since(startedAt).Seconds()
	previous.MetricsLastUpdateTimestamp = now.Unix()
	return previous
}

func totalLogicalArtifactSize(results []databaseResult) int64 {
	var total int64
	for _, result := range results {
		if result.SizeBytes > 0 {
			total += result.SizeBytes
		}
	}
	return total
}

func parseMetricDurationSeconds(raw string) float64 {
	duration, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return duration.Seconds()
}

func physicalRetryCount(result *physicalBackupResult) int {
	if result == nil || result.Attempts < 1 {
		return 0
	}
	return result.Attempts
}

func physicalRateLimitRetryCount(result *physicalBackupResult) int {
	if result == nil || result.RateLimitRetries < 0 {
		return 0
	}
	return result.RateLimitRetries
}

func readLastSuccessTimestamp(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "backup_last_success_timestamp") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func readExistingBackupMetricsSnapshot(path string) backupMetricsSnapshot {
	file, err := os.Open(path)
	if err != nil {
		return backupMetricsSnapshot{
			LogicalValidationStatus: -1,
		}
	}
	defer file.Close()

	snapshot := backupMetricsSnapshot{
		LogicalValidationStatus: -1,
		DatabaseRowCounts: make(map[string]int64),
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if brace := strings.IndexByte(name, '{'); brace >= 0 {
			name = name[:brace]
		}
		value := fields[len(fields)-1]
		switch name {
		case "backup_last_run_timestamp":
			snapshot.RunTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_success_timestamp":
			snapshot.SuccessTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_duration_seconds":
			snapshot.DurationSeconds, _ = strconv.ParseFloat(value, 64)
		case "backup_last_size_bytes":
			snapshot.SizeBytes, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_status":
			snapshot.Status, _ = strconv.Atoi(value)
		case "backup_upload_success":
			snapshot.UploadSuccess, _ = strconv.Atoi(value)
		case "backup_metrics_write_success":
			snapshot.MetricsWriteSuccess, _ = strconv.Atoi(value)
		case "backup_cleanup_success":
			snapshot.CleanupSuccess, _ = strconv.Atoi(value)
		case "backup_last_cleanup_timestamp":
			snapshot.CleanupTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_logical_last_attempted":
			snapshot.LogicalAttempted, _ = strconv.Atoi(value)
		case "backup_logical_last_status":
			snapshot.LogicalStatus, _ = strconv.Atoi(value)
		case "backup_logical_last_total_databases":
			snapshot.LogicalTotalDatabases, _ = strconv.Atoi(value)
		case "backup_logical_last_succeeded_databases":
			snapshot.LogicalSucceededDatabases, _ = strconv.Atoi(value)
		case "backup_logical_last_failed_databases":
			snapshot.LogicalFailedDatabases, _ = strconv.Atoi(value)
		case "backup_physical_last_attempted":
			snapshot.PhysicalAttempted, _ = strconv.Atoi(value)
		case "backup_physical_last_status":
			snapshot.PhysicalStatus, _ = strconv.Atoi(value)
		case "backup_physical_last_duration_seconds":
			snapshot.PhysicalDurationSeconds, _ = strconv.ParseFloat(value, 64)
		case "backup_adaptive_load_per_cpu":
			snapshot.AdaptiveLoadPerCPU, _ = strconv.ParseFloat(value, 64)
		case "backup_adaptive_logical_parallel":
			snapshot.AdaptiveLogicalParallel, _ = strconv.Atoi(value)
		case "backup_adaptive_xtrabackup_parallel":
			snapshot.AdaptiveXtrabackupParallel, _ = strconv.Atoi(value)
		case "backup_adaptive_xbcloud_parallel":
			snapshot.AdaptiveXbcloudParallel, _ = strconv.Atoi(value)
		case "backup_physical_retry_count":
			snapshot.PhysicalRetryCount, _ = strconv.Atoi(value)
		case "backup_physical_rate_limit_retry_count":
			snapshot.PhysicalRateLimitRetryCount, _ = strconv.Atoi(value)
		case "backup_run_in_progress":
			snapshot.RunInProgress, _ = strconv.Atoi(value)
		case "backup_current_run_start_timestamp":
			snapshot.CurrentRunStartTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_current_run_duration_seconds":
			snapshot.CurrentRunDurationSeconds, _ = strconv.ParseFloat(value, 64)
		case "backup_metrics_last_update_timestamp":
			snapshot.MetricsLastUpdateTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_logical_validation_last_status":
			snapshot.LogicalValidationStatus, _ = strconv.Atoi(value)
		default:
			if strings.HasPrefix(name, "backup_database_row_count") {
				// line looks like: backup_database_row_count{job="sdl_db_backup",service="mysql",env="pilot",database="db1"} 123
				// or maybe just backup_database_row_count{...,database="db1"}
				if dbIdx := strings.Index(name, `database="`); dbIdx >= 0 {
					startIdx := dbIdx + len(`database="`)
					endIdx := strings.IndexByte(name[startIdx:], '"')
					if endIdx >= 0 {
						dbName := name[startIdx : startIdx+endIdx]
						snapshot.DatabaseRowCounts[dbName], _ = strconv.ParseInt(value, 10, 64)
					}
				} else {
					// Fallback if parsed weirdly (name stripped of braces earlier)
					// wait, we stripped braces earlier:
					// if brace := strings.IndexByte(name, '{'); brace >= 0 { name = name[:brace] }
					// So name is exactly "backup_database_row_count".
					// The original line has the labels. Let's parse from line.
					if dbIdx := strings.Index(line, `database="`); dbIdx >= 0 {
						startIdx := dbIdx + len(`database="`)
						endIdx := strings.IndexByte(line[startIdx:], '"')
						if endIdx >= 0 {
							dbName := line[startIdx : startIdx+endIdx]
							snapshot.DatabaseRowCounts[dbName], _ = strconv.ParseInt(value, 10, 64)
						}
					}
				}
			}
		}
	}
	return snapshot
}

func writeBackupMetricsFile(path string, snapshot backupMetricsSnapshot, labels string) error {
	path = resolvedMetricsFilePath(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create metrics directory %s: %w", dir, err)
	}

	var body bytes.Buffer
	for _, metric := range []struct {
		Name  string
		Help  string
		Value string
	}{
		{
			Name:  "backup_last_run_timestamp",
			Help:  "Unix timestamp when the most recent backup run ended.",
			Value: strconv.FormatInt(snapshot.RunTimestamp, 10),
		},
		{
			Name:  "backup_last_success_timestamp",
			Help:  "Unix timestamp when the most recent fully successful backup run ended.",
			Value: strconv.FormatInt(snapshot.SuccessTimestamp, 10),
		},
		{
			Name:  "backup_last_duration_seconds",
			Help:  "Total runtime of the most recent backup job in seconds.",
			Value: strconv.FormatFloat(snapshot.DurationSeconds, 'f', 3, 64),
		},
		{
			Name:  "backup_last_size_bytes",
			Help:  "Size in bytes of the backup artifact produced by the most recent run.",
			Value: strconv.FormatInt(snapshot.SizeBytes, 10),
		},
		{
			Name:  "backup_last_status",
			Help:  "Status of the most recent backup run: 1 for full success, 0 for failure.",
			Value: strconv.Itoa(snapshot.Status),
		},
		{
			Name:  "backup_upload_success",
			Help:  "Upload result of the most recent backup run: 1 for success, 0 for failure or not reached.",
			Value: strconv.Itoa(snapshot.UploadSuccess),
		},
		{
			Name:  "backup_metrics_write_success",
			Help:  "Whether the most recent metrics file emission completed successfully: 1 for success, 0 for failure.",
			Value: strconv.Itoa(snapshot.MetricsWriteSuccess),
		},
		{
			Name:  "backup_cleanup_success",
			Help:  "Whether cleanup of old backups completed without errors in the most recent run: 1 for success, 0 for failure.",
			Value: strconv.Itoa(snapshot.CleanupSuccess),
		},
		{
			Name:  "backup_last_cleanup_timestamp",
			Help:  "Unix timestamp when cleanup of old backups last ran, or the previous value if cleanup was not reached in the most recent run.",
			Value: strconv.FormatInt(snapshot.CleanupTimestamp, 10),
		},
		{
			Name:  "backup_logical_last_attempted",
			Help:  "Whether the logical backup portion was attempted in the most recent run: 1 for attempted, 0 for skipped.",
			Value: strconv.Itoa(snapshot.LogicalAttempted),
		},
		{
			Name:  "backup_logical_last_status",
			Help:  "Status of the logical backup portion in the most recent run: 1 for success, 0 for failure or skipped.",
			Value: strconv.Itoa(snapshot.LogicalStatus),
		},
		{
			Name:  "backup_logical_last_total_databases",
			Help:  "Total logical databases selected for the most recent run.",
			Value: strconv.Itoa(snapshot.LogicalTotalDatabases),
		},
		{
			Name:  "backup_logical_last_succeeded_databases",
			Help:  "Logical databases backed up successfully in the most recent run.",
			Value: strconv.Itoa(snapshot.LogicalSucceededDatabases),
		},
		{
			Name:  "backup_logical_last_failed_databases",
			Help:  "Logical databases that failed in the most recent run.",
			Value: strconv.Itoa(snapshot.LogicalFailedDatabases),
		},
		{
			Name:  "backup_physical_last_attempted",
			Help:  "Whether the physical backup portion was attempted in the most recent run: 1 for attempted, 0 for skipped.",
			Value: strconv.Itoa(snapshot.PhysicalAttempted),
		},
		{
			Name:  "backup_physical_last_status",
			Help:  "Status of the physical backup portion in the most recent run: 1 for success, 0 for failure or skipped.",
			Value: strconv.Itoa(snapshot.PhysicalStatus),
		},
		{
			Name:  "backup_physical_last_duration_seconds",
			Help:  "Duration in seconds of the physical backup portion in the most recent run.",
			Value: strconv.FormatFloat(snapshot.PhysicalDurationSeconds, 'f', 3, 64),
		},
		{
			Name:  "backup_adaptive_load_per_cpu",
			Help:  "Observed 1-minute host load average divided by CPU count for the most recent run, or 0 when unavailable.",
			Value: strconv.FormatFloat(snapshot.AdaptiveLoadPerCPU, 'f', 3, 64),
		},
		{
			Name:  "backup_adaptive_logical_parallel",
			Help:  "Logical backup parallelism chosen for the most recent run after adaptive tuning.",
			Value: strconv.Itoa(snapshot.AdaptiveLogicalParallel),
		},
		{
			Name:  "backup_adaptive_xtrabackup_parallel",
			Help:  "Physical backup xtrabackup parallelism chosen for the most recent run after adaptive tuning.",
			Value: strconv.Itoa(snapshot.AdaptiveXtrabackupParallel),
		},
		{
			Name:  "backup_adaptive_xbcloud_parallel",
			Help:  "Physical backup xbcloud upload parallelism chosen for the most recent run after adaptive tuning.",
			Value: strconv.Itoa(snapshot.AdaptiveXbcloudParallel),
		},
		{
			Name:  "backup_physical_retry_count",
			Help:  "Total number of physical backup attempts used in the most recent run.",
			Value: strconv.Itoa(snapshot.PhysicalRetryCount),
		},
		{
			Name:  "backup_physical_rate_limit_retry_count",
			Help:  "Number of physical backup retries caused by xbcloud or S3 rate limiting in the most recent run.",
			Value: strconv.Itoa(snapshot.PhysicalRateLimitRetryCount),
		},
		{
			Name:  "backup_run_in_progress",
			Help:  "Whether a backup run is currently in progress: 1 for running, 0 for idle.",
			Value: strconv.Itoa(snapshot.RunInProgress),
		},
		{
			Name:  "backup_current_run_start_timestamp",
			Help:  "Unix timestamp when the current in-progress backup run started, or 0 when idle.",
			Value: strconv.FormatInt(snapshot.CurrentRunStartTimestamp, 10),
		},
		{
			Name:  "backup_current_run_duration_seconds",
			Help:  "Elapsed seconds for the current in-progress backup run, or 0 when idle.",
			Value: strconv.FormatFloat(snapshot.CurrentRunDurationSeconds, 'f', 3, 64),
		},
		{
			Name:  "backup_metrics_last_update_timestamp",
			Help:  "Unix timestamp when the backup metrics file was last refreshed.",
			Value: strconv.FormatInt(snapshot.MetricsLastUpdateTimestamp, 10),
		},
		{
			Name:  "backup_logical_validation_last_status",
			Help:  "1 if the last validated logical backup was healthy, 0 otherwise.",
			Value: strconv.Itoa(snapshot.LogicalValidationStatus),
		},
	} {
		fmt.Fprintf(&body, "# HELP %s %s\n", metric.Name, metric.Help)
		fmt.Fprintf(&body, "# TYPE %s gauge\n", metric.Name)
		fmt.Fprintf(&body, "%s{%s} %s\n", metric.Name, labels, metric.Value)
	}

	if len(snapshot.DatabaseRowCounts) > 0 {
		fmt.Fprintf(&body, "# HELP backup_database_row_count Number of rows in the database (exact or estimated).\n")
		fmt.Fprintf(&body, "# TYPE backup_database_row_count gauge\n")
		for dbName, count := range snapshot.DatabaseRowCounts {
			fmt.Fprintf(&body, "backup_database_row_count{%s,database=\"%s\"} %d\n", labels, dbName, count)
		}
	}

	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp metrics file in %s: %w", dir, err)
	}
	tempPath := tempFile.Name()
	cleanupTemp := func() {
		_ = os.Remove(tempPath)
	}

	if _, err := tempFile.Write(body.Bytes()); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("write temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("sync temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Chmod(backupMetricsFileMode); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("chmod temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		cleanupTemp()
		return fmt.Errorf("close temp metrics file %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanupTemp()
		return fmt.Errorf("rename temp metrics file to %s: %w", path, err)
	}
	if err := os.Chmod(path, backupMetricsFileMode); err != nil {
		return fmt.Errorf("chmod final metrics file %s: %w", path, err)
	}
	return nil
}

func snapshotValueMap(snapshot backupMetricsSnapshot) map[string]string {
	return map[string]string{
		"backup_last_run_timestamp":               strconv.FormatInt(snapshot.RunTimestamp, 10),
		"backup_last_success_timestamp":           strconv.FormatInt(snapshot.SuccessTimestamp, 10),
		"backup_last_duration_seconds":            strconv.FormatFloat(snapshot.DurationSeconds, 'f', 3, 64),
		"backup_last_size_bytes":                  strconv.FormatInt(snapshot.SizeBytes, 10),
		"backup_last_status":                      strconv.Itoa(snapshot.Status),
		"backup_upload_success":                   strconv.Itoa(snapshot.UploadSuccess),
		"backup_metrics_write_success":            strconv.Itoa(snapshot.MetricsWriteSuccess),
		"backup_cleanup_success":                  strconv.Itoa(snapshot.CleanupSuccess),
		"backup_last_cleanup_timestamp":           strconv.FormatInt(snapshot.CleanupTimestamp, 10),
		"backup_logical_last_attempted":           strconv.Itoa(snapshot.LogicalAttempted),
		"backup_logical_last_status":              strconv.Itoa(snapshot.LogicalStatus),
		"backup_logical_last_total_databases":     strconv.Itoa(snapshot.LogicalTotalDatabases),
		"backup_logical_last_succeeded_databases": strconv.Itoa(snapshot.LogicalSucceededDatabases),
		"backup_logical_last_failed_databases":    strconv.Itoa(snapshot.LogicalFailedDatabases),
		"backup_physical_last_attempted":          strconv.Itoa(snapshot.PhysicalAttempted),
		"backup_physical_last_status":             strconv.Itoa(snapshot.PhysicalStatus),
		"backup_physical_last_duration_seconds":   strconv.FormatFloat(snapshot.PhysicalDurationSeconds, 'f', 3, 64),
		"backup_adaptive_load_per_cpu":            strconv.FormatFloat(snapshot.AdaptiveLoadPerCPU, 'f', 3, 64),
		"backup_adaptive_logical_parallel":        strconv.Itoa(snapshot.AdaptiveLogicalParallel),
		"backup_adaptive_xtrabackup_parallel":     strconv.Itoa(snapshot.AdaptiveXtrabackupParallel),
		"backup_adaptive_xbcloud_parallel":        strconv.Itoa(snapshot.AdaptiveXbcloudParallel),
		"backup_physical_retry_count":             strconv.Itoa(snapshot.PhysicalRetryCount),
		"backup_physical_rate_limit_retry_count":  strconv.Itoa(snapshot.PhysicalRateLimitRetryCount),
		"backup_run_in_progress":                  strconv.Itoa(snapshot.RunInProgress),
		"backup_current_run_start_timestamp":      strconv.FormatInt(snapshot.CurrentRunStartTimestamp, 10),
		"backup_current_run_duration_seconds":     strconv.FormatFloat(snapshot.CurrentRunDurationSeconds, 'f', 3, 64),
		"backup_metrics_last_update_timestamp":    strconv.FormatInt(snapshot.MetricsLastUpdateTimestamp, 10),
		"backup_logical_validation_last_status":   strconv.Itoa(snapshot.LogicalValidationStatus),
	}
}

func GetObservabilityReport(cfg config) ObservabilityReport {
	path := resolvedMetricsFilePath(cfg.MetricsFile)
	report := ObservabilityReport{
		MetricsPath: path,
		Snapshot:    map[string]string{},
	}
	if err := validateMetricsPathWritable(path); err != nil {
		report.MetricsWritable = false
		report.MetricsStatus = err.Error()
	} else {
		report.MetricsWritable = true
		report.MetricsStatus = "metrics path writable"
	}
	info, err := os.Stat(path)
	if err != nil {
		report.MetricsFileExists = false
		report.LastWriteResult = "file not created yet"
		report.Snapshot = snapshotValueMap(readExistingBackupMetricsSnapshot(path))
		return report
	}
	report.MetricsFileExists = true
	report.MetricsFileSize = info.Size()
	report.MetricsFileMode = info.Mode().Perm().String()
	report.MetricsFileTime = info.ModTime()

	snapshot := readExistingBackupMetricsSnapshot(path)
	report.Snapshot = snapshotValueMap(snapshot)
	if snapshot.MetricsWriteSuccess == 1 {
		report.LastWriteResult = "success"
	} else {
		report.LastWriteResult = "failed-or-unknown"
	}
	if snapshot.MetricsLastUpdateTimestamp > 0 {
		report.LastUpdateTime = time.Unix(snapshot.MetricsLastUpdateTimestamp, 0).UTC()
	}
	return report
}

func validateMetricsPathWritable(path string) error {
	path = resolvedMetricsFilePath(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create metrics directory %s: %w", dir, err)
	}
	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".preflight-*")
	if err != nil {
		return fmt.Errorf("write test in %s: %w", dir, err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close write test file %s: %w", tempPath, err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("remove write test file %s: %w", tempPath, err)
	}
	return nil
}

func emitFinalBackupMetrics(cfg config, record runRecord, startedAt time.Time, logicalDue bool, physicalDue bool, uploadRequired bool, uploadSucceeded bool) {
	snapshot := buildFinalBackupMetricsSnapshot(cfg, record, startedAt, logicalDue, physicalDue, uploadRequired, uploadSucceeded)
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot, metricsLabelString(cfg)); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
		return
	}
	log.Printf(
		"metrics write succeeded path=%s status=%d upload_success=%d logical_attempted=%d logical_status=%d physical_attempted=%d physical_status=%d run_timestamp=%d success_timestamp=%d size_bytes=%d duration_seconds=%.3f",
		cfg.MetricsFile,
		snapshot.Status,
		snapshot.UploadSuccess,
		snapshot.LogicalAttempted,
		snapshot.LogicalStatus,
		snapshot.PhysicalAttempted,
		snapshot.PhysicalStatus,
		snapshot.RunTimestamp,
		snapshot.SuccessTimestamp,
		snapshot.SizeBytes,
		snapshot.DurationSeconds,
	)
}

func emitInProgressBackupMetrics(cfg config, startedAt time.Time) {
	snapshot := buildInProgressBackupMetricsSnapshot(cfg, startedAt)
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot, metricsLabelString(cfg)); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
		return
	}
	log.Printf(
		"metrics refresh succeeded path=%s in_progress=%d current_run_start=%d current_duration_seconds=%.3f last_update=%d",
		cfg.MetricsFile,
		snapshot.RunInProgress,
		snapshot.CurrentRunStartTimestamp,
		snapshot.CurrentRunDurationSeconds,
		snapshot.MetricsLastUpdateTimestamp,
	)
}

func emitValidationMetrics(cfg Config, status int) {
	snapshot := readExistingBackupMetricsSnapshot(cfg.MetricsFile)
	snapshot.LogicalValidationStatus = status
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot, metricsLabelString(cfg)); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
	}
}

func startRealtimeBackupMetricsEmitter(cfg config, startedAt time.Time) func() {
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	emitInProgressBackupMetrics(cfg, startedAt)

	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(backupMetricsUpdateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				emitInProgressBackupMetrics(cfg, startedAt)
			case <-stopCh:
				return
			}
		}
	}()

	return func() {
		close(stopCh)
		<-doneCh
	}
}
