"""Verify deployed logical capture with live configuration; no restore/deletion."""
import argparse
import datetime
import os
from pathlib import Path
import subprocess
from dotenv import dotenv_values

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--env', default=os.environ.get('BACKUP_ENV_FILE', str(root / '.env')))
args = parser.parse_args()
env = os.environ.copy()
env.update({'BACKUP_ENV_FILE': str(Path(args.env).absolute()),
            'BACKUP_LOGICAL_SCHEDULE': 'always'})
values = dict(dotenv_values(args.env))
values.update({key: value for key, value in env.items() if value.strip()})
backup_dir = Path(values.get('BACKUP_DIR') or root / 'backups')
logs = Path(values.get('BACKUP_LOG_DIR') or backup_dir / 'logs') / 'diagnostics'
logs.mkdir(mode=0o700, parents=True, exist_ok=True)
path = logs / ('manual-capture-' + datetime.datetime.now().strftime('%Y%m%dT%H%M%S') + '.log')
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
with os.fdopen(fd, 'w') as log:
    process = subprocess.Popen([str(root / 'sdl-db-backup')], cwd=root, env=env,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    for line in process.stdout:
        log.write(line)
        log.flush()
        if any(token in line for token in ['backup folder:', 'starting logical capture',
                                          'completed dump', 'backup summary',
                                          'backup failure reason', 'completion withheld',
                                          'capacity:', 's3 upload committed',
                                          'retention deletion HOLD']):
            print(line.rstrip(), flush=True)
    result = process.wait()
print('deployed capture exit status:', result, flush=True)
raise SystemExit(result)
