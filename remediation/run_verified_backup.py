"""One-off correct logical backup; never modifies .env/timers or deletes backups."""
import os,subprocess
from pathlib import Path
root=Path.cwd();backup=root/'remediation'/'captured-backups';logs=root/'remediation'/'oneoff-logs'
for p in [backup,logs]:p.mkdir(mode=0o700,exist_ok=True);p.chmod(0o700)
env=os.environ.copy();env.update({'BACKUP_ENV_FILE':str(root/'.env'),'MYSQLDUMP_BIN':'/tmp/sdl-private-mysql84/extracted/usr/bin/mysqldump','BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE':'true','BACKUP_DIR':str(backup),'BACKUP_LOG_DIR':str(logs),'BACKUP_METRICS_FILE':str(logs/'oneoff.prom'),'BACKUP_LOGICAL_CONSISTENCY':'global','BACKUP_LOGICAL_SCHEDULE':'always','BACKUP_LOGICAL_ENABLED':'true','BACKUP_PHYSICAL_ENABLED':'false','BACKUP_RETENTION_DELETE_ENABLED':'false','BACKUP_STORAGE_RESERVE_BYTES':str(2<<30),'BACKUP_STORAGE_ESTIMATE_BYTES':str(2<<30),'BACKUP_S3_LOGICAL_PREFIX':'recovery-hold/logical','BACKUP_S3_UPLOAD_MODE':'direct','BACKUP_LOGICAL_S3_UPLOAD_ENABLED':'true','BACKUP_REQUIRE_REMOTE_COMPLETION':'true','BACKUP_ALLOW_OBJECT_OMISSIONS':'false','BACKUP_EXECUTION_SOURCE':'tui','BACKUP_SCHEDULE_TIMEZONE':'Asia/Kolkata'})
fd=os.open(logs/'run-console.log',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
with os.fdopen(fd,'w') as log:
 process=subprocess.Popen(['/tmp/sdl-backup-remediation/candidate'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
 for line in process.stdout:
  log.write(line);log.flush()
  if any(token in line for token in ['backup started','backup folder:','due now','starting dump','completed dump','completion withheld','backup summary','backup failure reason','s3 upload committed','s3 upload error','capacity:','DDL']):print(line.rstrip(),flush=True)
 result=process.wait()
print('one-off exit status',result,flush=True)
raise SystemExit(result)
