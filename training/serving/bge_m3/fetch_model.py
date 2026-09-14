#!/usr/bin/env python3
"""Fetch exactly the pinned official files, resume safely, and verify every byte."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import time
import urllib.request


def sha256(path):
    h = hashlib.sha256()
    with path.open('rb') as source:
        while chunk := source.read(4 << 20):
            h.update(chunk)
    return h.hexdigest()


def fetch(root, item):
    target = root / item['path']
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() and target.stat().st_size == item['size_bytes'] and sha256(target) == item['sha256']:
        print('verified', item['path'], flush=True)
        return
    partial = target.with_name(target.name + '.partial')
    for attempt in range(3):
        offset = partial.stat().st_size if partial.exists() else 0
        if offset == item['size_bytes']:
            break
        if offset > item['size_bytes']:
            raise ValueError('partial file exceeds pinned size')
        headers = {'User-Agent': 'Sea-BGE-M3-fetch/1'}
        if offset:
            headers['Range'] = f'bytes={offset}-'
        try:
            req = urllib.request.Request(item['url'] + '?download=true', headers=headers)
            with urllib.request.urlopen(req, timeout=60) as response:
                if offset and response.status != 206:
                    offset = 0
                if response.status == 206:
                    content_range = response.headers.get('Content-Range', '')
                    if not content_range.startswith(f'bytes {offset}-'):
                        raise ValueError('unexpected resume range')
                done = offset
                last = offset
                with partial.open('ab' if offset else 'wb') as output:
                    while chunk := response.read(4 << 20):
                        done += len(chunk)
                        if done > item['size_bytes']:
                            raise ValueError('download exceeds pinned size')
                        output.write(chunk)
                        if done - last >= 64 << 20:
                            print(item['path'], done, '/', item['size_bytes'], flush=True)
                            last = done
                    output.flush()
                    os.fsync(output.fileno())
            if partial.stat().st_size == item['size_bytes']:
                break
        except OSError as error:
            print('retry', item['path'], attempt + 1, type(error).__name__, flush=True)
            if attempt == 2:
                raise
            time.sleep(2)
    if not partial.exists() or partial.stat().st_size != item['size_bytes'] or sha256(partial) != item['sha256']:
        raise ValueError('pinned model hash/size mismatch: ' + item['path'])
    partial.replace(target)
    print('downloaded+verified', item['path'], flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--directory', type=Path)
    args = parser.parse_args()
    lock = json.loads((Path(__file__).parent / 'model.lock.json').read_text())
    root = args.directory or Path.home() / '.cache/sea-models/bge-m3' / lock['revision']
    root.mkdir(parents=True, exist_ok=True)
    print('model directory:', root, 'total bytes:', lock['total_bytes'], flush=True)
    for item in lock['files']:
        fetch(root, item)
    (root / 'sea-model.lock.json').write_text(json.dumps(lock, indent=2) + '\n')
    print('READY', root, flush=True)

if __name__ == '__main__':
    main()
