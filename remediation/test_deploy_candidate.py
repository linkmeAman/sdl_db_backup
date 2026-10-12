"""Deployment transaction tests use disposable fixtures only."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('deploy', Path(__file__).with_name('deploy_candidate.py'))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        deploy.ROOT = Path(self.temp.name)
        deploy.BASE = deploy.ROOT / 'remediation'
        deploy.BASE.mkdir()
        (deploy.ROOT / '.env').write_text('SECRET=fixture-only\nBACKUP_DIR=old\nCUSTOM=keep\n')
        (deploy.ROOT / 'main.go').write_text('old source')
        (deploy.BASE / 'candidate.go').write_text('new source')
        self.overrides = {'BACKUP_DIR': str(deploy.BASE / 'production-backups')}
        (deploy.BASE / 'production-overrides.env').write_text('BACKUP_DIR=' + self.overrides['BACKUP_DIR'] + '\n')
        client = deploy.BASE / 'tools/mysql84/usr/bin/mysqldump'
        client.parent.mkdir(parents=True)
        client.write_bytes(b'fixture client')
        self.package = {
            'baseline_env_sha256': deploy.digest(deploy.ROOT / '.env'),
            'overrides_sha256': deploy.digest(deploy.BASE / 'production-overrides.env'),
            'dump_client_sha256': deploy.digest(client),
            'files': [{'path': 'main.go', 'candidate': 'candidate.go', 'mode': 0o644,
                       'baseline_sha256': deploy.digest(deploy.ROOT / 'main.go'),
                       'candidate_sha256': deploy.digest(deploy.BASE / 'candidate.go')}]}

    def test_apply_preserves_unknown_secrets_and_rollback(self):
        original = (deploy.ROOT / '.env').read_bytes()
        deploy.apply(self.package, self.overrides)
        self.assertEqual((deploy.ROOT / 'main.go').read_text(), 'new source')
        self.assertIn('SECRET=fixture-only', (deploy.ROOT / '.env').read_text())
        self.assertIn('CUSTOM=keep', (deploy.ROOT / '.env').read_text())
        self.assertEqual((deploy.ROOT / '.env').stat().st_mode & 0o777, 0o600)
        transaction = next((deploy.BASE / 'rollback').glob('*/state.json')).parent
        deploy.restore(transaction)
        self.assertEqual((deploy.ROOT / '.env').read_bytes(), original)
        self.assertEqual((deploy.ROOT / 'main.go').read_text(), 'old source')

    def test_drift_refuses_before_any_change(self):
        (deploy.ROOT / 'main.go').write_text('operator change')
        with self.assertRaisesRegex(RuntimeError, 'production file drift'):
            deploy.apply(self.package, self.overrides)
        self.assertEqual((deploy.ROOT / 'main.go').read_text(), 'operator change')
        self.assertFalse((deploy.BASE / 'rollback').exists())

    def test_write_failure_restores_originals(self):
        original = deploy.atomic
        failed = False

        def injected(path, data, mode):
            nonlocal failed
            if path == deploy.ROOT / '.env' and not failed:
                failed = True
                raise OSError('fixture write interruption')
            return original(path, data, mode)

        with patch.object(deploy, 'atomic', injected):
            with self.assertRaises(OSError):
                deploy.apply(self.package, self.overrides)
        self.assertEqual((deploy.ROOT / 'main.go').read_text(), 'old source')
        journal = next((deploy.BASE / 'rollback').glob('*/state.json'))
        self.assertEqual(json.loads(journal.read_text())['status'], 'ROLLED_BACK')


if __name__ == '__main__':
    unittest.main()
