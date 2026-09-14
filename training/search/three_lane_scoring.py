"""Pinned BGE-M3 three-lane scoring against the independently verified H10.b qrel reader.

This is exact small-corpus candidate scoring, not ANN retrieval, model training,
model activation, or a business-quality evaluation. H10.b does not carry an
all-candidates-judged receipt, so formal ranking metrics stay not_evaluable.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
from hashlib import sha256
import json
import math
from pathlib import Path, PurePosixPath
import re
from typing import Any

from pyarrow import fs

from sea_training.search_dataset import SearchDatasetSnapshot, open_search_dataset


SCHEMA_VERSION = "sea.search.three-lane-representations.v1"
LANES = ("dense", "sparse", "token_matrix")
SPLITS = ("train", "validation", "test")
HEX256 = re.compile(r"^[a-f0-9]{64}$")
MAX_PAIRS = 10_000
MAX_TOKEN_DOT_PRODUCTS = 200_000_000


class ThreeLaneScoringError(ValueError):
    """A frozen representation or its qrel binding cannot be safely scored."""


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ThreeLaneScoringError(message)


def _object_pairs(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        _require(key not in value, "duplicate JSON key")
        value[key] = item
    return value


def _json(raw: bytes) -> dict[str, Any]:
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_object_pairs,
                           parse_constant=lambda _: (_ for _ in ()).throw(ValueError("non-finite JSON")))
    except (UnicodeError, ValueError) as error:
        raise ThreeLaneScoringError("invalid representation JSON") from error
    _require(isinstance(value, dict), "representation JSON object required")
    return value


def _digest(raw: bytes) -> str:
    return sha256(raw).hexdigest()


def _sha(value: Any, label: str) -> str:
    _require(isinstance(value, str) and HEX256.fullmatch(value) is not None, f"invalid {label} SHA-256")
    return value


def _text(value: Any, label: str) -> str:
    _require(isinstance(value, str) and bool(value.strip()), f"empty {label}")
    return value


def _integer(value: Any, label: str, minimum: int = 0) -> int:
    _require(type(value) is int and value >= minimum, f"invalid {label}")
    return value


def _float(value: Any, label: str) -> float:
    _require(type(value) in (int, float), f"non-finite {label}")
    try:
        numeric = float(value)
    except OverflowError as error:
        raise ThreeLaneScoringError(f"non-finite {label}") from error
    _require(math.isfinite(numeric), f"non-finite {label}")
    return numeric


def _sum(values: Any, label: str) -> float:
    try:
        result = math.fsum(values)
    except (OverflowError, ValueError) as error:
        raise ThreeLaneScoringError(f"non-finite {label}") from error
    _require(math.isfinite(result), f"non-finite {label}")
    return result


def _utc(value: Any) -> datetime:
    _text(value, "availability time")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ThreeLaneScoringError("invalid content availability time") from error
    _require(parsed.tzinfo is not None and parsed.utcoffset() == timezone.utc.utcoffset(parsed),
             "content availability must be UTC")
    return parsed


def _path(base: Path, relative: Any, digest: str, kind: str) -> Path:
    _text(relative, "shard path")
    path = PurePosixPath(relative)
    _require(not path.is_absolute() and ".." not in path.parts and "\\" not in relative and
             path.parts == ("shards", "queries" if kind == "query" else "chunks", f"{digest}.jsonl"),
             "unsafe or non-content-addressed representation shard path")
    return base / path


def _read_lines(path: Path, expected_size: int, expected_hash: str, expected_rows: int) -> list[dict]:
    try:
        raw = path.read_bytes()
    except OSError as error:
        raise ThreeLaneScoringError("representation shard missing") from error
    _require(len(raw) == expected_size and _digest(raw) == expected_hash, "representation shard byte/hash mismatch")
    _require(raw.endswith(b"\n") and b"\r" not in raw and b"\n\n" not in raw,
             "representation shard requires nonempty LF-terminated JSONL")
    lines = raw.splitlines()
    _require(len(lines) == expected_rows, "representation shard row count mismatch")
    return [_json(line) for line in lines]


def _vector(value: Any, dimensions: int, label: str, *, l2: bool = True) -> tuple[float, ...]:
    _require(isinstance(value, list) and len(value) == dimensions, f"{label} dimension mismatch")
    vector = tuple(_float(item, label) for item in value)
    if l2:
        norm = _sum((item * item for item in vector), f"{label} norm")
        _require(abs(norm - 1.0) <= 2e-3, f"{label} L2 norm mismatch")
    return vector


def _representation(value: Any, profiles: dict) -> dict:
    _require(isinstance(value, dict) and set(value) == set(LANES), "missing or extra representation lane")
    dense = value["dense"]
    sparse = value["sparse"]
    matrix = value["token_matrix"]
    _require(isinstance(dense, dict) and set(dense) == {"values"}, "invalid dense representation")
    _require(isinstance(sparse, dict) and set(sparse) == {"indices", "weights"}, "invalid sparse representation")
    _require(isinstance(matrix, dict) and set(matrix) == {"shape", "values", "mask"}, "invalid token matrix")
    dense_dim = profiles["dense"]["representation_contract"]["dimensions"]
    token_dim = profiles["token_matrix"]["representation_contract"]["dimensions"]
    vocab = profiles["sparse"]["representation_contract"]["dimensions"]
    max_nonzero = profiles["sparse"]["representation_contract"]["max_nonzero"]
    max_tokens = profiles["token_matrix"]["representation_contract"]["max_tokens"]
    dense_values = _vector(dense["values"], dense_dim, "dense")
    indices, weights = sparse["indices"], sparse["weights"]
    _require(isinstance(indices, list) and isinstance(weights, list) and len(indices) == len(weights) and
             len(indices) <= max_nonzero, "sparse indices/weights length mismatch")
    last = -1
    terms = []
    for index, weight in zip(indices, weights):
        _integer(index, "sparse token id")
        _require(last < index < vocab, "sparse token IDs must be increasing within vocabulary")
        numeric = _float(weight, "sparse weight")
        _require(numeric > 0, "sparse learned weight must be positive")
        terms.append((index, numeric))
        last = index
    shape, values, mask = matrix["shape"], matrix["values"], matrix["mask"]
    _require(isinstance(shape, list) and len(shape) == 2 and
             type(shape[0]) is int and 1 <= shape[0] <= max_tokens and shape[1] == token_dim and
             isinstance(values, list) and isinstance(mask, list) and len(values) == len(mask) == shape[0] and
             all(type(active) is bool for active in mask) and any(mask), "invalid token matrix shape or mask")
    tokens = tuple(_vector(row, token_dim, "token", l2=active) for row, active in zip(values, mask))
    return {"dense": dense_values, "sparse": tuple(terms), "token_matrix": tokens,
            "mask": tuple(mask), "dimensions": token_dim}


def _check_model_and_profiles(manifest: dict, profile_dir: Path) -> dict:
    try:
        lock_raw = (profile_dir / "model.lock.json").read_bytes()
        profiles_raw = (profile_dir / "profiles.json").read_bytes()
    except OSError as error:
        raise ThreeLaneScoringError("BGE-M3 lock or profiles missing") from error
    _require(_digest(lock_raw) == _sha(manifest.get("model_lock_sha256"), "model lock") and
             _digest(profiles_raw) == _sha(manifest.get("profiles_sha256"), "profiles"),
             "model lock or profile bytes differ")
    lock, profiles = _json(lock_raw), _json(profiles_raw)
    _require(manifest.get("model_repository") == lock.get("repository") == "BAAI/bge-m3" and
             manifest.get("model_revision") == lock.get("revision") and
             manifest.get("lanes") == profiles and set(profiles) == set(LANES),
             "model revision or three representation spaces differ")
    tokenizer = profiles["dense"]["representation_contract"]["tokenizer_id"]
    _require(manifest.get("tokenizer_id") == tokenizer and
             all(profiles[lane]["representation_contract"]["tokenizer_id"] == tokenizer for lane in LANES),
             "tokenizer ID differs across lanes")
    contracts = {lane: profiles[lane]["representation_contract"] for lane in LANES}
    _require(contracts["dense"]["metric"] == "dot" and contracts["dense"]["normalization"] == "l2" and
             contracts["sparse"]["metric"] == "dot" and contracts["sparse"]["normalization"] == "none" and
             contracts["sparse"]["aggregation"] == "max" and
             contracts["token_matrix"]["metric"] == "maxsim" and
             contracts["token_matrix"]["normalization"] == "l2" and
             contracts["token_matrix"]["aggregation"] == "mean_maxsim", "unsupported lane scoring contract")
    return profiles


def load_representations(manifest_path: Path, expected_sha256: str, *,
                         profile_dir: Path | None = None) -> tuple[dict, list[dict], list[dict]]:
    """Verify original manifest/shards and exact pinned model spaces before decoding rows."""
    _sha(expected_sha256, "expected representation manifest")
    manifest_path = Path(manifest_path).resolve()
    _require(manifest_path.parent.name == "manifest" and manifest_path.name == f"{expected_sha256}.json",
             "representation manifest path is not content addressed")
    try:
        raw = manifest_path.read_bytes()
    except OSError as error:
        raise ThreeLaneScoringError("representation manifest missing") from error
    _require(_digest(raw) == expected_sha256, "representation manifest SHA-256 mismatch")
    manifest = _json(raw)
    _require(manifest.get("schema_version") == SCHEMA_VERSION and manifest.get("status") == "candidate_default_off",
             "unsupported or active representation manifest")
    _sha(manifest.get("dataset_manifest_sha256"), "dataset manifest")
    _text(manifest.get("dataset_id"), "dataset ID")
    _integer(manifest.get("dataset_revision"), "dataset revision", 1)
    _integer(manifest.get("source_row_count"), "source row count", 1)
    _text(manifest.get("warehouse_generation"), "warehouse generation")
    query_count = _integer(manifest.get("query_count"), "query count", 1)
    chunk_count = _integer(manifest.get("chunk_count"), "chunk count", 1)
    _require(manifest.get("data_kind") in ("synthetic", "observed"), "invalid representation data kind")
    profile_dir = profile_dir or Path(__file__).resolve().parents[1] / "serving" / "bge_m3"
    profiles = _check_model_and_profiles(manifest, profile_dir)
    shards = manifest.get("shards")
    _require(isinstance(shards, list) and len(shards) == 2 and
             [item.get("kind") for item in shards if isinstance(item, dict)] == ["query", "chunk"],
             "representation shards must be one query then one chunk")
    decoded = []
    for shard in shards:
        _require(set(shard) == {"kind", "path", "sha256", "size_bytes", "rows"}, "invalid representation shard descriptor")
        digest = _sha(shard["sha256"], "representation shard")
        size = _integer(shard["size_bytes"], "shard size", 1)
        rows = _integer(shard["rows"], "shard rows", 1)
        path = _path(Path(manifest_path).resolve().parent.parent, shard["path"], digest, shard["kind"])
        decoded.append(_read_lines(path, size, digest, rows))
    queries = [_row(row, "query", profiles) for row in decoded[0]]
    chunks = [_row(row, "chunk", profiles) for row in decoded[1]]
    _require(len(queries) == query_count and len(chunks) == chunk_count,
             "representation manifest query/chunk count mismatch")
    query_keys, chunk_keys, logical_chunks = set(), set(), set()
    for item in queries:
        _require(item["query_id"] not in query_keys, "duplicate query representation")
        query_keys.add(item["query_id"])
    for item in chunks:
        key = (item["document_id"], item["document_revision"], item["chunk_id"])
        logical = (item["document_id"], item["chunk_id"])
        _require(key not in chunk_keys and logical not in logical_chunks,
                 "duplicate or multi-version chunk representation")
        chunk_keys.add(key)
        logical_chunks.add(logical)
    return manifest, queries, chunks


def _row(row: dict, kind: str, profiles: dict) -> dict:
    common = {"text", "text_sha256", "representations"}
    expected = common | ({"query_id", "query_family_id", "near_duplicate_cluster_id", "split"} if kind == "query"
                         else {"document_id", "document_revision", "chunk_id", "chunk_key", "content_available_at"})
    _require(set(row) == expected, f"invalid {kind} representation row fields")
    text = _text(row["text"], f"{kind} text")
    _require(_digest(text.encode("utf-8")) == _sha(row["text_sha256"], f"{kind} text"),
             f"{kind} text hash mismatch")
    for field in expected - common:
        if field != "content_available_at":
            _text(row[field], f"{kind} {field}")
    if kind == "query":
        _require(row["split"] in SPLITS, "invalid query split")
    else:
        _utc(row["content_available_at"])
        canonical_key = json.dumps([row["document_id"], row["document_revision"], row["chunk_id"]],
                                   ensure_ascii=False, separators=(",", ":"))
        _require(row["chunk_key"] == canonical_key, "chunk key differs from qrel identity")
    return {**row, "_rep": _representation(row["representations"], profiles)}


def dense_dot(query: tuple[float, ...], chunk: tuple[float, ...]) -> float:
    _require(len(query) == len(chunk) and len(query) > 0, "dense score dimension mismatch")
    return _sum((left * right for left, right in zip(query, chunk)), "dense score")


def sparse_inner(query: tuple[tuple[int, float], ...], chunk: tuple[tuple[int, float], ...]) -> float:
    left = right = 0
    products = []
    while left < len(query) and right < len(chunk):
        if query[left][0] == chunk[right][0]:
            products.append(query[left][1] * chunk[right][1])
            left += 1
            right += 1
        elif query[left][0] < chunk[right][0]:
            left += 1
        else:
            right += 1
    return _sum(products, "sparse score")


def mean_maxsim(query: dict, chunk: dict) -> float:
    q_tokens = [row for row, active in zip(query["token_matrix"], query["mask"]) if active]
    d_tokens = [row for row, active in zip(chunk["token_matrix"], chunk["mask"]) if active]
    _require(bool(q_tokens) and bool(d_tokens) and query["dimensions"] == chunk["dimensions"],
             "empty or mismatched token matrix")
    maxima = [max(dense_dot(q_token, d_token) for d_token in d_tokens) for q_token in q_tokens]
    return _sum(maxima, "MaxSim score") / len(q_tokens)


def _chunk_key(row: dict) -> tuple[str, str, str]:
    return (row["document_id"], row["document_revision"], row["chunk_id"])


def _qrel_rows(snapshot: SearchDatasetSnapshot) -> tuple[dict, dict]:
    queries: dict[str, dict] = {}
    judgments: dict[tuple[str, tuple[str, str, str]], dict] = {}
    for split in SPLITS:
        for batch in snapshot.iter_batches(split):
            for row in batch.to_pylist():
                query = queries.setdefault(row["query_id"], row)
                _require(query["split"] == row["split"] and query["query_text_sha256"] == row["query_text_sha256"] and
                         query["query_family_id"] == row["query_family_id"] and
                         query["near_duplicate_cluster_id"] == row["near_duplicate_cluster_id"] and
                         query["query_time"] == row["query_time"], "qrel query changes after reader validation")
                key = (row["query_id"], _chunk_key(row))
                _require(key not in judgments, "duplicate visible qrel after reader validation")
                judgments[key] = row
    return queries, judgments


def score_three_lanes(representation_manifest_path: Path, *, expected_representation_sha256: str,
                      qrel_snapshot: SearchDatasetSnapshot, split: str, top_k: int = 10,
                      profile_dir: Path | None = None) -> dict:
    """Score every time-eligible frozen chunk for each query in one verified qrel split.

    The caller owns the H10.b `open_search_dataset` context; this function
    refuses a representation set that differs from that validated snapshot.
    """
    _require(split in SPLITS and type(top_k) is int and top_k > 0, "invalid split or top_k")
    manifest, encoded_queries, encoded_chunks = load_representations(
        representation_manifest_path, expected_representation_sha256, profile_dir=profile_dir)
    _require(manifest["dataset_manifest_sha256"] == qrel_snapshot.manifest_sha256 and
             manifest["dataset_id"] == qrel_snapshot.manifest["dataset_id"] and
             manifest["dataset_revision"] == qrel_snapshot.manifest["revision"] and
             manifest["source_row_count"] == qrel_snapshot.manifest["row_count"] and
             manifest["warehouse_generation"] == qrel_snapshot.manifest["source"]["generation"] and
             manifest["data_kind"] == qrel_snapshot.manifest["data_kind"],
             "representation/qrel snapshot differs")
    qrels, judgments = _qrel_rows(qrel_snapshot)
    encoded_by_id = {item["query_id"]: item for item in encoded_queries}
    _require(set(encoded_by_id) == set(qrels), "represented queries differ from verified qrel queries")
    chunks_by_key = {_chunk_key(item): item for item in encoded_chunks}
    for query_id, qrel in qrels.items():
        encoded = encoded_by_id[query_id]
        _require(encoded["split"] == qrel["split"] and encoded["query_family_id"] == qrel["query_family_id"] and
                 encoded["near_duplicate_cluster_id"] == qrel["near_duplicate_cluster_id"] and
                 encoded["text"] == qrel["query_text"] and encoded["text_sha256"] == qrel["query_text_sha256"],
                 "represented query differs from qrel authority")
    for (_, key), qrel in judgments.items():
        chunk = chunks_by_key.get(key)
        _require(chunk is not None and chunk["text"] == qrel["chunk_text"] and
                 chunk["text_sha256"] == qrel["chunk_text_sha256"] and
                 _utc(chunk["content_available_at"]) == qrel["content_available_at"],
                 "represented chunk differs from qrel authority")
    selected = [item for item in encoded_queries if item["split"] == split]
    pair_count = sum(sum(_utc(chunk["content_available_at"]) <= qrels[query["query_id"]]["query_time"]
                         for chunk in encoded_chunks) for query in selected)
    _require(pair_count <= MAX_PAIRS, "exact scoring pair budget exceeded")
    token_products = sum(sum(sum(query["_rep"]["mask"]) * sum(chunk["_rep"]["mask"]) *
                             query["_rep"]["dimensions"]
                             for chunk in encoded_chunks if _utc(chunk["content_available_at"]) <=
                             qrels[query["query_id"]]["query_time"]) for query in selected)
    _require(token_products <= MAX_TOKEN_DOT_PRODUCTS, "exact MaxSim operation budget exceeded")
    result = {"schema_version": "sea.search.three-lane-scoring.v1", "status": "candidate_default_off",
              "representation_manifest_sha256": expected_representation_sha256,
              "qrel_manifest_sha256": qrel_snapshot.manifest_sha256,
              "data_kind": manifest["data_kind"], "qrel_reader_status": qrel_snapshot.report["status"],
              "candidate_pool_scope": "all_frozen_chunks_available_at_query_time",
              "evaluation": {"status": "not_evaluable", "reason": "H10b_has_no_complete_judgment_scope_receipt",
                             "recall_at_k": None, "mrr_at_k": None, "ndcg_at_k": None},
              "split": split, "top_k": top_k, "queries": []}
    for query in sorted(selected, key=lambda item: item["query_id"]):
        qid = query["query_id"]
        query_time = qrels[qid]["query_time"]
        eligible = [chunk for chunk in encoded_chunks if _utc(chunk["content_available_at"]) <= query_time]
        _require(bool(eligible), "query has no time-eligible chunks")
        lane_rows = {lane: [] for lane in LANES}
        for chunk in eligible:
            key = _chunk_key(chunk)
            judged = judgments.get((qid, key))
            shared = {"document_id": key[0], "document_revision": key[1], "chunk_id": key[2],
                      "chunk_key": chunk["chunk_key"], "judged_mask": judged is not None,
                      "grade": judged["relevance_grade"] if judged is not None else None}
            qrep, drep = query["_rep"], chunk["_rep"]
            lane_rows["dense"].append({**shared, "score": dense_dot(qrep["dense"], drep["dense"])})
            lane_rows["sparse"].append({**shared, "score": sparse_inner(qrep["sparse"], drep["sparse"])})
            lane_rows["token_matrix"].append({**shared, "score": mean_maxsim(qrep, drep)})
        lanes = {}
        for lane in LANES:
            ranked = sorted(lane_rows[lane], key=lambda item: (-item["score"], item["document_id"],
                                                                 item["document_revision"], item["chunk_id"]))
            top = ranked[:top_k]
            unjudged = [item["chunk_key"] for item in top if not item["judged_mask"]]
            lanes[lane] = {"raw_scores": ranked, "top_k": top,
                           "judged_coverage": {"top_k_judged": len(top) - len(unjudged),
                                               "top_k_total": len(top),
                                               "candidate_judged": sum(item["judged_mask"] for item in ranked),
                                               "candidate_total": len(ranked),
                                               "unjudged_top_k": unjudged},
                           "evaluation": {"status": "not_evaluable",
                                          "reason": "top_k_unjudged" if unjudged else
                                                    "H10b_has_no_complete_judgment_scope_receipt",
                                          "recall_at_k": None, "mrr_at_k": None, "ndcg_at_k": None}}
        result["queries"].append({"query_id": qid, "query_family_id": query["query_family_id"],
                                  "near_duplicate_cluster_id": query["near_duplicate_cluster_id"],
                                  "candidate_set": [item["chunk_key"] for item in sorted(eligible, key=_chunk_key)],
                                  "lanes": lanes})
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Score a pinned BGE-M3 three-lane candidate on verified H10.b qrels")
    parser.add_argument("--representation-manifest", type=Path, required=True)
    parser.add_argument("--representation-sha256", required=True)
    parser.add_argument("--qrel-manifest-uri", required=True)
    parser.add_argument("--qrel-sha256", required=True)
    parser.add_argument("--split", choices=SPLITS, required=True)
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--output", type=Path, required=True, help="new single JSON file; existing files are never overwritten")
    parser.add_argument("--s3-endpoint", help="isolated anonymous S3 host:port for the H10.b reader")
    args = parser.parse_args(argv)
    filesystem = None
    if args.s3_endpoint:
        _require(not (args.s3_endpoint.startswith("http://") or args.s3_endpoint.startswith("https://")),
                 "S3 endpoint must be host:port")
        filesystem = fs.S3FileSystem(anonymous=True, region="us-east-1", scheme="http",
                                     endpoint_override=args.s3_endpoint)
    _require(not args.output.exists() and args.output.parent.is_dir(), "output JSON path must be new in existing directory")
    with open_search_dataset(args.qrel_manifest_uri, expected_manifest_sha256=args.qrel_sha256,
                             filesystem=filesystem) as snapshot:
        report = score_three_lanes(args.representation_manifest,
                                   expected_representation_sha256=args.representation_sha256,
                                   qrel_snapshot=snapshot, split=args.split, top_k=args.top_k)
    payload = json.dumps(report, sort_keys=True, ensure_ascii=False,
                         separators=(",", ":"), allow_nan=False).encode("utf-8")
    with args.output.open("xb") as file:
        file.write(payload)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
