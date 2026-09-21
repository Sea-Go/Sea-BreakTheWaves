#!/usr/bin/env python3
"""Verify one BTW artifact key through task-owned real SeaweedFS S3."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import socket
import subprocess
import sys
import tempfile
import urllib.parse
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO / "scripts"))
from local_s3 import start as start_s3  # noqa: E402


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(args: list[str], evidence: Path, name: str, env: dict[str, str]) -> None:
    with (evidence / name).open("wb") as output:
        result = subprocess.run(args, cwd=REPO, env=env, stdout=output,
                                stderr=subprocess.STDOUT, timeout=180)
    if result.returncode != 0:
        raise RuntimeError(f"{name} exited {result.returncode}; inspect task-owned evidence")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", required=True)
    arguments = parser.parse_args()
    runtime = Path(arguments.runtime).resolve()
    binary = runtime / "weed"
    if not binary.is_file():
        raise ValueError("locked SeaweedFS binary missing")
    evidence = Path(tempfile.mkdtemp(prefix="sea-btw-shared-s3-"))
    process = None
    try:
        process, prefix = start_s3(binary, evidence / "seaweedfs")
        address = urllib.parse.urlsplit(prefix)
        host, bucket = address.netloc, address.path.lstrip("/")
        if address.scheme != "http" or not host.startswith("127.0.0.1:") or bucket != "sea-fixture":
            raise ValueError("S3 process did not return an isolated loopback fixture")
        env = os.environ.copy()
        env.update(SEA_ARTIFACT_S3_HOST=host, SEA_ARTIFACT_S3_BUCKET=bucket,
                   GOMAXPROCS="2", GOFLAGS="-p=1")
        run(["go", "test", "-mod=readonly", "-race", "-count=1", "-v",
             "./service/common/artifacts"], evidence, "go-race.log", env)
        run(["go", "vet", "-mod=readonly", "./service/common/artifacts"],
            evidence, "vet.log", env)
        run(["go", "mod", "verify"], evidence, "mod-verify.log", env)
        report = {
            "status": "passed",
            "scope": "task_owned_SeaweedFS_S3_BTW_to_RTW_key_shape",
            "go_race_sha256": sha256(evidence / "go-race.log"),
            "vet_sha256": sha256(evidence / "vet.log"),
            "bucket": bucket,
            "production_object_store_checked": False,
        }
        (evidence / "report.json").write_text(json.dumps(report, sort_keys=True) + "\n")
        print(json.dumps(report, sort_keys=True))
    finally:
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)
            parsed = urllib.parse.urlsplit(prefix)
            print(f"Local evidence directory: {evidence}")
            with socket.socket() as probe:
                probe.settimeout(1)
                if probe.connect_ex(("127.0.0.1", parsed.port)) == 0:
                    raise RuntimeError("task-owned SeaweedFS S3 listener survived shutdown")
        else:
            print(f"Local evidence directory: {evidence}")


if __name__ == "__main__":
    main()
