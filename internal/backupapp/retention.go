package backupapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type RetentionDecision struct {
	RunID  string `json:"run_id"`
	Path   string `json:"path"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}
type RetentionPlan struct {
	CreatedAt      time.Time           `json:"created_at"`
	Decisions      []RetentionDecision `json:"decisions"`
	CompletePoints int                 `json:"complete_points"`
	DeleteEnabled  bool                `json:"delete_enabled"`
}

// PlanRetention is read-only. Unknown, incomplete, dependency-bearing and pinned
// runs are held. Only independently validated complete logical runs enter GFS.
func PlanRetention(cfg Config, current string) (RetentionPlan, error) {
	plan := RetentionPlan{CreatedAt: time.Now().UTC(), DeleteEnabled: cfg.RetentionDeleteEnabled}
	entries, e := os.ReadDir(cfg.BackupDir)
	if e != nil {
		return plan, e
	}
	type candidate struct {
		i int
		t time.Time
		r runRecord
	}
	var healthy []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		at, e := time.Parse(runTimestampLayout, entry.Name())
		if e != nil {
			continue
		}
		path := filepath.Join(cfg.BackupDir, entry.Name())
		d := RetentionDecision{RunID: entry.Name(), Path: path, Action: "HOLD", Reason: "unproven recovery value"}
		r, e := readManifest(path)
		switch {
		case filepath.Clean(path) == filepath.Clean(current):
			d.Reason = "current run"
		case fileExists(filepath.Join(path, ".recovery-pin")):
			d.Reason = "operator-pinned recovery point"
		case e != nil:
			d.Reason = "missing or invalid legacy manifest"
		case r.PhysicalBackup != nil || r.MySQLPITR != nil:
			d.Reason = "physical or binlog dependency requires explicit review"
		case verifyLogicalCapture(cfg, r, path) != nil:
			d.Reason = "partial, failed, missing or corrupt artifacts; preserve for review"
		default:
			healthy = append(healthy, candidate{len(plan.Decisions), at, r})
			d.Reason = "verified complete recovery point"
		}
		plan.Decisions = append(plan.Decisions, d)
	}
	sort.Slice(healthy, func(i, j int) bool { return healthy[i].t.After(healthy[j].t) })
	plan.CompletePoints = len(healthy)
	days, weeks, months := map[string]bool{}, map[string]bool{}, map[string]bool{}
	verified := 0
	for n, b := range healthy {
		d := &plan.Decisions[b.i]
		day := b.t.Format("2006-01-02")
		year, week := b.t.ISOWeek()
		wk := fmt.Sprintf("%d-%d", year, week)
		month := b.t.Format("2006-01")
		keep := n < max(1, cfg.RetentionMinComplete)
		if !days[day] && len(days) < cfg.RetentionDaily {
			keep = true
			days[day] = true
		}
		if !weeks[wk] && len(weeks) < cfg.RetentionWeekly {
			keep = true
			weeks[wk] = true
		}
		if !months[month] && len(months) < cfg.RetentionMonthly {
			keep = true
			months[month] = true
		}
		if b.r.ValidationStatus == "success" && b.r.ValidationMode == "sandbox restore test" && verified < cfg.RetentionMinVerified {
			keep = true
			verified++
			d.Reason = "restore-tested complete point"
		}
		if keep {
			d.Action = "KEEP"
		} else {
			d.Action = "DELETE_CANDIDATE"
			d.Reason = "outside GFS and protected complete coverage"
		}
	}
	return plan, nil
}
func fileExists(path string) bool { _, e := os.Stat(path); return e == nil }
func applyRetentionPlan(cfg config, plan RetentionPlan) error {
	if !cfg.RetentionDeleteEnabled {
		return nil
	}
	// Opt-in is explicit operator authorization; re-verify immediately before
	// quarantine. Interrupted quarantine remains recoverable, never auto-purged.
	quarantine := filepath.Join(cfg.BackupDir, ".retention-quarantine")
	for _, d := range plan.Decisions {
		if d.Action != "DELETE_CANDIDATE" {
			continue
		}
		r, e := readManifest(d.Path)
		if e != nil {
			return e
		}
		if e = verifyLogicalCapture(cfg, r, d.Path); e != nil {
			return e
		}
		if fileExists(filepath.Join(d.Path, ".recovery-pin")) {
			continue
		}
		if e = os.MkdirAll(quarantine, 0700); e != nil {
			return e
		}
		event := struct {
			RetentionDecision
			At    time.Time
			State string
		}{d, time.Now().UTC(), "QUARANTINE_INTENT"}
		b, _ := json.Marshal(event)
		if e = atomicWriteFile(filepath.Join(quarantine, d.RunID+".audit.json"), append(b, '\n'), 0600); e != nil {
			return e
		}
		if e = os.Rename(d.Path, filepath.Join(quarantine, d.RunID)); e != nil {
			return e
		}
	}
	return nil
}
