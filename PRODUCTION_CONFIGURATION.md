# Production backup configuration

This is a reference operating profile for a production deployment. Values shown
are examples; adapt paths, databases, schedules and bucket names to the host.
It is not the generic `.env.example` defaults. Credentials and backup contents
are never versioned.

## Verification status to track

Record for each deployment which properties have been independently verified:

- **Capture and remote integrity:** local and S3 sizes, SHA-256, gzip
  integrity, SQL checksums, dump footer, expected artifact inventory and
  completion markers.
- **Physical backups:** every remote chunk against xbcloud source MD5.
- **Recovery:** restore, physical preparation, RTO, offsite binlog continuity
  and alert delivery.

Capture integrity does not prove recoverability. Until a restore has been
performed, treat the deployment as **not yet verified for production recovery**.

## Configuration source and schedule

Authoritative environment file: `/opt/sdl-db-backup/.env`, owned by the service
account (for example `backup`), mode 0600. Production service:
`/etc/systemd/system/sdl-db-backup.service`, running as that account with
`ExecStart=/usr/bin/env go run ./main.go` from the repository. With
`BACKUP_SYSTEMD_SCOPE=system` the scheduler is system-level; do not add a user
timer as well.

```dotenv
BACKUP_SYSTEMD_SCOPE=system
BACKUP_SCHEDULE_TIMEZONE=Europe/Berlin
BACKUP_LOGICAL_ENABLED=true
BACKUP_LOGICAL_SCHEDULE=daily@02:00,06:00,12:00,16:00,18:00
BACKUP_PHYSICAL_ENABLED=true
BACKUP_PHYSICAL_SCHEDULE=weekly@sun,02:00
```

The managed timer drop-in is
`/etc/systemd/system/sdl-db-backup.timer.d/90-sdl-schedule.conf`. When a weekly
physical calendar coincides with a daily logical calendar, one service
invocation evaluates both schedules and uses the same run lock.

Saving settings is separate from changing the live timer. Preview with:

```bash
go run ./cmd/sdl-db-backup-schedule --env .env
```

For an explicitly authorized timer change, build the helper and run it through
narrowly scoped privileges:

```bash
go build -o /tmp/sdl-db-backup-schedule ./cmd/sdl-db-backup-schedule
sudo /tmp/sdl-db-backup-schedule --env /opt/sdl-db-backup/.env --apply
```

The transaction validates calendar syntax, preserves configuration ownership,
reloads systemd, restarts an active timer, verifies effective calendars and
rolls back on failure. A persistent timer can trigger a catch-up invocation.
No broad sudo grant is required.

Launch the TUI against the production configuration explicitly:

```bash
BACKUP_ENV_FILE=/opt/sdl-db-backup/.env ./sdl-db-backup-tui
```

For a manual check, select **logical only**, **normal upload behavior**, keep
the configured database scope, and leave **Force Run Now** enabled. This
overrides the logical schedule for that invocation. Selecting both modes also
forces a physical backup, even outside its weekly slot.

Manual and scheduled runs share a lock. A manual start while another run is
active is rejected: the TUI returns to Preview with a busy message, and active
metrics and backup history are untouched. Retry `Start backup` after the active
run finishes.

## Logical capture and integrity

```dotenv
BACKUP_LOGICAL_DATABASES=bk_app_db,app_db,central_db,messenger_db
BACKUP_LOGICAL_CONSISTENCY=global
BACKUP_ALLOW_OBJECT_OMISSIONS=false
BACKUP_LOGICAL_PARALLEL=4
BACKUP_LOGICAL_GZIP_LEVEL=4
BACKUP_LOGICAL_COMPRESSOR=auto
BACKUP_LOGICAL_COMPRESS_THREADS=4
BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE=true
MYSQLDUMP_BIN=/opt/mysql-8.4/bin/mysqldump
BACKUP_MYSQL_PITR_METADATA_ENABLED=false
RESTORE_TEST_ENABLED=false
```

