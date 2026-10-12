# Automatic local storage recovery — October 12, 2026

This rollout implements the operator-authorized local disk-pressure policy.
Deployment is APPLIED and automatic local cleanup is enabled. The production
validation capture and its independent local/S3 verification passed.

## Protection and configuration

The primary destination is `remediation/production-backups` on the workspace
filesystem. Original-volume backups are outside cleanup scope. No S3 deletion
is enabled. Restore testing remains disabled. No secondary directory is
approved; when protected coverage or safe capacity cannot be established,
cleanup is held and an insufficient-capacity run fails CRITICAL.

Before rollout, local points `2026-10-12_17-26-25` and
`2026-10-12_18-00-03` are protected. Their independent S3 verification is recorded
in `storage-dry-run.json`; checksum/gzip/object verification is not restore proof.
The 14:23 manual capture remains outside the automatic deletion destination.

The production settings enable `BACKUP_STORAGE_AUTO_CLEANUP=true`, retain
`BACKUP_RETENTION_DELETE_ENABLED=false`, and use high/low watermarks 92/90,
12 complete historical runs, 50% growth, 512 MiB concurrent-write allowance,
256 MiB temporary allowance, 2 GiB reserve and 128 free inodes. Expired temporary
artifacts need at least 24 hours; verification has a 20-minute context budget.
`BACKUP_STORAGE_ESTIMATE_BYTES=0` uses measured history with a 1 GiB floor.

Only validated regular single-link payload files in recognized timestamp runs
can be deleted. Manifests, audit history, keys, configuration, pinned/tested
points, unique partial data, physical components and recovery dependencies
remain. INTENT/outcome audits are fsynced under the configured log directory's
`storage-deletions/`. Interrupted intents revalidate eligibility and identities.

## Verification

Deterministic tests cover low-space capture plus complete independently verified
S3 publication; all-protected/failed-verification refusal; partial/complete
ordering; unexpected active-run disk exhaustion; larger-than-history captures;
concurrency; interrupted cleanup; network failure; and 150 scheduled captures
at 02:00, 06:00, 12:00, 16:00 and 18:00 across 30 days. Physical components,
restore-tested points, pins and binlog-dependent points remain protected.
The entire Go regression suite, race suite and vet passed before deployment.
The deployed-tree regression suite also passed. An additional race-tested runner
failure fixture confirms a failed run, FAILED recovery state, nonzero exit and
emitted critical metric when protected points cannot provide capacity. Five
disposable deployment tests verify rollback, drift refusal and symlink refusal.
See [test evidence](storage-test-results.json).

No artificial pressure is applied to production. A live dry run validates
real protection rules and performs no unlink. Live rollout verification records
actual configuration/source hashes, system timer state and a new six-database
capture. Alert source is updated, but external alert delivery remains unverified.

## Rollback and unavoidable failures

Disable automatic deletion by saving `BACKUP_STORAGE_AUTO_CLEANUP=false` in the
production environment file. Capture capacity gates remain active. Full release
rollback, when the service is idle and away from a scheduled trigger:

```bash
python3 remediation/deploy_storage_manager.py --rollback-latest
```

The deployment journal preserves original source, binaries and configuration;
rollback changes no timer and deletes no backup. Rollback cannot recreate a
payload already safely evicted later; its independently verified S3 counterpart
and local recovery metadata remain. Do not restore data into production.

Logical remote-only streaming is not enabled because the current integrity
contract validates a staged artifact and complete object inventory. Avoiding that
contract would require a separate change and validation. Physical capture keeps
existing streaming and checks work-directory capacity independently. Neither
workflow deletes protected physical components to create space.

Other filesystem consumers, rapid growth, S3 outages and insufficient eligible
history can still prevent capture. They produce honest failure/CRITICAL state.
There is no guarantee that a backup can never fail. Restore recovery and physical
preparation remain UNVERIFIED; no production-recovery readiness is claimed.

## Deployed capture evidence

