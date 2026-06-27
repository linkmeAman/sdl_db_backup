package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"sdl/sdl_db_backup/internal/backupapp"
)

func main() {
	var (
		mode  = flag.String("mode", "logical", "validation mode: logical or restore")
		runID = flag.String("run-id", "", "backup run id to validate; defaults to the latest recorded run")
	)
	flag.Parse()

	cfg, err := backupapp.LoadEffectiveConfig(os.Getenv("BACKUP_ENV_FILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "validation setup failed: %v\n", err)
		os.Exit(1)
	}

	selectedRunID, err := resolveRunID(cfg, strings.TrimSpace(*runID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "validation setup failed: %v\n", err)
		os.Exit(1)
	}

	modeName := strings.ToLower(strings.TrimSpace(*mode))
	var result backupapp.LogicalValidationResult
	switch modeName {
	case "", "logical":
		result, err = backupapp.ValidateLogicalRun(cfg, selectedRunID)
	case "restore", "sandbox", "sandbox_restore":
		result, err = backupapp.FullRestoreValidation(cfg, selectedRunID, func(line string) {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintln(os.Stderr, line)
			}
		})
		modeName = "restore"
	default:
		fmt.Fprintf(os.Stderr, "validation setup failed: unsupported mode %q (expected logical or restore)\n", *mode)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "validation failed to run: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderValidationReport(modeName, result))
	if !result.Valid {
		os.Exit(1)
	}
}

func resolveRunID(cfg backupapp.Config, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	runs, err := backupapp.ReadRunHistory(cfg.RunLogPath)
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("no backup runs recorded in %s", cfg.RunLogPath)
	}
	return runs[len(runs)-1].RunID, nil
}

func renderValidationReport(mode string, result backupapp.LogicalValidationResult) string {
	var b strings.Builder

	modeLabel := "logical validation"
	if mode == "restore" {
		modeLabel = "sandbox restore test"
	}

	status := "failed"
	if result.Valid {
		status = "success"
	}

	fmt.Fprintf(&b, "Validation Mode: %s\n", modeLabel)
	fmt.Fprintf(&b, "Run ID: %s\n", result.RunID)
	fmt.Fprintf(&b, "Result: %s\n", status)
	if strings.TrimSpace(result.Error) != "" {
		fmt.Fprintf(&b, "Error: %s\n", result.Error)
	}
	if len(result.Databases) > 0 {
		fmt.Fprintln(&b, "Databases:")
		for _, db := range result.Databases {
			line := "ok"
			if !db.Valid {
				line = "failed"
			}
			if strings.TrimSpace(db.Error) != "" {
				fmt.Fprintf(&b, "- %s: %s (%s)\n", db.Database, line, db.Error)
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", db.Database, line)
		}
	}

	return b.String()
}
