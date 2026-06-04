# SDL DB Backup — Changes Log

## Current Architecture

The project no longer relies on one large `main.go` flow only.

- `main.go` and `main_sync_upload.go` are non-interactive entrypoints
- `internal/backupapp/app.go` contains the shared backup engine and config logic
- `internal/backupapp/httpapi.go` provides the built-in REST API
- `internal/backupapp/platform.go` provides shared runtime/profile/systemd rendering helpers
- `cmd/sdl-db-backup-tui/main.go` launches the Bubble Tea terminal UI
- `internal/tui/app.go` contains the TUI model, screens, widgets, command palette, and async event handling
- `cmd/sdl-db-backup-health/main.go` provides a health-check CLI
- `cmd/sdl-db-backup-api/main.go` runs the REST API server

Recent operator-facing changes:

- replaced the utility-style TUI with a Bubble Tea operator console for dashboard, config editing, manual runs, logs, history, health checks, and user-systemd control
- added a built-in REST API for CRM/backend integration across backup runs, config, schedules, logs, storage, health, runtime, and user-level systemd actions
- added portable config and runtime metadata so runs record OS user, execution source, hostname, and pid
- added configurable user unit names plus rendered unit previews for host-specific systemd installation
- removed repo-shipped assumptions about one fixed working directory, home directory, or PHP upload host path from the main portable setup flow
- added command palette, searchable config editing, unsaved-change handling, toasts, and live run output
- added a Schedule page for backup timing, upload toggles, retention days, and permanent or temporary schedule changes
- added temporary overrides stored at `<BACKUP_LOG_DIR>/backup-temporary-overrides.json`, which are honored by scheduled service runs until expiry without rewriting `.env`
- added logical backup scope controls for selected databases and selected tables, defaulting to all granted databases and all tables
- improved the manual backup scope controls so `Esc` returns from table selection and `c`/`n`/right arrow visibly continue to preview
- added bulk and range selection for TUI backup scope, plus a multi-column table picker for large databases
- added table search in the TUI backup scope picker, with filtered bulk selection and extra back keys from table selection
- added permanent save from the TUI scope picker so selected databases/tables can be written to `.env` for scheduled logical backups
- added global TUI notifications so commands, confirmations, saves, errors, and state changes are visible on every page
- improved TUI layout sizing so content, sidebars, notifications, command palette, and confirmations share terminal height without clipping
- added a CLI health report command
- added an API server command
- added daily aggregate logs alongside per-run logs
- added `backup-runs.jsonl` as a structured run index
- changed logical dump generation so dump files do not start with `USE db_name;`
- added `--no-tablespaces` to logical dumps for least-privilege MySQL backup users

See `README.md` for the current commands, TUI controls, logging layout, and configuration flow.

## Overview

The original `main.go` performs MySQL dumps to `/mnt/volume_1/backup/mysql_backup/`.
All changes below were made in `main_test.go` (working copy) and synced to `main_sync_upload.go` for execution, leaving `main.go` untouched.

---

## 1. S3 Upload After Backup (main_test.go)

**Goal:** After all MySQL dumps complete, synchronously upload the backup folder to S3 bucket `ticklerightbackups` under prefix `db_backup/`.

### Config additions (`config` struct + `loadConfig()`)

| Env var | Default | Purpose |
|---|---|---|
| `BACKUP_S3_UPLOAD_SCRIPT` | _(empty)_ | Absolute path to `s3_upload_backup.php`. When set, CLI upload is used (**preferred**). |
| `BACKUP_S3_PHP_BIN` | `/usr/bin/php` | PHP binary for CLI upload. |
| `BACKUP_S3_UPLOAD_URL` | _(empty)_ | HTTP endpoint fallback (`s3Route.php`). Only used if `BACKUP_S3_UPLOAD_SCRIPT` is not set. |
| `BACKUP_S3_UPLOAD_TIMEOUT` | `2h` | Context timeout for the upload step. |

### New functions

**`uploadBackupToS3(cfg, runFolder)`**
Dispatcher. Prefers CLI upload if `S3UploadScript` is set; falls back to HTTP; logs a skip message if neither is configured.

**`uploadBackupViaCLI(cfg, runFolder)`**
Runs `php s3_upload_backup.php <runFolder>` as a subprocess with a context timeout.
- Streams PHP's STDERR (progress lines) directly to the Go logger in real time.
- Parses the JSON summary from PHP's STDOUT and logs the result.
- Bypasses nginx/PHP-FPM — **no web-server connection timeout**.

**`uploadBackupViaHTTP(cfg, runFolder)`**
Posts `{"action":"uploadBackupFolder","backupPath":"<runFolder>"}` to `BACKUP_S3_UPLOAD_URL`.
Falls back from the original EOF-prone approach; kept as a secondary option.

