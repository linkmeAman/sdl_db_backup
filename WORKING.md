# MySQL Database Backup Tool - How It Works

## Purpose
This is a comprehensive MySQL backup tool that automatically backs up all user databases, handles failures with retry logic, cleans up old backups, and optionally uploads them to S3.

---

## Main Components

### Configuration Structure
The program loads configuration from environment variables (or a `.env` file):
- **Database credentials**: `DB_USER`, `DB_PASS`, `DB_HOST`, `DB_PORT`
- **Backup settings**: `BACKUP_DIR`, `BACKUP_LOG_DIR`, `BACKUP_RETENTION_DAYS` (how long to keep old backups)
- **Timeouts**: Per-database timeout, discovery timeout, preflight checks
- **Retry logic**: `BACKUP_RETRY_COUNT`, base delay, max delay
- **S3 upload**: Optional URL or PHP script for uploading backups

---

## Execution Flow

### 1. Startup & Lock Acquisition
```
main() → run()
  ├─ Load configuration from .env or environment
  ├─ Initialize run logger (creates individual log file for this run)
  ├─ Acquire lock file to prevent parallel executions
  └─ If lock fails → exit (another backup already running)
```

### 2. Preflight Checks (`validatePrerequisites`)
- Verify `DB_USER` is configured
- Check that backup and log directories are writable
- Verify `mysql` and `mysqldump` binaries exist in PATH
- Test MySQL connectivity with a simple `SELECT 1` query

### 3. Database Discovery (`listDatabases`)
- Queries MySQL to get all databases: `SHOW DATABASES`
- Filters out system databases: `information_schema`, `performance_schema`, `mysql`, `sys`
- Returns list of user databases to back up

### 4. Max Execution Time Handling (`tryDisableMaxExecutionTime`)
- Reads MySQL's `@@GLOBAL.max_execution_time` setting
- If it's > 0, disables it (sets to 0) so large table dumps won't timeout
- Saves the original value to restore later after backup completes
- This prevents mysqldump from being killed mid-operation

### 5. Database Backup Loop
For each database found:
```
dumpWithRetry(database)
  ├─ Precheck: Discover broken views via discoverBrokenViews()
  │  └─ Queries information_schema.VIEWS to find views with invalid dependencies
  │
  ├─ Attempt dump (retry up to RetryCount times):
  │  └─ dumpDatabase()
  │     ├─ Creates gzip-compressed SQL file (.sql.gz)
  │     ├─ Uses mysqldump with flags:
  │     │  - --single-transaction (consistent backup)
  │     │  - --routines, --triggers, --events (include DB objects)
  │     │  - --force --ignore-error (skip broken views/definers)
  │     │  - --ignore-table (excludes detected broken views)
  │     └─ Streams output to gzip compressor, counts bytes
  │
  └─ On failure: Classify error and decide if retry is worth attempting
```

### 6. Error Classification & Retry Logic (`classifyFailure` & `shouldRetry`)
Errors are categorized:
- **No retry**: auth, permission, disk, view, definer, schema, config, binary errors
- **Retry**: connection issues, timeouts, command errors, etc.

Retry delays use exponential backoff:
- Start with `RetryBaseDelay` (default 2s)
- Double each attempt
- Cap at `RetryMaxDelay` (default 20s)

### 7. Cleanup Old Backups (`cleanupOldBackups`)
- After backup completes, deletes backup folders older than `RetentionDays`
- Parses folder names by timestamp format: `YYYY-MM-DD_HH-MM-SS`
- Skips the current run folder

### 8. Run Record & Manifest
- Collects results in `runRecord` struct (status, duration, success/failure counts, per-DB results)
- Writes `manifest.json` to the run folder
- Appends run summary to `backup-runs.jsonl` (JSON lines log file)

### 9. S3 Upload (`uploadBackupToS3`)
Two methods:
- **CLI mode**: Calls PHP script with folder path: `php script.php /path/to/backup`
- **HTTP mode**: POSTs JSON request to configured URL with backup folder path

---

## Key Data Structures

```go
databaseResult {
  Name, Status, Attempts
  Duration, OutputPath, SizeBytes
  ErrorCategory, Error
}

runRecord {
  RunID, Status, Duration
  DatabasesTotal, DatabasesSucceeded, DatabasesFailed
  Results[] databaseResult
}
```

---

## Output Files
- **Backup folder**: `{BACKUP_DIR}/{TIMESTAMP}/`
  - `database1.sql.gz`, `database2.sql.gz`, etc.
  - `manifest.json` (summary of this run)
- **Logs**: `{LOG_DIR}/{TIMESTAMP}.log` (detailed execution log)
- **Run history**: `{LOG_DIR}/backup-runs.jsonl` (appended one line per run)

---

## Exit Code
- `0` = success or warning-only run, including skipped missing tables that do not stop the backup
- `1` = partial or complete failure with at least one real database backup error

---

## Critical Features
✅ **Atomic backups** via `--single-transaction`  
✅ **Broken view detection** to prevent dump failures  
✅ **Exponential backoff** retry strategy  
✅ **Process-aware locking** (detects stale locks)  
✅ **Comprehensive error classification** for smart retry decisions  
✅ **Automatic retention cleanup**  
✅ **Optional S3 integration**  
✅ **Per-run logging** + centralized JSONL history

---

## Configuration Example

```env
# Database
DB_USER=backup_user
DB_PASS=secure_password
DB_HOST=127.0.0.1
DB_PORT=3306

# Backup paths
BACKUP_DIR=/mnt/volume_1/backup/mysql_backup
BACKUP_LOG_DIR=/mnt/volume_1/backup/mysql_backup/logs

# Retention & timeouts
BACKUP_RETENTION_DAYS=5
BACKUP_TIMEOUT_PER_DB=30m
BACKUP_DISCOVERY_TIMEOUT=30s
BACKUP_PREFLIGHT_TIMEOUT=15s

# Retry logic
BACKUP_RETRY_COUNT=3
BACKUP_RETRY_BASE_DELAY=2s
BACKUP_RETRY_MAX_DELAY=20s

# S3 upload (optional)
BACKUP_S3_UPLOAD_URL=https://your-server.com/upload
BACKUP_S3_UPLOAD_TIMEOUT=2h
```

---

## Typical Backup Run Sequence

1. **Lock acquired** → Check if another backup is running
2. **Preflight checks** → Verify MySQL connectivity and binaries
3. **Database discovery** → Get list of user databases
4. **Max execution time disabled** → Prevent timeout on large dumps
5. **For each database**:
   - Discover broken views
   - Dump with retry logic (up to 3 attempts with exponential backoff)
   - Record success/failure
6. **Cleanup** → Remove backups older than retention period
7. **Lock released** → Allow next backup to run
8. **S3 upload** (if configured) → Upload backup folder to S3
9. **Record logged** → Append run summary to backup-runs.jsonl

---

## Error Handling

The tool implements intelligent error classification:

| Error Type | Retryable | Example |
|-----------|-----------|---------|
| Authentication | No | Access denied |
| Permission | No | Permission denied |
| Disk | No | No space left on device |
| View | No | References invalid table (1356) |
| Definer | No | User specified as definer (1449) |
| Schema | No | Table doesn't exist (1146) |
| Config | No | Unknown variable, max_allowed_packet |
| Binary | No | Executable not found |
| Connection | Yes | Connection refused, server has gone away |
| Timeout | Yes | Deadline exceeded, timed out |
| Command | Yes | Other command errors |
