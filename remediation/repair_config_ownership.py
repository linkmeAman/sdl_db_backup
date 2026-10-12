"""Recover exactly the approved deployment configuration; preserve replaced file."""
import hashlib
import json
import os
from pathlib import Path

from deploy_candidate import ROOT, BASE, atomic, merged_env

transaction = BASE / 'rollback/20261012T171812'
state = json.loads((transaction / 'state.json').read_text())
overrides = dict(line.split('=', 1) for line in (BASE / 'production-overrides.env').read_text().splitlines()
                 if line and not line.startswith('#'))
approved = merged_env((transaction / 'original/.env').read_bytes(), overrides)
if hashlib.sha256(approved).hexdigest() != state['env_sha256']:
    raise RuntimeError('approved configuration reconstruction mismatch')
path = ROOT / '.env'
if path.stat().st_uid == os.getuid():
    print('configuration ownership already matches service account')
else:
    preserved = transaction / 'operator-env-before-ownership-repair'
    if preserved.exists():
        raise RuntimeError('preserved ownership-repair file already exists; review first')
    os.rename(path, preserved)
    try:
        atomic(path, approved, 0o600)
    except BaseException:
        os.rename(preserved, path)
        raise
    print('VERIFIED: approved configuration restored with service-account ownership; replaced file preserved')
