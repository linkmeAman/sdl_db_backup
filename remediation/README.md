# Backup reliability remediation — October 12, 2026

This is the dated production investigation record. See
[PRODUCTION_CONFIGURATION.md](../PRODUCTION_CONFIGURATION.md) for the applied
configuration and reusable operating guidance. Local deployment snapshots,
private inventories and SQL migration files referenced below remain on the
production host and are intentionally excluded from Git. Compact redacted
verification evidence is versioned.

## 1. Current verdict

**VERIFIED:** scheduled production run `2026-10-12_18-00-03` captures all six required databases in one MySQL transaction. The earlier protected capture `2026-10-12_14-23-04` is also retained. The latest independently verified 1,078,225,124-byte `all-databases.sql.gz` contains the expected 918 base tables, 382 views and 13 triggers; no routines or events were present. Gzip integrity, SQL checksum, expected object names and the complete remote payload were checked. Both the S3 dump and manifest pass independent size and SHA-256 checks. Offsite destination: `s3://ticklerightbackups/recovery-hold/logical/2026-10-12_18-00-03/`. SSE-S3 encryption is reported as AES256. Evidence: [deployed-backup-proof.json](deployed-backup-proof.json) and [earlier capture proof](final-backup-proof.json).

**UNVERIFIED:** recovery by restoring these artifacts. No restore was performed or will be performed under the current scope. The verified finding is capture and offsite integrity. **NOT YET VERIFIED FOR PRODUCTION RECOVERY.**

**DEPLOYED:** approved source/binary changes and production configuration are live. The operator applied the timer correction; independent systemd/TUI checks report `Scope=system` and `ScheduleDrift=false`. `.env` is readable by the developer service account at mode 0600. The approved view-definer correction is live. The immutable-manifest correction is also deployed. The automatic 18:00 service run succeeded in 11m30s; next timer trigger is October 13 at 02:00 IST.

## 2. Root causes

| Finding | State and evidence |
| --- | --- |
| Error 1356 for `pf_TickleRight_9210.co_eff_monthly_view_new` | **CONFIRMED:** the view used absent `developer@localhost`. Existing `developer@%` can evaluate the unchanged definition. Approved definer-only correction verified all 382 view prechecks. [Proof](evidence/sdl-view-fix-proof.json). Original production log: `/mnt/volume_1/backup/mysql_backup/logs/2026-10-12.log:146`. No grants or business-query changes were made. |
| Further dump error 3024 | **CONFIRMED:** long statements exceed `GLOBAL max_execution_time=30000`. The successful private MySQL 8.4.11 client initializes **only its dump session** with `max_execution_time=0`; the production global remains 30000. The initial failed run and incomplete files were preserved. |
| Predictable storage failure | **CONFIRMED:** `/mnt/volume_1` is 99% full, with 3,753,086,976 bytes available at the initial capacity check. Inodes were available. Backup cleanup occurs after capture. At the pre-deployment check, the alternative workspace filesystem had 47,038,832,640 available bytes; the applied new destination is on that filesystem. |
| Unsafe date-based retention | **CONFIRMED:** original cleanup selected directories by age without establishing complete recovery coverage. Logs show the October 10 noon local recovery point removed on October 11. Newer partial runs could displace healthy runs. |
| Misleading S3 completion | **CONFIRMED:** noon October 12 manifest is partial, with five captures, while the original uploader publishes `COMPLETED.json`. All five surviving payloads individually pass checksums; this remains a partial recovery point. [Evidence](evidence/sdl-current-remote-checksums.json). |
| Schedule mismatch and wrong TUI manager | **CONFIRMED:** the original installed `/etc/systemd/system/sdl-db-backup.timer` had 02:00, 06:00, 12:00, 18:00; `.env` additionally requests 16:00. Original TUI invokes `--user` despite system-level production units. Exact reason for the historical manual installation drift is **UNVERIFIED**; old installation/control paths did not reliably apply the desired configuration. |
| Truncated physical verification | **CONFIRMED:** original listing accounts for only the first 1,000 objects. October 11 physical prefix has approximately 3,880 objects and 20.4 GB, whereas its marker reports approximately 4.2 GB. Presence of checkpoints, tablespaces and a marker does not verify preparation or recoverability. |
| HTTP 404 and timeout history | Current October 10/11 logical S3 objects return `NoSuchKey`; version lookup returns `AccessDenied`. Historical 404 causes beyond these absent keys remain **UNVERIFIED**. Request timeouts and transient HTTP/XML codes have deterministic retry tests; deterministic schema and permission failures are not retried as network faults. |

