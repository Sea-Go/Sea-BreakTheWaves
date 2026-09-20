"""Run the same Go SDK twice against a process-restarted official Lite engine."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time


def serve(root: Path):
    from milvus_lite.adapter.grpc.server import start_server_in_thread
    server, db, port = start_server_in_thread(str(root / "data"), host="127.0.0.1", port=0)
    (root / "port").write_text(str(port))
    stop = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stop.set())
    signal.signal(signal.SIGINT, lambda *_: stop.set())
    try:
        stop.wait()
    finally:
        server.stop(grace=2).wait()
        db.close()


def run(root: Path):
    root.mkdir(parents=True, exist_ok=True)
    repo = Path(__file__).resolve().parents[4]
    for reopen in (False, True):
        (root / "port").unlink(missing_ok=True)
        with (root / f"server-{int(reopen)}.log").open("w") as log:
            process = subprocess.Popen([sys.executable, __file__, "--serve", str(root)], stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 60
                while not (root / "port").exists():
                    if process.poll() is not None:
                        raise RuntimeError(f"Lite startup exited {process.returncode}; see {log.name}")
                    if time.monotonic() > deadline:
                        raise TimeoutError("Lite port was not published")
                    time.sleep(.05)
                env = dict(os.environ, DENSE_MILVUS_ADDRESS="127.0.0.1:" + (root / "port").read_text(), DENSE_MILVUS_LITE="1", DENSE_MILVUS_EVIDENCE=str(root / "artifacts"), DENSE_MILVUS_REOPEN="1" if reopen else "0")
                with (root / f"go-{int(reopen)}.log").open("w") as output:
                    subprocess.run(["go", "test", "./internal/retrieval/dense", "-run", "^TestRealMilvus$", "-count=1", "-v"], cwd=repo, env=env, stdout=output, stderr=subprocess.STDOUT, timeout=180, check=True)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
    import faiss
    files = []
    for path in sorted((root / "data").rglob("*.hnsw.idx")):
        index = faiss.read_index(str(path))
        assert type(index).__name__ == "IndexHNSWFlat"
        assert index.d == 2 and index.ntotal == 4
        assert index.hnsw.nb_neighbors(1) == 16
        assert index.hnsw.efConstruction == 128
        files.append({"path": str(path.relative_to(root)), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "type": type(index).__name__, "rows": index.ntotal, "dimensions": index.d, "M": index.hnsw.nb_neighbors(1), "efConstruction": index.hnsw.efConstruction})
    assert files, "No persisted HNSW index found"
    report = {"engine": "milvus-lite", "version": "3.2.1", "source_commit": "43d1257774e629bc9f66873977ab7c320d5bf5a7", "go_sdk": "2.6.2", "fresh_build": "passed", "process_restart_without_rebuild": "passed", "cosine_raw_score_kind": "cosine_similarity", "cosine_raw_score_tolerance": 1e-5, "original_float64_score_tolerance": 1e-12, "pre_ann_revision_filter": "passed", "index_files": files, "scope": "Real local FAISS HNSW; synthetic encoding fixture; no production-scale claim."}
    (root / "lite-proof.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--serve", action="store_true")
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    if args.serve:
        serve(args.directory)
    else:
        run(args.directory)
