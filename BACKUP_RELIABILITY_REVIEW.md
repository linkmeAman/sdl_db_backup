# Backup Reliability Review

**Review date:** 2026-09-03
**Scope:** Backup creation, encryption, S3 upload, physical backup, recovery, retention, and verification paths in this repository.

## Executive summary

The application already includes useful operational foundations: logical and physical backup paths, manifests, artifact hashes, retries, GFS-style local retention, metrics, and optional sandbox restore checks. However, several gaps can cause a backup to be reported as successful without proving that a complete, recoverable copy exists off-host.

The most urgent issue is encrypted logical backups. The installer creates an encryption key by default, but the current logical validation and SQL-payload hashing paths attempt to read the encrypted output directly as gzip. This makes encrypted logical backups fail after creation and prevents the validation, inspect, and restore paths from handling `.sql.gz.enc` artifacts.

## Implementation status

The following changes have been implemented in this working tree:

- encrypted logical artifacts now use versioned, chunked AES-256-GCM; exact raw or hexadecimal key lengths are enforced, legacy CFB artifacts remain readable for migration, and the validation, sandbox restore, inspect, and CLI recovery paths understand `.sql.gz.enc` files;
- direct S3 upload verifies every uploaded object size and writes `COMPLETED.json` last; physical streaming verifies its object exists and writes `PHYSICAL_COMPLETED.json` last;
- full backups fail when objects would be omitted unless `BACKUP_ALLOW_OBJECT_OMISSIONS=true` is explicitly set, and table-scoped or omission-based backups are recorded as `partial` rather than full success;
- the newest configured number of sandbox-restore-verified local runs is protected from GFS cleanup;
- manifests, schedule state, temporary overrides, and run-history appends are made durable against interrupted writes;
- remote uploads, physical backups, and root-runner behavior now default to explicit opt-in.

The implementation now includes a prepare-only physical validation command,
PITR recovery metadata, PostgreSQL restore parity, and multipart S3 uploads.
The remaining operational work is to schedule those drills, retain and replay
binary logs, configure bucket versioning/Object Lock/cross-account replication,
and run live MySQL/PostgreSQL/MinIO integration tests. Those controls require
target infrastructure and cannot be safely inferred or enabled from this
repository alone.

## Remaining priority order

| Priority | Issue | Why it matters |
| --- | --- | --- |
| P0 | Physical backups need recurring, isolated restore drills | The code can prepare a remote stream, but scheduling and an actual server-start drill are operator responsibilities. |
| P1 | PITR needs independent binary-log archival and rehearsal | Coordinates are captured, but the application cannot safely manage a source server's binlog archive policy. |
| P1 | Direct S3 uploads need operational lifecycle monitoring | Multipart transfer is implemented; lifecycle and incomplete-upload cleanup need bucket policy. |
| P1 | Live recovery integration tests are not yet automated | Unit tests cannot prove compatibility with real MySQL, PostgreSQL, and S3 deployments. |
| P2 | Physical-drill observability is limited | Record and alert on physical-drill freshness, and enrich physical metadata where supported. |

## Detailed findings

### P0 — encrypted logical backups are not recoverable end to end

#### Evidence

- Logical backup filenames use `.sql.gz.enc` when `BACKUP_ENCRYPTION_KEY` is configured.
- The dump writer encrypts the file before the gzip stream is written.
- After dumping, `dumpWithRetry` calls `gzipPayloadSHA256` directly on that encrypted file.
- Logical validation, full restore testing, and inspection expect `.sql.gz` files and do not decrypt `.enc` files.
- The installer automatically generates an encryption key for a new configuration, so this path is enabled by default for fresh installs.

Relevant implementation: `internal/backupapp/app.go`, `internal/backupapp/database.go`, `internal/backupapp/crypto.go`, `internal/backupapp/restore.go`, `internal/backupapp/inspect.go`, and `install.sh`.

#### Impact

An encrypted backup can be written successfully but then fail hashing or validation. Even if an artifact were retained, the normal operator tools cannot inspect, validate, or restore it. In addition, AES-CFB is not authenticated: an attacker or storage corruption can alter ciphertext without guaranteed detection. The key helper silently pads or truncates arbitrary key strings; the installer's 64-character hexadecimal string is treated as text and truncated rather than decoded as a 32-byte key.

#### Recommendation

