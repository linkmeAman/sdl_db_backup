package backupapp

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type adaptiveResourceProfile struct {
	CPUs               int
	Load1              float64
	LoadPerCPU         float64
	LogicalParallel    int
	XtrabackupParallel int
	XbcloudParallel    int
	LoadDetected       bool
	TuningReason       string
}

func buildAdaptiveResourceProfile(cfg config, physicalThrottlePenalty int) adaptiveResourceProfile {
	cpus := runtime.NumCPU()
	if cpus < 1 {
		cpus = 1
	}

	profile := adaptiveResourceProfile{
		CPUs:               cpus,
		LogicalParallel:    normalizedLogicalParallelism(cfg.LogicalParallel),
		XtrabackupParallel: normalizedPhysicalParallelism(cfg.XtrabackupParallel),
		XbcloudParallel:    normalizedXbcloudParallelism(cfg.XbcloudParallel),
		TuningReason:       "configured",
	}

	load1, err := readHostLoadAverage()
	if err != nil {
		profile.TuningReason = "configured (load unavailable)"
		return profile
	}

	profile.Load1 = load1
	profile.LoadDetected = true
	profile.LoadPerCPU = load1 / float64(cpus)
	profile.LogicalParallel = chooseAdaptiveLogicalParallel(profile.LogicalParallel, cpus, profile.LoadPerCPU)
	profile.XtrabackupParallel = chooseAdaptivePhysicalParallel(profile.XtrabackupParallel, cpus, profile.LoadPerCPU)
	profile.XbcloudParallel = chooseAdaptiveXbcloudParallel(profile.XbcloudParallel, cpus, profile.LoadPerCPU)

	switch {
	case profile.LoadPerCPU >= 1.50:
		profile.TuningReason = "very-high-load reduction"
	case profile.LoadPerCPU >= 1.0:
		profile.TuningReason = "high-load reduction"
	case profile.LoadPerCPU >= 0.75:
		profile.TuningReason = "moderate-load reduction"
	case profile.LoadPerCPU <= 0.10:
		profile.TuningReason = "very-low-load boost"
	case profile.LoadPerCPU <= 0.20:
		profile.TuningReason = "low-load boost"
	case profile.LoadPerCPU <= 0.35:
		profile.TuningReason = "light-load boost"
	default:
		profile.TuningReason = "balanced"
	}

	for i := 0; i < physicalThrottlePenalty; i++ {
		if profile.XtrabackupParallel > 1 {
			profile.XtrabackupParallel = max(1, profile.XtrabackupParallel/2)
		}
		if profile.XbcloudParallel > 1 {
			profile.XbcloudParallel = max(1, profile.XbcloudParallel-1)
		}
	}
	if physicalThrottlePenalty > 0 {
		profile.TuningReason += fmt.Sprintf(" + throttling penalty x%d", physicalThrottlePenalty)
	}

	return profile
}

func readHostLoadAverage() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty /proc/loadavg")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func chooseAdaptiveLogicalParallel(base, cpus int, loadPerCPU float64) int {
	base = normalizedLogicalParallelism(base)
	maxParallel := max(1, cpus)
	switch {
	case loadPerCPU >= 1.50:
		return 1
	case loadPerCPU >= 1.0:
		return max(1, base/2)
	case loadPerCPU >= 0.75:
		return max(1, base-1)
	case loadPerCPU <= 0.10 && cpus >= 8:
		return min(maxParallel, base+2)
	case loadPerCPU <= 0.20:
		return min(maxParallel, base+1)
	case loadPerCPU <= 0.35 && base == 1 && cpus >= 2:
		return 2
	default:
		return min(maxParallel, base)
	}
}

func chooseAdaptivePhysicalParallel(base, cpus int, loadPerCPU float64) int {
	base = normalizedPhysicalParallelism(base)
	maxParallel := max(1, cpus)
	switch {
	case loadPerCPU >= 1.50:
		return max(1, base/3)
	case loadPerCPU >= 1.0:
		return max(1, base/2)
	case loadPerCPU >= 0.75:
		return max(1, base-1)
	case loadPerCPU <= 0.10 && cpus >= 8:
		return min(maxParallel, base+2)
	case loadPerCPU <= 0.20:
		return min(maxParallel, base+1)
	default:
		return min(maxParallel, base)
	}
}

func chooseAdaptiveXbcloudParallel(base, cpus int, loadPerCPU float64) int {
	base = normalizedXbcloudParallelism(base)
	maxParallel := min(max(1, cpus), 8)
	switch {
	case loadPerCPU >= 1.50:
		return 1
	case loadPerCPU >= 1.0:
		return max(1, base/2)
	case loadPerCPU >= 0.75:
		return max(1, base-1)
	case loadPerCPU <= 0.10 && cpus >= 8:
		return min(maxParallel, base+1)
	default:
		return min(maxParallel, base)
	}
}

func normalizedPhysicalParallelism(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

func normalizedXbcloudParallelism(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

func normalizedXbcloudFIFOStreams(value int, parallel int) int {
	value = max(1, value)
	parallel = normalizedXbcloudParallelism(parallel)
	if value > parallel {
		return parallel
	}
	return value
}

func logAdaptiveResourceProfile(profile adaptiveResourceProfile) {
	if profile.LoadDetected {
		log.Printf(
			"resource tuning: cpus=%d load1=%.2f load_per_cpu=%.2f logical_parallel=%d xtrabackup_parallel=%d xbcloud_parallel=%d reason=%s",
			profile.CPUs,
			profile.Load1,
			profile.LoadPerCPU,
			profile.LogicalParallel,
			profile.XtrabackupParallel,
			profile.XbcloudParallel,
			profile.TuningReason,
		)
		return
	}
	log.Printf(
		"resource tuning: cpus=%d logical_parallel=%d xtrabackup_parallel=%d xbcloud_parallel=%d reason=%s",
		profile.CPUs,
		profile.LogicalParallel,
		profile.XtrabackupParallel,
		profile.XbcloudParallel,
		profile.TuningReason,
	)
}
