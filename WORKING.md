# SDL DB Backup Architecture and Operating Model

This document describes the current implementation. It complements the
operator-focused [README](README.md) and recovery procedures in
[RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md).

## Architecture

```text
main.go ────────────────┐
TUI ────────────────────┼──> internal/backupapp ──> MySQL / PostgreSQL
REST API ───────────────┤             │
health / validation CLI ┘             ├──> local artifacts, manifests, logs
                                      ├──> S3 logical or physical backups
                                      └──> Prometheus textfile metrics
```

| Component | Responsibility |
| --- | --- |
| `main.go` | Non-interactive runner plus `inspect`, `decrypt`, `physical-validate`, `retention-plan`, and restricted `reconcile-capture` commands. |
| `internal/backupapp` | Shared configuration, scheduling, dumping, encryption, validation, S3, retention, metrics, health, and API logic. |
| `cmd/sdl-db-backup-tui` / `internal/tui` | Bubble Tea operator console. |
| `cmd/sdl-db-backup-schedule` | Render or explicitly apply/verify the authoritative timer with rollback. |
| `cmd/sdl-db-backup-api` | REST API server. |
| `cmd/sdl-db-backup-health` | Read-only operator health report. |
| `cmd/sdl-db-backup-validate` | Logical artifact validation and sandbox restore testing. |

## Configuration resolution

The effective configuration is built in this order:

1. Read the requested environment file (`BACKUP_ENV_FILE` or `.env`).
2. Apply a non-expired temporary override file.
3. Apply explicit process environment variables.

Process variables take precedence over temporary overrides. Temporary
overrides only affect managed settings and are stored in
`<BACKUP_LOG_DIR>/backup-temporary-overrides.json`. They are suitable for
testing a schedule or upload choice without editing `.env`.

## Logical backup method

1. The runner acquires a process-aware lock and initializes run logging.
2. It evaluates the logical schedule and validates the requested prerequisites.
3. It discovers granted non-system databases unless an explicit database scope
   is configured. A configured table scope is validated against the live
   schema before dumping.
4. Capacity is checked before capture. `per_database` mode runs concurrent
   dumps up to `BACKUP_LOGICAL_PARALLEL`; MySQL `global` mode uses one dump
   transaction across all selected InnoDB databases, a verified backup-lock
   connection, and before/after object inventories. Both stream compression
   and enforce the configured reserve. Maintain a schema-deployment freeze.
   The dump timeout is `BACKUP_LOGICAL_TIMEOUT_PER_DB`, including the global group.
5. MySQL uses `mysqldump`; PostgreSQL uses `pg_dump`. The MySQL path includes
   schema objects such as routines, triggers, and events while avoiding
   tablespace requirements for least-privilege accounts.
6. Each artifact is gzip-compressed. With `BACKUP_ENCRYPTION_KEY`, the output
   is encrypted using a versioned, chunked AES-256-GCM stream and named
   `.sql.gz.enc`.
7. The run writes artifact and SQL-payload hashes, object results, optional
   PITR coordinates, and optional verification metadata to `manifest.json`.
8. Transient failures are retried with exponential backoff. Failures that are
   clearly non-transient (for example credentials, permissions, or missing
   binaries) are not retried.

An empty scope means all available logical databases. Any intentional object
omission or table-scoped run is represented as a partial recovery set, not a
full backup success.

## Physical backup method

Physical backup is MySQL-only. When enabled and due, XtraBackup produces an
`xbstream` stream and `xbcloud` sends it directly to S3. No local physical
backup directory is kept. The implementation adapts xtrabackup and xbcloud
parallelism to host load and lowers upload concurrency when it detects S3
throttling.

After upload, every page of the chunk inventory is listed and every chunk is
streamed for source-MD5 comparison and SHA-256 recording. Required metadata
files and exact source/remote inventory agreement gate `PHYSICAL_COMPLETED.json`.
This verifies transfer integrity; preparation and recovery remain separate. The remote location is:

```text
s3://<BACKUP_S3_BUCKET>/<BACKUP_S3_PHYSICAL_PREFIX>/<run-id>/physical.xbstream
```

## Remote-upload method

Logical uploads are independently controlled by
`BACKUP_LOGICAL_S3_UPLOAD_ENABLED`.

| Mode | Method | Recovery-grade completion marker |
| --- | --- | --- |
| `direct` | Go signs S3 requests and performs resilient multipart uploads for large files. | Yes |
| `auto` | Tries direct first, then configured legacy PHP/HTTP helpers. | Only when direct succeeds |
| `php` | Calls the configured PHP upload script. | No |
| `http` | Calls the configured HTTP upload endpoint. | No |

