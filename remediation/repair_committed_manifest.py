"""Preserve finalization audit and recover the exact checksum-pinned S3 manifest.

No remote object, backup payload, database or timer is changed.
"""
import argparse
import hashlib
import json
from pathlib import Path
import boto3
from botocore.config import Config
from dotenv import dotenv_values
from deploy_candidate import atomic

values = dotenv_values('.env')
parser = argparse.ArgumentParser()
parser.add_argument('--run-id', default='2026-10-12_17-26-25')
args = parser.parse_args()
run_id = args.run_id
if '/' in run_id or '..' in run_id: raise ValueError('invalid run id')
folder = Path(values['BACKUP_DIR']) / run_id
prefix = values['BACKUP_S3_LOGICAL_PREFIX'].strip('/') + '/' + run_id + '/'
client = boto3.client('s3', region_name=values['BACKUP_S3_REGION'],
                      aws_access_key_id=values['BACKUP_S3_KEY_ID'],
                      aws_secret_access_key=values['BACKUP_S3_KEY_SECRET'],
                      config=Config(connect_timeout=10, read_timeout=30))
bucket = values['BACKUP_S3_BUCKET']
marker = json.loads(client.get_object(Bucket=bucket, Key=prefix + 'COMPLETED.json')['Body'].read())
remote = client.get_object(Bucket=bucket, Key=prefix + 'manifest.json')['Body'].read()
expected = next(artifact for artifact in marker['artifacts'] if artifact['key'] == 'manifest.json')
sha = hashlib.sha256(remote).hexdigest()
if len(remote) != expected['size_bytes'] or sha != expected['sha256'] or marker['status'] != 'COMPLETE':
    raise RuntimeError('committed remote manifest integrity failed')
remote_manifest = json.loads(remote)
local_data = (folder / 'manifest.json').read_bytes()
local_manifest = json.loads(local_data)
critical = ['run_id', 'expected_databases', 'consistency_method', 'results', 'status',
            'recovery_state', 'format_version', 'databases_total', 'databases_succeeded',
            'databases_failed', 'backup_type', 'compression', 'restore_test_state']
if any(local_manifest.get(key) != remote_manifest.get(key) for key in critical):
    raise RuntimeError('capture semantics differ; refusing metadata repair')
if remote_manifest['databases_succeeded'] != 6 or remote_manifest['databases_failed'] != 0:
    raise RuntimeError('remote manifest is incomplete')
audit = folder / 'manifest-finalization-audit.json'
if not audit.exists():
    atomic(audit, local_data, 0o600)
atomic(folder / 'manifest.json', remote, 0o640)
atomic(folder / 'manifest-commit.json', (json.dumps({'run_id': run_id, 'sha256': sha}) + '\n').encode(), 0o600)
proof = {'state': 'VERIFIED', 'run_id': run_id, 'remote_modified': False,
         'backup_payload_modified': False, 'semantic_fields_unchanged': True,
         'changed_bookkeeping_fields': [key for key in local_manifest
                                        if local_manifest.get(key) != remote_manifest.get(key)]}
atomic(Path('remediation/evidence/manifest-finalization-repair.json'),
       (json.dumps(proof, indent=2) + '\n').encode(), 0o600)
print(json.dumps(proof))