1. Immediately disable automatic encryption generation until encrypted artifacts are supported end to end, or deliver the complete fix as one release.
2. Replace CFB with authenticated encryption, such as an `age` envelope or a chunked AES-GCM format suitable for streaming.
3. Define a versioned artifact format containing algorithm, key ID, nonce/chunk metadata, and authenticated metadata.
4. Reject invalid keys. If hexadecimal/base64 encoding is supported, decode it explicitly and require exactly 32 key bytes.
5. Make hashing, validation, restore testing, and inspection open encrypted artifacts through one shared decrypting reader.
6. Add integration tests for encrypted artifact creation, corruption detection, validation, and restore.

### P0 — S3 uploads have no atomic completion protocol

#### Evidence

- The runner writes a manifest before the logical S3 upload begins.
- The directory walk uploads all files, including that pre-upload manifest.
- The runner then records upload success locally, but does not upload a final manifest or an explicit remote completion marker.
- A failed directory upload can leave a partial prefix in S3.

Relevant implementation: `internal/backupapp/app.go` and `internal/backupapp/s3.go`.

#### Impact

Operators or future restore tooling cannot safely distinguish a fully committed remote backup from a partially uploaded run. The object listing itself is not a reliable recovery contract.

#### Recommendation

1. Upload artifacts beneath a unique staging prefix, including checksums and sizes.
2. Verify every remote object with `HeadObject` and record version IDs/checksums.
3. Write a final, immutable `COMPLETED.json` object last. It must include run ID, artifact inventory, checksums, source host, database engine, completion time, and software version.
4. Treat only prefixes containing a valid completion object as restorable.
5. Ensure failed or abandoned staging prefixes are identifiable and cleaned by a separate lifecycle policy.

### Resolved in code — physical prepare-only restore validation

#### Evidence

- The physical stream upload verifies its remote size and writes
  `PHYSICAL_COMPLETED.json` only after that verification.
- `sdl-db-backup physical-validate <run-id>` downloads the completed stream to
  an isolated work directory, extracts it with `xbstream`, and runs
  `xtrabackup --prepare`.

Relevant implementation: `internal/backupapp/xtrabackup.go`.

#### Impact

The stream can now be independently downloaded, extracted, and prepared. A
prepare-only drill does not prove that an isolated MySQL instance will start or
that the application functions after recovery.

#### Recommendation

1. Schedule `physical-validate` on an isolated host and alert if it is stale.
2. Periodically start the prepared data in an isolated MySQL instance and run
   application smoke queries.
3. Extend the completion metadata and monitoring with version, LSN, and
   physical-drill timestamps when those values are available from the target
   XtraBackup/MySQL deployment.

### Partially resolved — MySQL PITR recovery metadata

#### Evidence

- MySQL logical dumps can use commented `--source-data=2` coordinates and
  persist each artifact's binlog file/position.
- The manifest can record the observed server UUID, version, GTID set, and
  binary-log state immediately before logical dumping begins.
- `RECOVERY_OPERATIONS.md` defines the isolated restore and binlog replay
  rehearsal sequence.

Relevant implementation: `internal/backupapp/database.go` and `internal/backupapp/xtrabackup.go`.

#### Impact

The artifact-level coordinate now establishes a replay boundary, but recovery
still depends on a complete independently retained binlog sequence.

#### Recommendation

Archive binary logs independently, define retention aligned with recovery
objectives, and execute the documented restore-and-replay rehearsal. Do not
mistake the run-level server observation for a shared snapshot across parallel
dumps.

### P1 — incomplete logical dumps can appear successful

#### Evidence

- MySQL dumping enables `--force` and ignores selected errors, including broken views and missing definers.
- Broken views may be excluded before a dump.
- A selected-table dump still records schema counts for the complete source database.

Relevant implementation: `internal/backupapp/database.go`.

#### Impact

The artifact may omit objects but be labelled successful. A user can mistake a table-scoped or object-skipping export for a complete database recovery point. The full restore validator can also compare an intentional partial restore against whole-database metadata.

#### Recommendation

1. Fail closed by default: an omitted object must fail a full-backup run.
2. Allow omission only through an explicit, named partial-backup policy.
3. Persist the requested scope, actual scope, and every omission with its reason in the manifest.
4. Use statuses such as `success_full`, `success_partial`, and `failed`; reserve a normal success for complete coverage.
5. Make restore validation compare only the declared scope for intentional partial backups.

### Resolved — PostgreSQL logical validation and sandbox restore parity

#### Evidence

- Configuration and documentation advertise `DB_ENGINE=postgres`.
- Logical validation now expects PostgreSQL's `pg_dump` completion marker when `DB_ENGINE=postgres`.
- Sandbox restore validation uses `psql`, creates a PostgreSQL-safe temporary database, imports with `ON_ERROR_STOP=1`, and queries PostgreSQL catalogs for the same restored-object, fingerprint, exact-row-count, and sampled-data validations as MySQL.
- PostgreSQL config defaults use port `5432` and restore user `postgres`; MySQL defaults are unchanged.

