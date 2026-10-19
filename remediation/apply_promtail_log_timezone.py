#!/usr/bin/env python3
"""Correct only the backup timestamp timezone; preserve scrape paths and positions."""
import argparse
import datetime
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'loki'))
from render_config import log_timezone


def corrected(text, zone):
    blocks = re.split(r'(?m)(?=^  - job_name:)', text)
    matches = 0
    for i, block in enumerate(blocks):
        if not re.match(r'  - job_name:\s*sdl-db-backup-run-logs\s*\n', block):
            continue
        matches += 1
        pattern = r'(?m)^(?P<indent> +)format: "2006/01/02 15:04:05\.000000"\n(?:\s+location: [^\n]*\n)?'
        blocks[i], count = re.subn(pattern, lambda m: m[0].splitlines()[0] + '\n' + m['indent'] + 'location: ' + json.dumps(zone) + '\n', block)
        if count != 1:
            raise RuntimeError('Expected exactly one backup timestamp stage')
    if matches != 1:
        raise RuntimeError('Expected exactly one backup scrape job')
    return ''.join(blocks)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--timezone', help='Override the runner host timezone')
    parser.add_argument('--config', action='append', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    zone = log_timezone({'BACKUP_LOG_TIMEZONE': args.timezone or os.environ.get('BACKUP_LOG_TIMEZONE')})
    changes = []
    for path in args.config:
        old = path.read_text(); new = corrected(old, zone)
        changes.append((path, old, new, path.stat()))
    print(json.dumps({'timezone': zone, 'configs': [str(p) for p, old, new, info in changes if old != new], 'scrape_paths_and_positions_changed': False}), flush=True)
    if not args.apply or all(old == new for p, old, new, info in changes):
        return
    if os.geteuid() != 0:
        raise SystemExit('Apply requires sudo for root-owned Promtail configuration and restart')
    stamp = datetime.datetime.now().strftime('%Y%m%dT%H%M%S')
    staged = []
    try:
        for path, old, new, info in changes:
            fd, name = tempfile.mkstemp(prefix='.backup-timezone-', dir=path.parent)
            with os.fdopen(fd, 'w') as stream:
                stream.write(new);stream.flush();os.fsync(stream.fileno())
            candidate = Path(name);staged.append((candidate, path))
            os.chown(candidate, info.st_uid, info.st_gid);os.chmod(candidate, info.st_mode & 0o777)
            subprocess.run(['/usr/local/bin/promtail', '-config.file='+name, '-check-syntax'], check=True)
            backup = path.with_name(path.name+'.before-timezone-'+stamp)
            with backup.open('x') as stream:
                os.chmod(backup, 0o600);stream.write(old)
        for candidate, path in staged:
            os.replace(candidate, path)
        subprocess.run(['systemctl', 'restart', 'promtail'], check=True)
        subprocess.run(['systemctl', 'is-active', '--quiet', 'promtail'], check=True)
    except BaseException:
        for path, old, new, info in changes:
            path.write_text(old);os.chmod(path, info.st_mode & 0o777)
        subprocess.run(['systemctl', 'restart', 'promtail'], check=False)
        raise
    finally:
        for candidate, path in staged:
            candidate.unlink(missing_ok=True)
    print('APPLIED: backup log timezone corrected; existing file offsets preserved', flush=True)


if __name__ == '__main__':
    main()
