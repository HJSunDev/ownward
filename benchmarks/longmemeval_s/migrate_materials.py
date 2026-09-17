"""Copy and verify a storage-format upgrade without regenerating semantic work.

Original prepared snapshots and their generation receipts remain immutable.
The new receipt records physical compatibility, not a new semantic generation.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import sqlite3
import struct
import subprocess
import time
import zlib


def hashes(root):
    result = {}
    for p in sorted(Path(root).rglob('*')):
        if p.is_symlink():
            raise ValueError('prepared material must not contain symbolic links')
        if p.is_file():
            with p.open('rb') as f:
                h = hashlib.sha256()
                for b in iter(lambda: f.read(1024 * 1024), b''):
                    h.update(b)
            result[p.relative_to(root).as_posix()] = h.hexdigest()
    return result


def records(path):
    with Path(path).open('rb') as f:
        while True:
            header = f.read(16)
            if not header:
                return
            if len(header) != 16 or header[:4] != b'OWD3':
                raise ValueError('prepared derived log is incomplete')
            size, vector_size, checksum = struct.unpack('<III', header[4:])
            raw, vector = f.read(size), f.read(vector_size)
            if f.read(4) != b'DONE' or zlib.crc32(raw + vector) != checksum:
                raise ValueError('prepared derived log checksum changed')
            yield json.loads(raw), vector


def verify_compatible(source, target):
    """Verify prepared, completed v4/v5 materials; reject incomplete snapshots."""
    source, target = Path(source), Path(target)
    pointer = json.loads((target / 'storage.json').read_text(encoding='utf-8'))
    database = target / 'stores' / pointer['id'] / 'ownward.sqlite'
    c = sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True)
    try:
        old_assets = {}
        with (source / 'assets/information.jsonl').open(encoding='utf-8') as f:
            for line in f:
                event = json.loads(line)
                if event['operation'] not in ('snapshot', 'create', 'update'):
                    raise ValueError('prepared snapshot must contain current assets only')
                a = event['value']; old_assets[a['id']] = a
        if c.execute('SELECT count(*) FROM live_assets WHERE deleted=0').fetchone()[0] != len(old_assets):
            raise ValueError('prepared asset coverage changed')
        for asset, old in old_assets.items():
            revision, payload = c.execute('SELECT revision,payload FROM live_assets WHERE id=? AND deleted=0', (asset,)).fetchone()
            content = b''.join(r[0] for r in c.execute('SELECT bytes FROM content_chunks WHERE payload=? AND part=0 ORDER BY ordinal', (payload,))).decode('utf-8')
            details = json.loads(b''.join(r[0] for r in c.execute('SELECT bytes FROM content_chunks WHERE payload=? AND part=1 ORDER BY ordinal', (payload,))))
            if revision != old['revision'] or content != old['content']:
                raise ValueError('prepared source or revision changed')
            for key in ('contexts', 'explicit_relations', 'source'):
                if details.get(key) != old.get(key):
                    raise ValueError('prepared source metadata changed: ' + key)
        current = source / 'state/organization.binlog'
        manifest = source / 'state/current.json'
        if manifest.exists():
            m = json.loads(manifest.read_text(encoding='utf-8'))
            generation = m.get('generation')
            if generation:
                current = source / 'state/generations' / generation / 'organization.binlog'
        expected = {v['asset_id']: (v, vector) for v, vector in records(current)}
        verified = 0
        for asset, (old, vector) in expected.items():
            if asset not in old_assets or old['asset_revision'] != old_assets[asset]['revision']:
                continue
            row = c.execute('SELECT organization FROM organization_current WHERE asset=?', (asset,)).fetchone()
            if row is None:
                raise ValueError('prepared organization unavailable after migration')
            org = row[0]
            actual = json.loads(b''.join(r[0] for r in c.execute('SELECT bytes FROM organization_chunks WHERE organization=? ORDER BY ordinal', (org,))))
            for key in ('analysis', 'input_assets', 'inputs_known', 'semantic_work_reference', 'semantic_receipt', 'status', 'asset_revision'):
                if actual.get(key) != old.get(key):
                    raise ValueError('prepared semantic content changed: ' + key)
            if vector and c.execute('SELECT data FROM vectors WHERE organization=?', (org,)).fetchone()[0] != vector:
                raise ValueError('prepared vector bytes changed')
            verified += 1
        pending = c.execute('SELECT count(*) FROM semantic_jobs').fetchone()[0]
        if pending:
            raise ValueError('prepared snapshot is not fully organized')
        return {'assets': len(old_assets), 'organizations': verified, 'pending_semantic_jobs': pending,
                'raw_and_semantics_equal': True, 'vectors_bitwise_equal': True}
    finally:
        c.close()


def migrate_snapshot(source, target, command, *, environment=None):
    source, target = Path(source).resolve(), Path(target).resolve()
    if target == source or source in target.parents or target in source.parents:
        raise ValueError('migration requires an independent destination')
    before = hashes(source)
    target.mkdir(parents=True, exist_ok=False)
    shutil.copytree(source, target, dirs_exist_ok=True)
    started = time.monotonic()
    subprocess.run([*command, '--data-dir', str(target)], env=environment, check=True,
                   stdout=subprocess.DEVNULL, timeout=600)
    proof = verify_compatible(source, target)
    if hashes(source) != before:
        raise ValueError('original prepared snapshot changed')
    receipt = {'schema': 'ownward.prepared-storage-migration/v1', 'source_sha256': before,
               'target_sha256': hashes(target), 'migration_seconds': time.monotonic()-started,
               'migration_binary_sha256': hashlib.sha256(Path(command[0]).read_bytes()).hexdigest(),
               'new_semantic_calls': 0, **proof}
    target.with_name(target.name + '.migration.json').write_text(json.dumps(receipt, indent=2)+'\n', encoding='utf-8')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source', type=Path, required=True)
    p.add_argument('--target', type=Path, required=True)
    p.add_argument('--binary', type=Path, required=True)
    args = p.parse_args()
    print(json.dumps(migrate_snapshot(args.source, args.target, [str(args.binary.resolve()), 'rules']), indent=2))
