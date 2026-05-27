# SDL DB Backup

Creates MySQL backups with separate logical and physical backup controls.

Behavior:
- Logical backups and physical backups can be enabled, scheduled, timed out, and uploaded independently through `.env`.
- Logical backups create `.sql.gz` files per database and can upload that run folder to S3 via the existing PHP or HTTP uploader.
- Physical backups stream `xtrabackup` directly to S3 as `physical.xbstream` and do not create a local physical backup directory.
- Retries each logical database dump up to the configured retry count.
- Deletes backup folders older than the configured retention period only after the current run completes.

## Files

- `main.go`: Go backup runner
- `main_test.go`: editable source kept in sync with the runtime file
- `main_sync_upload.go`: runtime file used for manual `go run`
- `mysql_full_backup.sh`: bash backup script
- `.env.example`: environment template
- `sdl-db-backup.service`: user service unit
- `sdl-db-backup.timer`: user timer unit

## Setup (No Root)

1. Create environment file:
   - `cp sdl_db_backup/.env.example sdl_db_backup/.env`
   - Set database credentials, logical/physical schedules, and S3 settings

2. For Go testing (logs progress in terminal):
   - `go run ./sdl_db_backup`

3. Ensure `go` is available to the service user:
   - `which go`

4. Install user units:
   - `mkdir -p ~/.config/systemd/user`
   - `cp sdl_db_backup/sdl-db-backup.service ~/.config/systemd/user/`
   - `cp sdl_db_backup/sdl-db-backup.timer ~/.config/systemd/user/`

5. Reload and enable timer:
   - `systemctl --user daemon-reload`
   - `systemctl --user enable --now sdl-db-backup.timer`

6. Verify:
   - `systemctl --user list-timers | grep sdl-db-backup`
   - `systemctl --user status sdl-db-backup.timer`
   - `journalctl --user -u sdl-db-backup.service -n 100 --no-pager`

## Environment Settings

Logical backup settings:
- `BACKUP_LOGICAL_ENABLED=true|false`
- `BACKUP_LOGICAL_SCHEDULE=always|disabled|daily@07:30|daily@00:00,06:00,12:00,18:00|weekly@sun,07:30|interval@24h`
- `BACKUP_LOGICAL_TIMEOUT_PER_DB=30m`
- `BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true|false`
- `DB_USER` and `DB_PASS` are used for logical backups (`mysql` / `mysqldump`)

Physical backup settings:
- `BACKUP_PHYSICAL_ENABLED=true|false`
- `BACKUP_PHYSICAL_SCHEDULE=always|disabled|daily@14:00|daily@00:00,06:00,12:00,18:00|weekly@sun,14:00|interval@168h`
- `BACKUP_PHYSICAL_TIMEOUT=6h`
- `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=true|false`
- `BACKUP_XTRABACKUP_USER` and `BACKUP_XTRABACKUP_PASS` are the MySQL credentials used only by physical backup
- `BACKUP_XTRABACKUP_RUN_AS_USER` is the Linux user used to execute `xtrabackup`, usually `mysql`
- `BACKUP_XTRABACKUP_WORK_DIR=/tmp`

Shared S3 settings:
- `BACKUP_S3_BUCKET=ticklerightbackups`
- `BACKUP_S3_REGION=ap-south-1`
- `BACKUP_S3_PHYSICAL_PREFIX=backup-as-it-is`
- `BACKUP_S3_KEY_ID=...`
- `BACKUP_S3_KEY_SECRET=...`

Logical S3 upload settings:
- `BACKUP_S3_UPLOAD_SCRIPT=/path/to/s3_upload_backup.php`
- `BACKUP_S3_UPLOAD_URL=https://...`
- `BACKUP_S3_UPLOAD_TIMEOUT=2h`

Notes:
- Logical backup upload and physical backup upload are separate. Logical upload is controlled by `BACKUP_LOGICAL_S3_UPLOAD_ENABLED`. Physical upload is controlled by `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED`.
- Physical backup currently supports direct S3 streaming only. If `BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false`, the physical backup is skipped.
- Physical backup can use a different MySQL user from logical backup. `DB_USER` is for logical dumps, while `BACKUP_XTRABACKUP_USER` is for xtrabackup.
- The physical backup MySQL user needs these grants: `BACKUP_ADMIN`, `PROCESS`, `RELOAD`, `LOCK TABLES`, `REPLICATION CLIENT`, plus `SELECT` on `performance_schema.replication_group_members` and `performance_schema.keyring_component_status` when those tables exist.
- `daily` supports one or more fixed clock times. Example: `daily@00:00,06:00,12:00,18:00`.
- Schedule state is tracked in `logs/backup-schedule-state.json`, so `daily`, `weekly`, and `interval` runs are evaluated separately for logical and physical backups.
- The systemd timer must run often enough for the schedule you choose. If you want very specific execution times, adjust `sdl-db-backup.timer` so it invokes the service at or before those times.
- The service unit now uses `TimeoutStartSec=0`, so application-level env timeouts control the run length.

## Output Layout

Each run creates a timestamped folder and stores one file per logical database dump:

- `<BACKUP_DIR>/<YYYY-MM-DD_HH-MM-SS>/<db_name>.sql.gz`

Physical backups are streamed directly to S3:

- `s3://<BACKUP_S3_BUCKET>/<BACKUP_S3_PHYSICAL_PREFIX>/<run_id>/physical.xbstream`

## Manual Test

- Go: `go run ./sdl_db_backup`
- Bash: `bash sdl_db_backup/mysql_full_backup.sh`

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
