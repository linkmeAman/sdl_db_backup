"""Exercise the shipped CLIs with real database clients and signed S3 traffic."""
import gzip
import hashlib
import http.client
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import boto3
from botocore.config import Config

RESULTS = Path('/results')
PASSWORD = 'integration-only-password'
KEY = '0123456789abcdef' * 4
BUCKET = 'integration-backups'
DATABASE = 'e2e_fixture'


class S3Proxy(BaseHTTPRequestHandler):
    """Forward signed requests unchanged; observe ordering and inject PUT failures."""
    events = []
    fail_manifest = False

    def log_message(self, *_):
        pass

    def forward(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        if self.fail_manifest and self.command == 'PUT' and self.path.endswith('/manifest.json'):
            status, headers, data = 503, {}, b'injected upload failure'
        else:
            connection = http.client.HTTPConnection('s3', 9000, timeout=30)
            try:
                connection.request(self.command, self.path, body, dict(self.headers))
                response = connection.getresponse()
                status, headers, data = response.status, dict(response.getheaders()), response.read()
            finally:
                connection.close()
        self.events.append((self.command, self.path, status))
        self.send_response(status)
        for name, value in headers.items():
            if name.lower() not in ('connection', 'transfer-encoding', 'content-length'):
                self.send_header(name, value)
        self.send_header('Content-Length', headers.get('Content-Length', str(len(data))))
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(data)

    do_PUT = forward
    do_HEAD = forward
    do_GET = forward
    do_POST = forward
    do_DELETE = forward


def command(args, env=None, data=None, ok=True):
    result = subprocess.run(args, env=env, input=data, capture_output=True, timeout=180)
    with (RESULTS / 'commands.log').open('ab') as log:
        log.write(('\n$ ' + ' '.join(args) + '\n').encode())
        # Decrypted SQL output is checked by the caller, not logged as binary.
        if 'decrypt' not in args:
            log.write(result.stdout)
        log.write(result.stderr)
    if ok and result.returncode != 0:
        raise AssertionError(f'{args} exited {result.returncode}:\n'
                             f'{result.stdout.decode(errors="replace")}\n'
                             f'{result.stderr.decode(errors="replace")}')
    if not ok and result.returncode == 0:
        raise AssertionError(f'{args} unexpectedly succeeded')
    return result


def sql(engine, target, query, database=None):
    env = dict(os.environ, MYSQL_PWD=PASSWORD, PGPASSWORD=PASSWORD)
    host = f'{engine}-{target}'
    if engine == 'mysql':
        args = ['mysql', '-h', host, '-u', 'root', '--batch', '--raw', '-N']
        if database:
            args += [database]
    else:
        args = ['psql', '-h', host, '-U', 'postgres', '-d', database or 'postgres',
                '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1']
    return command(args, env, query.encode()).stdout.decode().strip()


def database_names(engine, target):
    query = 'SHOW DATABASES' if engine == 'mysql' else 'SELECT datname FROM pg_database ORDER BY datname'
    return sorted(sql(engine, target, query).splitlines())


def environment(engine, case):
    base = RESULTS / engine / case
    base.mkdir(parents=True)
    # Do not inherit any backup, AWS, or database configuration from a developer.
    env = {name: value for name, value in os.environ.items()
           if not name.startswith(('BACKUP_', 'DB_', 'RESTORE_TEST_', 'AWS_', 'MYSQL', 'PG'))}
    env.update({
        'BACKUP_ENV_FILE': '/suite/empty.env',
        'DB_ENGINE': engine, 'DB_HOST': f'{engine}-source',
        'DB_PORT': '3306' if engine == 'mysql' else '5432',
        'DB_USER': 'root' if engine == 'mysql' else 'postgres', 'DB_PASS': PASSWORD,
        'BACKUP_DIR': str(base / 'backups'), 'BACKUP_LOG_DIR': str(base / 'logs'),
        'BACKUP_METRICS_FILE': str(base / 'metrics.prom'),
        'BACKUP_ALLOW_ROOT_RUNNER': 'true', 'BACKUP_LOGICAL_ENABLED': 'true',
        'BACKUP_LOGICAL_SCHEDULE': 'always', 'BACKUP_PHYSICAL_ENABLED': 'false',
        'BACKUP_LOGICAL_PARALLEL': '1', 'BACKUP_RETRY_COUNT': '1',
        # Ubuntu's MySQL 8.0 mysqldump lacks --init-command (production uses an 8.4
        # client); the session max_execution_time override is unit tested instead.
        'BACKUP_MYSQLDUMP_SESSION_TIMEOUT_OVERRIDE': 'false',
        'BACKUP_LOGICAL_DATABASES': DATABASE,
        'BACKUP_LOGICAL_TIMEOUT_PER_DB': '60s', 'BACKUP_PREFLIGHT_TIMEOUT': '30s',
        'BACKUP_EXACT_ROW_COUNTS': 'true', 'BACKUP_SAMPLE_DATA_CHECKS': 'true',
        'BACKUP_ENCRYPTION_KEY': KEY,
        'BACKUP_LOGICAL_S3_UPLOAD_ENABLED': 'true', 'BACKUP_S3_UPLOAD_MODE': 'direct',
        'BACKUP_REQUIRE_REMOTE_COMPLETION': 'true', 'BACKUP_S3_UPLOAD_TIMEOUT': '30s',
        'BACKUP_S3_ENDPOINT': 'http://127.0.0.1:9001',
        'BACKUP_S3_BUCKET': BUCKET, 'BACKUP_S3_REGION': 'us-east-1',
        'BACKUP_S3_KEY_ID': 'integration-access',
        'BACKUP_S3_KEY_SECRET': 'integration-secret-password',
        'BACKUP_S3_LOGICAL_PREFIX': f'{engine}/{case}',
        'RESTORE_TEST_ENABLED': 'true', 'RESTORE_TEST_HOST': f'{engine}-restore',
        'RESTORE_TEST_PORT': '3306' if engine == 'mysql' else '5432',
        'RESTORE_TEST_USER': 'root' if engine == 'mysql' else 'postgres',
        'RESTORE_TEST_PASS': PASSWORD,
    })
    return env


class Integration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        RESULTS.mkdir(parents=True, exist_ok=True)
        Path('/suite/empty.env').touch()
        cls.s3 = boto3.client('s3', endpoint_url='http://s3:9000',
                             aws_access_key_id='integration-access',
                             aws_secret_access_key='integration-secret-password',
                             region_name='us-east-1',
                             config=Config(connect_timeout=2, read_timeout=3,
                                           retries={'max_attempts': 0},
                                           s3={'addressing_style': 'path'}))
        deadline = time.monotonic() + 90
        while True:
            try:
                cls.s3.list_buckets()
                break
            except Exception:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(1)
        cls.s3.create_bucket(Bucket=BUCKET)
        cls.proxy = ThreadingHTTPServer(('127.0.0.1', 9001), S3Proxy)
        cls.addClassCleanup(cls.proxy.server_close)
        cls.addClassCleanup(cls.proxy.shutdown)
        threading.Thread(target=cls.proxy.serve_forever, daemon=True).start()

    def test_mysql(self):
        self.exercise('mysql')

    def test_postgres(self):
        self.exercise('postgres')

    def get_object(self, key):
        response = self.s3.get_object(Bucket=BUCKET, Key=key)
        try:
            return response['Body'].read()
        finally:
            response['Body'].close()

    def exercise(self, engine):
        sql(engine, 'source', f'CREATE DATABASE {DATABASE};')
        sql(engine, 'source', """
            CREATE TABLE customers (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL);
            CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), amount INTEGER);
            CREATE TABLE empty_table (id INTEGER PRIMARY KEY);
            INSERT INTO customers VALUES (1, 'Alice'), (2, 'O''Brien'), (3, 'Unicode café');
            INSERT INTO orders VALUES (10, 1, 1250), (11, 2, NULL);
            CREATE VIEW customer_names AS SELECT id, name FROM customers;
        """, DATABASE)
        source_names = database_names(engine, 'source')
        source_rows = sql(engine, 'source', 'SELECT * FROM customers ORDER BY id', DATABASE)
        restore_names = database_names(engine, 'restore')
        self.assertNotIn(DATABASE, restore_names)
        env = environment(engine, 'success')
        self.assertNotEqual(env['DB_HOST'], env['RESTORE_TEST_HOST'])
        S3Proxy.events.clear()
        command(['sdl-db-backup'], env)
        history = Path(env['BACKUP_LOG_DIR']) / 'backup-runs.jsonl'
        run = json.loads(history.read_text().splitlines()[-1])
        self.assertEqual(run['status'], 'success')
        self.assertEqual(run['logical_upload_status'], 'success')
        self.assertEqual(run['databases_failed'], 0)
        self.assertEqual(run['databases_succeeded'], 1)
        folder = Path(run['run_folder'])
        manifest = json.loads((folder / 'manifest.json').read_text())
        self.assertEqual(manifest['run_id'], run['run_id'])
        self.assertEqual(len(manifest['results']), 1)
        db = run['results'][0]
        self.assertEqual(db['name'], DATABASE)
        self.assertEqual(db['status'], 'success')
        self.assertEqual(db['table_count'], 3)
        self.assertEqual(db['view_count'], 1)
        self.assertEqual(db['row_count_mode'], 'exact')
        self.assertEqual(db['table_row_counts'], {'customers': 3, 'orders': 2, 'empty_table': 0})
        self.assertEqual(set(db['sample_row_hashes']), {'customers', 'orders', 'empty_table'})
        artifact = folder / f'{DATABASE}.sql.gz.enc'
        encrypted = artifact.read_bytes()
        self.assertTrue(encrypted.startswith(b'SDBE2'))
        self.assertNotIn(b'CREATE TABLE', encrypted)
        self.assertEqual(hashlib.sha256(encrypted).hexdigest(), db['artifact_sha256'])
        compressed = command(['sdl-db-backup', 'decrypt', str(artifact)], env).stdout
        payload = gzip.decompress(compressed)
        self.assertIn(b'CREATE TABLE', payload)
        self.assertEqual(hashlib.sha256(payload).hexdigest(), db['sql_sha256'])
        self.assertFalse(list(folder.glob('*.sql')))
        self.assertFalse(list(folder.glob('*.sql.gz')))

        # Read and independently hash every object committed by the S3 server.
        prefix = f"{env['BACKUP_S3_LOGICAL_PREFIX']}/{run['run_id']}"
        marker = json.loads(self.get_object(prefix + '/COMPLETED.json'))
        self.assertEqual(marker['format_version'], 2)
        self.assertEqual(marker['status'], 'COMPLETE')
        self.assertEqual(marker['run_id'], run['run_id'])
        self.assertEqual(marker['bucket'], BUCKET)
        self.assertEqual(marker['prefix'], prefix)
        self.assertEqual({item['key'] for item in marker['artifacts']},
                         {'manifest.json', artifact.name})
        events = list(S3Proxy.events)
        marker_index = next(i for i, event in enumerate(events)
                            if event[:2] == ('PUT', f'/{BUCKET}/{prefix}/COMPLETED.json'))
        for item in marker['artifacts']:
            remote = self.get_object(prefix + '/' + item['key'])
            self.assertEqual(len(remote), item['size_bytes'])
            self.assertEqual(hashlib.sha256(remote).hexdigest(), item['sha256'])
            if item['key'] == artifact.name:
                self.assertEqual(remote, encrypted)
            else:
                # The local manifest is finalized again after remote commit.
                remote_manifest = json.loads(remote)
                self.assertEqual(remote_manifest['run_id'], run['run_id'])
                self.assertEqual(remote_manifest['results'], manifest['results'])
                (folder / 'remote-manifest.json').write_bytes(remote)
            for method in ('PUT', 'HEAD'):
                self.assertIn((method, f"/{BUCKET}/{prefix}/{item['key']}", 200), events[:marker_index])
        self.assertFalse(any(method == 'PUT' for method, _, _ in events[marker_index + 1:]))
        (folder / 's3-events.json').write_text(json.dumps(events, indent=2))

        validate = ['sdl-db-backup-validate', '--run-id', run['run_id']]
        command(validate + ['--mode', 'logical'], env)
        wrong_key = dict(env, BACKUP_ENCRYPTION_KEY='f' * 64)
        command(['sdl-db-backup', 'decrypt', str(artifact)], wrong_key, ok=False)
        command(validate + ['--mode', 'logical'], wrong_key, ok=False)
        for broken in (encrypted[:-8], encrypted[:25] + bytes([encrypted[25] ^ 1]) + encrypted[26:]):
            try:
                artifact.write_bytes(broken)
                command(['sdl-db-backup', 'decrypt', str(artifact)], env, ok=False)
                command(validate + ['--mode', 'logical'], env, ok=False)
            finally:
                artifact.write_bytes(encrypted)

        # Restore the downloaded encrypted object, so remote durability is part
        # of the recovery path, then let the application verify schema and data.
        artifact.unlink()
        artifact.write_bytes(self.get_object(prefix + '/' + artifact.name))
        command(validate + ['--mode', 'restore'], env)
        verified = json.loads(history.read_text().splitlines()[-1])
        self.assertEqual(verified['validation_status'], 'success')
        self.assertEqual(verified['validation_mode'], 'sandbox restore test')
        self.assertTrue(all(db['valid'] for db in verified['validation_databases']))
        self.assertEqual(database_names(engine, 'restore'), restore_names)

        # Import success must not hide mismatched row counts or content hashes.
        saved_history = history.read_text()
        saved_manifest = (folder / 'manifest.json').read_bytes()
        for mismatch in ('row count', 'sample row hash'):
            bad = json.loads(saved_history.splitlines()[-1])
            if mismatch == 'row count':
                bad['results'][0]['table_row_counts']['customers'] += 1
            else:
                bad['results'][0]['sample_row_hashes']['customers'] = '0' * 64
            try:
                history.write_text(json.dumps(bad) + '\n')
                rejected = command(validate + ['--mode', 'restore'], env, ok=False)
                self.assertIn((mismatch + ' mismatch').encode(), rejected.stdout + rejected.stderr)
                failed_validation = json.loads(history.read_text().splitlines()[-1])
                self.assertEqual(failed_validation['validation_status'], 'failed')
            finally:
                history.write_text(saved_history)
                (folder / 'manifest.json').write_bytes(saved_manifest)
            self.assertEqual(database_names(engine, 'restore'), restore_names)
        self.assertEqual(database_names(engine, 'source'), source_names)
        self.assertEqual(sql(engine, 'source', 'SELECT * FROM customers ORDER BY id', DATABASE), source_rows)

        # Fail manifest upload after the encrypted artifact reached S3.
        failed_env = environment(engine, 'failed-upload')
        S3Proxy.events.clear()
        S3Proxy.fail_manifest = True
        try:
            command(['sdl-db-backup'], failed_env, ok=False)
        finally:
            S3Proxy.fail_manifest = False
        failed = json.loads((Path(failed_env['BACKUP_LOG_DIR']) / 'backup-runs.jsonl').read_text().splitlines()[-1])
        self.assertEqual(failed['logical_upload_status'], 'failed')
        self.assertNotEqual(failed['status'], 'success')
        failed_prefix = f"{failed_env['BACKUP_S3_LOGICAL_PREFIX']}/{failed['run_id']}/"
        keys = {obj['Key'] for obj in self.s3.list_objects_v2(Bucket=BUCKET, Prefix=failed_prefix).get('Contents', [])}
        self.assertIn(failed_prefix + artifact.name, keys)
        self.assertNotIn(failed_prefix + 'COMPLETED.json', keys)
        self.assertTrue(any(status == 503 for _, _, status in S3Proxy.events))
        # The pre-upload immutability check HEADs COMPLETED.json (404); it must never be written.
        self.assertFalse(any(method == 'PUT' and path.endswith('/COMPLETED.json')
                             for method, path, _ in S3Proxy.events))
        (RESULTS / engine / 'failed-upload' / 's3-events.json').write_text(json.dumps(S3Proxy.events, indent=2))


if __name__ == '__main__':
    unittest.main(verbosity=2)
