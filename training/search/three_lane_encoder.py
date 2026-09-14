"""Freeze actual locked BGE-M3 Dense/Sparse/ColBERT values for H10.b qrels.

Parent mode runs in the search-dataset reader environment. Worker mode runs in
the separate locked BGE-M3 environment; neither environment installs missing
packages or downloads model weights. This is a local, default-off candidate.
"""

from __future__ import annotations

import argparse
from hashlib import sha256
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit
import uuid


SCHEMA = "sea.search.three-lane-representations.v1"
WORKER_SCHEMA = "sea.search.three-lane-worker.v1"
LANES = ("dense", "sparse", "token_matrix")
BGE_ROOT = Path(__file__).resolve().parents[1] / "serving" / "bge_m3"


class EncodingError(ValueError):
    pass


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False, allow_nan=False).encode("utf-8")


def digest(body: bytes) -> str:
    return sha256(body).hexdigest()


def file_digest(path: Path) -> str:
    value = sha256()
    with path.open("rb") as source:
        while part := source.read(4 << 20):
            value.update(part)
    return value.hexdigest()


def freeze(root: Path, relative: str, body: bytes) -> Path:
    target = root / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        with target.open("xb") as file:
            file.write(body)
            file.flush()
            os.fsync(file.fileno())
    except FileExistsError:
        if target.read_bytes() != body:
            raise EncodingError("immutable representation object conflicts") from None
    if target.read_bytes() != body:
        raise EncodingError("representation object readback differs")
    return target


def _chunk_key(row: dict) -> str:
    return json.dumps([row["document_id"], row["document_revision"], row["chunk_id"]],
                      ensure_ascii=False, separators=(",", ":"))


def inventory(snapshot) -> dict:
    """Collect only reader-validated unique texts; never use a directory scan."""
    if snapshot.manifest["data_kind"] != "synthetic" or snapshot.manifest["revision"] != 2:
        raise EncodingError("first three-lane candidate requires synthetic H10.b revision 2")
    queries: dict[str, dict] = {}
    chunks: dict[str, dict] = {}
    for split in ("train", "validation", "test"):
        for batch in snapshot.iter_batches(split, batch_size=8192):
            for row in batch.to_pylist():
                query = {"query_id": row["query_id"], "query_family_id": row["query_family_id"],
                         "near_duplicate_cluster_id": row["near_duplicate_cluster_id"], "split": split,
                         "text": row["query_text"], "text_sha256": row["query_text_sha256"]}
                query["work_id"] = "q-" + digest(row["query_id"].encode())
                prior = queries.setdefault(row["query_id"], query)
                if prior != query:
                    raise EncodingError("query text or group differs across frozen qrels")
                key = _chunk_key(row)
                chunk = {"document_id": row["document_id"], "document_revision": row["document_revision"],
                         "chunk_id": row["chunk_id"], "chunk_key": key, "text": row["chunk_text"],
                         "text_sha256": row["chunk_text_sha256"],
                         "content_available_at": row["content_available_at"].isoformat()}
                chunk["work_id"] = "c-" + digest(key.encode())
                old = chunks.setdefault(key, chunk)
                if old != chunk:
                    raise EncodingError("document revision/chunk text differs across frozen qrels")
    if not queries or not chunks:
        raise EncodingError("frozen qrel corpus has no query or chunk")
    return {"schema_version": "sea.search.three-lane-inventory.v1",
            "dataset_manifest_sha256": snapshot.manifest_sha256,
            "queries": [queries[key] for key in sorted(queries)],
            "chunks": [chunks[key] for key in sorted(chunks)]}


def _finite_vector(values: object, dimensions: int, *, l2: bool) -> None:
    if not isinstance(values, list) or len(values) != dimensions or \
            any(type(x) not in (int, float) or not math.isfinite(x) for x in values):
        raise EncodingError("representation vector shape or finite value differs")
    square = sum(float(x) * float(x) for x in values)
    if not math.isfinite(square) or square <= 0 or l2 and abs(square - 1) > 1e-4:
        raise EncodingError("representation norm differs from locked profile")


