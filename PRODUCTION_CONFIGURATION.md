# Production backup configuration

This records the deployed profile first verified on October 12, 2026, with
the local-retention and live-verification update on October 19, 2026.
It is a host-specific operating profile, not the generic `.env.example`
defaults. Credentials and backup contents are never versioned.

## Verified operating state

Latest verification, October 19: run `2026-10-19_13-58-22` completed all six
logical databases and the physical backup successfully. Independent full local
and S3 reads verified sizes, SHA-256, gzip integrity, SQL hashes, exact artifact
inventory and completion markers. The physical run verified all 4,013 remote
chunks (20,975,993,263 bytes) against xbcloud source MD5 and recorded SHA-256.
Exactly four local logical payload runs remain. Fifteen obsolete local sets,
including legacy and diagnostic locations, were removed: 11,843,740,387 bytes.
Manifests and deletion journals remain, and offsite history is retained.

The timer remains active; its next trigger at final verification is October 19
at 16:00 IST. Manual systemd start required a sudo password, so the validation
ran directly as the configured `developer` service account. The previous
systemd failure status requires a privileged reset or the next successful
scheduled run to clear. Restore testing and physical preparation were not run.

- [October 19 retention and backup verification](remediation/local-retention-verification.json)

The following October 12 evidence records the initial deployment baseline.

The production validation run `2026-10-12_20-21-17`, using the enabled local
storage manager, captured **six of six** databases with no failures in
**10m43s**. Its 1,078,484,392-byte shared dump contains 921 base tables, 382 views
and 13 triggers; no routines/events were present. Independent full-payload
checks confirmed local/S3 sizes and SHA-256, gzip integrity, SQL checksum, dump
footer and expected artifact inventory. The preceding system-timer run at
18:00 also completed all six databases; the next timer trigger remains
October 13 at 02:00 IST.

- [Independent new-capture verification](remediation/storage-production-backup-proof.json)
- [Storage rollout and protection evidence](remediation/STORAGE_RECOVERY.md)
- [Deployed configuration/source verification](remediation/storage-deployment-proof.json)
- [Systemd/TUI status at verification](remediation/evidence/deployed-systemd-status.json)
- [Earlier investigation and limitations](remediation/README.md)

Capture and remote integrity are **VERIFIED**. Recovery by restore, historical
physical preparation, RTO, offsite binlog continuity and alert delivery remain
**UNVERIFIED**. No restore was performed; restore testing is disabled at the
operator's request. **NOT YET VERIFIED FOR PRODUCTION RECOVERY.**

## Configuration source and schedule

Authoritative environment file:
`/var/www/go-workspace/sdl/sdl_db_backup/.env`, owned by `developer`, mode 0600.
Production service: `/etc/systemd/system/sdl-db-backup.service`, running as
`developer` with `ExecStart=/usr/bin/env go run ./main.go` from the repository.
The actual scheduler is system-level; do not add a user timer for this profile.

```dotenv
BACKUP_SYSTEMD_SCOPE=system
BACKUP_SCHEDULE_TIMEZONE=Asia/Kolkata
BACKUP_LOGICAL_ENABLED=true
BACKUP_LOGICAL_SCHEDULE=daily@02:00,06:00,12:00,16:00,18:00
BACKUP_PHYSICAL_ENABLED=true
BACKUP_PHYSICAL_SCHEDULE=weekly@sun,02:00
```

The managed timer drop-in is
`/etc/systemd/system/sdl-db-backup.timer.d/90-sdl-schedule.conf`. Independent
systemd checks found every intended calendar, no drift, and service result
`success`. The next trigger at verification was October 13, 02:00 IST.
The Sunday physical calendar coincides with the daily 02:00 calendar; one
service invocation evaluates both schedules and uses the same run lock.

Saving settings is separate from changing the live timer. Preview with:

```bash
go run ./cmd/sdl-db-backup-schedule --env .env
```

For an explicitly authorized timer change, build the helper and run it through
the operator's existing narrowly scoped privileges:

