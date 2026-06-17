# SDL DB Backup

Creates MySQL backups with separate logical and physical backup controls.

## Overview

This project is a portable backup application for MySQL with three main operator surfaces:

- `main.go` and `main_sync_upload.go` are thin non-interactive runners
- `cmd/sdl-db-backup-tui/main.go` provides the terminal UI for interactive management
- `cmd/sdl-db-backup-api/main.go` provides the REST API for CRM/backend integration

Core behavior:
- Supports both `mysql` and `postgres` database engines.
- Logical backups and physical backups can be enabled, scheduled, timed out, and uploaded independently through `.env`.
- Logical backups create `.sql.gz` files per database (with AES-256-CFB `.enc` encryption if configured) and can upload that run folder to S3 via direct, PHP, or HTTP upload modes.
- Physical backups stream `xtrabackup` directly to S3 as `physical.xbstream` and do not create a local physical backup directory.
- Retries each logical database dump up to the configured retry count.
- Deletes backup folders using a Grandfather-Father-Son (GFS) rotation strategy to keep daily, weekly, and monthly snapshots.
- Writes both a per-run log (`<run_id>.log`) and a daily aggregate log (`YYYY-MM-DD.log`) in `BACKUP_LOG_DIR`.
- Records structured run history in `backup-runs.jsonl`.

If you need implementation history or change tracking, see `CHANGES.md`.
For Prometheus/Grafana monitoring details, see [GRAFANA_BACKUP_MONITORING.md](/var/www/go-workspace/sdl/sdl_db_backup/GRAFANA_BACKUP_MONITORING.md).

## Files

- `main.go`: non-interactive backup runner entrypoint
- `main_sync_upload.go`: alternate non-interactive entrypoint for manual `go run`
- `internal/backupapp/app.go`: shared backup engine, config handling, and systemd helpers
- `internal/backupapp/httpapi.go`: built-in REST API routes
- `internal/backupapp/platform.go`: runtime profile, rendered systemd units, and shared read helpers
- `cmd/sdl-db-backup-tui/main.go`: Bubble Tea terminal UI launcher
- `internal/tui/app.go`: TUI orchestration, state, and input routing
- `internal/tui/views.go`: page rendering, panels, and viewport-backed content views
- `internal/tui/input.go`: keyboard handling and focus routing
- `internal/tui/cmds.go`: async command helpers and loaders
- `internal/tui/helpers.go`: backup/schedule/systemd helpers and shared TUI utilities
- `cmd/sdl-db-backup-health/main.go`: CLI health check for latest run, daily log path, and prerequisites
- `cmd/sdl-db-backup-api/main.go`: REST API server
- `mysql_full_backup.sh`: bash backup script
- `.env.example`: environment template
- `sdl-db-backup.service`: portable unit template
- `sdl-db-backup.timer`: portable timer template
- `openapi.json`: machine-readable API route inventory

## Setup (No Root)

The easiest way to install and configure SDL DB Backup is by using the automated one-command installer. 

This installer will automatically verify dependencies, compile the Go binaries, install them to your local `~/.local/bin` path, set up your configuration folder at `~/.config/sdl-db-backup`, generate a secure encryption key, and configure the background systemd service.

```bash
git clone https://github.com/your-repo/sdl-db-backup.git
cd sdl-db-backup
./install.sh
```

**After Installation**: The installer will alert you to manually configure your sensitive credentials. Run the TUI to edit them seamlessly:
```bash
sdl-db-backup-tui
```
Then press `4` to enter the Config Editor and update `DB_USER`, `DB_PASS`, and S3 secrets.

### Manual Setup

If you prefer to set up the system manually without the automated installer:

1. Install required host tools:
   - `./scripts/install-tools.sh`
   - Optional: `./scripts/install-shortcuts.sh`

2. Create environment file:
   - `cp .env.example .env`
   - Set database credentials, logical/physical schedules, and S3 settings

