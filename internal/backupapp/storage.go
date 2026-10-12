package backupapp

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type CapacityReport struct {
	AvailableBytes uint64 `json:"available_bytes"`
	EstimatedBytes uint64 `json:"estimated_capture_bytes"`
	ReserveBytes   uint64 `json:"reserve_bytes"`
	RequiredBytes  uint64 `json:"required_bytes"`
	Sufficient     bool   `json:"sufficient"`
}

func inspectCapacity(path string, estimate, reserve uint64) (CapacityReport, error) {
	for {
		if _, e := os.Stat(path); e == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			return CapacityReport{}, fmt.Errorf("storage path unavailable")
		}
		path = parent
	}
	var st syscall.Statfs_t
	if e := syscall.Statfs(path, &st); e != nil {
		return CapacityReport{}, e
	}
	r := CapacityReport{AvailableBytes: st.Bavail * uint64(st.Bsize), EstimatedBytes: estimate, ReserveBytes: reserve}
	if estimate > ^uint64(0)-reserve {
		return r, fmt.Errorf("storage estimate overflow")
	}
	r.RequiredBytes = estimate + reserve
	r.Sufficient = r.AvailableBytes >= r.RequiredBytes && st.Ffree > 0
	return r, nil
}
func estimateCaptureBytes(cfg config) uint64 {
	estimate := uint64(max(0, cfg.StorageEstimateBytes))
	runs, e := ReadRunHistory(cfg.RunLogPath)
	if e == nil {
		for _, r := range runs {
			if r.Status != "success" || r.DatabasesTotal == 0 {
				continue
			}
			var total uint64
			seen := map[string]bool{}
			for _, d := range r.Results {
				if d.SizeBytes > 0 && !seen[d.OutputPath] {
					total += uint64(d.SizeBytes)
					seen[d.OutputPath] = true
				}
			}
			if total > estimate {
				estimate = total
			}
		}
	}
	// Existing captures already stream through gzip and encryption; no raw SQL copy.
	// Reserve 2x the largest observed compressed complete capture for growth.
	return max(uint64(1<<30), estimate*2)
}
func preflightCapacity(cfg config, logical bool) (CapacityReport, error) {
	estimate := uint64(16 << 20)
	if logical {
		estimate = estimateCaptureBytes(cfg)
	}
	r, e := inspectCapacity(cfg.BackupDir, estimate, uint64(max(0, cfg.StorageReserveBytes)))
	if e != nil {
		return r, e
	}
	if !r.Sufficient {
		return r, fmt.Errorf("insufficient backup capacity: available=%d required=%d (capture=%d reserve=%d); preserve recovery points and provision capacity", r.AvailableBytes, r.RequiredBytes, r.EstimatedBytes, r.ReserveBytes)
	}
	return r, nil
}

type reserveWriter struct {
	file    *os.File
	reserve uint64
	since   uint64
}

func (w *reserveWriter) Write(p []byte) (int, error) {
	if w.since == 0 || w.since >= 16<<20 {
		r, e := inspectCapacity(filepath.Dir(w.file.Name()), uint64(len(p)), w.reserve)
		if e != nil {
			return 0, e
		}
		if !r.Sufficient {
			return 0, fmt.Errorf("disk safety reserve reached during capture: %w", syscall.ENOSPC)
		}
		w.since = 0
	}
	n, e := w.file.Write(p)
	w.since += uint64(n)
	return n, e
}
