package backupapp

import "testing"

func TestChooseAdaptiveLogicalParallel(t *testing.T) {
	tests := []struct {
		name       string
		base       int
		cpus       int
		loadPerCPU float64
		want       int
	}{
		{name: "very high load forces single logical worker", base: 4, cpus: 8, loadPerCPU: 1.60, want: 1},
		{name: "high load halves parallelism", base: 4, cpus: 4, loadPerCPU: 1.10, want: 2},
		{name: "moderate load reduces by one", base: 3, cpus: 4, loadPerCPU: 0.80, want: 2},
		{name: "very low load on larger host boosts by two", base: 2, cpus: 8, loadPerCPU: 0.08, want: 4},
		{name: "low load boosts by one", base: 2, cpus: 4, loadPerCPU: 0.10, want: 3},
		{name: "single worker can scale to two on light load", base: 1, cpus: 4, loadPerCPU: 0.30, want: 2},
	}

	for _, tc := range tests {
		if got := chooseAdaptiveLogicalParallel(tc.base, tc.cpus, tc.loadPerCPU); got != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, got)
		}
	}
}

func TestChooseAdaptivePhysicalParallel(t *testing.T) {
	tests := []struct {
		name       string
		base       int
		cpus       int
		loadPerCPU float64
		want       int
	}{
		{name: "very high load sharply reduces physical parallelism", base: 6, cpus: 8, loadPerCPU: 1.60, want: 2},
		{name: "high load halves physical parallelism", base: 4, cpus: 4, loadPerCPU: 1.05, want: 2},
		{name: "moderate load reduces physical parallelism", base: 4, cpus: 4, loadPerCPU: 0.80, want: 3},
		{name: "very low load on larger host boosts physical parallelism by two", base: 4, cpus: 8, loadPerCPU: 0.08, want: 6},
		{name: "low load can boost physical parallelism", base: 2, cpus: 4, loadPerCPU: 0.10, want: 3},
	}

	for _, tc := range tests {
		if got := chooseAdaptivePhysicalParallel(tc.base, tc.cpus, tc.loadPerCPU); got != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, got)
		}
	}
}

func TestChooseAdaptiveXbcloudParallel(t *testing.T) {
	tests := []struct {
		name       string
		base       int
		cpus       int
		loadPerCPU float64
		want       int
	}{
		{name: "very high load forces single xbcloud worker", base: 4, cpus: 8, loadPerCPU: 1.60, want: 1},
		{name: "high load halves xbcloud parallelism", base: 4, cpus: 4, loadPerCPU: 1.05, want: 2},
		{name: "moderate load reduces xbcloud parallelism", base: 3, cpus: 4, loadPerCPU: 0.80, want: 2},
		{name: "very low load on larger host boosts xbcloud parallelism by one", base: 2, cpus: 8, loadPerCPU: 0.08, want: 3},
	}

	for _, tc := range tests {
		if got := chooseAdaptiveXbcloudParallel(tc.base, tc.cpus, tc.loadPerCPU); got != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, got)
		}
	}
}

func TestNormalizedXbcloudFIFOStreams(t *testing.T) {
	if got := normalizedXbcloudFIFOStreams(4, 2); got != 2 {
		t.Fatalf("expected fifo streams capped by parallelism, got %d", got)
	}
	if got := normalizedXbcloudFIFOStreams(0, 3); got != 1 {
		t.Fatalf("expected fifo streams minimum 1, got %d", got)
	}
}

func TestIsPhysicalUploadRateLimited(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{message: "xbcloud: S3 error message: Please reduce your request rate.", want: true},
		{message: "physical backup upload failed: Slow Down", want: true},
		{message: "physical backup upload failed: throttling detected", want: true},
		{message: "physical backup upload failed: permission denied", want: false},
	}

	for _, tc := range cases {
		if got := isPhysicalUploadRateLimited(tc.message); got != tc.want {
			t.Fatalf("message %q: expected %t, got %t", tc.message, tc.want, got)
		}
	}
}