3. For Go testing (logs progress in terminal):
   - `./scripts/run-main`
   - TUI: `./scripts/tui`
   - Health: `go run ./cmd/sdl-db-backup-health`
   - API: `go run ./cmd/sdl-db-backup-api`

4. Ensure `go` is available to the service user:
   - `which go`
   - If you are using the repo-local Go install, run `source ./scripts/env.sh` before invoking any `go run` command directly.
   - If you installed the shortcuts, you can run `sdl-db-backup-tui` and `sdl-db-backup-run` from anywhere after `~/.local/bin` is on your `PATH`.

5. Render or copy user units:
   - `mkdir -p ~/.config/systemd/user`
   - Use the TUI `Systemd` page or API `GET /api/v1/systemd` response to render host-specific unit content
   - If copying the template files directly, replace `{{WORKING_DIRECTORY}}`, `{{ENV_FILE}}`, and `{{SERVICE_UNIT_NAME}}` first

6. Reload and enable timer:
   - `systemctl --user daemon-reload`
   - `systemctl --user enable --now <your BACKUP_SYSTEMD_TIMER_NAME>`

7. Verify:
   - `systemctl --user list-timers | grep sdl-db-backup`
   - `systemctl --user status <your BACKUP_SYSTEMD_TIMER_NAME>`
   - `journalctl --user -u <your BACKUP_SYSTEMD_SERVICE_NAME> -n 100 --no-pager`

## Environment Settings

Logical backup settings:
- `BACKUP_LOGICAL_ENABLED=true|false`
- `BACKUP_LOGICAL_SCHEDULE=always|disabled|daily@07:30|daily@00:00,06:00,12:00,18:00|weekly@sun,07:30|interval@24h`
- `BACKUP_LOGICAL_DATABASES=db1,db2` or empty for all granted non-system databases
- `BACKUP_LOGICAL_TABLES=db1:table_a,table_b;db2:table_c` or empty for all validated tables/views
- `BACKUP_LOGICAL_TIMEOUT_PER_DB=30m`
- `BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true|false`
- `DB_ENGINE=mysql|postgres`
- `DB_USER` and `DB_PASS` are used for logical backups (`mysql` / `mysqldump` or `pg_dump`) and should point to a dedicated backup-only user
- `BACKUP_ENCRYPTION_KEY=...` (optional 32-byte string to enable AES-256-CFB stream encryption)

Physical backup settings:
- `BACKUP_PHYSICAL_ENABLED=true|false`
- `BACKUP_PHYSICAL_SCHEDULE=always|disabled|daily@14:00|daily@00:00,06:00,12:00,18:00|weekly@sun,14:00|interval@168h`
- `BACKUP_PHYSICAL_TIMEOUT=6h`
- `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=true|false`
- `BACKUP_XTRABACKUP_USER` and `BACKUP_XTRABACKUP_PASS` are the MySQL credentials used only by physical backup
- `BACKUP_XTRABACKUP_RUN_AS_USER` is the Linux user used to execute `xtrabackup`, usually `mysql`
- `BACKUP_XTRABACKUP_WORK_DIR=/tmp`

Shared S3 settings:
- `BACKUP_S3_BUCKET=...`
- `BACKUP_S3_REGION=...`
- `BACKUP_S3_LOGICAL_PREFIX=logical`
- `BACKUP_S3_PHYSICAL_PREFIX=backup-as-it-is`
- `BACKUP_S3_KEY_ID=...`
- `BACKUP_S3_KEY_SECRET=...`
- `BACKUP_METRICS_FILE=/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom`

Logical S3 upload settings:
- `BACKUP_S3_UPLOAD_MODE=direct|auto|php|http`
- `BACKUP_S3_UPLOAD_SCRIPT=/path/to/s3_upload_backup.php`
- `BACKUP_S3_UPLOAD_URL=https://...`
- `BACKUP_S3_UPLOAD_TIMEOUT=2h`

