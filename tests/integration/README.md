# Container integration tests

From the repository root:

```bash
./tests/integration/run.sh
```

Requires Docker Engine/Desktop with the Compose v2 plugin and a running daemon.
The first run needs network access to pull images, Go modules, and Ubuntu client
packages. No host Go, database clients, Python, or cloud credentials are needed.
Allow roughly 4 GB of Docker memory for four database servers and the S3 server.

The runner builds the actual backup and validation CLIs from the current checkout.
It uses MySQL 8.0, PostgreSQL 16, and a pinned versitygw S3 gateway image
(MinIO no longer publishes community container images). MySQL and PostgreSQL
each have separate source and restore servers. There are no published host ports,
host database mounts, or inherited `.env` files. All credentials and the encryption
key are public, disposable fixture values. A Dockerfile-specific ignore file keeps
host secrets and backup artifacts out of the build context.

The suite verifies for **both engines**:

- Logical database discovery with a database allow-list and successful encrypted backups of deterministic
  tables, an empty table, a view, Unicode text, quoted text, and NULL data.
- Manifest metadata, exact table row counts, sampled content hashes, encrypted
  format, artifact SHA-256, and decrypted/decompressed SQL SHA-256.
- CLI rejection of a wrong key, truncated ciphertext, and modified ciphertext.
- S3 object bytes, sizes, and SHA-256 values against `COMPLETED.json`, including
  the remote manifest. A forwarding proxy observes real signed S3 requests and
  asserts every artifact is uploaded and HEAD-verified before the marker PUT.
- Recovery using the encrypted object downloaded from S3. The application's
  sandbox validator imports it into a temporary database on the restore server
  and verifies schema, exact counts, and sampled content. The suite checks the
  persisted successful validation and temporary database cleanup.
- Deliberately incorrect expected row counts and content hashes fail restore verification and
  still removes the temporary database. Source databases and fixture rows remain
  unchanged throughout the restore tests.
- An injected HTTP 503 during manifest upload fails the backup and leaves the
  uploaded encrypted object without a completion marker.

The tests use small logical artifacts. Physical backups, PITR, multipart-size
artifacts, and AWS-specific behavior are outside this suite; the existing Go unit
tests cover multipart request mechanics.

Each invocation uses a unique Compose project. The wrapper preserves the suite's
exit status, saves diagnostics, and removes containers, networks, and volumes on
success, failure, or an interrupt. A forced kill or Docker crash can prevent the
exit trap from running; leftover projects have the `sdl-e2e-` prefix.

Output is saved to `tests/integration/results/` (ignored by Git): command output,
Compose logs, encrypted artifacts, manifests, run histories, metrics, and observed
S3 requests. Set `INTEGRATION_RESULTS_DIR=/absolute/path` to choose another output
directory; use a fresh directory to avoid mixing diagnostics from multiple runs.
`compose.log` contains the Python unittest summary. Commands have bounded timeouts,
service health checks gate startup, and the suite polls S3 readiness for up to
90 seconds. CI runs this same command in a separate 20-minute job and retains
results for seven days, including on failure.

## S3-compatible endpoints

`BACKUP_S3_ENDPOINT=http://s3:9000` selects a custom direct S3 endpoint. Custom
endpoints use path-style bucket addressing (`/<bucket>/<key>`), with the same
Signature V4 authentication and completion checks as AWS. A base path is allowed;
credentials, queries, and fragments in endpoint URLs are rejected. Omit this
setting to preserve the default AWS virtual-hosted regional endpoint. This setting
affects the built-in direct S3 client, not external `xbcloud`, PHP, or HTTP uploaders.
