#!/usr/bin/env python3
"""Render the Promtail template using backup configuration, without credentials."""
import argparse
import json
import os
from pathlib import Path
from zoneinfo import ZoneInfo

ROOT = Path(__file__).resolve().parent.parent


def log_timezone(values):
    zone = values.get('BACKUP_LOG_TIMEZONE') or os.environ.get('TZ')
    if not zone:
        try:
            zone = str(Path('/etc/localtime').resolve().relative_to('/usr/share/zoneinfo'))
        except ValueError:
            raise ValueError('Set BACKUP_LOG_TIMEZONE to the timezone used by the backup runner')
    ZoneInfo(zone)
    return zone


def main():
    from dotenv import dotenv_values
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env', default=os.environ.get('BACKUP_ENV_FILE', str(ROOT / '.env')))
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    values = dict(dotenv_values(args.env))
    values.update({key: value for key, value in os.environ.items() if value.strip()})
    backup_dir = Path(values.get('BACKUP_DIR') or Path.cwd() / 'backups')
    log_dir = Path(values.get('BACKUP_LOG_DIR') or backup_dir / 'logs').absolute()
    template = Path(__file__).with_name('promtail-config.yml').read_text()
    rendered = template.replace('${BACKUP_LOG_GLOB}', json.dumps(str(log_dir / '*_*.log')))
    rendered = rendered.replace('${BACKUP_LOG_TIMEZONE}', json.dumps(log_timezone(values)))
    if args.output:
        args.output.write_text(rendered)
    else:
        print(rendered, end='')


if __name__ == '__main__':
    main()