API settings:
- `BACKUP_API_ENABLED=true|false`
- `BACKUP_API_LISTEN_ADDR=127.0.0.1:8086`
- `BACKUP_API_BASE_PATH=/api/v1`
- `BACKUP_API_AUTH_ENABLED=true|false`
- `BACKUP_API_BEARER_TOKEN=...`
- `BACKUP_SYSTEMD_SERVICE_NAME=sdl-db-backup.service`
- `BACKUP_SYSTEMD_TIMER_NAME=sdl-db-backup.timer`
- `BACKUP_EXECUTION_SOURCE=runner|tui|api|...`

Notes:
- Logical backup upload and physical backup upload are separate. Logical upload is controlled by `BACKUP_LOGICAL_S3_UPLOAD_ENABLED`. Physical upload is controlled by `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED`.
- After each run, the runner writes Prometheus textfile metrics to `BACKUP_METRICS_FILE` or `/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom` by default for Node Exporter textfile collection.
- While a backup is running, the same metrics file is refreshed periodically so Prometheus/Grafana can show live run state via `backup_run_in_progress`, `backup_current_run_start_timestamp`, `backup_current_run_duration_seconds`, and `backup_metrics_last_update_timestamp`.
- The metrics file also separates logical and physical outcome via `backup_logical_last_attempted`, `backup_logical_last_status`, `backup_physical_last_attempted`, and `backup_physical_last_status` so Grafana can distinguish a logical success from a physical failure.
- Additional Grafana-facing metrics include `backup_metrics_write_success`, `backup_cleanup_success`, `backup_last_cleanup_timestamp`, `backup_logical_last_total_databases`, `backup_logical_last_succeeded_databases`, `backup_logical_last_failed_databases`, and `backup_physical_last_duration_seconds`.
- `BACKUP_S3_UPLOAD_MODE=direct` uploads logical backup files from Go directly to S3 using `BACKUP_S3_KEY_ID` and `BACKUP_S3_KEY_SECRET`. `php` uses `BACKUP_S3_UPLOAD_SCRIPT`, `http` uses `BACKUP_S3_UPLOAD_URL`, and `auto` tries direct upload before falling back to PHP/HTTP.
- The built-in API is disabled by default. Set `BACKUP_API_ENABLED=true` before starting `cmd/sdl-db-backup-api`.
- API bearer auth is also disabled by default. When `BACKUP_API_AUTH_ENABLED=true`, every request must send `Authorization: Bearer <BACKUP_API_BEARER_TOKEN>`.
- Logical backup scope is optional. Empty `BACKUP_LOGICAL_DATABASES` and `BACKUP_LOGICAL_TABLES` means the old behavior: back up every granted non-system database, all tables, and all views that pass the validation probe.
- Automatic logical discovery skips databases matching `bk_*` unless they are explicitly listed in `BACKUP_LOGICAL_DATABASES`.
- Table scope applies only to logical backups. Physical backup is a full MySQL data-file backup and is not table-selective.
- Physical backup currently supports direct S3 streaming only. If `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false`, the physical backup is skipped.
- Physical backup can use a different MySQL user from logical backup. `DB_USER` is for logical dumps, while `BACKUP_XTRABACKUP_USER` is for xtrabackup.
- Logical dumps keep full schema coverage (tables, views, triggers, events, and routines) while using `--no-tablespaces` to avoid needing `PROCESS` for logical backups on newer MySQL 8.0 servers.
- The logical backup MySQL user should not be an application/editor account. Grant it only `SELECT`, `SHOW VIEW`, `TRIGGER`, and `EVENT` on the application databases it must dump, plus global `SHOW_ROUTINE`.
- `SHOW DATABASES` only returns databases the logical backup user can access. That is expected when you grant only the selected application databases.
- When `BACKUP_LOGICAL_TABLES` is set, the runner checks the live schema first and skips any requested tables or views that no longer exist, fail validation, or are otherwise error-prone instead of failing the whole database dump.
- The backup runner attempts to set `@@GLOBAL.max_execution_time=0` during logical dumps. A restricted backup user may not have that privilege; the warning is non-fatal and backups continue.
- The physical backup MySQL user needs these grants: `BACKUP_ADMIN`, `PROCESS`, `RELOAD`, `LOCK TABLES`, `REPLICATION CLIENT`, plus `SELECT` on `performance_schema.replication_group_members` and `performance_schema.keyring_component_status` when those tables exist.
- `daily` supports one or more fixed clock times. Example: `daily@00:00,06:00,12:00,18:00`.
- Schedule state is tracked in `logs/backup-schedule-state.json`, so `daily`, `weekly`, and `interval` runs are evaluated separately for logical and physical backups.
- The systemd timer must run often enough for the schedule you choose. If you want very specific execution times, adjust `sdl-db-backup.timer` so it invokes the service at or before those times.
- The service unit now uses `TimeoutStartSec=0`, so application-level env timeouts control the run length.
- Runtime logs are written to both the per-run log file and the daily aggregate log in `BACKUP_LOG_DIR`.

