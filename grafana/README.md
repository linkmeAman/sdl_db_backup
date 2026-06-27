# Grafana Provisioning

This directory contains minimal Grafana provisioning examples for the SDL DB backup metrics and logs dashboards.

The dashboard queries are written for the scraped Prometheus label `exported_job="sdl_db_backup"`, which is how Node Exporter textfile collector exposes the runner's original `job` label.

## What To Use

- `provisioning/dashboards/sdl-db-backup-dashboard-provider.yml`
- `provisioning/datasources/sdl-db-backup-loki-datasource.yml`

Copy that provider file into Grafana's provisioning directory and set the dashboard path to wherever you mount:

- `dashboards/sdl-db-backup-observability.json`
- `dashboards/sdl-db-backup-logs.json`

If you want real backup log lines in Grafana, provision Loki and ship `BACKUP_LOG_DIR/*_*.log` with the sample config in:

- `../loki/promtail-config.yml`

## Copy/Paste Provisioning

```bash
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

## Recommended Mount Layout

- Grafana dashboard JSON files in a mounted directory
- Grafana dashboard provider pointing at that mounted directory
- Optional Loki datasource provisioning file if you want log panels too

Example folder contents:

```text
/var/lib/grafana/dashboards/sdl-db-backup/sdl-db-backup-observability.json
/var/lib/grafana/dashboards/sdl-db-backup/sdl-db-backup-logs.json
```

Then point the provider `options.path` at that directory.