**`logS3Result(result)`**
Shared helper that logs success/partial/failure from either upload path.

### `run()` return type change

`run()` now returns `(int, string)` — exit code **and** the run folder path — so `main()` can pass the folder to the upload step without re-reading config or state.

### `main()` flow

```
1. run()  →  MySQL dumps complete, returns (exitCode, runFolder)
2. uploadBackupToS3()  →  synchronous S3 upload
3. os.Exit(exitCode)
```
The upload runs regardless of whether the backup was `success` or `partial`, so even a partially completed backup set is pushed to S3.

---

## 2. MySQL max_execution_time Fix (main_test.go)

**Problem:** The MySQL server had `@@GLOBAL.max_execution_time = 30000` (30 seconds). Every `SELECT` during `mysqldump` of large tables (`notify_status`, `email_ready`) was killed with Error 3024, causing all retries to fail.

**Fix: `tryDisableMaxExecutionTime(cfg)`**

Called once in `run()` immediately after `validatePrerequisites()` and before any dump starts.

1. Reads the current `@@GLOBAL.max_execution_time`.
2. If it is already `0`, returns a no-op.
3. Otherwise runs `SET GLOBAL max_execution_time=0` (requires `SYSTEM_VARIABLES_ADMIN` privilege — the `developer` user has this).
4. Returns a **restore function** that is `defer`-ed; it sets the value back to the original after all dumps finish.

If the user lacks the privilege, a warning is logged and the backup continues (dumps may still fail on large tables).

---

## 3. New PHP CLI Script (s3_upload_backup.php)

**File:** `/var/www/developer.tickleright.in/routes/s3_upload_backup.php`

**Purpose:** A CLI-only PHP script called by the Go backup tool as a subprocess. Replaces the HTTP call to `s3Route.php?action=uploadBackupFolder` which was killed by nginx/PHP-FPM timeout on large uploads.

**Key behaviours:**

- Enforces `php_sapi_name() === 'cli'` — refuses web requests.
- Path traversal check: `backupPath` must be under `/mnt/volume_1/backup/mysql_backup`.
- Loads AWS credentials from `/home/developer/safe/keys/aws-backup-config.php` (separate from the main app credentials).
- Uploads each file with `MultipartUploader` (10 MB parts).
- Writes per-file progress to **STDERR** (Go streams this to the log in real time).
- Writes a JSON summary to **STDOUT** (Go parses this for the final log line).
- Exits `0` on full success, `1` on any failure.

**AWS config used:** `/home/developer/safe/keys/aws-backup-config.php`
```
bucket: ticklerightbackups
folder: db_backup
region: ap-south-1
```

---

## 4. s3Route.php — uploadBackupFolder updates

**File:** `/var/www/developer.tickleright.in/routes/s3Route.php`

Changes to the existing `uploadBackupFolder` action:

- Now loads `/home/developer/safe/keys/aws-backup-config.php` and creates a dedicated `$backupS3Client` with backup-specific credentials (previously used the app's S3 credentials and a hardcoded bucket name `ticklerightbackups`).
- Bucket and prefix (`db_backup/`) now come from `$backupConfig` instead of being hardcoded.
- **Bug fix:** `$e->getState()->getUploadId()` does not exist in the AWS SDK v3. Replaced with `$e->getState()->getId()['UploadId']`.

---

## 5. How to run

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup

# go run cannot run *_test.go files directly — use the synced copy:
cp -f main_test.go main_sync_upload.go
go run main_sync_upload.go
```

**Or build a binary:**
```bash
go build -o sdl_db_backup_sync main_sync_upload.go
./sdl_db_backup_sync
```

Logs are written to `/mnt/volume_1/backup/mysql_backup/logs/<run-id>.log` and also to stdout.
A `manifest.json` is written inside each backup folder on completion.
A JSONL run log is appended to `/mnt/volume_1/backup/mysql_backup/logs/backup-runs.jsonl`.

---

## 6. Files changed / created

| File | Status | Notes |
|---|---|---|
| `main_test.go` | Modified | Working copy with all changes |
| `main_sync_upload.go` | Created (copy) | Runnable copy (`go run` rejects `*_test.go`) |
| `.env` | Modified | Added `BACKUP_S3_*` variables |
| `.env.example` | Modified | Same new variables with empty defaults |
| `main.go` | **Untouched** | Original backup binary — not modified |
| `routes/s3Route.php` | Modified | `uploadBackupFolder` uses backup-specific S3 config; `getUploadId()` bug fixed |
| `routes/s3_upload_backup.php` | **Created** | New CLI-only PHP S3 upload script |