## Least-Privilege Logical Backup User

Use a dedicated MySQL account for `DB_USER` and `DB_PASS`. Do not reuse a phpMyAdmin or application user that can edit data or change schema.

SQL setup example:

```sql
CREATE USER 'backup_logical'@'localhost' IDENTIFIED BY 'strong-password';

GRANT SHOW_ROUTINE ON *.* TO 'backup_logical'@'localhost';

GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON `pf_central`.* TO 'backup_logical'@'localhost';
GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON `pf_messenger`.* TO 'backup_logical'@'localhost';
GRANT SELECT, SHOW VIEW, TRIGGER, EVENT ON `pf_TickleRight_9210`.* TO 'backup_logical'@'localhost';

FLUSH PRIVILEGES;
```

phpMyAdmin setup:

1. Open `User accounts` and create `backup_logical`.
2. Leave global data-editing and DDL privileges unchecked.
3. For each application database that should be backed up, grant only `SELECT`, `SHOW VIEW`, `TRIGGER`, and `EVENT`.
4. Use the SQL tab to run `GRANT SHOW_ROUTINE ON *.* TO 'backup_logical'@'localhost';` if phpMyAdmin does not expose that privilege directly.
5. Update `.env` so `DB_USER` and `DB_PASS` use the new backup-only account.

This user will be able to read only the databases you grant. New databases are not included automatically; add a matching per-database `GRANT` when needed.

## Output Layout

Each run creates a timestamped folder and stores one file per logical database dump:

- `<BACKUP_DIR>/<YYYY-MM-DD_HH-MM-SS>/<db_name>.sql.gz`

Physical backups are streamed directly to S3:

- `s3://<BACKUP_S3_BUCKET>/<BACKUP_S3_PHYSICAL_PREFIX>/<run_id>/physical.xbstream`

## Manual Test

- Go: `go run main.go`
- TUI: `go run ./cmd/sdl-db-backup-tui`
- API: `go run ./cmd/sdl-db-backup-api`
- Bash: `bash ./mysql_full_backup.sh`

## TUI

The TUI provides:

- dashboard-style home screen with latest run, health, runtime/API status, and quick actions
- sidebar + content panel navigation
- Schedule page for permanent or temporary backup timing changes
- searchable `.env` settings editor with masked secret fields, API settings, and systemd unit names
- unsaved-change tracking and safe save back to `.env`
- multi-step manual backup workflow with preview + confirmation
- live backup logs and final summary during manual runs
- log viewer for daily logs and the run index
- run history from `backup-runs.jsonl`
- Health page for latest run status, runtime metadata, scheduler guidance, and logical/physical prerequisite checks
- Observability page for metrics-path state, last metrics write result, and parsed `.prom` values
- Systemd page for user service/timer actions and rendered unit previews
- command palette for fast keyboard control, API auth toggle, and bearer token rotation
- global notifications for command success, failure, confirmations, saves, and silent state changes
- terminal-size-aware layout that reserves space for notifications, prompts, and scrollable content

