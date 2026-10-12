# Production backup configuration

This records the deployed configuration and evidence on October 12, 2026.
It is a host-specific operating profile, not the generic `.env.example`
defaults. Credentials and backup contents are never versioned.

## Verified operating state

The automatic system-level run `2026-10-12_18-00-03` captured **six of six**
databases with no failures in **11m30s**. Its 1,078,225,124-byte shared dump
contains 918 base tables, 382 views and 13 triggers; no routines/events were
present. Independent full-payload checks confirmed local/S3 sizes and SHA-256,
gzip integrity, SQL checksum, dump footer and expected artifact inventory.

- [Independent verification](remediation/deployed-backup-proof.json)
- [Systemd/TUI status at verification](remediation/evidence/deployed-systemd-status.json)
- [Investigation, limitations and rollback](remediation/README.md)

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

## Storage and retention

```dotenv
BACKUP_DIR=/var/www/go-workspace/sdl/sdl_db_backup/remediation/production-backups
BACKUP_LOG_DIR=/mnt/volume_1/backup/mysql_backup/logs
BACKUP_LOCK_FILE=/mnt/volume_1/backup/mysql_backup/logs/backup.lock
BACKUP_STORAGE_ESTIMATE_BYTES=2147483648
BACKUP_STORAGE_RESERVE_BYTES=2147483648
BACKUP_RETENTION_DELETE_ENABLED=false
BACKUP_RETENTION_MIN_COMPLETE=2
BACKUP_RETENTION_MIN_VERIFIED=1
BACKUP_RETENTION_DAILY=7
BACKUP_RETENTION_WEEKLY=4
BACKUP_RETENTION_MONTHLY=12
```

Preflight requires at least 6 GiB: twice the configured 2 GiB capture estimate
plus a 2 GiB reserve, increased if historical captures require more. Streaming
checks enforce the reserve and preserve failed files with incomplete names.
The original volume remains critically full. The new destination had roughly
44 GB free after verification: approximately seven days before the threshold
at 1.08 GB per capture and five captures/day, excluding growth and failures.
Provision lasting capacity before then; backups are not deleted automatically.

Preview retention without changing artifacts:

```bash
go run ./main.go retention-plan
```

GFS applies to verified complete captures. Latest complete coverage and unknown,
partial, physical and dependency-bearing points are protected. Explicitly
enabled cleanup journals quarantine; quarantine does **not** free disk space.
Any purge needs separate operator review and authorization. Legacy backups
remain at their original locations and have not been deleted.

## Remote publication and audit

```dotenv
BACKUP_S3_UPLOAD_MODE=direct
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true
BACKUP_REQUIRE_REMOTE_COMPLETION=true
BACKUP_S3_LOGICAL_PREFIX=recovery-hold/logical
BACKUP_S3_PHYSICAL_PREFIX=backup-as-it-is
```

The verified logical destination is
`s3://ticklerightbackups/recovery-hold/logical/2026-10-12_18-00-03/`.
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
supports `--md5`; live physical capture/preparation remains unverified.

Existing metrics report capacity, complete/partial capture status and remote
integrity. Updated alert-rule source is committed; live alert reload/delivery
and S3 versioning/Object Lock policy access remain unverified.

## Operational objectives and rollback

The largest desired snapshot interval is eight hours. With measured completion
near twelve minutes, a provisional normal-operation offsite RPO is about
8h15. This is not a tested SLA or a business-approved target. RTO is unmeasured;
restore work remains outside the authorized scope.

Source/configuration rollback and the timer-drop-in reversal are documented in
[the remediation report](remediation/README.md). Protected rollback snapshots,
private inventories, SQL migration files and deployment packages stay local
and are not committed. Rolling back code does not remove any backup artifact.
