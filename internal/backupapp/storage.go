package backupapp

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type CapacityReport struct {
	AvailableBytes uint64  `json:"available_bytes"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsableBytes    uint64  `json:"usable_bytes"`
	FreeInodes     uint64  `json:"free_inodes"`
	TotalInodes    uint64  `json:"total_inodes"`
	RequiredInodes uint64  `json:"required_inodes"`
	UsedPercent    float64 `json:"used_percent"`
	EstimatedBytes uint64  `json:"estimated_capture_bytes"`
	ReserveBytes   uint64  `json:"reserve_bytes"`
	RequiredBytes  uint64  `json:"required_bytes"`
	Sufficient     bool    `json:"sufficient"`
	FilesystemID   uint64  `json:"filesystem_id"`
}

func inspectCapacity(path string, estimate, reserve uint64) (CapacityReport, error) {
	for {
		_, err := os.Stat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return CapacityReport{}, err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return CapacityReport{}, fmt.Errorf("storage path unavailable")
		}
		path = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return CapacityReport{}, err
	}
	var info syscall.Stat_t
	if err := syscall.Stat(path, &info); err != nil {
		return CapacityReport{}, err
	}
	r := CapacityReport{AvailableBytes: st.Bavail * uint64(st.Bsize), TotalBytes: st.Blocks * uint64(st.Bsize), UsableBytes: (st.Blocks - st.Bfree + st.Bavail) * uint64(st.Bsize), FreeInodes: st.Ffree, TotalInodes: st.Files, RequiredInodes: 1, EstimatedBytes: estimate, ReserveBytes: reserve, FilesystemID: uint64(info.Dev)}
	if r.UsableBytes > 0 {
		r.UsedPercent = 100 * (1 - float64(r.AvailableBytes)/float64(r.UsableBytes))
	}
	if estimate > ^uint64(0)-reserve {
		return r, fmt.Errorf("storage estimate overflow")
	}
	r.RequiredBytes = estimate + reserve
	r.Sufficient = r.AvailableBytes >= r.RequiredBytes && r.FreeInodes > 0
	return r, nil
}
func estimateCaptureBytes(cfg config) uint64 {
	// Only recent complete captures influence size, not an indefinitely old maximum.
	estimate := uint64(max(0, cfg.StorageEstimateBytes))
	limit := cfg.StorageHistoryRuns
	if limit <= 0 {
		limit = 12
	}
	runs, err := ReadRunHistory(cfg.RunLogPath)
	if err == nil {
		used := 0
		for i := len(runs) - 1; i >= 0 && used < limit; i-- {
			r := runs[i]
			if r.Status != "success" || r.DatabasesTotal == 0 || r.DatabasesFailed != 0 {
				continue
			}
			total := uint64(max(0, totalLogicalArtifactSize(r.Results)))
			if total > estimate {
				estimate = total
			}
			used++
		}
	}
	estimate = max(uint64(1<<30), estimate)
	growth := cfg.StorageGrowthPercent
	if growth <= 0 {
		growth = 50
	}
	factor := uint64(100 + growth)
	if estimate > ^uint64(0)/factor {
		return ^uint64(0)
	}
	product := estimate * factor
	result := product / 100
	if product%100 != 0 {
		result++
	}
	return result
}
func preflightCapacity(cfg config, logical bool) (CapacityReport, error) {
	estimate := uint64(16 << 20)
	if logical {
		estimate = estimateCaptureBytes(cfg)
	}
	extra := uint64(max(0, cfg.StorageConcurrentBytes)) + uint64(max(0, cfg.StorageTemporaryBytes))
	if estimate > ^uint64(0)-extra {
		return CapacityReport{}, fmt.Errorf("storage estimate overflow")
	}
	estimate += extra
	r, err := inspectCapacity(cfg.BackupDir, estimate, uint64(max(0, cfg.StorageReserveBytes)))
	r.RequiredInodes = uint64(max(1, cfg.StorageMinFreeInodes))
	r.Sufficient = r.Sufficient && r.FreeInodes >= r.RequiredInodes
	if err != nil {
		return r, err
	}
	if !r.Sufficient {
		return r, fmt.Errorf("insufficient backup capacity: available=%d required=%d free_inodes=%d required_inodes=%d", r.AvailableBytes, r.RequiredBytes, r.FreeInodes, r.RequiredInodes)
	}
	return r, nil
}

// Mid-run recovery uses only previously validated cleanup intents. It never
// races a fresh snapshot inventory or silently redirects an active dump.
type reserveWriter struct {
	file    *os.File
	reserve uint64
	since   uint64
	inspect func(string, uint64, uint64) (CapacityReport, error)
}

func (w *reserveWriter) Write(p []byte) (int, error) {
	if w.since == 0 || w.since >= 16<<20 {
		inspect := w.inspect
		if inspect == nil {
			inspect = inspectCapacity
		}
		r, e := inspect(filepath.Dir(w.file.Name()), uint64(len(p)), w.reserve)
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
