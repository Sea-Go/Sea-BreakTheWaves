from __future__ import annotations

from copy import deepcopy
from datetime import timezone
from hashlib import sha256
import json
from pathlib import Path

import pytest

from sea_training.search_dataset import open_search_dataset
from test_authoritative import fixture as qrel_fixture
from three_lane_scoring import (ThreeLaneScoringError, dense_dot, load_representations, main,
                                mean_maxsim, score_three_lanes, sparse_inner)


ROOT = Path(__file__).resolve().parents[2]
PROFILES = ROOT / "serving" / "bge_m3" / "profiles.json"
LOCK = ROOT / "serving" / "bge_m3" / "model.lock.json"


def digest(raw: bytes) -> str:
    return sha256(raw).hexdigest()


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False,
                      separators=(",", ":"), allow_nan=False).encode()


def chunk_key(row: dict) -> str:
    return json.dumps([row["document_id"], row["document_revision"], row["chunk_id"]],
                      ensure_ascii=False, separators=(",", ":"))


def unit(first: float, second: float) -> list[float]:
    return [first, second] + [0.0] * 1022


def encoded(dense: list[float], sparse: list[tuple[int, float]], tokens: list[list[float]],
            mask: list[bool] | None = None) -> dict:
    return {"dense": {"values": dense},
            "sparse": {"indices": [index for index, _ in sparse],
                       "weights": [weight for _, weight in sparse]},
            "token_matrix": {"shape": [len(tokens), 1024], "values": tokens,
                             "mask": mask if mask is not None else [True] * len(tokens)}}


def rep_for(key: str, query: bool) -> dict:
    e1, e2, negative = unit(1, 0), unit(0, 1), unit(-1, 0)
    if query:
        return encoded(e1, [(1, 2.0), (2, 1.0)], [e1, e2])
    if key == "apple":
        return encoded(e1, [(1, 3.0)], [e1, e2])
    if key == "banana":
        return encoded(e2, [(2, 4.0)], [e2])
    if key == "pear":
        return encoded(negative, [], [negative])
    return encoded(e2, [], [e2])


def representation_fixture(root: Path, snapshot, mutate=None) -> tuple[Path, str]:
    queries, chunks = {}, {}
    for split in ("train", "validation", "test"):
        for batch in snapshot.iter_batches(split):
            for row in batch.to_pylist():
                queries[row["query_id"]] = {
                    "query_id": row["query_id"], "query_family_id": row["query_family_id"],
                    "near_duplicate_cluster_id": row["near_duplicate_cluster_id"],
                    "split": row["split"], "text": row["query_text"],
                    "text_sha256": row["query_text_sha256"],
                    "representations": rep_for(row["query_id"], True),
                }
                key = chunk_key(row)
                chunks[key] = {
                    "document_id": row["document_id"], "document_revision": row["document_revision"],
                    "chunk_id": row["chunk_id"], "chunk_key": key,
                    "text": row["chunk_text"], "text_sha256": row["chunk_text_sha256"],
                    "content_available_at": row["content_available_at"].astimezone(timezone.utc).isoformat(),
                    "representations": rep_for(row["document_id"], False),
                }
    rows = {"query": [queries[key] for key in sorted(queries)],
            "chunk": [chunks[key] for key in sorted(chunks)]}
    manifest = {
        "schema_version": "sea.search.three-lane-representations.v1", "status": "candidate_default_off",
        "dataset_manifest_sha256": snapshot.manifest_sha256,
        "dataset_id": snapshot.manifest["dataset_id"], "dataset_revision": snapshot.manifest["revision"],
        "source_row_count": snapshot.manifest["row_count"],
        "warehouse_generation": snapshot.manifest["source"]["generation"],
        "data_kind": snapshot.manifest["data_kind"], "model_repository": "BAAI/bge-m3",
        "model_revision": json.loads(LOCK.read_bytes())["revision"],
        "model_lock_sha256": digest(LOCK.read_bytes()),
        "profiles_sha256": digest(PROFILES.read_bytes()),
        "tokenizer_id": json.loads(PROFILES.read_bytes())["dense"]["representation_contract"]["tokenizer_id"],
        "lanes": json.loads(PROFILES.read_bytes()), "query_count": len(rows["query"]),
        "chunk_count": len(rows["chunk"]), "shards": []}
    if mutate:
        mutate(rows, manifest)
    for kind in ("query", "chunk"):
        raw = b"".join(canonical(row) + b"\n" for row in rows[kind])
        sha = digest(raw)
        relative = f"shards/{'queries' if kind == 'query' else 'chunks'}/{sha}.jsonl"
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        manifest["shards"].append({"kind": kind, "path": relative, "sha256": sha,
                                   "size_bytes": len(raw), "rows": len(rows[kind])})
    raw = canonical(manifest)
    sha = digest(raw)
    path = root / "manifest" / f"{sha}.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    return path, sha


