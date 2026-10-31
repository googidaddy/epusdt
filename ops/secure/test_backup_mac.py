"""Real age encryption with a synthetic local copy replacing SCP; never contacts SSH."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('backup_mac', Path(__file__).with_name('backup-mac.py'))
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


@unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'requires temporary age tooling')
class MacBackupTests(unittest.TestCase):
    def test_encrypted_roundtrip_and_rejected_incomplete_snapshot(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / 'source'
            source.mkdir()
            files = {}
            for name in ['gateway.db', 'runtime.db', 'gateway.json', 'revocations.jsonl', 'merchant.dump']:
                value = ('synthetic-' + name).encode('utf-8')
                (source / name).write_bytes(value)
                files[name] = {'bytes': len(value), 'sha256': hashlib.sha256(value).hexdigest()}
            manifest = source / 'manifest.json'
            manifest.write_text(json.dumps({'format': 1, 'files': files}), encoding='utf-8')
            identity = root / 'identity.txt'
            subprocess.run(['age-keygen', '-o', str(identity)], check=True, capture_output=True)
            recipient = subprocess.check_output(['age-keygen', '-y', str(identity)], text=True).strip()
            output = root / 'backup.tar.age'
            real_run = subprocess.run

            def local_transfer(args, **kwargs):
                if args[0] == 'scp':
                    self.assertEqual(args[3], 'synthetic-host:/prepared-snapshot')
                    shutil.copytree(source, args[4])
                    return subprocess.CompletedProcess(args, 0)
                return real_run(args, **kwargs)

            argv = ['backup-mac.py', '--host', 'synthetic-host', '--remote-snapshot', '/prepared-snapshot',
                    '--recipient', recipient, '--output', str(output)]
            old_umask = os.umask(0o077)
            try:
                with patch.object(sys, 'argv', argv), patch.object(backup.subprocess, 'run', side_effect=local_transfer):
                    backup.main()
                    saved = output.read_bytes()
                    self.assertTrue(saved.startswith(b'age-encryption.org/v1'))
                    self.assertEqual(output.stat().st_mode & 0o777, 0o600)
                    with self.assertRaises(SystemExit):
                        backup.main()
                    self.assertEqual(saved, output.read_bytes())
                    files.pop('gateway.db')
                    manifest.write_text(json.dumps({'format': 1, 'files': files}), encoding='utf-8')
                    argv[-1] = str(root / 'bad.tar.age')
                    with self.assertRaises(ValueError):
                        backup.main()
                    self.assertFalse((root / 'bad.tar.age').exists())
            finally:
                os.umask(old_umask)
            plain = subprocess.check_output(['age', '-d', '-i', str(identity), str(output)])
            with tarfile.open(fileobj=io.BytesIO(plain)) as archive:
                self.assertEqual(archive.extractfile('snapshot/merchant.dump').read(), b'synthetic-merchant.dump')
                self.assertEqual(archive.extractfile('snapshot/gateway.db').read(), b'synthetic-gateway.db')
            recorded = Path(str(output) + '.sha256').read_text(encoding='utf-8').split()[0]
            self.assertEqual(recorded, hashlib.sha256(saved).hexdigest())


if __name__ == '__main__':
    unittest.main()