Phase 0 evidence: [redacted inventory](evidence/phase0-inventory.json). October 8 18:00:01 is the latest retained historical six-database local capture that passed individual dump integrity checks ([proof](evidence/sdl-local-capture-check.json)); those separate dumps do not establish one shared snapshot. October 10's former complete local logical run is no longer present, and current offsite copies for October 10/11 logical runs were not found. Historical October 9 and October 11 physical backups remain preserved and **UNVERIFIED**. S3 versioning, Object Lock and bucket encryption policies could not be inspected with current permissions.

## 3. Implementation

Approved production changes are installed in the existing application. [staged](staged/) retains the reviewed candidate, [reliability.patch](reliability.patch) contains the cumulative source change, and [changed-files.json](changed-files.json) lists 35 source, test and documentation files. [package-manifest.json](package-manifest.json) pins the candidate and baseline hashes; protected rollback journals retain original production source, binaries and configuration.

* `internal/backupapp/global_capture.go` and `ddl_guard.go`: one streaming gzip dump for all required InnoDB databases, under one transaction and a verified `LOCK INSTANCE FOR BACKUP` connection. Source inventories and captured schema object names must match. Existing BACKUP_ADMIN authorization is used; no privileges are added. Unsupported engines or loss of the guard fail closed.
* `reliability.go`, `schema_inventory.go`, `database.go`: version 2 manifests retain legacy fields and add explicit run state, expected databases/objects, consistency method, artifact hashes, remote verification and separate restore state. Missing databases, omitted objects, truncated gzip, absent dump footer, incorrect checksum or inventory discrepancy withhold completion. The versioned `DROP VIEW` parser defect found during live capture was corrected and regression-tested; the retained artifact was fully reverified before reconciliation.
* `s3.go`, `multipart_journal.go`, `physical_integrity.go`: paginated inventory, bounded retries with jitter, independent full-payload verification, complete-marker publication last, and persisted ownership records for recovering interrupted multipart uploads. Physical uploads require source checksums and verify every chunk; multipart ETags are not treated as content hashes. Failed/partial markers cannot establish completion. Existing completed keys cannot be overwritten.
* `storage.go`: historical compressed-size estimation with growth allowance, configurable reserve, inode check, and reserve enforcement while streaming. Applied production estimate requires at least 6 GiB free: twice the configured 2 GiB capture estimate plus a 2 GiB reserve. Interrupted files are preserved with incomplete names and failure state.
* `retention.go`, `cleanup.go`: deletion is disabled by default. Dry-run policy uses verified complete recovery points, protects recent complete coverage, and holds unknown, partial, corrupt, physical and dependency-bearing points. Explicitly enabled cleanup uses journaled quarantine and never automatically purges it. No production backup has been deleted by this remediation.
* `systemd.go`, `schedule_apply.go`, `internal/tui`: correct service-manager selection, desired/effective calendars, drift, next/last triggers, result and configuration source. All repeated `TimersCalendar` entries are retained; verification rejects missing or extra triggers and timezone drift. Applying a timer is explicit, serialized and rolled back on failure.
* `durability.go` and `manifest_commit.go`: settings writes retain the original owner/group; ownership-preservation failures stop before replacement. Committed local/remote manifest bytes remain immutable, with later execution bookkeeping stored in an audit and the run index. Active capacity metrics read actual available storage.
* `recovery_health.go`, `metrics.go`, Prometheus rules: capture integrity, remote verification, recovery-point age, per-database failures and capacity are exposed. Existing notification infrastructure is retained; rules still need the normal monitoring deployment/reload procedure.

Run states are `RUNNING` until capture/upload checks finish; all required captures and configured remote verification are necessary for `COMPLETE`. A useful subset is `PARTIAL`; a failed capture is `FAILED`. Restore verification is a separate field and remains `UNVERIFIED`. No configuration flag can manufacture `RESTORE_TESTED` evidence. The existing restore path is guarded against treating the new shared bundle as separate per-database files; no restore workflow was implemented.

## 4. Verification

