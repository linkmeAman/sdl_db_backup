# Grafana Backup Monitoring Guide

This document defines a practical Grafana dashboard and alerting setup for the SDL DB backup service using Prometheus metrics emitted by the backup runner.

An importable Grafana dashboard JSON is included in this repo at:

- `dashboards/sdl-db-backup-observability.json`

Import that file into Grafana first if you want a ready-made starting dashboard, then use the sections below to customize thresholds, add alerts, or build extra panels.

If you also want the actual backup log stream in Grafana, this repo includes:

- `dashboards/sdl-db-backup-logs.json`
- `loki/promtail-config.yml`
- `loki/README.md`

An importable Prometheus alert rule file is also included in this repo at:

- `prometheus/sdl-db-backup-alerts.yml`

Load that rule file into Prometheus or your rule-evaluation stack if you want the documented backup alerts without copying them manually from this guide.

For a short index of all observability assets, start with:

- `observability/README.md`

The metrics come from the Node Exporter textfile collector output written by this repo:

- default path: `/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom`

Labels written into the `.prom` file:

- `job="sdl_db_backup"`
- `service="mysql"`
- `env="pilot"`

When these metrics are scraped through Node Exporter textfile collector, Prometheus renames the sample's `job` label to `exported_job` to avoid colliding with the scrape job label. Use `exported_job="sdl_db_backup"` in Grafana and alert queries against Prometheus.

## Metrics Vs Logs

The Prometheus textfile collector output is intentionally metrics-only. It does not embed backup log lines, SQL dump progress text, or S3 upload log messages.

Use the Prometheus dashboard for:

- current state
- last success age
- durations
- sizes
- cleanup and validation health
- live run state

Use Loki for:

- per-run backup log lines from start to finish
- `mysqldump` / logical progress messages
- S3 upload messages
- failure and warning investigation

The bundled Loki example scrapes only `BACKUP_LOG_DIR/*_*.log` so you get one log stream per run without duplicate lines from the daily aggregate log.

Optional additional label when configured:

- `region="us-east-1"`

To configure these labels, set the following in your `.env` file:
```bash
BACKUP_METRICS_JOB=sdl_db_backup
BACKUP_METRICS_SERVICE=mysql
BACKUP_METRICS_ENV=pilot
BACKUP_METRICS_REGION=us-east-1
```

## Available Metrics

Completed-run metrics:

- `backup_last_run_timestamp`
- `backup_last_success_timestamp`
- `backup_last_duration_seconds`
- `backup_last_size_bytes`
- `backup_last_status`
- `backup_upload_success`
- `backup_metrics_write_success`
- `backup_cleanup_success`
- `backup_last_cleanup_timestamp`
- `backup_logical_last_attempted`
- `backup_logical_last_status`
- `backup_logical_last_total_databases`
- `backup_logical_last_succeeded_databases`
- `backup_logical_last_failed_databases`
- `backup_logical_validation_last_status`
- `backup_physical_last_attempted`
- `backup_physical_last_status`
- `backup_physical_last_duration_seconds`
- `backup_adaptive_load_per_cpu`
- `backup_adaptive_logical_parallel`
- `backup_adaptive_xtrabackup_parallel`
- `backup_adaptive_xbcloud_parallel`
- `backup_physical_retry_count`
- `backup_physical_rate_limit_retry_count`

Live-run metrics:

- `backup_run_in_progress`
- `backup_current_run_start_timestamp`
- `backup_current_run_duration_seconds`
- `backup_metrics_last_update_timestamp`

## Full Metric Catalog

All backup metrics emitted by this repo are Prometheus `gauge` metrics. The exact label selector used in most examples in this document is:

