"""Read-only production artifact/S3 verification using the deployed policy."""
import argparse
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', default='remediation/storage-dry-run.json')
args = parser.parse_args()
env = os.environ.copy()
env.update({'BACKUP_ENV_FILE': str(root / '.env')})
process = subprocess.run([str(root / 'sdl-db-backup'), 'storage-plan'],
                         cwd=root, env=env, stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, text=True)
if process.stdout.strip():
    report = json.loads(process.stdout)
    path = root / args.output
    path.write_text(json.dumps(report, indent=2) + '\n')
    path.chmod(0o600)
    print(json.dumps({key: report.get(key) for key in
                      ['state', 'dry_run', 'initial', 'target_bytes', 'protected_local',
                       'protected_remote', 'removed_payload_bytes', 'reclaimed_bytes',
                       'fallback', 'error']}, indent=2), flush=True)
if process.returncode:
    print('storage plan failed; see report; no production artifact was deleted', flush=True)
raise SystemExit(process.returncode)
