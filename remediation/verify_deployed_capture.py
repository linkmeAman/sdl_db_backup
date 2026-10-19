"""Independent local and full S3 payload verification; never restores data."""
import argparse
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path

import boto3
from botocore.config import Config
from dotenv import dotenv_values

parser = argparse.ArgumentParser()
parser.add_argument('--run-id', required=True)
parser.add_argument('--output', default='remediation/deployed-backup-proof.json')
parser.add_argument('--env', default=os.environ.get('BACKUP_ENV_FILE', '.env'))
args = parser.parse_args()
if '/' in args.run_id or '..' in args.run_id:
    raise ValueError('invalid run id')
values = dict(dotenv_values(args.env))
values.update({key: value for key, value in os.environ.items() if value.strip()})
required = values['BACKUP_LOGICAL_DATABASES'].split(',')
bucket = values['BACKUP_S3_BUCKET']
prefix = values['BACKUP_S3_LOGICAL_PREFIX'].strip('/') + '/' + args.run_id + '/'
folder = Path(values['BACKUP_DIR']) / args.run_id
client = boto3.client('s3', region_name=values['BACKUP_S3_REGION'],
                      endpoint_url=values.get('BACKUP_S3_ENDPOINT') or None,
                      aws_access_key_id=values['BACKUP_S3_KEY_ID'],
                      aws_secret_access_key=values['BACKUP_S3_KEY_SECRET'],
                      config=Config(connect_timeout=10, read_timeout=30,
                                    retries={'max_attempts': 3}))


def hash_chunks(chunks):
    sha = hashlib.sha256()
    size = 0
    for chunk in chunks:
        size += len(chunk)
        sha.update(chunk)
    return size, sha.hexdigest()


marker = json.loads(client.get_object(Bucket=bucket, Key=prefix + 'COMPLETED.json')['Body'].read())
manifest = json.loads((folder / 'manifest.json').read_text())
out = {'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
       'run_id': args.run_id, 'destination': 's3://' + bucket + '/' + prefix,
       'marker_status': marker.get('status'), 'manifest_state': manifest.get('recovery_state'),
       'expected_databases_match': sorted(marker.get('expected_databases', [])) == sorted(required),
       'all_configured_databases_complete': len(manifest.get('results', [])) == len(required)
       and sorted(result['name'] for result in manifest['results']) == sorted(required)
       and all(result['status'] == 'success' for result in manifest['results']),
       'duration': manifest.get('duration'), 'capture_duration': manifest['results'][0].get('duration'),
       'consistency_method': manifest.get('consistency_method'),
       'restore_state': 'UNVERIFIED', 'artifacts': []}
expected_keys = {'all-databases.sql.gz', 'manifest.json'}
out['marker_artifact_inventory_match'] = len(marker['artifacts']) == 2 and {
    artifact['key'] for artifact in marker['artifacts']} == expected_keys
inventory = set()
for page in client.get_paginator('list_objects_v2').paginate(Bucket=bucket, Prefix=prefix):
    inventory.update(item['Key'][len(prefix):] for item in page.get('Contents', []))
out['remote_inventory_match'] = inventory == expected_keys | {'COMPLETED.json'}
for artifact in marker['artifacts']:
    if artifact['key'] not in expected_keys:
        raise ValueError('unexpected artifact path')
    response = client.get_object(Bucket=bucket, Key=prefix + artifact['key'])
    remote_size, remote_sha = hash_chunks(response['Body'].iter_chunks(chunk_size=1 << 20))
    response['Body'].close()
    with (folder / artifact['key']).open('rb') as stream:
        local_size, local_sha = hash_chunks(iter(lambda: stream.read(1 << 20), b''))
    result = {'key': artifact['key'], 'bytes': remote_size,
              'remote_size_match': remote_size == artifact['size_bytes'],
              'remote_sha256_match': remote_sha == artifact['sha256'],
              'local_size_match': local_size == artifact['size_bytes'],
              'local_sha256_match': local_sha == artifact['sha256'],
              'server_side_encryption': response.get('ServerSideEncryption')}
    out['artifacts'].append(result)
    print('payload verification:', json.dumps(result), flush=True)
with gzip.open(folder / 'all-databases.sql.gz', 'rb') as stream:
    raw = hashlib.sha256()
    tail = b''
    while True:
        block = stream.read(1 << 20)
        if not block:
            break
        raw.update(block)
        tail = (tail + block)[-4096:]
out['gzip_integrity'] = 'VERIFIED'
out['dump_footer_present'] = b'-- Dump completed on ' in tail
out['sql_sha256_match_all_results'] = all(result.get('sql_sha256') == raw.hexdigest()
                                            for result in manifest['results'])
out['expected_object_counts'] = {
    result['name']: {kind: len(names) for kind, names in result.get('expected_objects', {}).items()}
    for result in manifest['results']}
passed = all(out[key] for key in ['expected_databases_match', 'all_configured_databases_complete',
                                  'marker_artifact_inventory_match', 'remote_inventory_match',
                                  'dump_footer_present', 'sql_sha256_match_all_results'])
passed = passed and out['marker_status'] == 'COMPLETE' and out['manifest_state'] == 'COMPLETE'
passed = passed and all(all(result[key] for key in ['remote_size_match', 'remote_sha256_match',
                                                   'local_size_match', 'local_sha256_match'])
                           for result in out['artifacts'])
out['capture_and_remote_integrity_verdict'] = 'VERIFIED' if passed else 'FAILED'
out['verification_finished_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
path = Path(args.output)
path.write_text(json.dumps(out, indent=2) + '\n')
path.chmod(0o600)
print('independent deployed capture verdict:', out['capture_and_remote_integrity_verdict'], flush=True)
raise SystemExit(0 if passed else 1)