```promql
{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

If you also configure `BACKUP_METRICS_REGION`, add `,region="..."` to the selector.

### Run Outcome Metrics

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_last_status` | boolean-ish `0/1` | Overall result of the most recent backup run. `1` means full success. `0` means failed or partial outcome. | Main backup status stat | `backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_upload_success` | boolean-ish `0/1` | Whether the upload phase succeeded for the most recent run. `0` also covers upload not reached. | Upload status stat | `backup_upload_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_last_run_timestamp` | Unix seconds | When the most recent backup run ended. | Last run age panel via `time() - metric` | `backup_last_run_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_last_success_timestamp` | Unix seconds | When the most recent fully successful run ended. | Last successful backup age / stale-success alert | `backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_last_duration_seconds` | seconds | Total duration of the most recent backup run. | Duration stat / time-series | `backup_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_last_size_bytes` | bytes | Total artifact size recorded for the most recent backup run. | Size stat / trend chart | `backup_last_size_bytes{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

### Metrics and Cleanup Health

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_metrics_write_success` | boolean-ish `0/1` | Whether the metrics file itself was written successfully during the most recent run. | Alert on observability failure | `backup_metrics_write_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_metrics_last_update_timestamp` | Unix seconds | Last time the `.prom` file was refreshed. This updates during active runs too. | Detect stale textfile collector updates | `backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_cleanup_success` | boolean-ish `0/1` | Whether retention cleanup completed without error in the most recent run. | Cleanup status stat | `backup_cleanup_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_last_cleanup_timestamp` | Unix seconds | Last time old backup cleanup ran successfully enough to update the timestamp. | Cleanup age / cleanup freshness | `backup_last_cleanup_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

### Logical Backup Metrics

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_logical_last_attempted` | boolean-ish `0/1` | Whether logical backup was attempted in the most recent run. | Distinguish skipped vs attempted | `backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_logical_last_status` | boolean-ish `0/1` | Result of the logical portion of the most recent run. `0` means failed or skipped. | Logical backup status stat | `backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_logical_last_total_databases` | count | Number of logical databases selected in the most recent run. | Scope / workload stat | `backup_logical_last_total_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_logical_last_succeeded_databases` | count | Number of logical databases that completed successfully in the most recent run. | Success count stat | `backup_logical_last_succeeded_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_logical_last_failed_databases` | count | Number of logical databases that failed in the most recent run. | Failure count stat / alert annotation | `backup_logical_last_failed_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_logical_validation_last_status` | boolean-ish `0/1` | Result of the most recently persisted logical validation or sandbox restore test. | Restore confidence stat | `backup_logical_validation_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

### Physical Backup Metrics

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_physical_last_attempted` | boolean-ish `0/1` | Whether the physical backup path was attempted in the most recent run. | Distinguish skipped vs attempted | `backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_physical_last_status` | boolean-ish `0/1` | Result of the physical backup portion of the most recent run. `0` means failed or skipped. | Physical backup status stat | `backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_physical_last_duration_seconds` | seconds | Duration of the physical backup portion only. | Physical duration trend | `backup_physical_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_physical_retry_count` | count | Total physical attempts used in the most recent run. | Retry count stat | `backup_physical_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_physical_rate_limit_retry_count` | count | Retries specifically caused by `xbcloud`/S3 rate limiting. | Throttling visibility stat | `backup_physical_rate_limit_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

### Adaptive Tuning Metrics

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_adaptive_load_per_cpu` | load per CPU | 1-minute load average divided by CPU count for the most recent run. | Host pressure indicator | `backup_adaptive_load_per_cpu{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_adaptive_logical_parallel` | workers | Logical dump parallelism chosen for the most recent run. | Tuning visibility stat | `backup_adaptive_logical_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_adaptive_xtrabackup_parallel` | workers | `xtrabackup` physical parallelism chosen for the most recent run. | Physical tuning stat | `backup_adaptive_xtrabackup_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_adaptive_xbcloud_parallel` | workers | `xbcloud` upload parallelism chosen for the most recent run. | Upload tuning stat | `backup_adaptive_xbcloud_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

### Live Run Metrics

| Metric | Unit | Meaning | Typical Grafana Use | Query |
| --- | --- | --- | --- | --- |
| `backup_run_in_progress` | boolean-ish `0/1` | Whether a backup run is currently active. | Live run stat / stuck-run alert | `backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_current_run_start_timestamp` | Unix seconds | Start timestamp of the currently running backup, or `0` when idle. | Run start stat / elapsed formula | `backup_current_run_start_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |
| `backup_current_run_duration_seconds` | seconds | Elapsed duration of the active run, or `0` when idle. | Live progress duration panel | `backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}` |

## Dashboard Builder Checklist

If you want a complete observability dashboard, the minimum high-signal panels are:

