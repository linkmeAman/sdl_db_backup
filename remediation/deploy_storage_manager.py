"""Apply/rollback a frozen, tested local-storage release; never changes timers.

This installer does not delete backups. Runtime cleanup is separately governed
by the operator-authorized eligibility and recovery protection checks.
"""
import argparse
import datetime
import json
import os
from pathlib import Path

from deploy_candidate import ROOT, BASE, atomic, digest, locked_operation, merged_env

RELEASE = BASE / 'storage-release'
JOURNALS = BASE / 'storage-rollback'
OVERRIDES = {
    'BACKUP_STORAGE_AUTO_CLEANUP': 'true',
    'BACKUP_STORAGE_ESTIMATE_BYTES': '0',
    'BACKUP_STORAGE_RESERVE_BYTES': '2147483648',
    'BACKUP_STORAGE_HIGH_WATERMARK': '92',
    'BACKUP_STORAGE_LOW_WATERMARK': '90',
    'BACKUP_STORAGE_GROWTH_PERCENT': '50',
    'BACKUP_STORAGE_HISTORY_RUNS': '12',
    'BACKUP_STORAGE_CONCURRENT_BYTES': '536870912',
    'BACKUP_STORAGE_TEMPORARY_BYTES': '268435456',
    'BACKUP_STORAGE_MIN_FREE_INODES': '128',
    'BACKUP_STORAGE_TEMP_MAX_AGE': '24h',
    'BACKUP_STORAGE_VERIFY_TIMEOUT': '20m',
    'BACKUP_STORAGE_SECONDARY_DIR': '',
    'BACKUP_RETENTION_DELETE_ENABLED': 'false',
    'RESTORE_TEST_ENABLED': 'false',
}


def safe_path(name):
    path = Path(name)
    if path.is_absolute() or '..' in path.parts or name.startswith('remediation/production-backups'):
        raise RuntimeError('unsafe deployment path')
    if any((ROOT / Path(*path.parts[:i])).is_symlink() for i in range(1, len(path.parts) + 1)):
        raise RuntimeError('symlink deployment path')
    return ROOT / path


def owned_atomic(path, data, mode, uid=None, gid=None):
    # Atomic writes run as the service owner. Refuse ownership drift instead of
    # installing an environment file that the production service cannot read.
    if uid is not None and (uid != os.geteuid() or gid not in (os.getegid(), *os.getgroups())):
        raise RuntimeError('deployment must run as original file owner')
    atomic(path, data, mode)
    if uid is not None and (path.stat().st_uid != uid or path.stat().st_gid != gid):
        os.chown(path, uid, gid)


def restore(transaction, package):
    if transaction.resolve().parent != JOURNALS.resolve():
        raise RuntimeError('rollback must name a storage-release transaction')
    state = json.loads((transaction / 'state.json').read_text())
    allowed = {entry['path'] for entry in package['files']} | {'.env'}
    if any(entry['path'] not in allowed for entry in state['files']):
        raise RuntimeError('rollback journal contains an unexpected path')
    for entry in reversed(state['files']):
        target = safe_path(entry['path'])
        if entry['existed']:
            owned_atomic(target, (transaction / 'original' / entry['path']).read_bytes(),
                         entry['mode'], entry['uid'], entry['gid'])
        elif target.exists():
            target.unlink()  # Release source file only, never a backup artifact.
    state['status'] = 'ROLLED_BACK'
    atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    print('storage release ROLLED_BACK:', transaction)


def apply(package):
    dry = json.loads((BASE / 'storage-dry-run.json').read_text())
    if dry.get('state') != 'DRY_RUN' or not dry.get('protection_checked'):
        raise RuntimeError('successful independent protection dry run is required')
    if len(dry.get('protected_local', [])) < 1 or len(dry.get('protected_remote', [])) < 2:
        raise RuntimeError('dry run does not establish required recovery coverage')
    if digest(ROOT / '.env') != package['baseline_env_sha256']:
        raise RuntimeError('configuration drift; refreeze release')
    for entry in package['files']:
        target = safe_path(entry['path'])
        if digest(target) != entry['baseline_sha256'] or digest(RELEASE / entry['candidate']) != entry['candidate_sha256']:
            raise RuntimeError('deployment file drift: ' + entry['path'])
    JOURNALS.mkdir(mode=0o700, exist_ok=True)
    atomic(JOURNALS / 'go.mod', b'module local.invalid/storage-rollback\n\ngo 1.23\n', 0o600)
    unfinished = [p for p in JOURNALS.glob('*/state.json') if json.loads(p.read_text())['status'] == 'PREPARED']
    if unfinished:
        raise RuntimeError('interrupted deployment exists; rollback it before retrying')
    transaction = JOURNALS / datetime.datetime.now().strftime('%Y%m%dT%H%M%S')
    transaction.mkdir(mode=0o700)
    state = {'status': 'PREPARED', 'files': []}
    for name in [entry['path'] for entry in package['files']] + ['.env']:
        target = safe_path(name)
        exists = target.exists()
        info = target.stat() if exists else None
        entry = {'path': name, 'existed': exists, 'mode': info.st_mode & 0o777 if exists else 0o644,
                 'uid': info.st_uid if exists else os.geteuid(), 'gid': info.st_gid if exists else os.getegid()}
        if exists:
            atomic(transaction / 'original' / name, target.read_bytes(), entry['mode'])
        state['files'].append(entry)
    atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    try:
        for entry in package['files']:
            target = safe_path(entry['path'])
            old = next(row for row in state['files'] if row['path'] == entry['path'])
            owned_atomic(target, (RELEASE / entry['candidate']).read_bytes(), entry['mode'], old['uid'], old['gid'])
        original = (transaction / 'original/.env').read_bytes()
        old = state['files'][-1]
        owned_atomic(ROOT / '.env', merged_env(original, OVERRIDES), 0o600, old['uid'], old['gid'])
        state['status'] = 'APPLIED'
        state['env_sha256'] = digest(ROOT / '.env')
        atomic(transaction / 'state.json', (json.dumps(state, indent=2) + '\n').encode(), 0o600)
    except BaseException:
        restore(transaction, package)
        raise
    print('storage release APPLIED; automatic local cleanup enabled; no secondary; timer unchanged')
    print('rollback journal:', transaction)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument('--apply', action='store_true')
    group.add_argument('--rollback-latest', action='store_true')
    args = parser.parse_args()
    package = json.loads((RELEASE / 'package.json').read_text())
    if args.rollback_latest:
        candidates = sorted((p.parent for p in JOURNALS.glob('*/state.json')
                             if json.loads(p.read_text())['status'] in ('PREPARED', 'APPLIED')), reverse=True)
        if not candidates:
            raise RuntimeError('no active storage-release journal')
        locked_operation(lambda: restore(candidates[0], package))
    elif args.apply:
        locked_operation(lambda: apply(package))
    else:
        print(json.dumps({'mode': 'REVIEW', 'files': [entry['path'] for entry in package['files']],
                          'configuration': OVERRIDES, 'timer_changed': False,
                          'installer_backup_deletions': False}, indent=2))


if __name__ == '__main__':
    main()