Launch it from the project directory:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
go run ./cmd/sdl-db-backup-tui
```

If you prefer a binary:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
go build -o sdl-db-backup-tui ./cmd/sdl-db-backup-tui
./sdl-db-backup-tui
```

### TUI Pages

- `Dashboard`: latest run, health cards, runtime/API status, and quick actions
- `Backup`: multi-step manual backup workflow
- `Schedule`: change backup timing permanently or temporarily
- `Config`: searchable editor for `.env` values used by the backup service
- `Logs`: daily logs and run index viewer with filtering
- `History`: previous runs from `backup-runs.jsonl`
- `Health`: latest recorded run, effective runtime values, scheduler guidance, and prerequisite checks
- `Observability`: inspect metrics-path permissions, last metrics refresh result, and parsed Prometheus values
- `Systemd`: manage user service/timer units, review generated unit templates, and keep scheduling user-scoped

### TUI Keyboard Controls

- `1` to `9`: jump directly to `Dashboard`, `Backup`, `Schedule`, `Config`, `Logs`, `History`, `Health`, `Observability`, and `Systemd`
- `Tab`: switch focus between the left menu and the current page content
- `Esc`: move focus back to the left menu
- `j` / `k` or arrow keys: move through lists, settings, workflow options, and actions
- `Enter`: activate the selected row, option, or action
- `:`: open the command palette
- `/`: search settings on `Config`, filter logs on `Logs`
- `PageUp` / `PageDown`: scroll long config lists
- `g` / `G`: jump to top or bottom in scrollable pages
- `r`: refresh the current page where supported
- `b`: jump to the backup workflow
- `g`: jump to dashboard
- `Ctrl+S`: save `.env` from the `Config` page
- `q`: quit, with confirmation when config has unsaved changes

### TUI Config Editing

From the `Config` page:

1. Press `4` to open `Config`
2. Press `Tab` if the left menu still has focus
3. Press `/` to search by env key, group, label, or value
4. Move through matching settings with `j` / `k` or arrow keys
5. Press `Enter` to edit the selected value
6. Press `Enter` again to apply the new value to the temporary draft, or `Esc` to cancel editing
7. Press `Ctrl+S` to safely write your drafted changes back to the permanent `.env` file!

Notes:

- secret fields stay masked until `v` toggles reveal mode
- the config list is scrollable with `j` / `k`, `PageUp` / `PageDown`, and `g` / `G`
- the selected setting shows a hint explaining what the value controls
- saving updates managed `.env` keys while preserving unrelated comments and unknown lines as much as possible
- invalid values are shown inline on the selected setting
- `Ctrl+R` on the Config page reloads the current `.env` from disk and discards draft changes

### TUI Schedule Manager

From the `Schedule` page:

1. Press `3` to open `Schedule`
2. Choose `Apply Mode`
3. Use `permanent .env` when the change should become the saved default
4. Use `temporary override` when the change should last only until an expiry
5. Choose `Temporary Duration` when using temporary mode
6. Edit `Logical Backup Times`, `Physical/S3 Backup Time`, upload toggles, or `Retention Days`
7. Press `a`, `s`, or select `Apply Changes`

Schedule selection:

- common schedule and retention values open as selectable options instead of requiring manual typing
- use `j` / `k` to choose an option
- press `Enter` to apply the selected option
- press `e` inside the option picker to type a custom value
- the selected row shows a hint explaining the effect of that setting

Schedule fields:

- `Logical Backup Times` writes `BACKUP_LOGICAL_SCHEDULE`
- `Physical/S3 Backup Time` writes `BACKUP_PHYSICAL_SCHEDULE`
- `Logical S3 Upload` writes `BACKUP_LOGICAL_S3_UPLOAD_ENABLED`
- `Physical S3 Stream` writes `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED`
- `Retention Daily` writes `BACKUP_RETENTION_DAILY`

Permanent changes:

- saved directly to `.env`
- used by every future service and manual run

Temporary changes:

