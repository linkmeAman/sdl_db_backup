# Prometheus Rule File

This directory contains the SDL DB backup alert rules and scrape example:

- `sdl-db-backup-alerts.yml`
- `sdl-db-backup-scrape-example.yml`

Add that file to your Prometheus `rule_files` list or your rule-evaluation pipeline.

Example:

```yaml
rule_files:
  - /etc/prometheus/rules/sdl-db-backup-alerts.yml
```

## Notes

- The rule file assumes the default label set used throughout this repo.
- If you use custom `BACKUP_METRICS_*` label values, update the selectors in the rule file to match.
- The scrape example shows the required Node Exporter textfile collector directory flag and a basic Node Exporter scrape job.
- When Prometheus scrapes the textfile collector output, queries should use `exported_job="sdl_db_backup"` instead of `job="sdl_db_backup"`.
