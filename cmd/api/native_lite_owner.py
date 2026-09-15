#!/usr/bin/env python3
"""Own one disposable Milvus Lite process for three physical search lanes."""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import socket
import threading

from milvus_lite.server import Server


OWNER = "Search native three-lane isolated acceptance only"
VERSION = "3.2.1"


def write_once(path: Path, value: dict) -> None:
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "wb") as output:
        output.write(data)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    root = args.directory.resolve(strict=True)
    if not root.is_dir() or (root.stat().st_mode & 0o077):
        raise RuntimeError("native Lite database must be in a task-owned private directory")
    if (root / "runtime.json").exists() or (root / "three-lane.db").exists():
        raise RuntimeError("native Lite owner refuses an existing runtime or database")
    version = importlib.metadata.version("milvus-lite")
    if version != VERSION:
        raise RuntimeError("native Lite binary version differs from the fixed acceptance version")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        endpoint = "127.0.0.1:" + str(listener.getsockname()[1])
    server = Server(str(root / "three-lane.db"), endpoint)
    if not server.init() or not server.start():
        raise RuntimeError("native Lite failed to start")
    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    runtime = {
        "schema_version": "sea.search.native-lite-runtime.v1",
        "owner": OWNER,
        "endpoint": endpoint,
        "engine": "lite",
        "milvus_lite": version,
        "binary_sha256": hashlib.sha256(Path(server.milvus_bin).read_bytes()).hexdigest(),
        "directory": str(root),
        "release_path": str(root / "release"),
    }
    try:
        write_once(root / "runtime.json", runtime)
        print(json.dumps(runtime, sort_keys=True), flush=True)
        while not stopped.wait(0.25):
            if (root / "release").exists():
                break
            if server._p.poll() is not None:
                raise RuntimeError("native Lite process exited before release")
    finally:
        server.stop()
        write_once(root / "stop.json", {"owner": OWNER, "milvus_lite": version,
                                         "stopped": True, "endpoint": endpoint})


if __name__ == "__main__":
    main()
