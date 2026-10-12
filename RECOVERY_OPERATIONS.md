# Recovery Operations Runbook

This runbook defines the operational controls that must accompany the backup
application. Do not treat a successful backup process as a disaster-recovery
guarantee until these controls are configured and rehearsed.

## Current production scope

As of October 12, 2026, all six databases have passed shared-transaction capture
and independent local/S3 integrity verification. Restore testing is disabled
by operator instruction; no restore was run. Recovery readiness and RTO remain
unverified. Current settings and evidence are in
[PRODUCTION_CONFIGURATION.md](PRODUCTION_CONFIGURATION.md). The procedures below
require a separately authorized, isolated recovery exercise. The existing
per-database restore tool rejects global `all-databases.sql.gz` bundles.

## Physical restore drill

Run physical validation on a dedicated verification host with the same major
MySQL and Percona XtraBackup version as production. The command only downloads,
extracts, and prepares the backup; it never stops or modifies a MySQL server.

```bash
sdl-db-backup physical-validate <run-id>
```

Requirements:

- `xbcloud`, `xbstream`, and `xtrabackup` must be installed;
- `BACKUP_XTRABACKUP_WORK_DIR` must have free space for an extracted physical
  backup plus prepare overhead;
- the configured S3 credentials need read access to the physical prefix;
- the run must contain a successful `PHYSICAL_COMPLETED.json` marker.

Schedule this drill at least weekly. Alert if its most recent successful run is
older than the recovery objective. A prepare-only drill proves stream and redo
log consistency; periodically perform a further isolated MySQL startup and
application smoke test.

## Point-in-time recovery

PITR requires a recovery coordinate and a separately retained sequence of
binary logs. Enable binary logging with GTID where appropriate, archive logs to
an independent immutable destination, and retain them longer than the maximum
time between full backups plus the required recovery window.

For MySQL logical backups, enable `BACKUP_MYSQL_PITR_METADATA_ENABLED=true` to
record a pre-dump server observation in `manifest.json` and a
`mysql_pitr_coordinate` on each successful dump artifact. Use
`BACKUP_MYSQL_PITR_METADATA_REQUIRED=true` only after testing the necessary
backup-user grants (`REPLICATION CLIENT` plus the privileges required by
`mysqldump --source-data`, commonly `RELOAD` or `FLUSH_TABLES`). A run-level
observation is not a common transaction snapshot for parallel dump sessions;
use the artifact coordinate when selecting the binlog replay start/end for that
artifact.

For each rehearsal:

1. Restore the selected logical or physical base backup on an isolated host.
2. Apply archived binlogs from the recorded recovery coordinate.
3. Stop at a test timestamp or GTID.
4. Validate application-level invariants, not only database connectivity.
5. Record the elapsed recovery time and compare it to the RTO.

## S3 durability controls

Configure these controls in the target AWS account; they are intentionally not
enabled automatically by the application:

- bucket versioning;
- lifecycle rules that retain completed backup prefixes for the required GFS
  period and expire abandoned staging data separately;
- Object Lock in governance or compliance mode where retention must resist
  deletion or ransomware;
- replication to a separate AWS account and, when required, a separate region;
- least-privilege write credentials for the backup host and separate read-only
  credentials for restore drills.

Recovery selection must verify marker status, manifest requirements, complete
artifact inventory, sizes and checksums. `COMPLETED.json` or
`PHYSICAL_COMPLETED.json` presence alone is insufficient, especially for legacy
markers. Physical preparation and startup require separate evidence.

Automatic local deletion is held. `go run ./main.go retention-plan` explains
coverage without modifying artifacts. Authorized cleanup quarantines reviewed
candidates; it never automatically purges them. Preserve backup dependencies
and authorize any deletion separately.

## Release gate

Before releasing a backup configuration change, verify all of the following:

- `go test ./...` and static analysis pass;
- an encrypted logical backup is validated and restored;
- a completed S3 prefix is independently checked from a second host;
- a physical backup passes `physical-validate`;
- one end-to-end restore drill meets the RTO and RPO;
- alerts fire for failed, stale, incomplete, and unvalidated backups.
