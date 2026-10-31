#!/usr/bin/env python3
"""Manually fetch an already prepared private snapshot, encrypt with age on Mac.

No timer, automatic server snapshot, payment toggle, cleanup of old backups or
server restart. The user decides when to prepare and fetch a snapshot.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--host', default='RN')
    p.add_argument('--remote-snapshot', required=True)
    p.add_argument('--recipient', required=True, help='age public recipient; never the private identity')
    p.add_argument('--output', required=True, type=Path)
    p.add_argument('--handoff-root', type=Path, help='optional local dujiao_next root; includes only named handoff docs')
    a = p.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9_.-]+', a.host) or not re.fullmatch(r'/[A-Za-z0-9_./-]+', a.remote_snapshot) or '..' in a.remote_snapshot.split('/'):
        p.error('use a configured SSH alias and an absolute path without shell metacharacters')
    if a.output.exists() or Path(str(a.output)+'.sha256').exists() or not str(a.output).endswith('.tar.age'):
        p.error('choose a new output ending in .tar.age; existing backups are never overwritten')
    if not shutil.which('age'):
        p.error('install age on this Mac before manually running this command')
    os.umask(0o077)
    a.output.parent.mkdir(parents=True, exist_ok=True)
    # Temporary plaintext exists only during this explicitly invoked operation.
    with tempfile.TemporaryDirectory(prefix='epusdt-manual-backup-') as temp:
        root = Path(temp)
        subprocess.run(['scp', '-r', '--', a.host + ':' + a.remote_snapshot, str(root / 'snapshot')], check=True)
        snap = root / 'snapshot'
        manifest = json.loads((snap / 'manifest.json').read_text(encoding='utf-8'))
        required = {'gateway.db', 'runtime.db', 'gateway.json', 'revocations.jsonl'}
        if manifest.get('format') != 1 or not isinstance(manifest.get('files'), dict) or not required.issubset(manifest['files']):
            raise ValueError('invalid snapshot manifest')
        if len([name for name in manifest['files'] if name.startswith('merchant.')]) != 1:
            raise ValueError('paired merchant snapshot required')
        for name, expected in manifest['files'].items():
            if Path(name).name != name or (snap / name).is_symlink() or not (snap / name).is_file():
                raise ValueError('unsafe manifest path')
            path = snap / name
            h = hashlib.sha256()
            with path.open('rb') as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b''): h.update(chunk)
            if path.stat().st_size != expected['bytes'] or h.hexdigest() != expected['sha256']:
                raise ValueError('snapshot checksum mismatch')
        expected_files = set(manifest['files']) | {'manifest.json'}
        if any(path.is_symlink() or not path.is_file() or path.name not in expected_files for path in snap.iterdir()):
            raise ValueError('unexpected snapshot file or link')
        if a.handoff_root:
            docs = root / 'handoff'; docs.mkdir()
            for name in ['AGENTS.md', 'README.md', 'HANDOFF.md', 'Task.md', 'PROJECT_MAP.md', 'MAINTENANCE.md', 'DEPLOYMENT_REVIEW.md', 'SECURITY_REVIEW.md', 'LOCAL_ACCEPTANCE.md']:
                source = a.handoff_root / name
                if source.is_file(): shutil.copyfile(source, docs / name)
        archive = root / 'bundle.tar'
        with tarfile.open(archive, 'w') as tar:
            tar.add(snap, arcname='snapshot', recursive=True)
            if (root / 'handoff').exists(): tar.add(root / 'handoff', arcname='handoff')
        # Open exclusively: neither age nor a rerun may overwrite a prior backup.
        with a.output.open('xb') as target:
            try:
                subprocess.run(['age', '-r', a.recipient, str(archive)], stdout=target, check=True)
                target.flush(); os.fsync(target.fileno())
            except BaseException:
                a.output.unlink(missing_ok=True)
                raise
        hasher = hashlib.sha256()
        with a.output.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''): hasher.update(chunk)
        digest = hasher.hexdigest()
        with Path(str(a.output) + '.sha256').open('x', encoding='utf-8') as out:
            out.write(digest + '  ' + a.output.name + '\n')
    print('Encrypted backup saved and source checksums verified; private age identity must be kept separately.')

if __name__ == '__main__':
    main()
