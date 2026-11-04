import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
import fcntl


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    result = importlib.util.module_from_spec(spec); spec.loader.exec_module(result); return result
snap = module('snapshot', 'snapshot.py')
restore = module('restore', 'restore-prepare.py')


class RecoveryTests(unittest.TestCase):
    def test_snapshot_replay_revocation_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); state = root / 'state'
            for part in ['config', 'data', 'runtime', 'security']:
                (state / part).mkdir(parents=True)
                (state / part / '.epusdt-storage').write_text('secure-gateway-v1\n', encoding='utf-8')
            (state / 'config/gateway.json').write_text('{"synthetic":true}', encoding='utf-8')
            (state / 'security/revocations.jsonl').write_text('{"id":"old-revoked"}\n', encoding='utf-8')
            with sqlite3.connect(str(state / 'data/epusdt.db')) as db:
                db.execute('PRAGMA journal_mode=WAL')
                db.execute('CREATE TABLE payment_candidates(id INTEGER PRIMARY KEY, acknowledged_at_ms INTEGER)')
                db.execute('INSERT INTO payment_candidates VALUES (1, 123)')
                db.execute('CREATE TABLE secure_admin_sessions(hash TEXT)')
                db.execute("INSERT INTO secure_admin_sessions VALUES ('old-session')")
                db.commit()
            with sqlite3.connect(str(state / 'runtime/epusdt-runtime.db')) as db:
                db.execute('CREATE TABLE runtime_marker(value TEXT)'); db.execute("INSERT INTO runtime_marker VALUES ('retained')")
            merchant = root / 'merchant.sqlite'
            with sqlite3.connect(str(merchant)) as db:
                db.execute('CREATE TABLE payment_automation_control(frozen INTEGER, revision INTEGER)')
                db.execute('INSERT INTO payment_automation_control VALUES (1, 9)')
            output = root / 'snapshot'
            with (state / 'runtime/process.lock').open('a+b') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                with self.assertRaises(BlockingIOError): snap.snapshot(state, merchant, output, 'synthetic', 'sha256:synthetic', 9)
            snap.snapshot(state, merchant, output, 'synthetic', 'sha256:synthetic', 9)
            with self.assertRaises(ValueError): snap.snapshot(state, merchant, output, 'synthetic', 'sha256:synthetic', 9)
            (state / 'security/revocations.jsonl').write_text('{"id":"new-revoked"}\n', encoding='utf-8')
            target = root / 'restored'
            restore.prepare(output, state / 'security', target, state / 'config/gateway.json')
            with sqlite3.connect(str(target / 'data/epusdt.db')) as db:
                self.assertEqual(db.execute('SELECT acknowledged_at_ms FROM payment_candidates').fetchone()[0], 0)
                self.assertEqual(db.execute('SELECT count(*) FROM secure_admin_sessions').fetchone()[0], 0)
            revocations = [json.loads(line)['id'] for line in (target / 'security/revocations.jsonl').read_text(encoding='utf-8').splitlines()]
            self.assertCountEqual(revocations, ['old-revoked', 'new-revoked'])
            with self.assertRaises(ValueError): restore.prepare(output, state / 'security', target, state / 'config/gateway.json')
            manifest_path = output / 'manifest.json'
            original_manifest = manifest_path.read_text(encoding='utf-8')
            missing = json.loads(original_manifest)
            del missing['files']['gateway.db']
            manifest_path.write_text(json.dumps(missing), encoding='utf-8')
            with self.assertRaises(ValueError): restore.prepare(output, state / 'security', root / 'missing-manifest-entry', state / 'config/gateway.json')
            manifest_path.write_text(original_manifest, encoding='utf-8')
            with (output / 'gateway.db').open('ab') as file: file.write(b'tampered')
            with self.assertRaises(ValueError): restore.prepare(output, state / 'security', root / 'bad-restore', state / 'config/gateway.json')
            self.assertFalse((root / 'bad-restore').exists())

if __name__ == '__main__': unittest.main()