def test_hand_calculated_three_lanes_and_unjudged_gate(tmp_path: Path) -> None:
    qrel_path, qrel_sha = qrel_fixture(tmp_path / "qrels")
    with open_search_dataset(qrel_path, expected_manifest_sha256=qrel_sha) as snapshot:
        rep_path, rep_sha = representation_fixture(tmp_path / "rep", snapshot)
        report = score_three_lanes(rep_path, expected_representation_sha256=rep_sha,
                                   qrel_snapshot=snapshot, split="train", top_k=3)
        query = report["queries"][0]
        lanes = query["lanes"]
        assert report["status"] == "candidate_default_off"
        assert report["evaluation"]["status"] == "not_evaluable"
        assert report["qrel_reader_status"] == "validated_synthetic_fixture"
        assert report["candidate_pool_scope"] == "all_frozen_chunks_available_at_query_time"
        for lane in ("dense", "sparse", "token_matrix"):
            assert len(lanes[lane]["raw_scores"]) == len(query["candidate_set"])
            assert lanes[lane]["evaluation"] == {
                "status": "not_evaluable", "reason": "H10b_has_no_complete_judgment_scope_receipt" if
                lanes[lane]["judged_coverage"]["unjudged_top_k"] == [] else "top_k_unjudged",
                "recall_at_k": None, "mrr_at_k": None, "ndcg_at_k": None}
        def score(lane: str, doc: str) -> float:
            return next(row["score"] for row in lanes[lane]["raw_scores"] if row["document_id"] == doc)
        assert score("dense", "apple") == pytest.approx(1)
        assert score("dense", "banana") == pytest.approx(0)
        assert score("dense", "pear") == pytest.approx(-1)
        assert score("sparse", "apple") == pytest.approx(6)
        assert score("sparse", "banana") == pytest.approx(4)
        assert score("sparse", "pear") == pytest.approx(0)
        assert score("token_matrix", "apple") == pytest.approx(1)
        assert score("token_matrix", "banana") == pytest.approx(.5)
        assert score("token_matrix", "pear") == pytest.approx(-.5)
        assert [item["document_id"] for item in lanes["sparse"]["top_k"][:2]] == ["apple", "banana"]
        assert [item["document_id"] for item in lanes["sparse"]["raw_scores"][2:]] == ["citrus", "coffee", "pear"]
        assert lanes["sparse"]["judged_coverage"]["unjudged_top_k"]
        assert any(item["judged_mask"] is False and item["grade"] is None for item in lanes["sparse"]["top_k"])
        assert all(lane["evaluation"]["recall_at_k"] is None for lane in lanes.values())
        top_two = score_three_lanes(rep_path, expected_representation_sha256=rep_sha,
                                    qrel_snapshot=snapshot, split="train", top_k=2)
        assert top_two["queries"][0]["lanes"]["sparse"]["judged_coverage"]["unjudged_top_k"] == []
        assert top_two["queries"][0]["lanes"]["sparse"]["evaluation"]["reason"] == \
            "H10b_has_no_complete_judgment_scope_receipt"


