package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	report, err := backupapp.GetHealthReport(context.Background(), os.Getenv("BACKUP_ENV_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderHealthReport(report))
}

func renderHealthReport(report backupapp.HealthReport) string {
	var out bytes.Buffer

	fmt.Fprintf(&out, "Config: %s\n", report.ConfigPath)
	fmt.Fprintf(&out, "Daily Log: %s\n", report.DailyLogPath)
	fmt.Fprintf(&out, "Run Log Index: %s\n", report.RunLogPath)
	fmt.Fprintf(&out, "Runtime User: %s\n", report.Runtime.CurrentUser)
	fmt.Fprintf(&out, "Execution Source: %s\n", report.Runtime.ExecutionSource)
	fmt.Fprintf(&out, "Scheduler Context: %s\n", report.Runtime.SchedulerContext)
	fmt.Fprintf(&out, "Service Unit: %s\n", report.Runtime.ServiceUnitName)
	fmt.Fprintf(&out, "Timer Unit: %s\n", report.Runtime.TimerUnitName)
	fmt.Fprintf(&out, "Scheduler Guidance: %s\n", report.Runtime.SchedulerGuidance)
	if report.Runtime.PotentialConflictReason != "" {
		fmt.Fprintf(&out, "Scheduler Warning: %s\n", report.Runtime.PotentialConflictReason)
	}
	if len(report.Runtime.AuditChecklist) > 0 {
		fmt.Fprintln(&out, "Runtime Audit Checklist:")
		for _, item := range report.Runtime.AuditChecklist {
			fmt.Fprintf(&out, "  - %s\n", item)
		}
	}
	fmt.Fprintln(&out)

	if report.LatestRun == nil {
		fmt.Fprintln(&out, "Latest Run: none recorded yet")
	} else {
		fmt.Fprintln(&out, "Latest Run:")
		fmt.Fprintf(&out, "  Timestamp: %s\n", report.LatestRun.Timestamp.Format(time.RFC3339))
		fmt.Fprintf(&out, "  Status: %s\n", report.LatestRun.Status)
		fmt.Fprintf(&out, "  Run ID: %s\n", report.LatestRun.RunID)
		fmt.Fprintf(&out, "  Run Folder: %s\n", report.LatestRun.RunFolder)
		fmt.Fprintf(&out, "  Log File: %s\n", report.LatestRun.LogFile)
		fmt.Fprintf(&out, "  Duration: %s\n", report.LatestRun.Duration)
		fmt.Fprintf(&out, "  Runtime: user=%s source=%s host=%s pid=%d\n", report.LatestRun.OSUser, report.LatestRun.ExecutionSource, report.LatestRun.Hostname, report.LatestRun.PID)
		fmt.Fprintf(&out, "  Databases: total=%d success=%d failed=%d\n", report.LatestRun.DatabasesTotal, report.LatestRun.DatabasesSucceeded, report.LatestRun.DatabasesFailed)
		if report.LatestRun.AdaptiveLogicalParallel > 0 || report.LatestRun.AdaptivePhysicalParallel > 0 || report.LatestRun.AdaptiveXbcloudParallel > 0 {
			fmt.Fprintf(&out, "  Adaptive Tuning: load/cpu=%.3f logical=%d physical=%d xbcloud=%d reason=%s\n",
				report.LatestRun.AdaptiveLoadPerCPU,
				report.LatestRun.AdaptiveLogicalParallel,
				report.LatestRun.AdaptivePhysicalParallel,
				report.LatestRun.AdaptiveXbcloudParallel,
				fallback(report.LatestRun.AdaptiveTuningReason, "unknown"),
			)
		}
		if report.LatestRun.FinalOutcome != "" {
			fmt.Fprintf(&out, "  Final Outcome: %s\n", report.LatestRun.FinalOutcome)
		}
		if report.LatestRun.FailureReason != "" {
			fmt.Fprintf(&out, "  Failure Reason: %s\n", report.LatestRun.FailureReason)
		}
		if report.LatestRun.CleanupError != "" {
			fmt.Fprintf(&out, "  Cleanup: %s\n", report.LatestRun.CleanupError)
		}
	}

	fmt.Fprintln(&out)
	fmt.Fprintf(&out, "Logical Prereqs: %s - %s\n", report.Logical.Status, report.Logical.Message)
	fmt.Fprintf(&out, "Physical Prereqs: %s - %s\n", report.Physical.Status, report.Physical.Message)
	fmt.Fprintf(&out, "Metrics Health: %s - %s\n", report.Metrics.Status, report.Metrics.Message)
	if len(report.Directories) > 0 {
		fmt.Fprintln(&out, "Directories:")
		for _, check := range report.Directories {
			fmt.Fprintf(&out, "  %s: %s - %s\n", check.Name, check.Status, check.Message)
		}
	}
	if report.Observability.MetricsPath != "" {
		fmt.Fprintf(&out, "Observability: path=%s writable=%t last_write=%s\n",
			report.Observability.MetricsPath,
			report.Observability.MetricsWritable,
			fallback(report.Observability.LastWriteResult, "unknown"),
		)
		if len(report.Observability.Snapshot) > 0 {
			fmt.Fprintln(&out, "Parsed Metrics:")
			for _, key := range healthMetricOrder() {
				if value, ok := report.Observability.Snapshot[key]; ok {
					fmt.Fprintf(&out, "  %s=%s\n", key, fallback(value, "0"))
				}
			}
		}
	}
	fmt.Fprintf(&out, "Restore Verification: restore_test=%t exact_row_counts=%t sample_data_checks=%t sample_data_rows=%d\n",
		report.RestoreVerification.RestoreTestEnabled,
		report.RestoreVerification.ExactRowCounts,
		report.RestoreVerification.SampleDataChecks,
		report.RestoreVerification.SampleDataRows,
	)
	return out.String()
}

func healthMetricOrder() []string {
	return []string{
		"backup_last_status",
		"backup_upload_success",
		"backup_metrics_write_success",
		"backup_cleanup_success",
		"backup_last_cleanup_timestamp",
		"backup_logical_last_status",
		"backup_logical_last_total_databases",
		"backup_logical_last_succeeded_databases",
		"backup_logical_last_failed_databases",
		"backup_physical_last_status",
		"backup_physical_last_duration_seconds",
		"backup_adaptive_load_per_cpu",
		"backup_adaptive_logical_parallel",
		"backup_adaptive_xtrabackup_parallel",
		"backup_adaptive_xbcloud_parallel",
		"backup_physical_retry_count",
		"backup_physical_rate_limit_retry_count",
		"backup_logical_validation_last_status",
		"backup_run_in_progress",
		"backup_last_run_timestamp",
		"backup_last_success_timestamp",
		"backup_last_duration_seconds",
		"backup_last_size_bytes",
		"backup_metrics_last_update_timestamp",
	}
}

func fallback(value string, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
