# SDL DB Backup Observability Bundle

This folder groups the observability assets for the SDL DB backup service.

## Included Assets

- `../GRAFANA_BACKUP_MONITORING.md`: metric catalog, PromQL examples, dashboard layout, and alert explanations
- `../dashboards/sdl-db-backup-observability.json`: importable Grafana dashboard template
- `../dashboards/sdl-db-backup-logs.json`: importable Grafana logs dashboard for live backup log streaming through Loki
- `../prometheus/sdl-db-backup-alerts.yml`: importable Prometheus alert rule file
- `../prometheus/sdl-db-backup-scrape-example.yml`: minimal Node Exporter scrape example showing the required textfile collector path
- `../loki/README.md`: Loki and Promtail setup notes for shipping backup logs to Grafana

## Import Order

1. Import the dashboard JSON into Grafana.
2. Load the alert rule file into Prometheus or your rule-evaluation layer.
3. If you want actual backup log lines in Grafana, ship `BACKUP_LOG_DIR/*_*.log` to Loki with `../loki/promtail-config.yml`.
4. Use the metric catalog in `GRAFANA_BACKUP_MONITORING.md` to adapt labels, thresholds, or panel names.

## Default Label Set

The metrics file writes these labels:

- `job="sdl_db_backup"`
- `service="mysql"`
- `env="pilot"`

When you query the scraped metrics in Prometheus or Grafana, use `exported_job="sdl_db_backup"` because Node Exporter textfile collector exposes the metric's original `job` label as `exported_job`.

If you set `BACKUP_METRICS_REGION`, add `region="..."` to the selectors.

## What To Build First

- `backup_run_in_progress`
- `backup_last_status`
- `backup_upload_success`
- `backup_last_success_timestamp`
- `backup_last_duration_seconds`
- `backup_last_size_bytes`
- `backup_metrics_write_success`
- `backup_cleanup_success`
- `backup_logical_validation_last_status`

For live backup logs, Prometheus is not enough. Use Loki and the bundled logs dashboard with the same `job="sdl_db_backup", service="mysql", env="pilot"` label set.
