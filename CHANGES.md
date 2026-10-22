# SDL DB Backup Change Log

## October 12, 2026 — production backup reliability

- Capture all required MySQL databases in one transaction when `global` mode
  is selected; require InnoDB, a backup lock and complete object inventories.
- Introduce version 2 run states and fail-closed artifact verification. A capture
  missing any required database cannot publish `COMPLETED.json`.
- Verify every S3 payload and paginated physical chunk inventory; use bounded
  transient retries and persist multipart ownership journals.
- Freeze committed manifests and preserve later execution bookkeeping separately.
- Add capacity preflight, a streaming safety reserve and labelled failed artifacts.
- Hold deletion by default; protect complete coverage and journal quarantine.
- Target the authoritative systemd manager, show desired/effective schedule drift,
  and explicitly apply calendars with validation and rollback.
- Preserve settings-file ownership during atomic replacement and expose live
  capacity metrics through the existing monitoring integration.
- Views with a missing definer must be repaired before global capture.
  Capture integrity is not a claim of recovery readiness.
- Reference production settings: [PRODUCTION_CONFIGURATION.md](PRODUCTION_CONFIGURATION.md).

## Earlier implementation

This repository is no longer a single-script MySQL dump utility. The current
implementation uses a shared Go backup engine with non-interactive, terminal
UI, REST API, health-check, and validation entrypoints.

### Backup and recovery reliability

- Logical artifacts support authenticated, versioned AES-256-GCM streaming
  encryption. Keys must be exactly 32 raw bytes or 64 hexadecimal characters;
  legacy encrypted artifacts remain readable for migration.
- Logical validation, inspection, decryption, and sandbox restore testing
  understand encrypted `.sql.gz.enc` artifacts.
- Logical run manifests record artifact SHA-256 and, when available, the
  decompressed SQL payload SHA-256.
- Direct S3 uploads verify remote objects and commit `COMPLETED.json` last.
  Physical streams verify their object and commit `PHYSICAL_COMPLETED.json`
  last.
- Large direct S3 uploads use multipart transfer with transient-failure retry
  and abort incomplete multipart uploads on failure.
- Full recovery sets fail closed when objects would be omitted. Explicitly
  table-scoped or omission-based work is recorded as partial.
- Local GFS cleanup protects the configured number of recent
  sandbox-restore-verified runs.
- Manifests, run history, schedule state, and temporary overrides use atomic
  writes to reduce interrupted-write corruption.

### Database support and validation

- Logical backup and validation support both MySQL and PostgreSQL.
- MySQL logical backups can capture optional PITR coordinates and server
  observations. Binary-log retention and replay remain external operational
  controls.
- Sandbox restore verification checks schema object counts and fingerprints;
  exact row totals and sampled row-content hashes can be enabled for stronger
  verification.
- Physical MySQL validation uses a non-destructive download, extraction, and
  `xtrabackup --prepare` drill.

### Operations and interfaces

- Logical and physical schedules are independent, persist state, and catch up
  missed fixed daily/weekly slots.
- The runner protects against overlapping runs, writes daily plus per-run logs,
  stores JSONL history, and emits Prometheus textfile metrics throughout a run.
- Adaptive tuning reduces logical, xtrabackup, and xbcloud parallelism under
  host load; physical upload retries lower concurrency when S3 throttles.
- The Bubble Tea TUI supports manual runs, scope selection, config editing,
  temporary overrides, health, metrics, log/history inspection, and
  user-systemd control.
- The REST API exposes configuration, effective configuration, runs, health,
  schedules, systemd state/actions, logs, storage, runtime data, and restore
  validation.
- Remote upload, physical backup, and scheduled root execution are opt-in by
  default.

## Compatibility notes

- The supported runner is `main.go`; `main_sync_upload.go` is not part of the
  current repository and should not be used in commands or automation.
- Physical backup remains MySQL-only and is direct-to-S3 only.
- PHP and HTTP logical upload helpers remain available for compatibility, but
  they do not provide the direct-upload completion protocol. Keep
  `BACKUP_REQUIRE_REMOTE_COMPLETION=true` for recovery-grade backups.
- Successful backup execution alone is not a recovery guarantee. Follow
  [RECOVERY_OPERATIONS.md](RECOVERY_OPERATIONS.md) for recurring drills, PITR
  binlog archival, S3 durability controls, and release gates.
