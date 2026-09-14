#!/usr/bin/env python3
"""Test-only owner of one pinned native Lite process and temporary database."""
import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import socket
import tempfile
import threading

from milvus_lite.server import Server

parser = argparse.ArgumentParser()
parser.add_argument('--restore-directory', type=Path)
args = parser.parse_args()
root = args.restore_directory or Path(tempfile.mkdtemp(prefix='sea-sparse-native-'))
if args.restore_directory:
    prior = json.loads((root / 'runtime.json').read_text())
    if prior.get('owner') != 'Sparse isolated acceptance only' or prior.get('milvus_lite') != '2.5.1':
        raise RuntimeError('not this task isolated database')
    (root / 'release').unlink(missing_ok=True)
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    endpoint = '127.0.0.1:' + str(sock.getsockname()[1])
server = Server(str(root / 'sparse.db'), endpoint)
if not server.init() or not server.start():
    raise RuntimeError('native Lite failed to start')
info = {'endpoint': endpoint, 'milvus_lite': importlib.metadata.version('milvus-lite'), 'binary_sha256': hashlib.sha256(Path(server.milvus_bin).read_bytes()).hexdigest(), 'binary': server.milvus_bin, 'directory': str(root), 'owner': 'Sparse isolated acceptance only'}
(root / 'runtime.json').write_text(json.dumps(info, indent=2) + '\n')
# Structured test handshake, not application observability acceptance.
print(json.dumps(info), flush=True)
stop = threading.Event()
signal.signal(signal.SIGTERM, lambda *_: stop.set())
signal.signal(signal.SIGINT, lambda *_: stop.set())
try:
    while not stop.wait(.5):
        if (root / 'release').exists():
            break
        if server._p.poll() is not None:
            raise RuntimeError('native Lite process exited')
finally:
    server.stop()
