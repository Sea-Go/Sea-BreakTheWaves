#!/usr/bin/env python3
"""Own one disposable Milvus Lite process for three physical search lanes."""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import signal
import threading

from milvus_lite.adapter.grpc.server import start_server_in_thread


OWNER = "Search native three-lane isolated acceptance only"
VERSION = "3.2.1"
FAISS_VERSION = "1.15.0"


def write_once(path: Path, value: dict) -> None:
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "wb") as output:
        output.write(data)


def package_root_sha256() -> str:
    """Hash each installed wheel file by its relative path and actual bytes."""
    distribution = importlib.metadata.distribution("milvus-lite")
    if not distribution.files:
        raise RuntimeError("locked native Lite distribution has no installed file manifest")
    digest = hashlib.sha256(b"sea.search.milvus-lite.package.v1\0")
    for item in sorted(distribution.files, key=str):
        path = Path(distribution.locate_file(item))
        if not path.is_file():
            raise RuntimeError("locked native Lite package file is missing")
        data = path.read_bytes()
        name = str(item).encode()
        digest.update(len(name).to_bytes(4, "big") + name)
        digest.update(len(data).to_bytes(8, "big") + hashlib.sha256(data).digest())
    return digest.hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    root = args.directory.resolve(strict=True)
    if not root.is_dir() or (root.stat().st_mode & 0o077):
        raise RuntimeError("native Lite database must be in a task-owned private directory")
    if (root / "runtime.json").exists() or (root / "data").exists():
        raise RuntimeError("native Lite owner refuses an existing runtime or database")
    version = importlib.metadata.version("milvus-lite")
    if version != VERSION:
        raise RuntimeError("native Lite package version differs from the fixed acceptance version")
    engine_root = package_root_sha256()
    faiss_version = importlib.metadata.version("faiss-cpu")
    if faiss_version != FAISS_VERSION:
        raise RuntimeError("native FAISS HNSW library version differs from the fixed acceptance version")
    server, db, port = start_server_in_thread(str(root / "data"),
                                               host="127.0.0.1", port=0)
    endpoint = "127.0.0.1:" + str(port)
    stopped = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopped.set())
    signal.signal(signal.SIGINT, lambda *_: stopped.set())
    runtime = {
        "schema_version": "sea.search.native-lite-runtime.v2",
        "owner": OWNER,
        "endpoint": endpoint,
        "engine": "lite",
        "milvus_lite": version,
        "engine_package_sha256": engine_root,
        "faiss_version": faiss_version,
        "directory": str(root),
        "release_path": str(root / "release"),
    }
    try:
        write_once(root / "runtime.json", runtime)
        print(json.dumps(runtime, sort_keys=True), flush=True)
        while not stopped.wait(0.25):
            if (root / "release").exists():
                break
    finally:
        server.stop(grace=2).wait()
        db.close()
        write_once(root / "stop.json", {"owner": OWNER, "milvus_lite": version,
                                         "faiss_version": faiss_version,
                                         "engine_package_sha256": engine_root,
                                         "stopped": True, "endpoint": endpoint})


if __name__ == "__main__":
    main()
