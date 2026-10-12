"""Deployment transaction tests using disposable files, never production paths."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import deploy_storage_manager as deployment


class StorageReleaseDeploymentTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.base = self.root / 'remediation'
        self.release = self.base / 'storage-release'
        self.release.mkdir(parents=True)
        self.journals = self.base / 'storage-rollback'
        for name, value in [('ROOT', self.root), ('BASE', self.base),
                            ('RELEASE', self.release), ('JOURNALS', self.journals)]:
            p = patch.object(deployment, name, value)
            p.start()
            self.addCleanup(p.stop)
        (self.root / '.env').write_text('BACKUP_STORAGE_AUTO_CLEANUP=false\nRESTORE_TEST_ENABLED=false\n')
        (self.root / '.env').chmod(0o600)
        (self.root / 'runner.go').write_text('original source\n')
        (self.release / 'runner.go').write_text('tested new source\n')
        self.package = {
            'baseline_env_sha256': deployment.digest(self.root / '.env'),
            'files': [{'path': 'runner.go', 'candidate': 'runner.go', 'mode': 0o644,
                       'baseline_sha256': deployment.digest(self.root / 'runner.go'),
                       'candidate_sha256': deployment.digest(self.release / 'runner.go')}],
        }
        proof = {'state': 'DRY_RUN', 'protection_checked': True,
                 'protected_local': ['local'], 'protected_remote': ['remote1', 'remote2']}
        (self.base / 'storage-dry-run.json').write_text(json.dumps(proof))

    def test_apply_and_rollback_preserve_configuration_and_source(self):
        before = (self.root / '.env').read_bytes()
        deployment.apply(self.package)
        self.assertEqual((self.root / 'runner.go').read_text(), 'tested new source\n')
        self.assertIn('BACKUP_STORAGE_AUTO_CLEANUP=true', (self.root / '.env').read_text())
        self.assertEqual((self.root / '.env').stat().st_mode & 0o777, 0o600)
        transaction = next(self.journals.glob('*/state.json')).parent
        deployment.restore(transaction, self.package)
        self.assertEqual((self.root / '.env').read_bytes(), before)
        self.assertEqual((self.root / 'runner.go').read_text(), 'original source\n')

    def test_failure_after_source_write_rolls_back(self):
        original = deployment.owned_atomic
        calls = 0

        def fail_once(path, *args):
            nonlocal calls
            calls += 1
            if calls == 2:
                raise OSError('simulated environment write failure')
            return original(path, *args)

        with patch.object(deployment, 'owned_atomic', fail_once):
            with self.assertRaises(OSError):
                deployment.apply(self.package)
        self.assertEqual((self.root / 'runner.go').read_text(), 'original source\n')
        self.assertIn('BACKUP_STORAGE_AUTO_CLEANUP=false', (self.root / '.env').read_text())
        state = json.loads(next(self.journals.glob('*/state.json')).read_text())
        self.assertEqual(state['status'], 'ROLLED_BACK')

    def test_configuration_drift_refuses_any_write(self):
        (self.root / '.env').write_text('changed configuration\n')
        with self.assertRaisesRegex(RuntimeError, 'configuration drift'):
            deployment.apply(self.package)
        self.assertEqual((self.root / 'runner.go').read_text(), 'original source\n')

    def test_unverified_dry_run_refuses_any_write(self):
        (self.base / 'storage-dry-run.json').write_text('{"state":"CRITICAL"}')
        with self.assertRaisesRegex(RuntimeError, 'dry run is required'):
            deployment.apply(self.package)
        self.assertEqual((self.root / 'runner.go').read_text(), 'original source\n')

    def test_symlink_destination_refused(self):
        (self.root / 'runner.go').unlink()
        (self.root / 'runner.go').symlink_to(self.release / 'runner.go')
        with self.assertRaisesRegex(RuntimeError, 'symlink'):
            deployment.apply(self.package)
        self.assertEqual((self.release / 'runner.go').read_text(), 'tested new source\n')


if __name__ == '__main__':
    unittest.main()
