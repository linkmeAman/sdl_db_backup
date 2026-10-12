"""Verify deployed logical capture with live configuration; no restore/deletion."""
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
env = os.environ.copy()
env.update({'BACKUP_ENV_FILE': str(root / '.env'),
            'BACKUP_LOGICAL_SCHEDULE': 'always',
            'BACKUP_PHYSICAL_ENABLED': 'false',
            'BACKUP_EXECUTION_SOURCE': 'tui'})
path = root / 'remediation/oneoff-logs/deployed-capture-console.log'
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
