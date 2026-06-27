package grafana

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrafanaProvisioningDocsReferenceDashboardAsset(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("README.md"))
	if err != nil {
		t.Fatalf("read grafana readme: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"provisioning/dashboards/sdl-db-backup-dashboard-provider.yml",
		"dashboards/sdl-db-backup-observability.json",
		"provisioning/datasources/sdl-db-backup-loki-datasource.yml",
		"dashboards/sdl-db-backup-logs.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected grafana readme to mention %q", want)
		}
	}
}

func TestGrafanaProvisioningProviderReferencesDashboardFolder(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("provisioning", "dashboards", "sdl-db-backup-dashboard-provider.yml"))
	if err != nil {
		t.Fatalf("read grafana provider: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"apiVersion: 1",
		"folder: SDL DB Backup",
		"path: /var/lib/grafana/dashboards/sdl-db-backup",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected grafana provider to contain %q", want)
		}
	}
}

func TestGrafanaLokiDatasourceProvisioningFileLooksReasonable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("provisioning", "datasources", "sdl-db-backup-loki-datasource.yml"))
	if err != nil {
		t.Fatalf("read grafana loki datasource: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"apiVersion: 1",
		"type: loki",
		"url: http://localhost:3100",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected grafana loki datasource to contain %q", want)
		}
	}
}
