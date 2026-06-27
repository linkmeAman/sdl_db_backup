package observability

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestGrafanaGuideMentionsEveryEmittedBackupMetric(t *testing.T) {
	metricsPath := filepath.Join("..", "internal", "backupapp", "metrics.go")
	guidePath := filepath.Join("..", "GRAFANA_BACKUP_MONITORING.md")

	metricsSource, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatalf("read metrics source: %v", err)
	}
	guideSource, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read grafana guide: %v", err)
	}

	re := regexp.MustCompile(`Name:\s+"(backup_[a-z0-9_]+)"`)
	matches := re.FindAllStringSubmatch(string(metricsSource), -1)
	if len(matches) == 0 {
		t.Fatalf("did not find any backup metric definitions in %s", metricsPath)
	}

	seen := map[string]struct{}{}
	var metrics []string
	for _, match := range matches {
		name := match[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		metrics = append(metrics, name)
	}
	sort.Strings(metrics)

	guide := string(guideSource)
	var missing []string
	for _, metric := range metrics {
		if !strings.Contains(guide, "`"+metric+"`") && !strings.Contains(guide, metric+"{") {
			missing = append(missing, metric)
		}
	}

	if len(missing) > 0 {
		t.Fatalf("grafana guide is missing emitted metrics: %s", strings.Join(missing, ", "))
	}
}
