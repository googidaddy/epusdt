#!/usr/bin/env python3
"""Explicit offline gateway snapshot; never schedules or starts/stops services.

The caller freezes merchant U-payment automation, stops the gateway, and supplies
an independently consistent merchant snapshot obtained AFTER the gateway stopped.
The output is private plaintext until the Mac wrapper encrypts it with age.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
from datetime import datetime, timezone


def sha256(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def snapshot(state, merchant, output, revision, image_id, freeze_revision):
    if not state.is_dir() or not merchant.is_file() or not state.is_absolute():
        raise ValueError('absolute gateway state and existing merchant snapshot required')
    if freeze_revision < 1 or not revision or not image_id:
        raise ValueError('recorded merchant freeze revision and actual image/source identity required')
    if output.exists():
        raise ValueError('snapshot output must not exist')
    for directory in ('config', 'data', 'runtime', 'security'):
        if not (state / directory).is_dir():
            raise ValueError('missing persistent directory')
        if directory != 'config' and (state / directory / '.epusdt-storage').read_text(encoding='utf-8') != 'secure-gateway-v1\n':
            raise ValueError('unrecognized storage marker')
    # The secure gateway holds this same lock for its entire serve lifetime.
    with (state / 'runtime/process.lock').open('a+b') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        output.mkdir(mode=0o700)
        try:
            for source, name in [(state / 'data/epusdt.db', 'gateway.db'),
                                 (state / 'runtime/epusdt-runtime.db', 'runtime.db')]:
                if not source.is_file():
                    raise ValueError('persistent database missing')
                with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True) as src:
                    with sqlite3.connect(str(output / name)) as dst:
                        src.backup(dst)
                        if dst.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                            raise ValueError('database integrity check failed')
                os.chmod(output / name, 0o600)
            shutil.copyfile(state / 'config/gateway.json', output / 'gateway.json')
            shutil.copyfile(state / 'security/revocations.jsonl', output / 'revocations.jsonl')
            shutil.copyfile(merchant, output / ('merchant' + ''.join(merchant.suffixes)))
            files = {}
            for path in output.iterdir():
                os.chmod(path, 0o600)
                files[path.name] = {'sha256': sha256(path), 'bytes': path.stat().st_size}
            manifest = {'format': 1, 'created_at': datetime.now(timezone.utc).isoformat(),
                        'source_revision': revision, 'image_id': image_id,
                        'merchant_freeze_revision': freeze_revision,
                        'ordering': 'merchant frozen; gateway stopped; merchant snapshot; gateway snapshot',
                        'restore_requires_reconciliation': True, 'files': files}
            (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
            os.chmod(output / 'manifest.json', 0o600)
        except BaseException:
            # Only remove the newly created, incomplete output, never an old backup.
            shutil.rmtree(output)
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--state', required=True, type=Path)
    parser.add_argument('--merchant-snapshot', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--image-id', required=True)
    parser.add_argument('--freeze-revision', type=int, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    snapshot(args.state, args.merchant_snapshot, args.output, args.revision, args.image_id, args.freeze_revision)
    print('Consistent gateway snapshot complete; keep private and encrypt before archival.')

if __name__ == '__main__':
    main()