1. `backup_run_in_progress`
2. `backup_last_status`
3. `backup_upload_success`
4. `time() - backup_last_success_timestamp`
5. `backup_last_duration_seconds`
6. `backup_last_size_bytes`
7. `backup_logical_last_status`
8. `backup_physical_last_status`
9. `backup_metrics_write_success`
10. `backup_cleanup_success`
11. `backup_logical_validation_last_status`
12. `backup_physical_rate_limit_retry_count`
13. `backup_adaptive_load_per_cpu`
14. `backup_current_run_duration_seconds`

## Copy-Paste Metric Selectors

Use these as a quick reference while building panels:

```promql
backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_upload_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_last_run_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_last_size_bytes{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_metrics_write_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_cleanup_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_last_cleanup_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_last_total_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_last_succeeded_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_last_failed_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_logical_validation_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_physical_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_adaptive_load_per_cpu{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_adaptive_logical_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_adaptive_xtrabackup_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_adaptive_xbcloud_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_physical_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_physical_rate_limit_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_current_run_start_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

## Live Backup Logs In Grafana

For live backup logs, use Loki labels with `job`, not `exported_job`.

Example LogQL selectors:

```logql
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"}
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"} |~ "mysql full backup started|run log file:|logical backup:|physical backup:|starting dump|completed dump|s3 upload|backup summary|backup failure reason"
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"} |~ "failed|warning|permission denied|rate limit|retry"
```

Recommended setup:

1. Ship `BACKUP_LOG_DIR/*_*.log` with `loki/promtail-config.yml`.
2. Import `dashboards/sdl-db-backup-logs.json`.
3. Keep the dashboard time range around the active run.
4. Use ascending sort in the logs panel if you want to read the backup from the start instead of the newest line first.

The logs dashboard has two panels:

- `Backup Lifecycle Events`: high-signal run milestones without the full noise floor
- `Full Per-Run Backup Log`: the full raw run log stream

## Recommended Dashboard Layout

### Row 1: Current State

#### Backup Running

Panel type:
- `Stat`

Query:

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Idle`
- `1 = Running`

Suggested thresholds:
- `0` green
- `1` yellow or blue

#### Overall Backup Status

Panel type:
- `Stat`

Query:

```promql
backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Partial`
- `1 = Success`

Suggested thresholds:
- `0` red
- `1` green

#### Backup Upload Success

Panel type:
- `Stat`

Query:

```promql
backup_upload_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Not Reached`
- `1 = Success`

#### Metrics Write Success

Panel type:
- `Stat`

Query:

```promql
backup_metrics_write_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

#### Cleanup Success

Panel type:
- `Stat`

Query:

```promql
backup_cleanup_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

#### Logical Backup Status

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Skipped`
- `1 = Success`

#### Physical Backup Status

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Skipped`
- `1 = Success`

#### Last Logical Validation Status

Panel type:
- `Stat`

Query:

```promql
backup_logical_validation_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Not Yet Validated`
- `1 = Success`

## Row 2: Freshness

### Last Run Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_run_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Full Success Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

Suggested thresholds:
- green: within expected SLA
- red: older than expected SLA

### Metrics Freshness

Panel type:
- `Stat`

Query:

```promql
time() - backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

## Row 3: Duration and Size

### Current Run Duration

Panel type:
- `Stat`

Query:

```promql
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Backup Duration

Panel type:
- `Stat`

Query:

```promql
backup_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Backup Size

Panel type:
- `Stat`

Query:

```promql
backup_last_size_bytes{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- bytes

### Last Physical Backup Duration

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Cleanup Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_cleanup_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

## Row 3B: Resource Tuning

### Host Load Per CPU Used For Tuning

Panel type:
- `Stat`

Query:

```promql
backup_adaptive_load_per_cpu{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Adaptive Logical Parallelism

Panel type:
- `Stat`

Query:

```promql
backup_adaptive_logical_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Adaptive Physical Parallelism

Panel type:
- `Stat`

Query:

```promql
backup_adaptive_xtrabackup_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Adaptive xbcloud Parallelism

Panel type:
- `Stat`

Query:

```promql
backup_adaptive_xbcloud_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Physical Retry Count

Panel type:
- `Stat`

Query:

```promql
backup_physical_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Physical Rate-Limit Retry Count

Panel type:
- `Stat`

Query:

```promql
backup_physical_rate_limit_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

## Row 4: Time Series

### Current Run Duration Trend

Panel type:
- `Time series`

Query:

```promql
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Last Backup Duration Trend

Panel type:
- `Time series`

Query:

```promql
backup_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Last Backup Size Trend

Panel type:
- `Time series`

Query:

```promql
backup_last_size_bytes{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

## Row 5: Attempted vs Skipped

### Logical Attempted

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Skipped`
- `1 = Attempted`

### Physical Attempted

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

## Row 6: Logical Database Counts

### Logical Databases Total

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_total_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Logical Databases Succeeded

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_succeeded_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Logical Databases Failed

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_failed_databases{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Skipped`
- `1 = Attempted`

## Prometheus Queries

### Status Queries

Overall:

```promql
backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Upload:

```promql
backup_upload_success{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Logical attempted:

```promql
backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Logical status:

```promql
backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Logical validation status:

```promql
backup_logical_validation_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical attempted:

```promql
backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical status:

```promql
backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Adaptive logical parallelism:

```promql
backup_adaptive_logical_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Adaptive physical parallelism:

```promql
backup_adaptive_xtrabackup_parallel{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical retry count:

```promql
backup_physical_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical rate-limit retry count:

```promql
backup_physical_rate_limit_retry_count{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Running:

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Age Queries

Last run age:

```promql
time() - backup_last_run_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Last full success age:

```promql
time() - backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Metrics freshness:

```promql
time() - backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

### Duration and Size Queries

Last completed duration:

```promql
backup_last_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Current run duration:

```promql
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

Last size:

```promql
backup_last_size_bytes{exported_job="sdl_db_backup",service="mysql",env="pilot"}
```

## Useful Derived Expressions

### Logical Failed In Last Run

```promql
backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

### Physical Failed In Last Run

```promql
backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

### Backup Stuck Running

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"} > 7200
```

### Metrics Stale During Run

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
(time() - backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}) > 120
```

## Alert Rules

### Backup Failed Overall

Expression:

```promql
backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Logical Backup Failed

Expression:

```promql
backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Physical Backup Failed

Expression:

```promql
backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Backup Stuck Running

Expression:

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"} > 7200
```

For:
- `10m`

### Metrics Stopped Updating During Run

Expression:

```promql
backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
(time() - backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}) > 120
```

For:
- `2m`

### No Successful Backup Recently

Expression:

```promql
(time() - backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}) > 43200
```

Example threshold:
- `12h`

Adjust to match your backup SLA.

## Example Prometheus Rule File

The expressions above are easier to operationalize when stored as Prometheus alerting rules. The example below assumes the default label set from this repo and separates backup execution failures from observability and cleanup failures.

```yaml
groups:
  - name: sdl-db-backup
    rules:
      - alert: SDLDBBackupFailedOverall
        expr: backup_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 5m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "SDL DB backup failed"
          description: "The latest SDL DB backup run did not complete successfully."

      - alert: SDLDBLogicalBackupFailed
        expr: |
          backup_logical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
          and
          backup_logical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 5m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "SDL logical backup failed"
          description: "The most recent logical backup attempt failed."

      - alert: SDLDBPhysicalBackupFailed
        expr: |
          backup_physical_last_attempted{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
          and
          backup_physical_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 5m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "SDL physical backup failed"
          description: "The most recent physical backup attempt failed."

      - alert: SDLDBBackupUploadFailed
        expr: backup_upload_success{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 5m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "SDL backup upload failed"
          description: "Backup creation may have succeeded, but the final upload stage failed."

      - alert: SDLDBBackupMetricsWriteFailed
        expr: backup_metrics_write_success{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 5m
        labels:
          severity: warning
          team: platform
        annotations:
          summary: "SDL backup metrics write failed"
          description: "The backup runner could not update the Node Exporter textfile metric output."

      - alert: SDLDBBackupCleanupFailed
        expr: backup_cleanup_success{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 15m
        labels:
          severity: warning
          team: platform
        annotations:
          summary: "SDL backup cleanup failed"
          description: "The latest backup run completed with cleanup errors. Check for ownership or retention drift."

      - alert: SDLDBLogicalValidationFailed
        expr: backup_logical_validation_last_status{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 0
        for: 15m
        labels:
          severity: warning
          team: platform
        annotations:
          summary: "SDL logical backup validation failed"
          description: "The latest logical backup validation or restore verification failed."

      - alert: SDLDBBackupStuckRunning
        expr: |
          backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
          and
          backup_current_run_duration_seconds{exported_job="sdl_db_backup",service="mysql",env="pilot"} > 7200
        for: 10m
        labels:
          severity: warning
          team: platform
        annotations:
          summary: "SDL backup appears stuck"
          description: "A backup is still marked running after exceeding the expected duration threshold."

      - alert: SDLDBBackupMetricsStaleDuringRun
        expr: |
          backup_run_in_progress{exported_job="sdl_db_backup",service="mysql",env="pilot"} == 1
          and
          (time() - backup_metrics_last_update_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}) > 120
        for: 2m
        labels:
          severity: warning
          team: platform
        annotations:
          summary: "SDL backup metrics stopped updating"
          description: "The backup still reports an in-progress run, but the textfile metrics are no longer refreshing."

      - alert: SDLDBNoSuccessfulBackupRecently
        expr: (time() - backup_last_success_timestamp{exported_job="sdl_db_backup",service="mysql",env="pilot"}) > 43200
        for: 15m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "SDL backup success is stale"
          description: "No fully successful backup has been recorded within the expected SLA window."
```

If you use custom metric labels in `.env`, replace the label selectors in the rules above to match your configured `BACKUP_METRICS_JOB`, `BACKUP_METRICS_SERVICE`, `BACKUP_METRICS_ENV`, and optional `BACKUP_METRICS_REGION`.

## Threshold Suggestions

### Last Run Age

- green: `< 8h`
- yellow: `8h - 12h`
- red: `> 12h`

### Last Full Success Age

- green: `< 12h`
- red: `> 12h`

Adjust these to match your actual schedule.

### Last Duration

- green: `< 15m`
- yellow: `15m - 30m`
- red: `> 30m`

### Last Size

Use trend monitoring and anomaly detection. A sudden sharp drop usually matters more than a hard threshold.

## How To Interpret The Metrics

### Full Success

Typical values:

- `backup_last_status = 1`
- `backup_upload_success = 1`
- `backup_logical_last_status = 1` when logical backup was attempted
- `backup_physical_last_status = 1` when physical backup was attempted

### Logical Success, Physical Failure

Typical values:

- `backup_last_status = 0`
- `backup_logical_last_attempted = 1`
- `backup_logical_last_status = 1`
- `backup_physical_last_attempted = 1`
- `backup_physical_last_status = 0`

This means the logical backup completed successfully, but the physical backup failed.

### Skipped By Schedule

Typical values depend on the previous run snapshot, but for the most recent run:

- `backup_last_status` may still be `1` if the run completed cleanly with nothing due
- `backup_logical_last_attempted = 0` if logical was skipped
- `backup_physical_last_attempted = 0` if physical was skipped

### Live Running State

While a backup is running:

- `backup_run_in_progress = 1`
- `backup_current_run_duration_seconds` increases over time
- `backup_metrics_last_update_timestamp` refreshes periodically

## Recommended Operator Read Order

When investigating a problem, check in this order:

1. `backup_run_in_progress`
2. `backup_last_status`
3. `backup_logical_last_status`
4. `backup_physical_last_status`
5. `backup_upload_success`
6. `time() - backup_last_success_timestamp`

This lets you distinguish:

- total backup failure
- logical-only failure
- physical-only failure
- upload-only failure
- stale-success condition

## Observability UI / Grafana Fixes (June 26 2026)
- **Threshold Fixes:** Corrected hardcoded `80` thresholds across size (now uses bytes with limits at 5 GiB / 10 GiB) and duration panels (limits at 15m / 30m).
- **Timestamp Alerts:** Adjusted `Last Success Age`, `Current Run Start`, etc., to prevent false-positive red alerts on standard Unix timestamps.
- **Visual Consistency:** Enforced `colorMode: "value"` across all Stat panels for uniform dark backgrounds and clear typography. Added explicit `noValue: "Idle"` mapping.
- **Validation Status mapping:** Added explicit `-1` to `Not Validated` mapping to prevent raw fallback numbers.
- **Graph Modes:** Changed step behaviour away from `area` continuous slopes to `none` (staircase style) for distinct backup runs.
- **Loki Data Source Provisioning:** Resolved missing datasource variables (`${DS_LOKI}`) by explicitly defining the `sdl-db-backup-loki` UID in both the dashboard JSON and `grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml`.
- **Promtail Connectivity:** Fixed file permissions on `/mnt/volume_1/backup/mysql_backup/logs/` (requiring `755` for traversal and `644` for files) to allow Promtail to ingest logs without access denied errors.
