"""Reverify the preserved capture after the corrected parser, then publish safely."""
import os,subprocess
from pathlib import Path
root=Path.cwd();logs=root/'remediation/oneoff-logs';env=os.environ.copy();env.update({'BACKUP_ENV_FILE':str(root/'.env'),'BACKUP_DIR':str(root/'remediation/captured-backups'),'BACKUP_LOG_DIR':str(logs),'BACKUP_METRICS_FILE':str(logs/'oneoff.prom'),'BACKUP_LOGICAL_CONSISTENCY':'global','BACKUP_PHYSICAL_ENABLED':'false','BACKUP_RETENTION_DELETE_ENABLED':'false','BACKUP_S3_LOGICAL_PREFIX':'recovery-hold/logical','BACKUP_S3_UPLOAD_MODE':'direct','BACKUP_REQUIRE_REMOTE_COMPLETION':'true','BACKUP_ALLOW_OBJECT_OMISSIONS':'false'})
result=subprocess.run(['/tmp/sdl-backup-remediation/candidate','reconcile-capture','2026-10-12_14-23-04'],env=env)
raise SystemExit(result.returncode)
