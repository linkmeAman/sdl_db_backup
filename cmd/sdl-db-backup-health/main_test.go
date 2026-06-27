package main

import (
	"strings"
	"testing"
	"time"

	"sdl/sdl_db_backup/internal/backupapp"
)

func TestRenderHealthReportIncludesSchedulerAndDirectoryDiagnostics(t *testing.T) {
	report := backupapp.HealthReport{
		ConfigPath:   "/tmp/.env",
		DailyLogPath: "/tmp/2026-06-25.log",
		RunLogPath:   "/tmp/backup-runs.jsonl",
		Runtime: backupapp.RuntimeProfile{
			CurrentUser:             "developer",
			ExecutionSource:         "runner",
			SchedulerContext:        "user-level scheduled runner",
			ServiceUnitName:         "sdl-db-backup.service",
			TimerUnitName:           "sdl-db-backup.timer",
			SchedulerGuidance:       "Use only a user-level systemd timer/service for scheduled runs.",
			PotentialConflictReason: "",
			AuditChecklist: []string{
				"Inspect root crontab and /etc/cron.* for backup commands.",
				"Inspect system-level systemd units for duplicate backup triggers.",
			},
		},
		Logical:  backupapp.HealthCheck{Name: "logical", Status: "ok", Message: "logical ok"},
		Physical: backupapp.HealthCheck{Name: "physical", Status: "warn", Message: "ownership drift detected"},
		Metrics:  backupapp.HealthCheck{Name: "metrics", Status: "ok", Message: "metrics path writable"},
		Directories: []backupapp.HealthCheck{
			{Name: "backup_dir", Status: "warn", Message: "root-owned file detected"},
			{Name: "log_dir", Status: "ok", Message: "writable"},
		},
		Observability: backupapp.ObservabilityReport{
			MetricsPath:     "/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom",
			MetricsWritable: true,
			LastWriteResult: "success",
			Snapshot: map[string]string{
				"backup_last_status":                   "1",
				"backup_adaptive_xbcloud_parallel":     "1",
				"backup_physical_retry_count":          "2",
				"backup_metrics_last_update_timestamp": "1760000000",
			},
		},
		RestoreVerification: backupapp.RestoreVerificationProfile{
			RestoreTestEnabled: true,
			ExactRowCounts:     true,
			SampleDataChecks:   true,
			SampleDataRows:     25,
		},
		LatestRun: &backupapp.LatestRunInfo{
			Timestamp:                time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC),
			Status:                   "failed",
			RunID:                    "run-1",
			RunFolder:                "/tmp/run-1",
			LogFile:                  "/tmp/run-1.log",
			Duration:                 "3m0s",
			DatabasesTotal:           2,
			DatabasesSucceeded:       1,
			DatabasesFailed:          1,
			AdaptiveLoadPerCPU:       0.42,
			AdaptiveLogicalParallel:  2,
			AdaptivePhysicalParallel: 3,
			AdaptiveXbcloudParallel:  1,
			AdaptiveTuningReason:     "balanced",
			FinalOutcome:             "1 of 2 database backups failed",
			FailureReason:            "logical backup upload failed: s3 unavailable",
			OSUser:                   "developer",
			ExecutionSource:          "runner",
			Hostname:                 "host-1",
			PID:                      1234,
		},
	}

	text := renderHealthReport(report)
	for _, want := range []string{
		"Scheduler Context: user-level scheduled runner",
		"Scheduler Guidance: Use only a user-level systemd timer/service for scheduled runs.",
		"Runtime Audit Checklist:",
		"- Inspect root crontab and /etc/cron.* for backup commands.",
		"Adaptive Tuning: load/cpu=0.420 logical=2 physical=3 xbcloud=1 reason=balanced",
		"Failure Reason: logical backup upload failed: s3 unavailable",
		"Metrics Health: ok - metrics path writable",
		"Directories:",
		"backup_dir: warn - root-owned file detected",
		"Observability: path=/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom writable=true last_write=success",
		"Parsed Metrics:",
		"backup_last_status=1",
		"backup_adaptive_xbcloud_parallel=1",
		"backup_physical_retry_count=2",
		"Restore Verification: restore_test=true exact_row_counts=true sample_data_checks=true sample_data_rows=25",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected rendered health report to contain %q, got:\n%s", want, text)
		}
	}
}
