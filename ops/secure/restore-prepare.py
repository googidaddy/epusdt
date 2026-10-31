#!/usr/bin/env python3
"""Prepare an isolated gateway restore; never overwrite live state or resume.

Merchant restore is a separate primary-DB operation. Run payment-ops freeze on
that restored DB before starting API/workers. Reconcile the backup gap before
resuming U payments. Blockchain queries cannot reconstruct already delivered cards.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3


def prepare(snapshot, current_security, output, current_config):
    if output.exists(): raise ValueError('restore target must not exist')
    manifest = json.loads((snapshot / 'manifest.json').read_text(encoding='utf-8'))
    required = {'gateway.db', 'runtime.db', 'gateway.json', 'revocations.jsonl'}
    if manifest.get('format') != 1 or not isinstance(manifest.get('files'), dict) or not required.issubset(manifest['files']):
        raise ValueError('unsupported or incomplete snapshot')
    if len([name for name in manifest['files'] if name.startswith('merchant.')]) != 1:
        raise ValueError('paired merchant snapshot required')
    for name, entry in manifest['files'].items():
        if Path(name).name != name or (snapshot / name).is_symlink() or not (snapshot / name).is_file(): raise ValueError('unsafe snapshot path')
        h = hashlib.sha256()
        with (snapshot / name).open('rb') as f:
            for block in iter(lambda: f.read(1024 * 1024), b''): h.update(block)
        if h.hexdigest() != entry['sha256'] or (snapshot / name).stat().st_size != entry['bytes']:
            raise ValueError('snapshot digest mismatch')
    # Never replace current revocations/credentials with an old config snapshot.
    if (current_security / '.epusdt-storage').read_text(encoding='utf-8') != 'secure-gateway-v1\n':
        raise ValueError('current security state unavailable; operator recovery required')
    revocations = {}
    for path in [snapshot / 'revocations.jsonl', current_security / 'revocations.jsonl']:
        for line in path.read_text(encoding='utf-8').splitlines():
            if not line.strip(): continue
            row = json.loads(line)
            if not isinstance(row.get('id'), str) or not row['id']: raise ValueError('invalid revocation ledger')
            revocations[row['id']] = row
    if not current_config.is_file(): raise ValueError('current private config required')
    output.mkdir(mode=0o700)
    try:
        for part in ['config', 'data', 'runtime', 'security']:
            (output / part).mkdir(mode=0o700)
            if part != 'config': (output / part / '.epusdt-storage').write_text('secure-gateway-v1\n', encoding='utf-8')
        shutil.copyfile(snapshot / 'gateway.db', output / 'data/epusdt.db')
        shutil.copyfile(snapshot / 'runtime.db', output / 'runtime/epusdt-runtime.db')
        shutil.copyfile(current_config, output / 'config/gateway.json')
        with (output / 'security/revocations.jsonl').open('w', encoding='utf-8') as out:
            for row in revocations.values(): out.write(json.dumps(row) + '\n')
        with sqlite3.connect(str(output / 'data/epusdt.db')) as db:
            if db.execute('PRAGMA integrity_check').fetchone()[0] != 'ok': raise ValueError('restored DB corrupt')
            tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
            for table in ['secure_admin_sessions', 'secure_admin_challenges', 'secure_bootstrap_grants']:
                if table in tables: db.execute('DELETE FROM ' + table)
            # Idempotent merchant receipt stores tolerate replay; never erase evidence.
            db.execute('UPDATE payment_candidates SET acknowledged_at_ms = 0')
            db.commit()
        for path in output.rglob('*'):
            if path.is_file(): os.chmod(path, 0o600)
        (output / 'RECONCILIATION_REQUIRED.txt').write_text(
            'Isolated gateway restore only. Do not attach live domains or open U payments.\n'
            'Restore merchant primary DB separately, run payment-ops freeze, verify paired snapshot identity,\n'
            'reconcile allocations/event consumption/fulfillment since the snapshot, then explicitly review resume.\n', encoding='utf-8')
    except BaseException:
        shutil.rmtree(output)
        raise


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--snapshot', required=True, type=Path)
    p.add_argument('--current-security', required=True, type=Path)
    p.add_argument('--current-config', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path)
    a = p.parse_args(); os.umask(0o077)
    prepare(a.snapshot, a.current_security, a.output, a.current_config)
    print('Isolated restore prepared; current credentials/revocations retained, old sessions removed, evidence replay queued. Merchant remains a separate frozen restore.')
if __name__ == '__main__': main()