**PASS:** final staged `go test ./...`, `go test -race ./...`, and `go vet ./...`. **PASS:** three deployment transaction fixture tests (`python3 -m unittest remediation/test_deploy_candidate.py -v`) cover preserved unknown configuration/secrets, drift rejection and rollback after interrupted writes. Built runner, TUI, health and schedule-helper executables.

Deterministic regressions cover six successes versus five, invalid views, missing/schema-mismatched/corrupt artifacts, insufficient capacity, interrupted dumps, upload corruption, retryable timeouts, multipart ownership/recovery, 1,001 listed objects, timer mismatch/wrong scope/rollback/timezone/extra triggers, failed-newer-run retention, interrupted quarantine, legacy manifests and concurrent locks. Physical chunk corruption fails verification. Physical preparation and isolated restore success are **NOT VERIFIED**, and no restore failure simulation is represented as a live restore test.

The earlier shared capture duration was **5m3s**; the deployed run records **5m35s** including capture-stage checks; the scheduled 18:00 capture stage was **5m21s**, with total execution **11m30s**. Deployed total execution was **11m56s**, measured in its preserved finalization audit and run index. The earlier 14:23 manual run took **2h18m44s**, including the initial validation defect, investigation and reconciliation; this is not an unattended upload-duration baseline. The earlier manual run verification finished at **16:46 IST**; the scheduled 18:00 run independently passed complete local/remote verification at **18:16 IST**. Artifacts remain under `remediation/captured-backups/2026-10-12_14-23-04/`, separate from the original automatic-cleanup destination. All original retained backups and failed-run files remain preserved.

## 5. Risks and pending authorization

The operator approved production deployment and the timer correction. Applied configuration: [production-overrides.env](production-overrides.env). It changes new logical output to `remediation/production-backups`, remote prefix to `recovery-hold/logical`, retains original logs and shared lock, enables global consistency, disables deletion, selects system scope and explicit Asia/Kolkata timezone, and disables the configured restore-test flag as requested. It preserves desired backup frequency and all other configuration values.

The private MySQL client was extracted without changing system packages from the official HTTPS MySQL repository, with repository-advertised SHA-256 verified. Package signature verification was not independently established. The exact deployed client binary is pinned by the package manifest and was used successfully against the source MySQL 8.0.46. Source checkpoints are observations, not a verified offsite binlog archive or guaranteed PITR chain.

Reviewed deployment commands (already authorized; deployment refuses baseline drift rather than overwriting operator changes):

```bash
# Review only; no production mutation.
python3 remediation/deploy_candidate.py

# Deployment: shared lock, idle-service/window checks,
# protected rollback journal, baseline/hash checks, atomic individual files.
python3 remediation/deploy_candidate.py --apply

# Timer transaction applied by operator; requires narrow privilege.
sudo remediation/releases/candidate/sdl-db-backup-schedule --env /var/www/go-workspace/sdl/sdl_db_backup/.env --apply
```

The timer helper installs only `/etc/systemd/system/sdl-db-backup.timer.d/90-sdl-schedule.conf`, reloads systemd, restarts the active timer and verifies all effective calendars. Intended logical times are 02:00, 06:00, 12:00, **16:00**, 18:00 Asia/Kolkata. The existing Sunday 02:00 physical trigger is included without adding a second scheduler. Persistent timers may launch a catch-up run when the missing time is introduced. No broad sudo grant is proposed; run this exact helper through existing operator privileges.

Deployment restores originals automatically on a caught failure. A crash leaves a PREPARED journal for explicit rollback. Files are individually atomic; the application lock and the deployment window prevent a running backup from reading an intermediate package. Repeated deployment refuses baseline drift. Rollback affects package source, binaries and configuration, not backup artifacts:

```bash
python3 remediation/deploy_candidate.py --rollback-latest
# To restore the complete original package instead of only the latest correction:
python3 remediation/deploy_candidate.py --rollback remediation/rollback/20261012T171812
```

If the timer transaction itself fails, it restores its previous drop-in/environment and reloads the prior state. To undo a successful new timer later, the currently absent original drop-in can be restored by moving the managed addition out of systemd, then reloading and restarting the timer:

```bash
sudo mv /etc/systemd/system/sdl-db-backup.timer.d/90-sdl-schedule.conf /var/www/go-workspace/sdl/sdl_db_backup/remediation/rollback/timer-dropin.disabled
sudo systemctl daemon-reload
sudo systemctl restart sdl-db-backup.timer
```