The enabled manager admitted production validation run `2026-10-12_20-21-17`
without deletion because available capacity was sufficient. It captured all six
databases in one shared transaction and committed the complete S3 copy at
20:31:59 IST. Total console duration was 10m42.896s; the immutable committed
manifest records 10m42.634s before final execution bookkeeping. The compressed
bundle is 1,078,484,392 bytes and contains the required inventory of 921 tables,
382 views and 13 triggers, with zero routines/events and no object omissions.

[Independent verification](storage-production-backup-proof.json) read all remote
and local payload bytes and verified marker/database inventory, SHA-256, gzip,
SQL hash and footer. [Deployment proof](storage-deployment-proof.json) confirms
enabled settings, runtime file hashes, environment owner/mode and authoritative
configuration source. Systemd remains in system scope, with every daily slot and
Sunday physical slot installed and no schedule drift. Next trigger: October 13
02:00 IST. This validation capture was manually initiated; the next timer
invocation with the new manager has not yet been observed.

No production backup payload was deleted during this rollout. The successful
low-capacity deletion/capture/S3 sequence is a deterministic simulation using
real fixture files and mock S3, not artificial filling of production storage.

## Capacity and cleanup dry runs

At 20:49 IST the primary filesystem had **41,361,891,328 free bytes** of
332,805,349,376 total, with 39,268,313 free inodes. The latest measured capture
is 1,078,484,392 bytes; five captures/day accumulate approximately **5.39 GB/day**
without cleanup. The next-capture requirement is **4,570,516,604 bytes**, including
all configured allowances and reserve. Measured compressed growth from the
18:00 capture was 259,268 bytes (about 0.024%); this short history cannot establish
a long-term database growth rate.

[Pre-enable dry run](storage-dry-run.json): both existing local points and their
S3 copies independently verified; both KEEP; zero deletions/reclaimed bytes.
[Post-capture dry run](storage-post-capture-plan.json): the latest two protected
local and offsite points are **2026-10-12_20-21-17** and
**2026-10-12_18-00-03**. The older **2026-10-12_17-26-25** logical payload
(1,078,155,104 bytes) is eligible for future pressure cleanup after its complete
S3 counterpart passed independent verification. Its metadata remains protected.
Current usage is below the high watermark, so no deletion is necessary and no
production payload has been unlinked. The manual 14:23 capture and original
volume's historical backups remain outside the automatic deletion destination.

[Capacity evidence](storage-capacity-proof.json) projects **6.82 days** to the
capture-capacity gate without additional cleanup, and approximately **2.73 days**
to the first high-watermark cleanup if capture sizes and other writes stay stable.
Automatic cleanup maintains rolling local coverage instead of accumulating every
historical payload. These forecasts exclude unexpected growth and unrelated
writes. The separate physical streaming work filesystem has approximately
3.18 GB free; its capacity is checked independently and its recovery components
are never purged by this manager.

## Remaining operator step: external alert evaluation

[Monitoring evidence](storage-monitoring-proof.json) confirms Prometheus scrapes
automatic-cleanup enabled=1, critical=0, last-backup success=1 and capacity safe=1.
One active Alertmanager is configured. Capacity metrics describe the last
capture preflight; the TUI/health capacity check measures current storage.
The application emits critical logs and metrics, but the host's configured
`/etc/prometheus/rules/*.yml` contains no backup rules. Consequently external
backup notification delivery remains **BLOCKED/UNVERIFIED**. `promtool check
rules prometheus/sdl-db-backup-alerts.yml` passed all 12 rules. The attempted
noninteractive install failed with `sudo: a password is required`; no monitoring
configuration was changed.

From an operator terminal at this repository root:

```bash
sudo python3 remediation/apply_storage_alerts.py --apply
python3 remediation/verify_storage_monitoring.py
```

The installer preserves an existing rule file, validates rules and complete
Prometheus configuration, reloads the existing service, verifies the rule through
the local API and rolls back automatically on failure. Manual rule rollback:

```bash
sudo python3 remediation/apply_storage_alerts.py --rollback
```

No broad privileges or new external service are introduced. Do not send operator
passwords here. A loaded rule is not proof that a notification was delivered;
no production critical event was manufactured to test delivery.
