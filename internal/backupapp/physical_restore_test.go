package backupapp

import "testing"

func TestPhysicalObjectKey(t *testing.T) {
	cfg := config{S3PhysicalPrefix: " backup-as-it-is/ "}
	if got := physicalObjectKey(cfg, "2026-09-03_01-02-03"); got != "backup-as-it-is/2026-09-03_01-02-03/physical.xbstream" {
		t.Fatalf("unexpected physical object key %q", got)
	}
}