def validate_representations(value: object, profiles: dict) -> None:
    if not isinstance(value, dict) or set(value) != set(LANES):
        raise EncodingError("three distinct model heads required")
    dense, sparse, tokens = (value[kind] for kind in LANES)
    if not isinstance(dense, dict) or set(dense) != {"values"}:
        raise EncodingError("dense shape differs")
    _finite_vector(dense["values"], profiles["dense"]["representation_contract"]["dimensions"], l2=True)
    if not isinstance(sparse, dict) or set(sparse) != {"indices", "weights"}:
        raise EncodingError("learned sparse fields differ")
    indices, weights = sparse["indices"], sparse["weights"]
    maximum = profiles["sparse"]["representation_contract"]["max_nonzero"]
    vocabulary = profiles["sparse"]["representation_contract"]["dimensions"]
    if not isinstance(indices, list) or not isinstance(weights, list) or not 1 <= len(indices) <= maximum or \
            len(indices) != len(weights) or any(type(index) is not int or index < 0 or index >= vocabulary
                                                for index in indices) or \
            any(left >= right for left, right in zip(indices, indices[1:])) or \
            any(type(weight) not in (int, float) or not math.isfinite(weight) or weight <= 0
                for weight in weights) or not math.isfinite(sum(float(weight) ** 2 for weight in weights)):
        raise EncodingError("learned sparse IDs/weights differ from official head")
    if not isinstance(tokens, dict) or set(tokens) != {"shape", "values", "mask"}:
        raise EncodingError("token matrix fields differ")
    shape, matrix, mask = tokens["shape"], tokens["values"], tokens["mask"]
    rows = shape[0] if isinstance(shape, list) and len(shape) == 2 and type(shape[0]) is int else 0
    if not isinstance(shape, list) or len(shape) != 2 or type(shape[1]) is not int or \
            shape != [rows, profiles["token_matrix"]["representation_contract"]["dimensions"]] or \
            not 1 <= rows <= profiles["token_matrix"]["representation_contract"]["max_tokens"] or \
            not isinstance(matrix, list) or len(matrix) != rows or \
            not isinstance(mask, list) or len(mask) != rows or any(flag is not True for flag in mask):
        raise EncodingError("effective token mask or matrix shape differs")
    for vector in matrix:
        _finite_vector(vector, shape[1], l2=True)


def worker(inventory_path: Path, inventory_sha256: str, result_path: Path,
           model_directory: Path, lock_path: Path, profiles_path: Path) -> None:
    raw = inventory_path.read_bytes()
    if digest(raw) != inventory_sha256:
        raise EncodingError("worker inventory bytes changed")
    source = json.loads(raw)
    if source.get("schema_version") != "sea.search.three-lane-inventory.v1":
        raise EncodingError("worker inventory contract differs")
    sys.path.insert(0, str(BGE_ROOT / "src"))
    from sea_bge_m3.contracts import MODEL_ID, PROFILES, REVISION
    from sea_bge_m3.model import Provider

    profiles = json.loads(profiles_path.read_bytes())
    lock = json.loads(lock_path.read_bytes())
    if profiles != PROFILES or lock["revision"] != REVISION:
        raise EncodingError("BGE profile or pinned model revision differs")
    provider = Provider(model_directory, lock_path)  # checks all cached files; never downloads
    configuration_id = str(uuid.uuid5(uuid.NAMESPACE_URL,
                                       "sea-search-three-lane:" + source["dataset_manifest_sha256"] + REVISION))
    output = {"schema_version": WORKER_SCHEMA, "model_revision": REVISION,
              "model_lock_sha256": file_digest(lock_path), "profiles_sha256": file_digest(profiles_path)}
    for name, role in (("queries", "query"), ("chunks", "document")):
        entries = source[name]
        encoded = []
        for start in range(0, len(entries), 2):
            batch = entries[start:start + 2]
            result_by_id = {item["work_id"]: {} for item in batch}
            for kind in LANES:
                profile = PROFILES[kind]
                request = {"model": MODEL_ID, "configuration_id": configuration_id,
                           "output_contract": profile["output_contract"],
                           "representation_contract_id": profile["representation_contract"]["id"],
                           "representation_space": profile["representation_space"],
                           "role": role,
                           "input": [{"id": item["work_id"], "text": item["text"]} for item in batch]}
                response = provider.represent(request)
                if response["role"] != role or response["model"] != MODEL_ID or len(response["data"]) != len(batch):
                    raise EncodingError("official model response identity changed")
                for item in response["data"]:
                    if item["id"] not in result_by_id or kind in result_by_id[item["id"]]:
                        raise EncodingError("official model response repeated an item")
                    result_by_id[item["id"]][kind] = item[kind]
            encoded.extend({"work_id": item["work_id"], "representations": result_by_id[item["work_id"]]}
                           for item in batch)
        output[name] = encoded
    result_path.write_bytes(canonical(output))