def test_cli_writes_one_new_deterministic_report(tmp_path: Path) -> None:
    qrel_path, qrel_sha = qrel_fixture(tmp_path / "qrels")
    with open_search_dataset(qrel_path, expected_manifest_sha256=qrel_sha) as snapshot:
        rep_path, rep_sha = representation_fixture(tmp_path / "rep", snapshot)
    output = tmp_path / "report.json"
    args = ["--representation-manifest", str(rep_path), "--representation-sha256", rep_sha,
            "--qrel-manifest-uri", str(qrel_path), "--qrel-sha256", qrel_sha,
            "--split", "train", "--top-k", "2", "--output", str(output)]
    assert main(args) == 0
    result = json.loads(output.read_bytes())
    assert result["split"] == "train" and result["top_k"] == 2
    assert result["evaluation"]["status"] == "not_evaluable"
    assert result["data_kind"] == "synthetic" and result["qrel_reader_status"] == "validated_synthetic_fixture"
    with pytest.raises(ThreeLaneScoringError, match="must be new"):
        main(args)


def test_mask_tie_break_and_small_vector_arithmetic() -> None:
    assert dense_dot((1., 2.), (3., 4.)) == 11.
    assert sparse_inner(((1, 2.), (3, 5.)), ((2, 7.), (3, 4.))) == 20.
    query = {"token_matrix": ((1., 0.), (0., 1.), (99., 99.)), "mask": (True, True, False), "dimensions": 2}
    chunk = {"token_matrix": ((1., 0.), (0., 1.), (-99., -99.)), "mask": (True, True, False), "dimensions": 2}
    assert mean_maxsim(query, chunk) == 1.
    with pytest.raises(ThreeLaneScoringError, match="non-finite sparse score"):
        sparse_inner(((1, 1e308),), ((1, 1e308),))


def add_multiversion(rows: dict, manifest: dict) -> None:
    old = rows["chunk"][0]
    revised = {**deepcopy(old), "document_revision": "r2"}
    revised["chunk_key"] = json.dumps([old["document_id"], "r2", old["chunk_id"]],
                                      ensure_ascii=False, separators=(",", ":"))
    rows["chunk"].append(revised)
    manifest["chunk_count"] += 1


@pytest.mark.parametrize("mutation,expected", [
    (lambda rows, m: rows["query"][0]["representations"]["dense"]["values"].pop(), "dimension"),
    (lambda rows, m: rows["query"][0]["representations"]["sparse"]["indices"].append(1), "sparse"),
    (lambda rows, m: rows["query"][0]["representations"]["token_matrix"].pop("mask"), "token matrix"),
    (lambda rows, m: m.__setitem__("model_revision", "wrong"), "model revision"),
    (lambda rows, m: m.__setitem__("profiles_sha256", "0" * 64), "profile bytes"),
    (add_multiversion, "multi-version"),
])
def test_bad_representation_rejected(tmp_path: Path, mutation, expected: str) -> None:
    qrel_path, qrel_sha = qrel_fixture(tmp_path / "qrels")
    with open_search_dataset(qrel_path, expected_manifest_sha256=qrel_sha) as snapshot:
        rep_path, rep_sha = representation_fixture(tmp_path / "rep", snapshot, mutate=mutation)
        with pytest.raises(ThreeLaneScoringError, match=expected):
            load_representations(rep_path, rep_sha)


def test_representation_manifest_and_shard_hash_required(tmp_path: Path) -> None:
    qrel_path, qrel_sha = qrel_fixture(tmp_path / "qrels")
    with open_search_dataset(qrel_path, expected_manifest_sha256=qrel_sha) as snapshot:
        rep_path, rep_sha = representation_fixture(tmp_path / "rep", snapshot)
        with pytest.raises(ThreeLaneScoringError, match="content addressed"):
            load_representations(rep_path, "0" * 64)
        rep_path.write_bytes(rep_path.read_bytes() + b" ")
        with pytest.raises(ThreeLaneScoringError, match="manifest SHA"):
            load_representations(rep_path, rep_sha)
        rep_path.write_bytes(rep_path.read_bytes()[:-1])
        manifest = json.loads(rep_path.read_bytes())
        shard = tmp_path / "rep" / manifest["shards"][0]["path"]
        shard.write_bytes(shard.read_bytes().replace(b"q-train", b"z-train"))
        with pytest.raises(ThreeLaneScoringError, match="shard byte/hash"):
            load_representations(rep_path, rep_sha)
