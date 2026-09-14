"""Run the same Go SDK twice against an isolated process-restarted Lite HNSW."""
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
                        raise RuntimeError(f"Lite exited {process.returncode}; see {log.name}")
                    if time.monotonic() > deadline:
                        raise TimeoutError("Lite port was not published")
                    time.sleep(.05)
                env = dict(os.environ, MULTIVECTOR_MILVUS_ADDRESS="127.0.0.1:" + (root / "port").read_text(), MULTIVECTOR_MILVUS_LITE="1", MULTIVECTOR_EVIDENCE_DIR=str(root / "evidence"), MULTIVECTOR_REOPEN="1" if reopen else "0")
                with (root / f"go-{int(reopen)}.log").open("w") as output:
                    selection = "^TestRealMilvus$" if reopen else "^TestRealMilvus"
                    subprocess.run(["go", "test", "./internal/retrieval/multivector", "-run", selection, "-count=1", "-v"], cwd=repo, env=env, stdout=output, stderr=subprocess.STDOUT, timeout=180, check=True)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
    import faiss

    files = []
    clean_rows = []
    mutation_rows = []
    for path in sorted((root / "data").rglob("*.hnsw.idx")):
        hnsw = faiss.read_index(str(path))
        assert type(hnsw).__name__ == "IndexHNSWFlat"
        assert hnsw.d == 2 and hnsw.ntotal >= 1
        assert hnsw.hnsw.efConstruction == 128
        if "sea_multi_acceptance_" in str(path):
            clean_rows.append(hnsw.ntotal)
            assert hnsw.hnsw.nb_neighbors(1) == 16
        elif "sea_multi_mutation_" in str(path):
            mutation_rows.append(hnsw.ntotal)
        else:
            raise AssertionError("Unexpected collection in task-only native engine")
        files.append({"sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "type": type(hnsw).__name__, "rows": hnsw.ntotal, "dimensions": hnsw.d, "layer0_neighbors": hnsw.hnsw.nb_neighbors(0), "efConstruction": hnsw.hnsw.efConstruction})
    assert clean_rows == [7] and sum(mutation_rows) >= 7, "Persisted token-row indexes differ from fixtures"
    report = {"engine": "milvus-lite", "version": "3.2.1", "source_commit": "43d1257774e629bc9f66873977ab7c320d5bf5a7", "go_sdk": "2.6.2", "fresh_build": "passed", "process_restart_without_rebuild": "passed", "changed_vector_rejected_on_load_and_search": "passed", "query_path": "independent token-row HNSW/IP then complete MaxSim", "index_files": files, "scope": "Isolated seven-token synthetic fixture; no production scale or Collector acceptance."}
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