Global mode uses one `mysqldump --single-transaction --databases ...` invocation
and one `all-databases.sql.gz` file. Per-database results reference that shared
file; configured parallelism does not create independent global snapshots.
All required objects, gzip integrity, footer, sizes and hashes must verify.
A subset cannot publish a complete marker.

Because global mode is one stream, `BACKUP_LOGICAL_PARALLEL` does not speed it
up; compression does. `BACKUP_LOGICAL_COMPRESSOR=auto` (the default) streams the
dump through `pigz` with `BACKUP_LOGICAL_COMPRESS_THREADS` threads when pigz is
installed, and otherwise uses the built-in single-threaded gzip. Both write
standard gzip with identical SQL content; restore and validation are unchanged.
pigz is typically several times faster, with output a few percent larger. Set
`gzip` to force the built-in compressor; an explicit `pigz` fails before
dumping if the binary is missing.

`BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE=true` requires a `mysqldump` that
supports `--init-command` (a MySQL 8.4 client, for example). It sets
`max_execution_time=0` for the dump session only, leaving the server's global
value unchanged. A private client can be installed outside system packages and
selected with `MYSQLDUMP_BIN`; keep it out of Git.

Global capture requires InnoDB and `BACKUP_ADMIN` authorization for the backup
lock. Exclude schema deployments while capturing: backup locking and object-name
inventory checks do not prove unchanged SQL definitions under arbitrary
metadata changes. Views with missing definers must be repaired before capture.

## Consolidated backup logs

Keep logs and operational state in one directory, for example
`/srv/backup/logs/sdl-db-backup`: daily/run logs, run history, schedule state,
the shared backup lock and deletion audits.
These are deployment settings in `.env`, not paths embedded in the runner.
`BACKUP_DIR`, `BACKUP_LOG_DIR`, `BACKUP_LOCK_FILE`, and
`BACKUP_XTRABACKUP_WORK_DIR` control their respective locations. When omitted,
the log and physical work directories default to `BACKUP_DIR/logs` and
`BACKUP_DIR/xtrabackup-work`; the shared lock defaults to `BACKUP_LOG_DIR/backup.lock`.
The migration tool accepts `--env`, `--log-dir`, and `--journal-dir`; without
`--log-dir` it only reports the configured location. The deployment helper also
reads the configured lock. Render the collector example using
`python3 loki/render_config.py --env .env --output /tmp/sdl-promtail.yml`.

When moving the log directory, compatibility symlinks from the old paths keep
historical manifest/log references valid, and let an existing Promtail keep its
file identities and saved tail offsets without a restart or duplicate copies.
Configure Promtail's backup timestamp stage with the host timezone so entries
are not rejected as out of order.

## Storage and retention

`BACKUP_LOCAL_MAX_BACKUPS=4` limits local logical payloads to four timestamped
runs, independently of disk pressure. The runner reserves one slot before
capture, retaining three independently verified local points before writing the
fourth. Obsolete payloads are unlinked after full verification of their exact
offsite counterpart. If an independent S3 policy has expired that counterpart,
an old complete logical run outside the explicit local count window may instead
expire after verification of the entire newer local window and two newer
complete offsite points. This count-only exception does not apply to partial,
scoped, pinned, restore-tested or recovery-dependent runs, and the application
does not change S3 retention. When the last eligible payload is removed, known metadata
and the obsolete timestamp folder are removed too, and existing metadata-only
leftovers are swept automatically; central deletion audits and S3 history are
retained. Pins, restore evidence, unknown artifacts and physical/binlog
dependencies still prevent unsafe deletion. With sufficient verified capture
capacity, an unmet count limit is reported as a warning; insufficient space
still blocks the next capture.

A combined run whose only failure is its physical task may have its logical
payload independently verified for retention. Its committed PARTIAL manifest
and marker remain unchanged, and it cannot count as a complete offsite recovery
point. At least two independently verified COMPLETE offsite points are required.

