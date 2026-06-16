# Grafana Backup Monitoring Guide

This document defines a practical Grafana dashboard and alerting setup for the SDL DB backup service using Prometheus metrics emitted by the backup runner.

The metrics come from the Node Exporter textfile collector output written by this repo:

- default path: `/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom`

Base label set used in the examples below:

- `job="sdl_db_backup"`
- `service="mysql"`
- `env="pilot"`
- `region="us-east-1"`

To configure these labels, set the following in your `.env` file:
```bash
BACKUP_METRICS_JOB=sdl_db_backup
BACKUP_METRICS_SERVICE=mysql
BACKUP_METRICS_ENV=production
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
- `backup_physical_last_attempted`
- `backup_physical_last_status`
- `backup_physical_last_duration_seconds`

Live-run metrics:

- `backup_run_in_progress`
- `backup_current_run_start_timestamp`
- `backup_current_run_duration_seconds`
- `backup_metrics_last_update_timestamp`

## Recommended Dashboard Layout

### Row 1: Current State

#### Backup Running

Panel type:
- `Stat`

Query:

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"}
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
backup_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
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
backup_upload_success{job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Not Reached`
- `1 = Success`

#### Metrics Write Success

Panel type:
- `Stat`

Query:

```promql
backup_metrics_write_success{job="sdl_db_backup",service="mysql",env="pilot"}
```

#### Cleanup Success

Panel type:
- `Stat`

Query:

```promql
backup_cleanup_success{job="sdl_db_backup",service="mysql",env="pilot"}
```

#### Logical Backup Status

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Skipped`
- `1 = Success`

#### Physical Backup Status

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Failed/Skipped`
- `1 = Success`

## Row 2: Freshness

### Last Run Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_run_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Full Success Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_success_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
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
time() - backup_metrics_last_update_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

## Row 3: Duration and Size

### Current Run Duration

Panel type:
- `Stat`

Query:

```promql
backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Backup Duration

Panel type:
- `Stat`

Query:

```promql
backup_last_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Backup Size

Panel type:
- `Stat`

Query:

```promql
backup_last_size_bytes{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- bytes

### Last Physical Backup Duration

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

### Last Cleanup Age

Panel type:
- `Stat`

Query:

```promql
time() - backup_last_cleanup_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

Unit:
- duration / seconds

## Row 4: Time Series

### Current Run Duration Trend

Panel type:
- `Time series`

Query:

```promql
backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Last Backup Duration Trend

Panel type:
- `Time series`

Query:

```promql
backup_last_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Last Backup Size Trend

Panel type:
- `Time series`

Query:

```promql
backup_last_size_bytes{job="sdl_db_backup",service="mysql",env="pilot"}
```

## Row 5: Attempted vs Skipped

### Logical Attempted

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Skipped`
- `1 = Attempted`

### Physical Attempted

Panel type:
- `Stat`

Query:

```promql
backup_physical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"}
```

## Row 6: Logical Database Counts

### Logical Databases Total

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_total_databases{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Logical Databases Succeeded

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_succeeded_databases{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Logical Databases Failed

Panel type:
- `Stat`

Query:

```promql
backup_logical_last_failed_databases{job="sdl_db_backup",service="mysql",env="pilot"}
```

Value mapping:
- `0 = Skipped`
- `1 = Attempted`

## Prometheus Queries

### Status Queries

Overall:

```promql
backup_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
```

Upload:

```promql
backup_upload_success{job="sdl_db_backup",service="mysql",env="pilot"}
```

Logical attempted:

```promql
backup_logical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"}
```

Logical status:

```promql
backup_logical_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical attempted:

```promql
backup_physical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"}
```

Physical status:

```promql
backup_physical_last_status{job="sdl_db_backup",service="mysql",env="pilot"}
```

Running:

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Age Queries

Last run age:

```promql
time() - backup_last_run_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

Last full success age:

```promql
time() - backup_last_success_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

Metrics freshness:

```promql
time() - backup_metrics_last_update_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}
```

### Duration and Size Queries

Last completed duration:

```promql
backup_last_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

Current run duration:

```promql
backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"}
```

Last size:

```promql
backup_last_size_bytes{job="sdl_db_backup",service="mysql",env="pilot"}
```

## Useful Derived Expressions

### Logical Failed In Last Run

```promql
backup_logical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_logical_last_status{job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

### Physical Failed In Last Run

```promql
backup_physical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_physical_last_status{job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

### Backup Stuck Running

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"} > 7200
```

### Metrics Stale During Run

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
(time() - backup_metrics_last_update_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}) > 120
```

## Alert Rules

### Backup Failed Overall

Expression:

```promql
backup_last_status{job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Logical Backup Failed

Expression:

```promql
backup_logical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_logical_last_status{job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Physical Backup Failed

Expression:

```promql
backup_physical_last_attempted{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_physical_last_status{job="sdl_db_backup",service="mysql",env="pilot"} == 0
```

For:
- `5m`

### Backup Stuck Running

Expression:

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql",env="pilot"} > 7200
```

For:
- `10m`

### Metrics Stopped Updating During Run

Expression:

```promql
backup_run_in_progress{job="sdl_db_backup",service="mysql",env="pilot"} == 1
and
(time() - backup_metrics_last_update_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}) > 120
```

For:
- `2m`

### No Successful Backup Recently

Expression:

```promql
(time() - backup_last_success_timestamp{job="sdl_db_backup",service="mysql",env="pilot"}) > 43200
```

Example threshold:
- `12h`

Adjust to match your backup SLA.

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
