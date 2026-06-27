package prometheus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrapeExampleMentionsNodeExporterTextfileCollector(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("sdl-db-backup-scrape-example.yml"))
	if err != nil {
		t.Fatalf("read scrape example: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"--collector.textfile.directory=/var/lib/node_exporter/textfile_collector",
		"sdl_db_backup.prom",
		"job_name: node_exporter",
		"node-exporter:9100",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected scrape example to mention %q", want)
		}
	}
}
