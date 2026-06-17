package backupapp

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func cleanupOldBackups(backupDir, currentRun string, cfg config) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	type backup struct {
		path string
		time time.Time
	}

	var backups []backup
	currentRun = filepath.Clean(currentRun)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runTime, err := time.Parse(runTimestampLayout, entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Clean(filepath.Join(backupDir, entry.Name()))
		if currentRun != "" && path == currentRun {
			continue
		}
		backups = append(backups, backup{path: path, time: runTime})
	}

	// Sort backups from newest to oldest
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[j].time.After(backups[i].time) {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	keep := make(map[string]bool)

	// Keep Daily
	seenDays := make(map[string]bool)
	daysKept := 0
	for _, b := range backups {
		day := b.time.Format("2006-01-02")
		if !seenDays[day] {
			seenDays[day] = true
			if daysKept < cfg.RetentionDaily {
				keep[b.path] = true
				daysKept++
			}
		}
	}

	// Keep Weekly
	seenWeeks := make(map[string]bool)
	weeksKept := 0
	for _, b := range backups {
		year, week := b.time.ISOWeek()
		weekStr := fmt.Sprintf("%d-W%02d", year, week)
		if !seenWeeks[weekStr] {
			seenWeeks[weekStr] = true
			if weeksKept < cfg.RetentionWeekly {
				keep[b.path] = true
				weeksKept++
			}
		}
	}

	// Keep Monthly
	seenMonths := make(map[string]bool)
	monthsKept := 0
	for _, b := range backups {
		month := b.time.Format("2006-01")
		if !seenMonths[month] {
			seenMonths[month] = true
			if monthsKept < cfg.RetentionMonthly {
				keep[b.path] = true
				monthsKept++
			}
		}
	}

	// Delete unkept
	for _, b := range backups {
		if !keep[b.path] {
			log.Printf("deleting old backup folder: %s", b.path)
			if err := os.RemoveAll(b.path); err != nil {
				return fmt.Errorf("delete old backup %s: %w", b.path, err)
			}
		}
	}

	return nil
}