```bash
go build -o /tmp/sdl-db-backup-schedule ./cmd/sdl-db-backup-schedule
sudo /tmp/sdl-db-backup-schedule --env /var/www/go-workspace/sdl/sdl_db_backup/.env --apply
```

The transaction validates calendar syntax, preserves configuration ownership,
reloads systemd, restarts an active timer, verifies effective calendars and
rolls back on failure. A persistent timer can trigger a catch-up invocation.
No broad sudo grant is required or introduced by these changes.

Launch the TUI against the production configuration explicitly:

```bash
BACKUP_ENV_FILE=/var/www/go-workspace/sdl/sdl_db_backup/.env ./sdl-db-backup-tui
```

## Logical capture and integrity

```dotenv
BACKUP_LOGICAL_DATABASES=bk_pf_TickleRight_9210,bk_pf_messenger,pf_TickleRight_9210,pf_central,pf_messenger,pf_messenger_dev
BACKUP_LOGICAL_CONSISTENCY=global
BACKUP_ALLOW_OBJECT_OMISSIONS=false
BACKUP_LOGICAL_PARALLEL=4
BACKUP_LOGICAL_GZIP_LEVEL=4
BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE=true
MYSQLDUMP_BIN=/var/www/go-workspace/sdl/sdl_db_backup/remediation/tools/mysql84/usr/bin/mysqldump
BACKUP_MYSQL_PITR_METADATA_ENABLED=false
RESTORE_TEST_ENABLED=false
```

Global mode uses one `mysqldump --single-transaction --databases ...` invocation
and one `all-databases.sql.gz` file. Per-database results reference that shared
file; configured parallelism does not create independent global snapshots.
All required objects, gzip integrity, footer, sizes and hashes must verify.
A subset cannot publish a complete marker.

The source MySQL version observed was 8.0.46. The private MySQL 8.4.11 dump
client supports `--init-command`; it sets `max_execution_time=0` for its session.
The production global remains 30000 ms. The private client was extracted from
the official HTTPS repository without replacing system packages; repository
SHA-256 was checked, but package signature verification was not established.
The binary is a local deployment prerequisite and is excluded from Git.

Global capture requires InnoDB and existing `BACKUP_ADMIN` authorization for
the backup lock. No privileges were granted during remediation. Exclude schema
deployments while capturing: backup locking and object-name inventory checks
do not prove unchanged SQL definitions under arbitrary metadata changes.
The approved view repair changed only the missing definer to an existing
account, with unchanged business definition and security mode.

## Consolidated backup logs

As of October 19, 2026, the canonical log and operational-state directory is
`/mnt/volume_1/backup/logs/sdl-db-backup`. Daily/run logs, run history,
schedule state, the shared backup lock and deletion audits are together there.
Earlier one-off and smoke-test logs are under its private `diagnostics/` tree.
These are deployment settings in `.env`, not paths embedded in the runner.
`BACKUP_DIR`, `BACKUP_LOG_DIR`, `BACKUP_LOCK_FILE`, and
`BACKUP_XTRABACKUP_WORK_DIR` control their respective locations. When omitted,
the log and physical work directories default to `BACKUP_DIR/logs` and
`BACKUP_DIR/xtrabackup-work`; the shared lock defaults to `BACKUP_LOG_DIR/backup.lock`.
The migration tool accepts `--env`, `--log-dir`, and `--journal-dir`; without
`--log-dir` it only reports the configured location. The deployment helper also
reads the configured lock. Render the collector example using
`python3 loki/render_config.py --env .env --output /tmp/sdl-promtail.yml`.
The old runtime and diagnostic paths are compatibility symlinks to this single
physical tree; historical manifest/log references stay valid. Live Promtail
continues through its old path alias, preserving file identities and saved tail
offsets. No collector restart or duplicate log copy was introduced.

The migration verified every moved file checksum and permission, shared-lock
exclusion, readable history/state, current health and collector readiness.
See [log consolidation verification](remediation/log-path-verification.json).

### Configuration and capture verification, October 19

