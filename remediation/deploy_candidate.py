"""Review by default; explicit --apply installs the frozen candidate, never timers.

No backup objects are deleted. --rollback restores only package files/configuration.
Run from the repository root as the existing developer service owner.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parent.parent
BASE = ROOT / 'remediation'


def digest(path):
    if not path.exists():
        return None
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def atomic(path, data, mode):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.deploy-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, mode)
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def merged_env(data, overrides):
    remaining = dict(overrides)
    lines = []
    for line in data.decode().splitlines():
        key = line.split('=', 1)[0].strip()
        if key in overrides:
            if key in remaining:
                lines.append(key + '=' + remaining.pop(key))
        else:
            lines.append(line)
    lines += [key + '=' + value for key, value in remaining.items()]
    return ('\n'.join(lines) + '\n').encode()


def require_idle():
    out = subprocess.check_output(['systemctl', 'show', 'sdl-db-backup.service',
                                  '--property=ActiveState', '--value'], text=True).strip()
    if out not in ('inactive', 'failed'):
        raise RuntimeError('production service must be idle before deployment')
    trigger = subprocess.check_output(['systemctl', 'show', 'sdl-db-backup.timer',
                                      '--property=NextElapseUSecRealtime', '--value'], text=True).strip()
    if trigger:
        # Current host timer reports IST; refuse an unexpected clock interpretation.
        fields = trigger.split()
        if len(fields) != 4 or fields[-1] != 'IST':
            raise RuntimeError('unexpected timer timezone; review deployment window')
        next_time = datetime.datetime.fromisoformat(fields[1] + 'T' + fields[2]).replace(
            tzinfo=ZoneInfo('Asia/Kolkata'))
        if (next_time - datetime.datetime.now(next_time.tzinfo)).total_seconds() < 300:
            raise RuntimeError('next timer trigger is too close; defer deployment')


def locked_operation(action):
    # Use the same inode and PID protocol as the new runner and reject legacy owners.
    lock = Path('/mnt/volume_1/backup/mysql_backup/logs/backup.lock')
    with lock.open('a+') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        stream.seek(0)
        match = re.search(r'pid=(\d+)', stream.read())
        if match and int(match[1]) != os.getpid():
            try:
                os.kill(int(match[1]), 0)
            except ProcessLookupError:
                pass
            else:
                raise RuntimeError('legacy backup process is active')
        require_idle()
        stream.seek(0)
        stream.truncate()
        stream.write('pid=' + str(os.getpid()) + ' deployment=true')
        stream.flush()
        os.fsync(stream.fileno())
        try:
            action()
        finally:
            stream.seek(0)
            stream.truncate()
            stream.flush()
            os.fsync(stream.fileno())


def restore(transaction):
    state = json.loads((transaction / 'state.json').read_text())
    for entry in reversed(state['files']):
        target = ROOT / entry['path']
        if entry['existed']:
            atomic(target, (transaction / 'original' / entry['path']).read_bytes(), entry['mode'])
        elif target.exists():
            # Package source/binaries only; backup paths cannot enter this journal.
            target.unlink()
    state['status'] = 'ROLLED_BACK'
    atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    print('ROLLED_BACK:', transaction)


def apply(package, overrides):
    if digest(BASE / 'production-overrides.env') != package['overrides_sha256']:
        raise RuntimeError('proposed configuration changed; freeze and review again')
    if digest(ROOT / '.env') != package['baseline_env_sha256']:
        raise RuntimeError('production configuration drift; review before deployment')
    if digest(BASE / 'tools/mysql84/usr/bin/mysqldump') != package['dump_client_sha256']:
        raise RuntimeError('private dump client changed')
    for entry in package['files']:
        if digest(BASE / entry['candidate']) != entry['candidate_sha256']:
            raise RuntimeError('candidate changed: ' + entry['path'])
        if digest(ROOT / entry['path']) != entry['baseline_sha256']:
            raise RuntimeError('production file drift: ' + entry['path'])
    transaction = BASE / 'rollback' / datetime.datetime.now().strftime('%Y%m%dT%H%M%S')
    transaction.mkdir(parents=True, mode=0o700)
    transaction.chmod(0o700)
    # Historical snapshots contain partial Go packages; exclude them from ./....
    marker = BASE / 'rollback/go.mod'
    if not marker.exists():
        atomic(marker, b'module local.invalid/sdl-backup-rollback\n\ngo 1.23\n', 0o600)
    files = package['files'] + [{'path': '.env'}]
    state = {'status': 'PREPARED', 'files': []}
    for entry in files:
        target = ROOT / entry['path']
        if target.is_symlink():
            raise RuntimeError('refusing symlink target: ' + entry['path'])
        exists = target.exists()
        mode = target.stat().st_mode & 0o777 if exists else 0o644
        if exists:
            atomic(transaction / 'original' / entry['path'], target.read_bytes(), mode)
        state['files'].append({'path': entry['path'], 'existed': exists, 'mode': mode})
    atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    try:
        for entry in package['files']:
            atomic(ROOT / entry['path'], (BASE / entry['candidate']).read_bytes(), entry['mode'])
        atomic(ROOT / '.env', merged_env((ROOT / '.env').read_bytes(), overrides), 0o600)
        directory = ROOT / overrides['BACKUP_DIR']
        directory.mkdir(mode=0o700, exist_ok=True)
        directory.chmod(0o700)
        state['status'] = 'APPLIED'
        state['env_sha256'] = digest(ROOT / '.env')
        atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    except BaseException:
        restore(transaction)
        raise
    print('APPLIED; timer unchanged; rollback journal:', transaction)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument('--apply', action='store_true')
    group.add_argument('--rollback', type=Path)
    group.add_argument('--rollback-latest', action='store_true')
    args = parser.parse_args()
    package = json.loads((BASE / 'package-manifest.json').read_text())
    overrides = dict(line.split('=', 1) for line in (BASE / 'production-overrides.env').read_text().splitlines()
                     if line and not line.startswith('#'))
    if args.rollback or args.rollback_latest:
        if args.rollback_latest:
            transactions = sorted((BASE / 'rollback').glob('*/state.json'), reverse=True)
            eligible = [p.parent for p in transactions
                        if json.loads(p.read_text())['status'] in ('APPLIED', 'PREPARED')]
            if not eligible:
                raise RuntimeError('no unapplied rollback journal')
            transaction = eligible[0].resolve()
        else:
            transaction = args.rollback.resolve()
        if transaction.parent != (BASE / 'rollback').resolve():
            raise RuntimeError('rollback must name a prepared package transaction')
        locked_operation(lambda: restore(transaction))
    elif args.apply:
        locked_operation(lambda: apply(package, overrides))
    else:
        print(json.dumps({'mode': 'DRY_RUN', 'files': [entry['path'] for entry in package['files']],
                          'proposed_config': overrides, 'timer_changed': False,
                          'backup_deletions': False}, indent=2))


if __name__ == '__main__':
    main()
