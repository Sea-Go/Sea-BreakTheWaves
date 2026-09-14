from __future__ import annotations

from copy import deepcopy
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
from pathlib import Path

import pyarrow as pa
import pyarrow.parquet as pq
import pytest

from sea_training.dataset import DatasetError, contract_directory

from search_training.authoritative import PAIR_POLICY_ID, open_authoritative_pairs
from search_training.trainer import TrainConfig, TrainError, train


UTC = timezone.utc
WINDOWS = {
    "train": (datetime(2026, 9, 1, tzinfo=UTC), datetime(2026, 9, 3, tzinfo=UTC)),
    "validation": (datetime(2026, 9, 3, tzinfo=UTC), datetime(2026, 9, 5, tzinfo=UTC)),
    "test": (datetime(2026, 9, 5, tzinfo=UTC), datetime(2026, 9, 7, tzinfo=UTC)),
}


def digest(raw: bytes) -> str:
    return sha256(raw).hexdigest()


def row(split: str, doc: str, grade: int, *, judged: bool = True, query: str | None = None) -> dict:
    day = WINDOWS[split][0]
    qid = query or f"q-{split}"
    text = "train apple" if split == "train" else f"holdoutonly {split}"
    chunk = f"{doc} passage about {text}"
    judgment = f"judge-{split}-{doc}"
    return {"judgment_id": judgment, "judgment_revision": 1,
            "query_id": qid, "query_family_id": f"family-{split}",
            "near_duplicate_cluster_id": f"near-{split}",
            "query_text": text, "query_text_sha256": digest(text.encode()),
            "document_id": doc, "document_revision": "r1", "chunk_id": f"{doc}:chunk-1",
            "chunk_text": chunk, "chunk_text_sha256": digest(chunk.encode()),
            "relevance_grade": grade, "judged_mask": judged,
            "judgment_source": "synthetic_fixture", "judgment_source_ref": f"fixture/{judgment}",
            "judgment_source_hash": digest(judgment.encode()),
            "query_time": day + timedelta(hours=8),
            "content_available_at": day - timedelta(days=2),
            "judged_at": day + timedelta(hours=9),
            "available_at": day + timedelta(hours=10),
            "revoked_at": None, "split": split}


def rows() -> dict[str, list[dict]]:
    return {"train": [row("train", "apple", 3), row("train", "banana", 1), row("train", "pear", 0)],
            "validation": [row("validation", "citrus", 3), row("validation", "coffee", 0)],
            "test": [row("test", "espresso", 3), row("test", "orange", 0)]}


def fixture(root: Path, source: dict[str, list[dict]] | None = None) -> tuple[Path, str]:
    root.mkdir(parents=True)
    source = source or rows()
    columns = json.loads((contract_directory() / "search-qrel.columns.v1.json").read_text())
    def arrow_type(name: str) -> pa.DataType:
        return pa.timestamp("us", tz="UTC") if name == "timestamp[us, tz=UTC]" else pa.type_for_alias(name)
    schema = pa.schema([pa.field(c["name"], arrow_type(c["arrow_type"]), nullable=c["nullable"]) for c in columns])
    files, splits = [], []
    for split, records in source.items():
        path = root / f"{split}-00000.parquet"
        pq.write_table(pa.Table.from_pylist(records, schema=schema), path)
        files.append({"path": path.name, "split": split, "shard_index": 0,
                      "rows": len(records), "size_bytes": path.stat().st_size, "sha256": digest(path.read_bytes())})
        start, end = WINDOWS[split]
        splits.append({"name": split, "start": start.isoformat(), "end": end.isoformat(),
                       "rows": len(records), "shard_count": 1})
    cutoff = datetime(2026, 9, 7, 12, tzinfo=UTC)
    manifest = {"schema_version": "sea.search-qrel-dataset.v1", "row_contract": "sea.search-qrel.v1",
                "dataset_id": "graded-test-fixture", "revision": 1, "parent_revision": None,
                "parent_manifest_sha256": None, "domain": "search", "data_kind": "synthetic",
                "created_at": (cutoff + timedelta(hours=1)).isoformat(),
                "judgment_policy_id": "synthetic-graded-qrel-v1",
                "split_policy_id": "query-time-family-nearcluster-v1", "near_duplicate_scope": "query",
                "columns": columns,
                "source": {"warehouse_run_id": "fixture-run-1", "project_revision": "fixture-rev-1",
                           "generation": "fixture-g1", "ingest_cutoff": cutoff.isoformat(),
                           "batches": [{"batch_id": "fixture", "sha256": digest(b"fixture")}],
                           "watermarks": [{"source": "synthetic-search-qrel", "partition": "fixture-0",
                                           "position": 7, "event_time": cutoff.isoformat()}],
                           "judgment_snapshot_sha256": digest(b"judgments"),
                           "content_snapshot_sha256": digest(b"content"),
                           "dbt_manifest_sha256": digest(b"dbt-manifest"),
                           "dbt_run_results_sha256": digest(b"dbt-results"),
                           "recipe_sha256": digest(b"recipe")},
                "splits": splits, "files": files, "row_count": sum(len(r) for r in source.values())}
    path = root / "manifest.json"
    path.write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
    return path, digest(path.read_bytes())


