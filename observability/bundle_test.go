package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservabilityBundleREADMEReferencesCoreAssets(t *testing.T) {
	path := filepath.Join("README.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle readme: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		"GRAFANA_BACKUP_MONITORING.md",
		"dashboards/sdl-db-backup-observability.json",
		"dashboards/sdl-db-backup-logs.json",
		"prometheus/sdl-db-backup-alerts.yml",
		"prometheus/sdl-db-backup-scrape-example.yml",
		"loki/README.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected bundle readme to mention %q", want)
		}
	}
}
