package backupapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dustin/go-humanize"
)

// InspectDB holds details about a single backed-up database file.
type InspectDB struct {
	Name     string // database name (without .sql.gz)
	FilePath string // absolute path to the .sql.gz file
	SizeStr  string // human-readable file size
}

// InspectRun holds the structured result of inspecting a backup run directory.
type InspectRun struct {
	RunID          string
	RunFolder      string
	LogicalDBs     []InspectDB
	HasPhysical    bool
	PhysicalFolder string
	PhysicalSize   string
}

// RestoreCommandsForDB returns the exact restore command for a specific logical DB.
func (r InspectRun) RestoreCommandsForDB(db InspectDB, dbUser string) []string {
	return []string{
		fmt.Sprintf("zcat %s | mysql -u %s -p %s", db.FilePath, dbUser, db.Name),
	}
}

// PhysicalRestoreCommands returns the step-by-step xtrabackup restore commands.
func (r InspectRun) PhysicalRestoreCommands() []string {
	return []string{
		fmt.Sprintf("xtrabackup --prepare --target-dir=%s", r.PhysicalFolder),
		"sudo systemctl stop mysql",
		"sudo mv /var/lib/mysql /var/lib/mysql.bak",
		fmt.Sprintf("xtrabackup --copy-back --target-dir=%s", r.PhysicalFolder),
		"sudo chown -R mysql:mysql /var/lib/mysql",
		"sudo systemctl start mysql",
	}
}

// InspectRunData loads the structured inspect result for a given run.
func InspectRunData(ctx context.Context, envPath string, runID string) (*InspectRun, error) {
	cfg, err := LoadConfig(envPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}

	runFolder := filepath.Join(cfg.BackupDir, runID)
	info, err := os.Stat(runFolder)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Local backup directory not found: %s\n\n(It may have been cleaned up by retention policies or only exist in S3)", runFolder)
	}

	result := &InspectRun{
		RunID:     runID,
		RunFolder: runFolder,
	}

	logicalFiles, err := filepath.Glob(filepath.Join(runFolder, "*.sql.gz"))
	if err == nil {
		for _, file := range logicalFiles {
			stat, statErr := os.Stat(file)
			sizeStr := "unknown size"
			if statErr == nil {
				sizeStr = humanize.Bytes(uint64(stat.Size()))
			}
			base := filepath.Base(file)
			dbName := strings.TrimSuffix(base, ".sql.gz")
			result.LogicalDBs = append(result.LogicalDBs, InspectDB{
				Name:     dbName,
				FilePath: file,
				SizeStr:  sizeStr,
			})
		}
	}

	physicalFolder := filepath.Join(runFolder, "physical")
	physicalInfo, physicalErr := os.Stat(physicalFolder)
	if physicalErr == nil && physicalInfo.IsDir() {
		result.HasPhysical = true
		result.PhysicalFolder = physicalFolder
		var totalSize int64
		_ = filepath.Walk(physicalFolder, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				totalSize += info.Size()
			}
			return nil
		})
		result.PhysicalSize = humanize.Bytes(uint64(totalSize))
	}

	return result, nil
}

// InspectRunToString returns a plain-text summary (used by the CLI `inspect` subcommand).
func InspectRunToString(ctx context.Context, envPath string, runID string) (string, error) {
	cfg, err := LoadConfig(envPath)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %v", err)
	}

	data, err := InspectRunData(ctx, envPath, runID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Backup Run: %s\n", runID))
	sb.WriteString(fmt.Sprintf("Location:   %s\n\n", data.RunFolder))

	if len(data.LogicalDBs) == 0 && !data.HasPhysical {
		sb.WriteString("No databases found in this run.\n")
		return sb.String(), nil
	}

	if len(data.LogicalDBs) > 0 {
		sb.WriteString("=== LOGICAL BACKUPS (Databases) ===\n")
		for _, db := range data.LogicalDBs {
			sb.WriteString(fmt.Sprintf(" • %-30s %s\n", db.Name, db.SizeStr))
		}
		sb.WriteString("\n--- How to Restore a Logical Backup ---\n")
		sb.WriteString("For each database, run:\n")
		for _, db := range data.LogicalDBs {
			cmds := data.RestoreCommandsForDB(db, cfg.DBUser)
			sb.WriteString(fmt.Sprintf("  [%s]\n", db.Name))
			for _, cmd := range cmds {
				sb.WriteString("    " + cmd + "\n")
			}
		}
		sb.WriteString("\n")
	}

	if data.HasPhysical {
		sb.WriteString("=== PHYSICAL BACKUP ===\n")
		sb.WriteString(fmt.Sprintf(" • physical/  %s\n\n", data.PhysicalSize))
		sb.WriteString("--- How to Restore a Physical Backup ---\n")
		for i, cmd := range data.PhysicalRestoreCommands() {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, cmd))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}
