# Loki Log Streaming

Prometheus textfile metrics do not contain backup log lines. They only expose machine-readable state such as status, size, duration, and live in-progress timestamps.

If you want to see the actual backup logs in Grafana while a run is active, ship the backup log files to Loki.

This repo includes:

- `promtail-config.yml`: example Promtail file-scrape config for SDL DB backup run logs
- `../dashboards/sdl-db-backup-logs.json`: importable Grafana logs dashboard
- `../grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml`: optional Grafana Loki datasource provisioning example

## Why File Scraping

The backup runner writes both:

- a per-run log file like `2026-06-26_10-18-04.log`
- a daily aggregate log like `2026-06-26.log`

The Promtail example scrapes only the per-run files with `*_*.log` so Grafana shows each backup once without duplicate lines from the daily aggregate log.

File scraping is also preferred here because it captures:

- scheduled systemd runs
- manual TUI runs
- direct CLI runs

without depending on journal access.

## Quick Start

1. Copy `promtail-config.yml` to your Promtail host.
2. Replace the `__path__` value so it matches `BACKUP_LOG_DIR/*_*.log`.
3. Start or reload Promtail.
4. Add Loki to Grafana with `../grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml` or configure the datasource manually.
5. Import `../dashboards/sdl-db-backup-logs.json`.

## Copy/Paste Setup

Use these commands on a host where Promtail and Grafana are installed.

```bash
sudo install -d -m 755 /etc/promtail /var/lib/promtail

sudo cp /var/www/go-workspace/sdl/sdl_db_backup/loki/promtail-config.yml \
  /etc/promtail/promtail-config.yml

sudo systemctl enable --now promtail
sudo systemctl restart promtail
sudo journalctl -u promtail -n 50 --no-pager

sudo install -d -m 755 \
  /etc/grafana/provisioning/dashboards \
  /etc/grafana/provisioning/datasources \
  /var/lib/grafana/dashboards/sdl-db-backup

sudo cp /var/www/go-workspace/sdl/sdl_db_backup/grafana/provisioning/dashboards/sdl-db-backup-dashboard-provider.yml \
  /etc/grafana/provisioning/dashboards/sdl-db-backup-dashboard-provider.yml

sudo cp /var/www/go-workspace/sdl/sdl_db_backup/grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml \
  /etc/grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml

sudo cp /var/www/go-workspace/sdl/sdl_db_backup/dashboards/sdl-db-backup-observability.json \
  /var/lib/grafana/dashboards/sdl-db-backup/

sudo cp /var/www/go-workspace/sdl/sdl_db_backup/dashboards/sdl-db-backup-logs.json \
  /var/lib/grafana/dashboards/sdl-db-backup/

sudo systemctl restart grafana-server
```

If Promtail cannot read the backup logs, fix the log directory permissions so the Promtail user can traverse and read the per-run files:

```bash
sudo chmod 755 /mnt/volume_1/backup/mysql_backup/logs
sudo chmod 644 /mnt/volume_1/backup/mysql_backup/logs/*_*.log
```

If Loki is not on the same host, edit the datasource URL in
`/etc/grafana/provisioning/datasources/sdl-db-backup-loki-datasource.yml`
before restarting Grafana.

## Labels Used By The Example

The sample Promtail config attaches:

- `job="sdl_db_backup"`
- `service="mysql"`
- `env="pilot"`
- `log_kind="run"`

Unlike Prometheus textfile metrics, Loki keeps `job` as `job`. Do not use `exported_job` in LogQL.

## Example LogQL

Full per-run backup logs:

```logql
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"}
```

High-signal lifecycle events only:

```logql
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"} |~ "mysql full backup started|run log file:|logical backup:|physical backup:|starting dump|completed dump|s3 upload|backup summary|backup failure reason"
```

Failure-oriented lines:

```logql
{job="sdl_db_backup",service="mysql",env="pilot",log_kind="run"} |~ "failed|warning|permission denied|rate limit|retry"
```

## What You Will See During A Running Backup

Typical lines include:

- `mysql full backup started`
- `run log file: ...`
- `logical backup: due now ...`
- `starting dump for database=...`
- `completed dump for database=...`
- `s3 upload: ...`
- `backup summary status=...`

If you keep the dashboard time range around the active run and use ascending sort, Grafana shows the run from the beginning instead of dropping you into the latest lines first.
