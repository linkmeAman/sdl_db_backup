"""Install the validated backup alert rules, verify reload, roll back on failure."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / 'prometheus/sdl-db-backup-alerts.yml'
TARGET = Path('/etc/prometheus/rules/sdl-db-backup-alerts.yml')
SAVED = Path('/etc/prometheus/rules/.sdl-db-backup-alerts.previous')
STATE = Path('/etc/prometheus/rules/.sdl-db-backup-alerts.state.json')

def atomic(path, data):
    fd, name = tempfile.mkstemp(prefix='.sdl-alerts-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(name, 0o644)
        os.replace(name, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def loaded():
    with urllib.request.urlopen('http://127.0.0.1:9090/api/v1/rules', timeout=10) as r:
        groups = json.load(r)['data']['groups']
    return any(rule.get('name') == 'SDLBackupStorageRecoveryCritical'
               for group in groups for rule in group.get('rules', []))

def reload_and_verify(expected):
    subprocess.run(['systemctl', 'reload', 'prometheus.service'], check=True)
    for _ in range(10):
        if loaded() == expected:
            return
        time.sleep(1)
    raise RuntimeError('Prometheus loaded rule state did not match')

def rollback():
    state = json.loads(STATE.read_text())
    if state['existed']:
        atomic(TARGET, SAVED.read_bytes())
    else:
        TARGET.unlink(missing_ok=True)
    reload_and_verify(state['previous_loaded'])
    state['status'] = 'ROLLED_BACK'
    atomic(STATE, (json.dumps(state) + '\n').encode())
    print('backup alert rules ROLLED_BACK')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument('--apply', action='store_true')
    group.add_argument('--rollback', action='store_true')
    args = parser.parse_args()
    subprocess.run(['promtool', 'check', 'rules', str(SOURCE)], check=True)
    if not (args.apply or args.rollback):
        print('Validated; proposed target:', TARGET)
        return
    if os.geteuid() != 0:
        raise RuntimeError('root is required for /etc/prometheus/rules and service reload')
    if TARGET.is_symlink() or STATE.is_symlink() or SAVED.is_symlink():
        raise RuntimeError('refusing symlink alert transaction paths')
    if args.rollback:
        rollback()
        return
    if TARGET.exists() and TARGET.read_bytes() == SOURCE.read_bytes() and loaded():
        print('backup alert rules already applied and loaded')
        return
    if STATE.exists() and json.loads(STATE.read_text()).get('status') == 'PREPARED':
        raise RuntimeError('interrupted rule transaction exists; rollback before retry')
    state = {'status': 'PREPARED', 'existed': TARGET.exists(), 'previous_loaded': loaded()}
    if state['existed']:
        atomic(SAVED, TARGET.read_bytes())
    atomic(STATE, (json.dumps(state) + '\n').encode())
    try:
        atomic(TARGET, SOURCE.read_bytes())
        subprocess.run(['promtool', 'check', 'config', '/etc/prometheus/prometheus.yml'], check=True)
        reload_and_verify(True)
        state['status'] = 'APPLIED'
        atomic(STATE, (json.dumps(state) + '\n').encode())
    except BaseException:
        rollback()
        raise
    print('backup alert rules APPLIED and verified loaded; notification delivery remains untested')

if __name__ == '__main__':
    main()
