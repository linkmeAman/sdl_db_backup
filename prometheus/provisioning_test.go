package prometheus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrometheusREADMEReferencesAlertRuleFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("README.md"))
	if err != nil {
		t.Fatalf("read prometheus readme: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"sdl-db-backup-alerts.yml",
		"rule_files:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected prometheus readme to mention %q", want)
		}
	}
}