Active runner defaults and operational helpers use configured directories; no
host-specific `/mnt/volume_1` path remains embedded in their source. The three
rebuilt runner, health, and TUI binaries were installed and checksum-verified.
Run `2026-10-19_15-21-15` finished successfully at 15:42 IST with all six logical
databases, 1,088,541,600 compressed bytes, and a committed S3 completion marker.
Independent full S3 downloads matched local SHA-256 and sizes; gzip integrity,
the dump completion footer, and all recorded SQL hashes passed. Four local
payload runs remain; the 02:00 payload was safely evicted. Restore testing was
not performed. The persisted logical and physical schedules remain unchanged.

Promtail reads the centralized files through its existing compatibility alias.
The operator applied the backup timestamp timezone correction at 15:50 IST;
both configurations now use the host timezone, and Promtail is active and ready.
A uniquely labeled diagnostic entry appended to the configured run log was
independently retrieved from Loki at 15:51 IST, confirming delivery with a current
timestamp. The backup payloads were unchanged. Earlier entries rejected before
the correction were not replayed.

`remediation/apply_promtail_log_timezone.py` uses the host timezone, accepts
`--timezone`, preserves existing scrape paths and positions, and changes only
the backup timestamp stage. Repeating the following command is a no-op when
the settings already match:

```bash
sudo python3 remediation/apply_promtail_log_timezone.py \
  --config /etc/promtail/config.yaml \
  --config /etc/promtail/promtail-config.yml --apply
```

The Git fields in the verification report record the checkout before publication.
See [configuration and sync verification](remediation/configuration-sync-verification.json).

## Storage and retention

Updated October 19, 2026: `BACKUP_LOCAL_MAX_BACKUPS=4` limits local logical
payloads to four timestamped runs, independently of disk pressure. The runner
reserves one slot before capture, retaining three independently verified local
points before writing the fourth. Obsolete payloads are unlinked after full
verification of their exact offsite counterpart; manifests and audit records
remain. S3 history is retained. Pins, restore evidence, unknown artifacts and
physical/binlog dependencies still prevent unsafe deletion; an unmet limit
blocks another capture rather than growing local payload storage.

A combined run whose only failure is its physical task may have its logical
payload independently verified for retention. Its committed PARTIAL manifest
and marker remain unchanged, and it cannot count as a complete offsite recovery
point. At least two independently verified COMPLETE offsite points are required.


```dotenv
BACKUP_DIR=/var/www/go-workspace/sdl/sdl_db_backup/remediation/production-backups
BACKUP_LOG_DIR=/mnt/volume_1/backup/logs/sdl-db-backup
BACKUP_LOCK_FILE=/mnt/volume_1/backup/logs/sdl-db-backup/backup.lock
BACKUP_STORAGE_AUTO_CLEANUP=true
BACKUP_LOCAL_MAX_BACKUPS=4
BACKUP_STORAGE_ESTIMATE_BYTES=0
BACKUP_STORAGE_RESERVE_BYTES=2147483648
BACKUP_STORAGE_HIGH_WATERMARK=92
BACKUP_STORAGE_LOW_WATERMARK=90
BACKUP_STORAGE_GROWTH_PERCENT=50
BACKUP_STORAGE_HISTORY_RUNS=12
BACKUP_STORAGE_CONCURRENT_BYTES=536870912
BACKUP_STORAGE_TEMPORARY_BYTES=268435456
BACKUP_STORAGE_MIN_FREE_INODES=128
BACKUP_STORAGE_TEMP_MAX_AGE=24h
BACKUP_STORAGE_VERIFY_TIMEOUT=20m
BACKUP_STORAGE_SECONDARY_DIR=
BACKUP_RETENTION_DELETE_ENABLED=false
BACKUP_RETENTION_MIN_COMPLETE=2
BACKUP_RETENTION_MIN_VERIFIED=1
BACKUP_RETENTION_DAILY=7
BACKUP_RETENTION_WEEKLY=4
BACKUP_RETENTION_MONTHLY=12
```

