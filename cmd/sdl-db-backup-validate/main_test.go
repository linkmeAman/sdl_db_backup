package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sdl/sdl_db_backup/internal/backupapp"
)

func TestResolveRunIDReturnsRequestedValue(t *testing.T) {
	cfg := backupapp.Config{RunLogPath: filepath.Join(t.TempDir(), "backup-runs.jsonl")}
	got, err := resolveRunID(cfg, "run-123")
	if err != nil {
		t.Fatalf("resolveRunID returned error: %v", err)
	}
	if got != "run-123" {
		t.Fatalf("expected requested run id, got %q", got)
	}
}

func TestResolveRunIDReturnsLatestRecordedRun(t *testing.T) {
	dir := t.TempDir()
	runLogPath := filepath.Join(dir, "backup-runs.jsonl")
	data := `{"run_id":"run-1","status":"success"}
{"run_id":"run-2","status":"failed"}`
	if err := os.WriteFile(runLogPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	got, err := resolveRunID(backupapp.Config{RunLogPath: runLogPath}, "")
	if err != nil {
		t.Fatalf("resolveRunID returned error: %v", err)
	}
	if got != "run-2" {
		t.Fatalf("expected latest run id run-2, got %q", got)
	}
}

func TestResolveRunIDFailsWithoutRecordedRuns(t *testing.T) {
	cfg := backupapp.Config{RunLogPath: filepath.Join(t.TempDir(), "missing.jsonl")}
	_, err := resolveRunID(cfg, "")
	if err == nil || !strings.Contains(err.Error(), "no backup runs recorded") {
		t.Fatalf("expected no-runs error, got %v", err)
	}
}

func TestRenderValidationReportIncludesErrorsAndDatabaseStatuses(t *testing.T) {
	report := renderValidationReport("restore", backupapp.LogicalValidationResult{
		RunID: "run-9",
		Valid: false,
		Error: "validation failed for pf_central: row count mismatch",
		Databases: []backupapp.DatabaseValidationResult{
			{Database: "pf_app", Valid: true},
			{Database: "pf_central", Valid: false, Error: "row count mismatch"},
		},
	})

	for _, want := range []string{
		"Validation Mode: sandbox restore test",
		"Run ID: run-9",
		"Result: failed",
		"Error: validation failed for pf_central: row count mismatch",
		"- pf_app: ok",
		"- pf_central: failed (row count mismatch)",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("expected report to contain %q, got:\n%s", want, report)
		}
	}
}