- saved to `<BACKUP_LOG_DIR>/backup-temporary-overrides.json`
- used by scheduled service runs, health checks, and effective config loading until expiry
- do not rewrite `.env`
- can be cleared from the Schedule page with `c` or `Clear Temporary Override`

Examples:

- `daily@02:00` means one backup per day at 02:00
- `daily@00:00,06:00,12:00,18:00` means four backups per day
- `weekly@sun,02:00` is useful for weekly physical/S3 backups
- `interval@6h` runs after six hours have passed since the last successful backup

### TUI Manual Backup Flow

From the `Backup` page:

1. Choose `Run Mode`
2. Choose `Upload Mode`
3. Choose backup scope
   - Press `c`, `n`, or right arrow to continue with the current scope
   - Press `Enter` on a database to inspect its tables and views
   - Press `/` inside an object list to search/filter tables and views by name
   - Press `Space` to select or clear a database/table/view
   - Press `d` to select all listed databases, or all currently shown objects in the current database
   - Press `m`, move to another row, then press `s` to select that whole range
   - Press `u` to clear the current range, or clear all object checks in the current database
   - Press `PageUp` / `PageDown` or `Home` / `End` to move quickly through long object lists
   - Press `Esc`, `b`, left arrow, or `h` inside an object list to return to the database list
   - Press `p` or `Ctrl+S` to save the current selected scope permanently to `.env`
4. Leave `Force Run Now` enabled if you want the run to ignore the saved schedule for that one execution
5. Optionally enable `Preflight Only` if you want validation without producing backup artifacts
6. Review the preview panel
7. Activate `Run Backup` or `Run Preflight`
8. Watch progress in the same `Backup` page live log panel

Manual run notes:

- the manual preview uses in-memory overrides only
- `.env` is not rewritten unless you explicitly save from `Config`
- `local only` disables uploads for that run
- `preflight only` validates MySQL connectivity, directory permissions, metrics-path writability, and upload prerequisites without creating backups
- physical backup is skipped in `local only` mode because physical backup currently supports direct S3 streaming only
- database/table selection applies to logical backups only
- no database selected means all granted databases
- selected database with no selected tables means all validated tables and views in that database
- on the scope step, `c`, `n`, right arrow, or `l` moves forward to preview
- table/object lists are shown in multiple columns when the terminal is wide enough
- table search filters selection actions, so `d` selects only matching shown objects when a search is active
- permanent scope save writes `BACKUP_LOGICAL_DATABASES` and `BACKUP_LOGICAL_TABLES`; scheduled logical backups use that saved scope

### TUI Logs, Health, And Systemd

From `Logs`:

- `d` loads the current daily aggregate log
- `i` loads the structured run index
- `/` filters visible log lines
- `f` toggles follow mode
- `g` / `G` jump to top or bottom

From `Health`:

- `r` reloads the latest run summary and prerequisite checks

From `Systemd`:

- `r` reloads user-unit state
- `Enter` runs the selected action after confirmation
- service and timer actions call the matching `systemctl --user` command
- the selected action shows the exact command and what it does
- the page shows the last command result as success or failure

The TUI systemd page expects the service and timer to be installed as user units. If they are missing, the page will show `load=not-found`.

## Health Check

Use the CLI health check to see:

- latest recorded run status from `backup-runs.jsonl`
- today’s aggregate daily log path
- logical backup prerequisite status
- physical backup prerequisite status

Run it from the project directory:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
go run ./cmd/sdl-db-backup-health
```

Example output covers:

- config file in use
- today’s daily log file path
- latest run timestamp, status, run folder, log file, duration, and database counts
- logical prerequisite status
- physical prerequisite status

## Commands

Use these commands from the project directory:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
```

Run the normal non-interactive backup runner:

```bash
go run main.go
```

Run the alternate non-interactive entrypoint:

```bash
go run main_sync_upload.go
```

Open the TUI:

```bash
go run ./cmd/sdl-db-backup-tui
```

Run the health check:

```bash
go run ./cmd/sdl-db-backup-health
```