The automatic local manager runs before capture. The maximum of the most recent
12 complete compressed captures receives a 50% growth allowance, 512 MiB for
concurrent writes, 256 MiB for temporary files and a separate 2 GiB reserve.
Current capture history is approximately 1.08 GB; projected required free space
is approximately 4.57 GB. Actual bytes and inode availability are measured on
the destination filesystem and checked during capture.

At 92% usage, cleanup targets 90% usage or required capture capacity, whichever
requires more free space. It removes only eligible obsolete local payloads,
oldest eligible first, with full independent S3 proof and protected local/offsite
coverage. At least one independently verified complete logical local point and two
verified complete offsite points are required. The configured count is
protected, reserving one slot before a logical capture; a capacity emergency
can reduce local copies further while preserving those minimum safeguards. No remote deletion is
enabled. Metadata, pinned/restore-tested points, uniquely useful partials,
physical components and binlog dependencies remain protected.

Preview without deleting artifacts:

```bash
BACKUP_ENV_FILE=.env ./sdl-db-backup storage-plan
```

Legacy GFS quarantine remains disabled and is bypassed while the storage manager
is enabled. On October 19, obsolete payloads in the original-volume and diagnostic
capture locations were removed after current-backup verification; their metadata
remains. Recurring count retention applies to the configured active destination.
No secondary path is approved; insufficient safe capacity fails CRITICAL.
Logical remote-only streaming is not enabled. Existing physical streaming is
preserved with separate capacity checking of its work directory.

See [current capacity, protected IDs, dry-run, tests and rollout evidence](remediation/STORAGE_RECOVERY.md).
The forecast is time to exhaustion without additional cleanup, not a promise of
permanent capacity. Growth or unrelated writes can still require intervention.

## Remote publication and audit

```dotenv
BACKUP_S3_UPLOAD_MODE=direct
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true
BACKUP_REQUIRE_REMOTE_COMPLETION=true
BACKUP_S3_LOGICAL_PREFIX=recovery-hold/logical
BACKUP_S3_PHYSICAL_PREFIX=backup-as-it-is
```

The verified logical destination is
`s3://ticklerightbackups/recovery-hold/logical/2026-10-12_20-21-17/`.
S3 reported AES256 server-side encryption. Direct uploads check whole payloads,
every listing page and exact inventory before publishing `COMPLETED.json`.
Partial/failed runs receive separate markers. Never treat an old completion
marker alone as proof of completeness or recoverability.

Committed manifest bytes are frozen and pinned by `manifest-commit.json`.
Later execution bookkeeping belongs in `execution-finalization.json` and run
history. The verified 17:26 and 18:00 remote manifests precede this final
ordering fix and have an empty duration field; their measured durations remain
in preserved local audits and history. Their local copies were aligned with
the exact committed remote bytes without modifying any dump or remote object.
Future runs set duration before freezing the uploaded manifest.

Physical verification now uses every remote chunk and xbcloud source MD5,
recording SHA-256 independently of multipart ETags. The installed binary
supports `--md5`; the October 19 physical capture and every remote chunk were
verified. Physical preparation and recovery remain unverified.

Existing metrics report last-capture preflight capacity, complete/partial capture
status and remote integrity; TUI/health measures current capacity. Prometheus
scrapes the new storage metrics, but the host has no backup alert rules loaded.
The validated rule installer requires an operator sudo terminal; see the storage
report for install/rollback. Alert evaluation/delivery and S3 versioning/Object
Lock policy access remain unverified.

## Operational objectives and rollback

The largest desired snapshot interval is eight hours. With measured completion
near twelve minutes, a provisional normal-operation offsite RPO is about
8h15. This is not a tested SLA or a business-approved target. RTO is unmeasured;
restore work remains outside the authorized scope.

Source/configuration rollback and the timer-drop-in reversal are documented in
[the remediation report](remediation/README.md). Protected rollback snapshots,
private inventories, SQL migration files and deployment packages stay local
and are not committed. Rolling back code does not remove any backup artifact.