With `BACKUP_REQUIRE_REMOTE_COMPLETION=true` (the default), a configured
legacy-only method is rejected for recovery-grade runs. Direct uploads verify
whole uploaded payloads, exact artifact sizes and expected inventory. All
required databases and objects must pass for `COMPLETED.json`; other captures
receive `PARTIAL.json` or `FAILED.json`. Completed destinations cannot be
overwritten. Multipart ownership journals support safe abort/retry after a
crash without deleting committed objects. A marker alone, especially a legacy
marker, is not proof of recovery value.

## Validation and recovery methods

### Logical validation

`cmd/sdl-db-backup-validate --mode logical` opens the recorded artifacts,
authenticates/decrypts encrypted files when needed, validates gzip and engine
completion markers, and verifies persisted artifact and SQL hashes when
present. Committed manifests remain immutable; final execution bookkeeping
is persisted separately in `execution-finalization.json` and JSONL history.

### Sandbox restore test

`--mode restore` imports a selected logical run into a bounded temporary
database on the configured restore target. The target must never be
production. The test validates schema object counts and fingerprint; it also
checks exact row counts or sampled row hashes when those features were enabled
when the source backup was created. The existing per-database workflow refuses
shared global bundles. Production restore testing is disabled by operator
instruction; capture verification does not set restore-tested state.

### Physical validation

`main.go physical-validate <run-id>` requires a completed physical stream and
downloads, extracts, and runs `xtrabackup --prepare` in the configured work
directory. It is non-destructive to a MySQL server, but it still needs an
isolated host and sufficient temporary disk space.

### PITR metadata

MySQL logical runs can record a pre-dump server observation plus a coordinate
for each successful dump. Those coordinates are not a replacement for
independent binary-log archiving or rehearsed replay. See
[RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md) for the required operating
controls.

## Scheduling and lifecycle

Logical and physical schedules are independently evaluated from the last
successful run state:

- `always`: due on every invocation.
- `disabled`: never due.
- `daily@HH:MM[,HH:MM]`: one or more local-time clock slots.
- `weekly@day,HH:MM`: weekly local-time slot.
- `interval@24h`: due after the duration since the last successful run.

Missed daily and weekly slots are caught up at the next invocation. The systemd
timer should run at least as frequently as the smallest scheduling interval.

Automatic deletion is held by default (`BACKUP_RETENTION_DELETE_ENABLED=false`).
`retention-plan` is read-only. GFS selection considers verified complete logical
captures; minimum complete/restore-tested points, partial/failed/unknown
artifacts, physical backups, dependencies and operator pins are protected.
Explicitly enabled cleanup journals a quarantine rename under a retention
lock. It does not free disk space by purging quarantine; an operator must
review and authorize any purge separately.

Systemd scope and schedule timezone are explicit configuration. Saving settings
retains file ownership and does not change the timer. Explicit schedule apply
is serialized, verifies all effective calendars and rolls back on failure.
See [production configuration](PRODUCTION_CONFIGURATION.md).

## Records and observability

Every run produces:

| Record | Location | Contents |
| --- | --- | --- |
| Run log | `<BACKUP_LOG_DIR>/<run-id>.log` | Detailed log for one run. |
| Daily log | `<BACKUP_LOG_DIR>/YYYY-MM-DD.log` | Aggregate day log. |
| Run history | `<BACKUP_LOG_DIR>/backup-runs.jsonl` | Append-only structured run summaries. |
| Manifest | `<BACKUP_DIR>/<run-id>/manifest.json` | Artifact inventory, hashes, results, and persisted validation. |
| Commit pin | `<BACKUP_DIR>/<run-id>/manifest-commit.json` | Immutable manifest checksum after publication. |
| Finalization audit | `<BACKUP_DIR>/<run-id>/execution-finalization.json` | Execution bookkeeping after manifest commitment. |
| Schedule state | `<BACKUP_LOG_DIR>/backup-schedule-state.json` | Latest successful logical and physical schedule state. |
| Metrics | `BACKUP_METRICS_FILE` | Prometheus textfile samples, including live run status. |

See [GRAFANA_BACKUP_MONITORING.md](GRAFANA_BACKUP_MONITORING.md) for metric
definitions and alert recommendations. For live log streaming, use the sample
Promtail configuration in [loki/promtail-config.yml](loki/promtail-config.yml).
