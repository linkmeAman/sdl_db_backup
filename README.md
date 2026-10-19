# SDL DB Backup

SDL DB Backup is a Go-based backup service for MySQL and PostgreSQL. It creates
logical backups, can stream MySQL physical backups to S3, and provides a
terminal UI, REST API, health checks, validation tools, and Prometheus metrics.

## Contents

- [What it does](#what-it-does)
- [Quick start](#quick-start)
- [How backups are made](#how-backups-are-made)
- [Configuration](#configuration)
- [Production configuration](PRODUCTION_CONFIGURATION.md)
- [Run, validate, and restore](#run-validate-and-restore)
- [Scheduling and operations](#scheduling-and-operations)
- [Interfaces and observability](#interfaces-and-observability)
- [Documentation map](#documentation-map)

## What it does

### Logical backups

- Supports `DB_ENGINE=mysql` and `DB_ENGINE=postgres`.
- Discovers non-system databases available to the backup user, or limits work
  to `BACKUP_LOGICAL_DATABASES` and `BACKUP_LOGICAL_TABLES`.
- Supports `per_database` captures and MySQL `global` captures. Global mode
  writes one `all-databases.sql.gz` artifact using one transaction across the
  selected InnoDB databases, with a backup lock and object-inventory checks.
  Encryption uses authenticated, versioned AES-256-GCM (`.sql.gz.enc`).
- Records an artifact SHA-256 and, when available, the decompressed SQL
  payload SHA-256 in the run manifest.
- Retries transient dump failures with exponential backoff and writes a
  structured result for every database.
- Supports optional MySQL PITR metadata, exact row-count checks, and sampled
  row-content hashes for deeper restore verification.

### Physical backups

- Supports MySQL physical backups through Percona XtraBackup and streams the
  result directly to S3 with `xbcloud`; physical backups are not table scoped.
- Paginates the entire remote inventory and verifies every physical chunk
  against xbcloud source checksums before publishing `PHYSICAL_COMPLETED.json`.
  Upload integrity does not establish physical preparation or recoverability.
- Includes a prepare-only validation command that downloads, extracts, and
  prepares a completed stream without changing a MySQL server.

### Reliability controls

- Uses a persistent file lock and legacy-PID checks to prevent overlapping runs.
- Checks estimated storage and a safety reserve before capture and enforces the
  reserve while streaming; failed captures retain labelled incomplete artifacts.
- Evaluates logical and physical schedules independently and persists state in
  `backup-schedule-state.json`.
- Holds automatic deletion by default. Retention planning applies GFS to
  verified complete captures, protects minimum complete and restore-tested
  coverage, and keeps partial/unknown/dependent points. Explicitly enabled
  cleanup quarantines candidates with a durable journal; it does not purge them.
- Writes manifests, schedule state, temporary overrides, and run history
  atomically.
- Uses direct S3 uploads by default. Direct uploads verify objects and write
  `COMPLETED.json` last only when every required database/object succeeds.
  Failed/partial runs receive separate markers. Validate manifests and hashes;
  historical marker presence alone is insufficient proof of completeness.
- Emits Prometheus textfile metrics and keeps per-run, daily, and JSONL history
  logs.

## Quick start

### Prerequisites

- Go 1.24.2 or newer to build from source.
- For MySQL logical backups: `mysql` and `mysqldump`. With the default
  `BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE=true`, the dump client must support
  `--init-command` (the deployed MySQL 8.4.11 client does). The override sets only
  its session timeout; it never changes the production global timeout.
- For PostgreSQL logical backups: `pg_dump`; `psql` is also required for
  sandbox restore testing.
- For MySQL physical backups and physical validation: `xtrabackup`, `xbcloud`,
  and `xbstream`.

Install the MySQL client on Debian/Ubuntu with:

```bash
sudo apt update
sudo apt install -y mysql-client
```

Or bootstrap the local Go toolchain and check available host tools:

```bash
./scripts/install-tools.sh
```

### Configure a local run

```bash
cp .env.example .env
```

At minimum, set `DB_USER` and `DB_PASS`. Set the logical schedule to `always`
for a one-off test, and leave remote upload disabled until S3 settings are
ready:

```env
DB_ENGINE=mysql
DB_HOST=127.0.0.1
DB_PORT=3306
DB_USER=backup_logical
DB_PASS=replace-me
BACKUP_LOGICAL_SCHEDULE=always
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false
BACKUP_PHYSICAL_ENABLED=false
```

Run a preflight first. It verifies connectivity, paths, metrics output, and
configured upload prerequisites without creating artifacts:

```bash
go run ./cmd/sdl-db-backup-tui
```

Choose **Backup**, enable **Preflight Only**, and run it. To perform a
non-interactive backup afterward:

```bash
go run ./main.go
```

### Install as a service

The installer builds the runner, TUI, health-check, and API binaries; creates
an environment file; and installs a systemd timer. A non-root invocation uses
user-scoped locations (`~/.local/bin`, `~/.config/sdl-db-backup`, and
`~/.config/systemd/user`). Running the installer as root is an explicit
system-wide installation and enables root-runner support in that installation's
configuration.

```bash
./install.sh
```

After installation, update the generated `.env` before enabling remote or
physical backups. Launch the TUI with `sdl-db-backup-tui` to edit managed
settings safely.

## How backups are made

The runner uses the following sequence for each invocation:

1. Load `.env`, then apply active temporary overrides, then explicit process
   environment variables. Explicit process variables have highest precedence.
2. Acquire the run lock, create the per-run and daily log streams, and refresh
   in-progress metrics.
3. Evaluate the logical and physical schedules separately. `always` runs on
   every invocation; `disabled` skips; `daily`, `weekly`, and `interval` are
   evaluated from persisted successful-run state.
4. Run preflight checks for the selected work: credentials, client binaries,
   database connectivity, writable backup/log/metrics paths, and upload
   configuration.
5. Check available capacity, inodes and the safety reserve. Capture one shared
   MySQL transaction in `global` mode, or separate concurrent dumps up to
   `BACKUP_LOGICAL_PARALLEL` in `per_database` mode. Verify gzip, hashes, dump
   completion and expected schema objects before accepting capture success.
6. If enabled and due, stream the MySQL physical backup directly to S3.
7. Upload logical artifacts when enabled. Direct uploads use multipart transfer
   for large objects, retry transient failures, verify objects with S3, and
   commit `COMPLETED.json` last.
8. Hold deletion unless explicitly enabled; otherwise journal retention
   quarantine after protecting recovery coverage. Freeze committed manifests,
   store final execution bookkeeping separately, and publish final metrics.

The complete operational explanation and artifact layout are in
[WORKING.md](WORKING.md). The current implementation changes are tracked in
[CHANGES.md](CHANGES.md).

## Configuration

Start from [.env.example](.env.example). Secrets must stay out of source
control. The groups below cover the settings operators normally change.

### Database and storage

| Setting | Purpose |
| --- | --- |
| `DB_ENGINE` | `mysql` (default) or `postgres`. Physical backup is MySQL-only. |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASS` | Logical database connection. PostgreSQL defaults to port `5432`; MySQL defaults to `3306`. |
| `BACKUP_DIR`, `BACKUP_LOG_DIR` | Local artifact and log locations. |
| `BACKUP_ENCRYPTION_KEY` | Optional exact 32-byte raw key or 64-character hexadecimal key for AES-256-GCM encryption. Store it separately from backups. |
| `BACKUP_STORAGE_RESERVE_BYTES` | Free-space reserve; default 2 GiB. |
| `BACKUP_STORAGE_ESTIMATE_BYTES` | Minimum compressed capture estimate; doubled for growth and compared with historical complete sizes. `0` uses history with a 1 GiB floor. |
| `BACKUP_ALLOW_ROOT_RUNNER` | Allows a scheduled runner to run as root. Defaults to `false`. |

### Logical backups

| Setting | Purpose |
| --- | --- |
| `BACKUP_LOGICAL_CONSISTENCY` | `per_database` (compatible default) or MySQL `global`; global mode requires InnoDB, no table scope, and existing `BACKUP_ADMIN` authorization. Maintain a schema-deployment freeze during capture. |
| `BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE` | Initializes the dump session with `max_execution_time=0`; requires a compatible client. Defaults to `true`. |
| `BACKUP_LOGICAL_ENABLED` | Enables logical work. |
| `BACKUP_LOGICAL_SCHEDULE` | `always`, `disabled`, `daily@HH:MM[,HH:MM]`, `weekly@day,HH:MM`, or `interval@24h`. |
| `BACKUP_LOGICAL_DATABASES` | Comma-separated database allow-list. Empty means all granted non-system databases. |
| `BACKUP_LOGICAL_TABLES` | Optional table scope, for example `db1:users,orders;db2:audit_log`. |
| `BACKUP_LOGICAL_TIMEOUT_PER_DB` | Per-database dump timeout. |
| `BACKUP_LOGICAL_PARALLEL` | Maximum concurrent database dumps. Start at `1`–`2`. |
| `BACKUP_LOGICAL_GZIP_LEVEL` | Compression level: `1` fastest, `9` smallest, `0` uncompressed stream. |
| `BACKUP_ALLOW_OBJECT_OMISSIONS` | Allows intentional omissions. Defaults to `false`; scoped/omission-based output is recorded as partial rather than full success. |
| `BACKUP_MYSQL_PITR_METADATA_ENABLED` | Adds MySQL dump coordinates and server observation metadata. Requires independently retained binlogs. |
| `BACKUP_EXACT_ROW_COUNTS` | Captures exact source row counts for stronger but slower sandbox verification. |
| `BACKUP_SAMPLE_DATA_CHECKS` | Captures deterministic hashes over a bounded primary-key row sample. |

### S3 uploads

| Setting | Purpose |
| --- | --- |
| `BACKUP_LOGICAL_S3_UPLOAD_ENABLED` | Enables logical remote upload. Defaults to `false`. |
| `BACKUP_S3_UPLOAD_MODE` | `direct`, `auto`, `php`, or `http`. Use `direct` for recovery-grade uploads. |
| `BACKUP_REQUIRE_REMOTE_COMPLETION` | Requires a direct remote completion marker. Defaults to `true`; legacy PHP/HTTP modes are not recovery-grade with this enabled. |
| `BACKUP_S3_ENDPOINT` | Optional direct S3-compatible HTTP(S) endpoint; uses path-style bucket addressing. Empty uses AWS. |
| `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION` | Destination bucket and region. |
| `BACKUP_S3_KEY_ID`, `BACKUP_S3_KEY_SECRET` | S3 credentials (the standard AWS variable aliases are also accepted). |
| `BACKUP_S3_LOGICAL_PREFIX` | Prefix for logical run folders. |
| `BACKUP_S3_UPLOAD_SCRIPT`, `BACKUP_S3_UPLOAD_URL` | Required only by `php` or `http` upload modes. |

Logical remote recovery requires:

```text
s3://<bucket>/<logical-prefix>/<run-id>/COMPLETED.json
```

### Physical backups and retention

| Setting | Purpose |
| --- | --- |
| `BACKUP_PHYSICAL_ENABLED` | Enables MySQL physical backup. Defaults to `false`. |
| `BACKUP_PHYSICAL_SCHEDULE` | Independent physical schedule using the same format as logical scheduling. |
| `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED` | Enables physical S3 streaming. A disabled upload skips physical work. |
| `BACKUP_XTRABACKUP_*`, `BACKUP_XBCLOUD_*` | XtraBackup client, credentials, socket, working directory, and parallelism controls. |
| `BACKUP_RETENTION_DELETE_ENABLED` | Defaults to `false`; explicit enablement permits journaled quarantine, never automatic purge. |
| `BACKUP_RETENTION_MIN_COMPLETE` | Minimum verified complete captures protected; default 2, minimum 1. |
| `BACKUP_RETENTION_DAILY`, `BACKUP_RETENTION_WEEKLY`, `BACKUP_RETENTION_MONTHLY` | GFS local retention counts. |
| `BACKUP_RETENTION_MIN_VERIFIED` | Number of newest sandbox-restore-verified local runs protected from cleanup. |

## Run, validate, and restore

All commands use `.env` by default. Point to another file with
`BACKUP_ENV_FILE=/path/to/.env`.

### Commands

```bash
# Run the scheduled non-interactive runner once.
go run ./main.go

# Inspect a recorded run and show restore-oriented details.
go run ./main.go inspect <run-id>

# Decrypt an encrypted logical artifact to stdout.
go run ./main.go decrypt <artifact.sql.gz.enc> > artifact.sql.gz

# Validate a logical run; omit --run-id to use the newest recorded run.
go run ./cmd/sdl-db-backup-validate --mode logical --run-id <run-id>

# Run the destructive sandbox restore test. Use only an isolated restore host.
go run ./cmd/sdl-db-backup-validate --mode restore --run-id <run-id>

# Download, extract, and prepare a completed physical stream on a verification host.
go run ./main.go physical-validate <run-id>

# Show health and prerequisite information.
go run ./cmd/sdl-db-backup-health

# Launch the TUI or REST API.
go run ./cmd/sdl-db-backup-tui
go run ./cmd/sdl-db-backup-api
```

`RESTORE_TEST_ENABLED=true` is required for sandbox restore testing. Its
`RESTORE_TEST_*` target must be isolated: the test creates and removes bounded
temporary databases. Never point it at production.

For physical and PITR recovery procedures, prerequisites, and release gates,
follow [RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md).

### Output layout

```text
<BACKUP_DIR>/
  <run-id>/
    <database>.sql.gz[.enc]       # per_database
    all-databases.sql.gz[.enc]   # global; one shared artifact
    manifest.json
    manifest-commit.json        # checksum pin after remote publication
    execution-finalization.json # later bookkeeping, when committed

<BACKUP_LOG_DIR>/
  <run-id>.log
  YYYY-MM-DD.log
  backup-runs.jsonl
  backup-schedule-state.json
  backup-temporary-overrides.json
```

Version 2 manifests retain legacy fields and add `RUNNING`, `FAILED`, `PARTIAL`
and `COMPLETE` recovery states, expected objects and separate restore evidence.
A committed manifest is immutable; later execution details go into the JSONL
index and finalization audit. Capture verification never implies a restore test.
The existing per-database restore command rejects shared global bundles.

Preview retention without changing artifacts:

```bash
go run ./main.go retention-plan
```

## Scheduling and operations

The application evaluates logical and physical schedules independently. Set
`BACKUP_SYSTEMD_SCOPE=system` for system-level production units, `user` for a
user installation, or `auto` to select installed system units when present.
`BACKUP_SCHEDULE_TIMEZONE` makes timezone interpretation explicit. The TUI
shows the effective timer, desired schedule, drift, configuration source,
next/last triggers and service result; authorization failures are reported
without switching service managers.

For the current system deployment, inspect without `--user`:

```bash
systemctl status sdl-db-backup.timer
journalctl -u sdl-db-backup.service -n 100 --no-pager
# Preview only:
go run ./cmd/sdl-db-backup-schedule --env .env
# Explicit operator-authorized apply, using a built helper:
go build -o /tmp/sdl-db-backup-schedule ./cmd/sdl-db-backup-schedule
sudo /tmp/sdl-db-backup-schedule --env /absolute/path/to/.env --apply
```

Saving `.env` does not automatically change the live timer. **Apply saved
schedule** installs a managed drop-in, validates calendar syntax, reloads,
restarts an active timer and verifies effective calendars. Failure rolls back
configuration and the prior drop-in. Persistent timers can trigger a catch-up
run. For user installations, use `systemctl --user` and `journalctl --user`.

The templates [sdl-db-backup.service](sdl-db-backup.service) and
[sdl-db-backup.timer](sdl-db-backup.timer) still require placeholder rendering.
Current production values and verified evidence are documented in
[PRODUCTION_CONFIGURATION.md](PRODUCTION_CONFIGURATION.md).

Use temporary overrides for bounded testing without rewriting `.env`. The TUI
Schedule page or `PUT /api/v1/schedules/temporary-overrides` stores them in
`<BACKUP_LOG_DIR>/backup-temporary-overrides.json`; they expire automatically.

### MySQL least privilege

Use a dedicated logical backup account. For each database it needs, grant
`SELECT`, `SHOW VIEW`, `TRIGGER`, and `EVENT`, plus global `SHOW_ROUTINE` when
routine dumps are required. PITR metadata and physical backups need additional
privileges; use the exact requirements and review process in
[RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md). Do not reuse an application
or administrative account.

## Interfaces and observability

### Terminal UI

The Bubble Tea TUI manages manual runs, scoped logical backups, permanent and
temporary schedules, `.env` edits, logs, run history, health, metrics, and
system- or user-scoped systemd actions. Its full keyboard and page reference is in
[TUI_REFERENCE.md](TUI_REFERENCE.md).

### REST API

Set `BACKUP_API_ENABLED=true` and run `go run ./cmd/sdl-db-backup-api`. The API
listens on `127.0.0.1:8086` and serves `/api/v1` by default. Enable bearer
authentication with `BACKUP_API_AUTH_ENABLED=true` and
`BACKUP_API_BEARER_TOKEN`.

The API exposes health, runs, configuration, temporary schedules, systemd,
logs, storage, runtime data, and logical restore validation/testing. The full
machine-readable contract is [openapi.json](openapi.json).

### Prometheus, Grafana, and Loki

The runner writes Node Exporter textfile metrics to `BACKUP_METRICS_FILE`
(default: `/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom`).
Metrics represent current run state, outcomes, freshness, size, duration,
cleanup, validation, adaptive parallelism, and physical retry state.

- [GRAFANA_BACKUP_MONITORING.md](GRAFANA_BACKUP_MONITORING.md): metric catalog,
  PromQL, dashboard layout, and alert guidance.
- [observability/README.md](observability/README.md): import order for the
  dashboard and alert bundle.
- [prometheus/README.md](prometheus/README.md): scrape and alert-rule setup.
- [loki/README.md](loki/README.md): ship per-run logs to Loki.

## Documentation map

| Document | Use it for |
| --- | --- |
| [WORKING.md](WORKING.md) | Architecture, execution flow, methods, and files produced by a run. |
| [CHANGES.md](CHANGES.md) | Current implementation changes and compatibility notes. |
| [RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md) | Physical restore drills, PITR, S3 durability controls, and release gates. |
| [TUI_REFERENCE.md](TUI_REFERENCE.md) | TUI pages, workflow, and keyboard controls. |
| [GRAFANA_BACKUP_MONITORING.md](GRAFANA_BACKUP_MONITORING.md) | Metrics, dashboards, PromQL, and alerts. |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Development setup and test expectations. |
| [SECURITY.md](SECURITY.md) | Vulnerability reporting policy. |

## Integration tests

Run `./tests/integration/run.sh` with Docker and Compose v2 to test MySQL,
PostgreSQL, encryption, MinIO completion markers, and isolated restores. See the
[integration test guide](tests/integration/README.md) for coverage and diagnostics.

## Automatic local storage recovery

`BACKUP_STORAGE_AUTO_CLEANUP=true` enables authorized pre-capture deletion of
obsolete local payloads. It is independent of legacy GFS quarantine and never
removes S3 objects. Default is false. Preview against the actual configuration:

```bash
BACKUP_ENV_FILE=.env ./sdl-db-backup storage-plan
```

Before deleting, the manager independently reads and verifies two complete S3
recovery points and the candidate's counterpart, including every listing page,
SHA-256, gzip/decryption, SQL hash, footer and expected schema inventory.
Successful integrity checks do not establish restore success. The newest valid
complete local point always remains; the next newest is preserved when capacity
permits. Pinned/restore-tested points, physical components, binlog dependencies,
unknown legacy artifacts and uniquely useful partials remain protected.

Pressure begins at the configured high watermark or insufficient projected
capture capacity. Cleanup stops at the larger of capture requirements and the
low-watermark target. Order: expired safely disposable failed temporary files,
exact duplicate partial payloads, oldest eligible complete logical payloads.
Capacity is measured again after every unlink. Manifests, keys and configuration
remain. A shared run lock and retention lock prevent overlapping operations;
atomic per-file INTENT/outcome journals make interrupted cleanup recoverable.

The estimate uses the maximum recent complete compressed capture plus growth,
concurrent-write and temporary-space allowances, with a separate safety reserve
and inode threshold. Active dump writes recheck the reserve every 16 MiB.
Unexpected disk exhaustion fails honestly and preserves incomplete artifacts.
With no approved usable secondary destination, insufficient safe capacity emits
CRITICAL and preserves protected recovery points. Logical remote-only streaming
is not enabled: capture/object validation currently depends on staged files.
Physical capture retains its existing streaming workflow and its work directory
receives a separate capacity gate; no physical component is automatically purged.

The manifest, health CLI, TUI and metrics expose capacity, decisions, reclaimed
space, protected points and the forecast without further cleanup. Deletion audits
are under `BACKUP_LOG_DIR/storage-deletions/`; latest health is in
`BACKUP_LOG_DIR/storage-status.json`. See
[production storage evidence and rollback](remediation/STORAGE_RECOVERY.md).

### Limit local backups by count

With `BACKUP_STORAGE_AUTO_CLEANUP=true`, set `BACKUP_LOCAL_MAX_BACKUPS=4` to
keep at most four local logical dump runs. The default `0` disables the count
limit; a configured limit must be at least three. Cleanup reserves a slot
before capture and deletes older payloads only after independent local/offsite
verification. Metadata and S3 history remain. Protected or unverified artifacts
can block cleanup and a new capture when the limit cannot be met safely.

Preview with `./sdl-db-backup storage-plan`; enforce the configured policy
without starting a capture with `./sdl-db-backup storage-cleanup`.

Production backup logs, history, schedule state, shared lock and deletion audits
are consolidated at `/srv/backup/logs/sdl-db-backup`; historical
one-off and smoke-test logs are under its private `diagnostics/` subdirectory.
See [production configuration](PRODUCTION_CONFIGURATION.md) for compatibility
links and verification evidence.
