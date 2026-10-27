package backupapp

import (
	"context"
	"strings"
	"testing"
)

func TestStorageReportsLocalRunWithoutOffsiteCopy(t *testing.T) {
	cfg, v, m, _, s3 := storagePoints(t, 5)
	v.available = 9000
	cfg.StorageMaxLocalBackups = 0
	m.cfg = cfg
	s3.mu.Lock()
	for key := range s3.objects {
		if strings.HasSuffix(key, "2026-10-03_12-00-00/COMPLETED.json") {
			delete(s3.objects, key)
		}
	}
	s3.mu.Unlock()
	report, err := m.ensure(context.Background(), "", true, false)
	if err != nil {
		t.Fatalf("report-only gap must not block: %v", err)
	}
	if len(report.LocalWithoutOffsite) != 1 || report.LocalWithoutOffsite[0] != "2026-10-03_12-00-00" {
		t.Fatalf("offsite gap not reported: %+v", report.LocalWithoutOffsite)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 5 {
		t.Fatalf("held run was deleted: count=%d", count)
	}
}