def _merge_inventory(entries: list[dict], representations: object, profiles: dict) -> list[dict]:
    if not isinstance(representations, list) or len(representations) != len(entries):
        raise EncodingError("worker item count differs from validated qrels")
    by_id = {}
    for item in representations:
        if not isinstance(item, dict) or set(item) != {"work_id", "representations"} or \
                not isinstance(item["work_id"], str) or \
                item["work_id"] in by_id:
            raise EncodingError("worker item identity differs")
        validate_representations(item["representations"], profiles)
        by_id[item["work_id"]] = item["representations"]
    if set(by_id) != {entry["work_id"] for entry in entries}:
        raise EncodingError("worker lost or substituted source identity")
    return [{**{key: value for key, value in entry.items() if key != "work_id"},
             "representations": by_id[entry["work_id"]]} for entry in entries]


def _jsonl(rows: list[dict]) -> bytes:
    return b"".join(canonical(row) + b"\n" for row in rows)


def encode(manifest_uri: str, expected_manifest_sha256: str, model_directory: Path,
           bge_python: Path, output_dir: Path, *, s3_endpoint: str | None = None) -> dict:
    from pyarrow import fs
    from sea_training.search_dataset import open_search_dataset

    filesystem = None
    if s3_endpoint is not None:
        endpoint = urlsplit(s3_endpoint)
        if endpoint.scheme != "http" or endpoint.hostname not in ("127.0.0.1", "localhost") or \
                not endpoint.port or endpoint.username or endpoint.password or endpoint.path not in ("", "/") or \
                endpoint.query or endpoint.fragment:
            raise EncodingError("anonymous S3 fixture endpoint must be loopback HTTP")
        filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                     endpoint_override=f"{endpoint.hostname}:{endpoint.port}")
    with open_search_dataset(manifest_uri, expected_manifest_sha256=expected_manifest_sha256,
                             filesystem=filesystem) as snapshot:
        fixed = inventory(snapshot)
        manifest = snapshot.manifest
    lock_path, profiles_path = BGE_ROOT / "model.lock.json", BGE_ROOT / "profiles.json"
    lock_raw, profiles_raw = lock_path.read_bytes(), profiles_path.read_bytes()
    lock, profiles = json.loads(lock_raw), json.loads(profiles_raw)
    if lock.get("repository") != "BAAI/bge-m3" or set(profiles) != set(LANES) or \
            profiles["token_matrix"]["representation_contract"]["aggregation"] != "mean_maxsim":
        raise EncodingError("model lock or BGE three-lane profile differs")
    missing = [item["path"] for item in lock["files"] if not (model_directory / item["path"]).is_file()]
    if missing:
        raise EncodingError("cached locked BGE model files missing; no download is permitted")
    if not bge_python.is_file() or not os.access(bge_python, os.X_OK):
        raise EncodingError("locked BGE Python runtime unavailable")
    output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".sea-bge-encoder-", dir=output_dir) as scratch:
        temporary = Path(scratch)
        inventory_path, result_path = temporary / "inventory.json", temporary / "result.json"
        inventory_bytes = canonical(fixed)
        inventory_path.write_bytes(inventory_bytes)
        # Keep the venv interpreter path; resolving its symlink would bypass
        # the locked environment and lose torch/FlagEmbedding dependencies.
        command = [str(bge_python.absolute()), str(Path(__file__).resolve()), "--worker",
                   "--inventory", str(inventory_path), "--inventory-sha256", digest(inventory_bytes),
                   "--result", str(result_path), "--model-directory", str(model_directory.resolve()),
                   "--model-lock", str(lock_path), "--profiles", str(profiles_path)]
        environment = os.environ.copy()
        environment.update(HF_HUB_OFFLINE="1", TRANSFORMERS_OFFLINE="1", TOKENIZERS_PARALLELISM="false")
        with (output_dir / "worker.log").open("wb") as log:
            completed = subprocess.run(command, env=environment, stdout=log, stderr=subprocess.STDOUT,
                                       timeout=20 * 60, check=False)
        if completed.returncode != 0:
            raise EncodingError("locked BGE CPU worker failed; inspect worker.log")
        try:
            produced = json.loads(result_path.read_bytes())
        except (OSError, ValueError) as exc:
            raise EncodingError("locked BGE CPU worker returned no valid result") from exc
    if set(produced) != {"schema_version", "model_revision", "model_lock_sha256", "profiles_sha256", "queries", "chunks"} or \
            produced.get("schema_version") != WORKER_SCHEMA or produced.get("model_revision") != lock["revision"] or \
            produced.get("model_lock_sha256") != digest(lock_raw) or produced.get("profiles_sha256") != digest(profiles_raw):
        raise EncodingError("worker model/profile identity differs")
    queries = _merge_inventory(fixed["queries"], produced.get("queries"), profiles)
    chunks = _merge_inventory(fixed["chunks"], produced.get("chunks"), profiles)
    shards = []
    for kind, directory, rows in (("query", "queries", queries), ("chunk", "chunks", chunks)):
        body = _jsonl(rows)
        sha = digest(body)
        relative = f"shards/{directory}/{sha}.jsonl"
        freeze(output_dir, relative, body)
        shards.append({"kind": kind, "path": relative, "sha256": sha,
                       "size_bytes": len(body), "rows": len(rows)})
    frozen = {"schema_version": SCHEMA, "status": "candidate_default_off",
              "dataset_id": manifest["dataset_id"], "dataset_revision": manifest["revision"],
              "dataset_manifest_sha256": expected_manifest_sha256, "data_kind": manifest["data_kind"],
              "source_row_count": manifest["row_count"],
              "warehouse_generation": manifest["source"]["generation"],
              "model_repository": lock["repository"], "model_revision": lock["revision"],
              "model_lock_sha256": digest(lock_raw), "profiles_sha256": digest(profiles_raw),
              "tokenizer_id": profiles["dense"]["representation_contract"]["tokenizer_id"],
              "lanes": profiles, "query_count": len(queries), "chunk_count": len(chunks), "shards": shards}
    body = canonical(frozen)
    manifest_sha = digest(body)
    manifest_path = freeze(output_dir, f"manifest/{manifest_sha}.json", body)
    result = {"status": "candidate_default_off", "manifest_path": str(manifest_path),
              "manifest_sha256": manifest_sha, "dataset_manifest_sha256": expected_manifest_sha256,
              "model_revision": lock["revision"], "query_count": len(queries), "chunk_count": len(chunks),
              "dense_dimensions": 1024, "sparse_vocabulary": 250002,
              "token_matrix_aggregation": "mean_maxsim", "trained_weights": False,
              "model_quality": None, "activation": "none"}
    (output_dir / "report.json").write_bytes(canonical(result) + b"\n")
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--worker", action="store_true")
    parser.add_argument("--inventory")
    parser.add_argument("--inventory-sha256")
    parser.add_argument("--result")
    parser.add_argument("--model-lock")
    parser.add_argument("--profiles")
    parser.add_argument("--manifest-uri")
    parser.add_argument("--expected-manifest-sha256")
    parser.add_argument("--model-directory")
    parser.add_argument("--bge-python")
    parser.add_argument("--s3-endpoint")
    parser.add_argument("--output")
    args = parser.parse_args()
    if args.worker:
        if not all((args.inventory, args.inventory_sha256, args.result, args.model_directory,
                    args.model_lock, args.profiles)):
            parser.error("worker requires fixed inventory/model paths")
        worker(Path(args.inventory), args.inventory_sha256, Path(args.result),
               Path(args.model_directory), Path(args.model_lock), Path(args.profiles))
    else:
        if not all((args.manifest_uri, args.expected_manifest_sha256, args.model_directory,
                    args.bge_python, args.output)):
            parser.error("encoder requires pinned qrel/model/runtime/output inputs")
        print(json.dumps(encode(args.manifest_uri, args.expected_manifest_sha256,
                                Path(args.model_directory), Path(args.bge_python), Path(args.output),
                                s3_endpoint=args.s3_endpoint), sort_keys=True, ensure_ascii=False))


if __name__ == "__main__":
    main()