def test_graded_pairs_fit_only_train_and_exact_resume(tmp_path: Path) -> None:
    manifest, expected = fixture(tmp_path / "source")
    with open_authoritative_pairs(manifest, expected_manifest_sha256=expected) as snapshot:
        assert snapshot.manifest_sha256 == expected and snapshot.pair_policy_id == PAIR_POLICY_ID
        assert snapshot.reader_report["status"] == "validated_synthetic_fixture"
        assert {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")} == {
            "train": 2, "validation": 1, "test": 1}
        assert [(p["positive"]["grade"], p["negative"]["grade"], p["sample_weight"])
                for p in snapshot.rows["train"]] == [(3, 1, 2 / 3), (3, 0, 1.0)]
        assert all(p["pair_mask"] and p["positive"]["judged_mask"] and p["negative"]["judged_mask"]
                   and p["query_family_id"] == "family-train" and p["near_duplicate_cluster_id"] == "near-train"
                   and p["negative_source"] == "explicit_judged_lower_qrel"
                   for p in snapshot.rows["train"])
        config = TrainConfig(epochs=3, seed=31)
        full, resumed = tmp_path / "full.json", tmp_path / "resumed.json"
        full_candidate, resumed_candidate = tmp_path / "full-candidate.json", tmp_path / "resumed-candidate.json"
        complete = train(snapshot, config, full, candidate_path=full_candidate)
        stopped = train(snapshot, config, resumed, max_steps=2, candidate_path=resumed_candidate)
        assert stopped["status"] == "INTERRUPTED" and not resumed_candidate.exists()
        with open_authoritative_pairs(manifest, expected_manifest_sha256=expected) as reverified:
            assert reverified.pair_snapshot_sha256 == snapshot.pair_snapshot_sha256
            recovered = train(reverified, config, resumed, resume=True, candidate_path=resumed_candidate)
        assert complete == recovered
        assert full.read_bytes() == resumed.read_bytes()
        assert full_candidate.read_bytes() == resumed_candidate.read_bytes()
        assert complete["status"] == "COMPLETED_CANDIDATE_SYNTHETIC"
        assert complete["model_quality"] is None and complete["loss_interpretation"] == "optimization_diagnostic_only"
        assert complete["reader_status"] == "validated_synthetic_fixture"
        state = json.loads(full.read_bytes())["payload"]
        assert "holdoutonly" not in state["idf"]
        assert state["pair_snapshot_sha256"] == snapshot.pair_snapshot_sha256
        candidate = json.loads(full_candidate.read_bytes())
        assert candidate["active"] is False and candidate["model_quality"] is None
        assert candidate["pair_snapshot_sha256"] == snapshot.pair_snapshot_sha256
        changed = replace(snapshot, pair_snapshot_sha256="0" * 64)
        with pytest.raises(TrainError, match="pair snapshot hash differs"):
            train(changed, config, full, resume=True)


def test_authoritative_reader_rejects_unpinned_or_tampered_parquet(tmp_path: Path) -> None:
    manifest, expected = fixture(tmp_path / "source")
    with pytest.raises(DatasetError, match="manifest hash mismatch"):
        with open_authoritative_pairs(manifest, expected_manifest_sha256="0" * 64):
            pass
    shard = manifest.parent / "train-00000.parquet"
    original = shard.read_bytes()
    shard.write_bytes(original + b"tamper")
    with pytest.raises(DatasetError, match="artifact size mismatch"):
        with open_authoritative_pairs(manifest, expected_manifest_sha256=expected):
            pass
    shard.write_bytes(b"X" + original[1:])
    with pytest.raises(DatasetError, match="artifact hash mismatch"):
        with open_authoritative_pairs(manifest, expected_manifest_sha256=expected):
            pass


@pytest.mark.parametrize("change,reason", [
    (lambda data: data["train"][2].update(judged_mask=False), "unjudged"),
    (lambda data: data["train"][2].update(judgment_source="human_judgment"), "judgment source"),
    (lambda data: data["validation"][0].update(near_duplicate_cluster_id="near-train"), "near-duplicate"),
    (lambda data: (data["train"][1].update(relevance_grade=3),
                   data["train"][2].update(relevance_grade=3)), "no same-query"),
])
def test_authoritative_training_rejects_mask_group_or_missing_low(tmp_path: Path, change, reason: str) -> None:
    data = deepcopy(rows())
    change(data)
    manifest, expected = fixture(tmp_path / "source", data)
    with pytest.raises(DatasetError, match=reason):
        with open_authoritative_pairs(manifest, expected_manifest_sha256=expected):
            pass