Build local binaries:

```bash
go build -o sdl-db-backup-tui ./cmd/sdl-db-backup-tui
go build -o sdl-db-backup-health ./cmd/sdl-db-backup-health
go build -o sdl-db-backup main.go
```

## Configuring The Service

The backup service reads its configuration from `.env` by default.

Main config file:

- `.env`

Example template:

- `.env.example`

Use a different env file temporarily:

```bash
BACKUP_ENV_FILE=/var/www/go-workspace/sdl/sdl_db_backup/.env.test go run main.go
```

Important config groups:

- database connection: `DB_USER`, `DB_PASS`, `DB_HOST`, `DB_PORT`
- local storage: `BACKUP_DIR`, `BACKUP_LOG_DIR`
- logical backup behavior: `BACKUP_LOGICAL_*`
- physical backup behavior: `BACKUP_PHYSICAL_*`
- physical backup MySQL user: `BACKUP_XTRABACKUP_*`
- shared S3 settings: `BACKUP_S3_*`
- upload helper settings: `BACKUP_S3_UPLOAD_*`

## Logs And Backup Output

Backups:

- logical dumps: `<BACKUP_DIR>/<run_id>/<db_name>.sql.gz`
- physical backup: streamed to `s3://<bucket>/<prefix>/<run_id>/physical.xbstream`

Logs:

- per-run log: `<BACKUP_LOG_DIR>/<run_id>.log`
- daily aggregate log: `<BACKUP_LOG_DIR>/<YYYY-MM-DD>.log`
- run index: `<BACKUP_LOG_DIR>/backup-runs.jsonl`
- schedule state: `<BACKUP_LOG_DIR>/backup-schedule-state.json`
- temporary schedule override: `<BACKUP_LOG_DIR>/backup-temporary-overrides.json`

Quick inspection commands:

```bash
ls -lh /mnt/volume_1/backup/mysql_backup/logs
tail -n 50 /mnt/volume_1/backup/mysql_backup/logs/$(date +%F).log
tail -n 20 /mnt/volume_1/backup/mysql_backup/logs/backup-runs.jsonl
```

## Systemd Control Commands

Install user units:

```bash
mkdir -p ~/.config/systemd/user
# Render unit contents from the TUI or API, then write them to:
#   ~/.config/systemd/user/<service-name>
#   ~/.config/systemd/user/<timer-name>
systemctl --user daemon-reload
systemctl --user enable --now <your BACKUP_SYSTEMD_TIMER_NAME>
```

Common control commands:

```bash
systemctl --user status <your BACKUP_SYSTEMD_SERVICE_NAME>
systemctl --user status <your BACKUP_SYSTEMD_TIMER_NAME>
systemctl --user start <your BACKUP_SYSTEMD_SERVICE_NAME>
systemctl --user restart <your BACKUP_SYSTEMD_SERVICE_NAME>
systemctl --user stop <your BACKUP_SYSTEMD_SERVICE_NAME>
systemctl --user start <your BACKUP_SYSTEMD_TIMER_NAME>
systemctl --user stop <your BACKUP_SYSTEMD_TIMER_NAME>
systemctl --user enable <your BACKUP_SYSTEMD_TIMER_NAME>
systemctl --user disable <your BACKUP_SYSTEMD_TIMER_NAME>
journalctl --user -u <your BACKUP_SYSTEMD_SERVICE_NAME> -n 100 --no-pager
```

## REST API

Run the API server:

```bash
go run ./cmd/sdl-db-backup-api
```

The API is disabled by default. Set `BACKUP_API_ENABLED=true` to start it. If `BACKUP_API_AUTH_ENABLED=true`, send:

```text
Authorization: Bearer <BACKUP_API_BEARER_TOKEN>
```

Key routes:

