package main

import (
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

	fmt.Printf("Config: %s\n", report.ConfigPath)
	fmt.Printf("Daily Log: %s\n", report.DailyLogPath)
	fmt.Printf("Run Log Index: %s\n", report.RunLogPath)
	fmt.Printf("Runtime User: %s\n", report.Runtime.CurrentUser)
	fmt.Printf("Execution Source: %s\n", report.Runtime.ExecutionSource)
	fmt.Printf("Service Unit: %s\n", report.Runtime.ServiceUnitName)
	fmt.Printf("Timer Unit: %s\n", report.Runtime.TimerUnitName)
	fmt.Println()

	if report.LatestRun == nil {
		fmt.Println("Latest Run: none recorded yet")
	} else {
		fmt.Println("Latest Run:")
		fmt.Printf("  Timestamp: %s\n", report.LatestRun.Timestamp.Format(time.RFC3339))
		fmt.Printf("  Status: %s\n", report.LatestRun.Status)
		fmt.Printf("  Run ID: %s\n", report.LatestRun.RunID)
		fmt.Printf("  Run Folder: %s\n", report.LatestRun.RunFolder)
		fmt.Printf("  Log File: %s\n", report.LatestRun.LogFile)
		fmt.Printf("  Duration: %s\n", report.LatestRun.Duration)
		fmt.Printf("  Runtime: user=%s source=%s host=%s pid=%d\n", report.LatestRun.OSUser, report.LatestRun.ExecutionSource, report.LatestRun.Hostname, report.LatestRun.PID)
		fmt.Printf("  Databases: total=%d success=%d failed=%d\n", report.LatestRun.DatabasesTotal, report.LatestRun.DatabasesSucceeded, report.LatestRun.DatabasesFailed)
		if report.LatestRun.FailureReason != "" {
			fmt.Printf("  Failure: %s\n", report.LatestRun.FailureReason)
		}
		if report.LatestRun.CleanupError != "" {
			fmt.Printf("  Cleanup: %s\n", report.LatestRun.CleanupError)
		}
	}

	fmt.Println()
	fmt.Printf("Logical Prereqs: %s - %s\n", report.Logical.Status, report.Logical.Message)
	fmt.Printf("Physical Prereqs: %s - %s\n", report.Physical.Status, report.Physical.Message)
}