The approved view correction has separate [rollback SQL](view-definer-rollback.sql). Reverting it would intentionally recreate the missing-definer failure; execute only if explicitly required by the operator. Do not use `--force` or enable object omissions.

## 6. Remaining work

1. **VERIFIED:** the final package is deployed, all 38 file hashes match, the service-account configuration is readable, and TUI/systemd checks report system scope, no schedule drift and a successful service result. The automatic 18:00 run captured six databases and published its verified offsite marker in 11m30s. [Deployment evidence](evidence/deployed-package-proof.json); [timer/TUI evidence](evidence/deployed-systemd-status.json).
2. **VERIFIED:** the 18:00 timer started the actual production service and it exited successfully with all six databases captured. The deployed 17:26 verification run succeeded in 11m56s and its complete local/remote payload hashes pass independent checks ([proof](evidence/17-26-25-independent-proof.json)); the scheduled 18:00 run has its own [independent proof](deployed-backup-proof.json). Physical source-checksum integration remains UNVERIFIED; the installed xbcloud 8.0.35 help confirms `--md5` support. Preserve historical physical runs.
3. **UNVERIFIED:** long-term capacity policy. The measured complete logical artifact is approximately 1.08 GB, so five daily captures alone consume approximately 5.4 GB/day before failed attempts and growth. Roughly 44 GB remaining on the new capture filesystem is a short bridge, approximately seven days at current capture size before the 6 GiB preflight threshold, excluding growth and failed attempts. The original volume still has only approximately 3.2 GB available. No automatic deletion is proposed. Revisit a verified retention policy and provision capacity before the reserve is reached.
4. **UNVERIFIED:** S3 versioning/immutability/access policy, independent offsite binlog archiving, and end-to-end alert delivery. Read permissions for bucket policies and versions remain insufficient; alert rule source was updated but live delivery/reload has not been proven. Seek narrowly scoped read access before proposing policy changes; no new permissions were granted.
5. **UNVERIFIED, outside current requested scope:** restore recoverability and RTO. No restore work will proceed. Desired schedule implies a largest snapshot interval of eight hours, plus capture and remote-publication delay. The normal deployed verification run took 11m56s, suggesting a provisional schedule-based RPO of about 8h15; one observation does not establish an SLA. Confirm unattended latency, retries and business tolerance before committing a target. RTO remains unmeasured.

Acceptance: six-database capture and offsite integrity **VERIFIED**; partial-completion/retention/storage/pagination/concurrency safeguards **TESTED AND DEPLOYED**; production schedule and TUI scope **VERIFIED**; committed-manifest immutability **TESTED AND DEPLOYED**; isolated recovery **UNVERIFIED**. This report deliberately does not declare production recovery readiness.

Operational consistency limitation: the snapshot shares one transaction across all selected InnoDB databases. Maintain a schema-deployment freeze during capture; MySQL's backup lock blocks operations affecting database files and is not a blanket guarantee for every metadata operation. Object-name inventories detect added/removed objects; they do not independently prove identical SQL definitions under arbitrary concurrent metadata changes. See [MySQL backup-lock documentation](https://dev.mysql.com/doc/refman/8.0/en/lock-instance-for-backup.html).

Follow-up findings and corrections: the privileged timer helper initially replaced `.env` under a different owner, causing its 17:29 catch-up attempt to fail with permission denied. The exact approved deployment configuration was restored, the replaced file preserved in the protected rollback journal, and atomic settings writes now preserve original ownership. Independent verification also found local/remote manifest drift limited to `ended_at` and `duration`; capture semantics and payload hashes matched. The exact committed S3 manifest was restored locally with an audit retained ([proof](evidence/manifest-finalization-repair.json)). The tested finalization correction freezes committed manifests and writes subsequent execution metadata separately.

Final deployment rollback journal: `remediation/rollback/20261012T181355`. The earlier original-package journal remains `remediation/rollback/20261012T171812`. Original remote manifests for the 17:26 and 18:00 runs were preserved unchanged; their remote duration field predates the ordering fix and is empty. Full measured durations remain in each preserved local finalization audit and run history. Future runs populate duration before freezing the uploaded manifest.

The physical checksum path follows Percona's documented `backup_name.md5` convention ([xbcloud 8.0 documentation](https://docs.percona.com/percona-xtrabackup/8.0/xbcloud-binary-overview.html)). It is fixture-tested and supported by the installed binary help; an actual new physical capture/verification remains unverified.