- `GET /api/v1/backups/health`
- `GET /api/v1/backups/runs`
- `POST /api/v1/backups/runs`
- `GET /api/v1/backups/runs/{run_id}`
- `GET /api/v1/config`
- `PUT /api/v1/config`
- `GET /api/v1/config/effective`
- `GET|PUT|DELETE /api/v1/schedules/temporary-overrides`
- `GET /api/v1/systemd`
- `POST /api/v1/systemd/actions/{action}`
- `GET /api/v1/logs/daily`
- `GET /api/v1/logs/runs`
- `GET /api/v1/storage`
- `GET /api/v1/runtime`
- `GET /api/v1/restore` returns not implemented in this version

See `openapi.json` for the machine-readable contract.

Example authenticated request:

```bash
curl \
  -H "Authorization: Bearer $BACKUP_API_BEARER_TOKEN" \
  http://127.0.0.1:8086/api/v1/backups/health
```

## Duplicate Backup Audit

Scheduled runs are intended to execute only as the `developer` user. The runner now refuses the scheduled `runner` path when it is launched as `root`, so any root-owned scheduled execution is a misconfiguration outside the normal user timer flow.

If you still see both `root` and `developer` owned backup runs, the extra scheduler is outside this repo. Audit these locations:

- `sudo crontab -l` and `/etc/cron.*`
- `systemctl list-timers --all` and `systemctl list-units --type=service`
- deployment hooks, supervisor configs, or any wrapper script that invokes both `main.go` and `mysql_full_backup.sh`
- root-owned systemd units or timers that call `go run ./main.go` directly

## Temporary Test Overrides

You can test backups without changing `.env` by overriding environment variables for a single command.

Force both logical and physical backups immediately:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
BACKUP_LOGICAL_SCHEDULE=always \
BACKUP_PHYSICAL_SCHEDULE=always \
go run main_sync_upload.go
```

Force only logical backup:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
BACKUP_LOGICAL_SCHEDULE=always \
BACKUP_PHYSICAL_ENABLED=false \
go run main_sync_upload.go
```

Run a local-only logical backup with the restricted logical user:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
BACKUP_LOGICAL_SCHEDULE=always \
BACKUP_PHYSICAL_ENABLED=false \
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false \
go run main_sync_upload.go
```

Force only physical backup:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
BACKUP_LOGICAL_ENABLED=false \
BACKUP_PHYSICAL_SCHEDULE=always \
go run main_sync_upload.go
```

Skip S3 upload temporarily during a test run:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
BACKUP_LOGICAL_S3_UPLOAD_ENABLED=false \
BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false \
go run main_sync_upload.go
```

Use a separate test env file instead of the real `.env`:

```bash
cd /var/www/go-workspace/sdl/sdl_db_backup
cp .env .env.test
BACKUP_ENV_FILE=/var/www/go-workspace/sdl/sdl_db_backup/.env.test go run main_sync_upload.go
```

Notes:
- `always` ignores the saved schedule timing for that run and forces the backup to be due immediately.
- `daily@00:00,06:00,12:00,18:00` runs on fixed clock slots and does not drift based on the last successful backup timestamp.
- `interval@6h` starts counting from the last successful logical backup time stored in `logs/backup-schedule-state.json`.
- `weekly@sun,02:00` is satisfied once the physical backup succeeds at any time after that Sunday's `02:00` window.

Example schedules:
- Run logical backups every invocation: `BACKUP_LOGICAL_SCHEDULE=always`
- Run logical backups once a day at 07:30: `BACKUP_LOGICAL_SCHEDULE=daily@07:30`
- Run logical backups on fixed slots (midnight, 6am, noon, 6pm): `BACKUP_LOGICAL_SCHEDULE=daily@00:00,06:00,12:00,18:00`
- Run physical backups once a week on Sunday at 02:00: `BACKUP_PHYSICAL_SCHEDULE=weekly@sun,02:00`
- Run physical backups on fixed slots (midnight, 6am, noon, 6pm): `BACKUP_PHYSICAL_SCHEDULE=daily@00:00,06:00,12:00,18:00`
- Run physical backups every 24 hours: `BACKUP_PHYSICAL_SCHEDULE=interval@24h`