Relevant implementation: `internal/backupapp/config.go`, `internal/backupapp/app.go`, `internal/backupapp/database.go`, and `internal/backupapp/restore.go`.

#### Impact

PostgreSQL logical recovery verification now has parity with the MySQL workflow. It still requires a live PostgreSQL integration test in CI and an isolated, permission-limited restore target in production.

#### Recommendation

Maintain a disposable PostgreSQL restore integration test that exercises a real `pg_dump` artifact through download, validation, sandbox import, and cleanup.

### Resolved in code — resilient multipart direct S3 uploads

#### Evidence

- Artifacts at least 64 MiB use the S3 multipart protocol with 16 MiB parts.
- Each part is SHA-256 hashed and transient network, 429, and 5xx failures are
  retried with a fresh request body.
- Failed multipart transfers issue an abort request; a final `HEAD` check still
  occurs before the run's `COMPLETED.json` marker is written.

Relevant implementation: `internal/backupapp/s3.go`.

#### Impact

Large logical backups no longer rely on a single PUT. An interrupted process
does restart a multipart upload rather than resuming its existing upload ID;
the abort/lifecycle policy is still important for abandoned uploads.

#### Recommendation

Configure an S3 lifecycle rule to abort incomplete multipart uploads and use
short-lived or instance-role credentials where the deployment platform supports
them.

### P2 — retention does not preserve known-good recovery points

#### Evidence

- GFS cleanup evaluates only timestamped local run directories.
- It does not require remote upload success, artifact validation success, or restore-test success before deleting older local copies.
- S3 lifecycle, object versioning, and immutable retention are not part of the backup contract.

Relevant implementation: `internal/backupapp/cleanup.go`.

#### Impact

The last locally validated restore point can be removed even when newer backups have never been restore-tested. Remote retention is left to manual configuration.

#### Recommendation

Keep a configurable minimum number of known-good, restore-verified recovery points. Protect the newest verified backup from cleanup. Configure bucket versioning, lifecycle retention, optional Object Lock, and cross-account/cross-region replication according to the required recovery period.

### P2 — production defaults should fail safely

#### Evidence

- Several backup behaviors are enabled by default.
- The runtime configuration contains a default bucket name.
- Root scheduled execution is allowed by default.

Relevant implementation: `internal/backupapp/config.go`, `.env.example`, and `install.sh`.

#### Impact

A first installation or configuration mistake can run with more privilege than intended or target an unexpected production destination.

#### Recommendation

Require explicit production opt-in for physical backups, remote uploads, root execution, and the bucket/account identity. Reject placeholder values and validate the expected AWS account ID, bucket, region, and prefix before a run begins.

### P2 — recovery needs integration testing and routine drills

#### Current limitation

Unit and package tests are now runnable and cover the implemented recovery
logic. They do not substitute for an integration environment with real MySQL,
PostgreSQL, and S3-compatible object storage.

#### Recommendation

Add a CI integration suite using disposable MySQL, PostgreSQL, and MinIO/S3 environments. Cover at least:

- unencrypted and encrypted logical backup creation;
- corruption detection for local and downloaded artifacts;
- interrupted/retried upload with no false completion marker;
- full logical restore after local retention has removed the run directory;
- table-scoped versus complete-backup manifest semantics;
- physical stream download, extraction, prepare, and isolated startup;
- a point-in-time recovery rehearsal using archived binlogs;
- crash/restart behavior while writing manifests, schedule state, and run history.

## Recommended implementation sequence

1. Configure versioning, lifecycle, Object Lock, and cross-account/region
   replication in the target bucket.
2. Schedule prepare-only physical validation and periodic full isolated startup
   drills.
3. Archive binlogs separately and rehearse PITR against a requested stop time.
4. Add CI integration tests with disposable MySQL, PostgreSQL, and MinIO/S3.
5. Alert on stale validation and incomplete/abandoned remote uploads.

## Definition of a trustworthy successful backup

A run should be marked `success_full` only when all of the following are true:

1. Every object in the declared backup scope was exported without unapproved omissions.
2. Every artifact is authenticated and its checksum matches the final manifest.
3. Every artifact is present remotely with verified size/checksum/version.
4. The remote completion manifest was written successfully as the final commit step.
5. The backup has a documented recovery coordinate for the recovery objective, including PITR where required.
6. A recent isolated restore test has succeeded for the applicable backup type.