```dotenv
BACKUP_DIR=/srv/backup/mysql_backup
BACKUP_LOG_DIR=/srv/backup/logs/sdl-db-backup
BACKUP_LOCK_FILE=/srv/backup/logs/sdl-db-backup/backup.lock
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
BACKUP_STORAGE_VERIFY_CACHE_TTL=24h
BACKUP_STORAGE_SECONDARY_DIR=
BACKUP_RETENTION_DELETE_ENABLED=false
BACKUP_RETENTION_MIN_COMPLETE=2
BACKUP_RETENTION_MIN_VERIFIED=1
BACKUP_RETENTION_DAILY=7
BACKUP_RETENTION_WEEKLY=4
BACKUP_RETENTION_MONTHLY=12
```

When moving `BACKUP_DIR` to a new volume, copy the retained runs, verify their
SHA-256 before removing the source copies, and keep committed manifest bytes
unchanged so run history still resolves.

`BACKUP_STORAGE_VERIFY_CACHE_TTL` (default `24h`, `0` disables) lets the
pre-capture manager skip re-reading recovery points that were fully verified
within that window and have not changed. Every full verification (after
capture, before upload, during retention) still re-reads artifacts and records
its result in `BACKUP_LOG_DIR/storage-verify-cache.json` (mode 0600, no key
material). A local entry is reused only while size, mtime, ctime, inode, device,
manifest hashes and the encryption key are unchanged; an S3 entry only while a
HEAD shows the same ETag, size, version and Last-Modified, and the completion
marker, manifest and listing are still checked live. Without it, every run
re-reads each protected local point in full and downloads offsite points again.

The automatic local manager runs before capture. The maximum of the most recent
12 complete compressed captures receives a 50% growth allowance, 512 MiB for
concurrent writes, 256 MiB for temporary files and a separate 2 GiB reserve.
Actual bytes and inode availability are measured on the destination filesystem
and checked during capture.

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

Legacy GFS quarantine is bypassed while the storage manager is enabled.
Recurring count retention applies to the configured active destination.
Without an approved secondary path, insufficient safe capacity fails CRITICAL.
Physical streaming has separate capacity checking of its work directory.

The forecast is time to exhaustion without additional cleanup, not a promise of
permanent capacity. Growth or unrelated writes can still require intervention.

## Remote publication and audit

```dotenv
BACKUP_S3_UPLOAD_MODE=direct
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true
BACKUP_REQUIRE_REMOTE_COMPLETION=true
BACKUP_S3_LOGICAL_PREFIX=logical/
BACKUP_S3_PHYSICAL_PREFIX=physical
```

Logical captures land under `s3://<bucket>/<logical prefix>/<run id>/`.
Changing `BACKUP_S3_LOGICAL_PREFIX` affects new uploads only: existing captures
retain their recorded destinations, and storage protection verification and its
final identity checks follow each manifest's destination in the same configured
bucket, so historical coverage is preserved. Historical S3 objects are not moved.

Direct uploads check whole payloads, every listing page and exact inventory
before publishing `COMPLETED.json`. Partial/failed runs receive separate
markers. Never treat an old completion marker alone as proof of completeness or
recoverability.

Committed manifest bytes are frozen and pinned by `manifest-commit.json`.
Later execution bookkeeping belongs in `execution-finalization.json` and run
history.

Physical verification uses every remote chunk and xbcloud source MD5, recording
SHA-256 independently of multipart ETags; it requires an xbcloud that supports
`--md5`. Physical preparation and recovery must be verified separately.

Metrics report last-capture preflight capacity, complete/partial capture status
and remote integrity; TUI/health measures current capacity. Load backup alert
rules into Prometheus and verify alert delivery, and review S3 versioning and
Object Lock policy, as separate deployment steps.

## Operational objectives and rollback

Derive a provisional offsite RPO from the largest snapshot interval plus the
measured capture and upload duration. It is not a tested SLA until restores have
been exercised; measure RTO with real restores.

Keep rollback snapshots, private inventories, SQL migration files and
deployment packages local; do not commit them. Rolling back code does not
remove any backup artifact.
