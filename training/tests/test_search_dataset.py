from __future__ import annotations

from copy import deepcopy
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
import os
from pathlib import Path

import pyarrow as pa
from pyarrow import fs
import pyarrow.parquet as pq
import pytest

from sea_training.dataset import DatasetError, contract_directory
from sea_training.search_dataset import open_search_dataset


BASE = datetime(2026, 9, 10, tzinfo=timezone.utc)


def iso(value: datetime) -> str:
    return value.isoformat().replace("+00:00", "Z")


def row(index: int, split: str, day: int, grade: int, **changes) -> dict:
    query_time = BASE + timedelta(days=day, hours=12)
    query_text = f"how to search {index}"
    chunk_text = f"judged chunk content {index}"
    value = {
        "judgment_id": f"judgment-{index}", "judgment_revision": 1,
        "query_id": f"query-{index}", "query_family_id": f"family-{index}",
        "near_duplicate_cluster_id": f"query-cluster-{index}",
        "query_text": query_text, "query_text_sha256": sha256(query_text.encode()).hexdigest(),
        "document_id": f"document-{index}", "document_revision": "r1",
        "chunk_id": f"chunk-{index}", "chunk_text": chunk_text,
        "chunk_text_sha256": sha256(chunk_text.encode()).hexdigest(),
        "relevance_grade": grade, "judged_mask": True,
        "judgment_source": "synthetic_fixture", "judgment_source_ref": f"fixture-qrel/{index}",
        "judgment_source_hash": sha256(f"fixture-source-{index}".encode()).hexdigest(),
        "query_time": query_time, "content_available_at": query_time - timedelta(hours=1),
        "judged_at": query_time + timedelta(minutes=2),
        "available_at": query_time + timedelta(minutes=3), "revoked_at": None,
        "split": split,
    }
    value.update(changes)
    return value


def fixture_rows() -> list[dict]:
    return [row(1, "train", 0, 3), row(2, "train", 0, 0),
            row(3, "validation", 1, 2), row(4, "test", 2, 1)]


def make_dataset(directory, rows=None, change_manifest=None):
    directory.mkdir(parents=True, exist_ok=True)
    rows = fixture_rows() if rows is None else rows
    columns = json.loads((contract_directory() / "search-qrel.columns.v1.json").read_text())
    def dtype(declared):
        return pa.timestamp("us", tz="UTC") if declared == "timestamp[us, tz=UTC]" else pa.type_for_alias(declared)
    schema = pa.schema([pa.field(c["name"], dtype(c["arrow_type"]), nullable=c["nullable"]) for c in columns])
    files, splits = [], []
    for day, split in enumerate(("train", "validation", "test")):
        selected = [r for r in rows if r["split"] == split]
        shard = directory / f"{split}-0.parquet"
        pq.write_table(pa.Table.from_pylist(selected, schema=schema), shard)
        files.append({"path": shard.name, "split": split, "shard_index": 0,
                      "rows": len(selected), "size_bytes": shard.stat().st_size,
                      "sha256": sha256(shard.read_bytes()).hexdigest()})
        splits.append({"name": split, "start": iso(BASE + timedelta(days=day)),
                       "end": iso(BASE + timedelta(days=day + 1)),
                       "rows": len(selected), "shard_count": 1})
    cutoff = iso(BASE + timedelta(days=4))
    manifest = {
        "schema_version": "sea.search-qrel-dataset.v1", "row_contract": "sea.search-qrel.v1",
        "dataset_id": "search-qrel-fixture-001", "revision": 1,
        "parent_revision": None, "parent_manifest_sha256": None,
        "domain": "search", "data_kind": "synthetic", "created_at": cutoff,
        "judgment_policy_id": "fixture-judgment-v1", "split_policy_id": "query-family-cluster-v1",
        "near_duplicate_scope": "query", "columns": columns,
        "source": {"warehouse_run_id": "fixture-warehouse-run", "project_revision": "fixture-r1",
                   "generation": "search-qrel-fixture-g1", "ingest_cutoff": cutoff,
                   "batches": [{"batch_id": "fixture-b1", "sha256": "a" * 64}],
                   "watermarks": [{"source": "rtw.search.qrel", "partition": "0", "position": 4,
                                   "event_time": cutoff}],
                   "judgment_snapshot_sha256": "b" * 64, "content_snapshot_sha256": "c" * 64,
                   "dbt_manifest_sha256": "d" * 64, "dbt_run_results_sha256": "e" * 64,
                   "recipe_sha256": "f" * 64},
        "splits": splits, "files": files, "row_count": len(rows),
    }
    if change_manifest:
        change_manifest(manifest)
    path = directory / "manifest.json"
    path.write_text(json.dumps(manifest, sort_keys=True, ensure_ascii=False))
    return path, manifest, sha256(path.read_bytes()).hexdigest()


def rewrite(path, manifest):
    path.write_text(json.dumps(manifest, sort_keys=True, ensure_ascii=False))


def test_judged_pointwise_shards_are_pinned_before_batches(tmp_path):
    path, _, digest = make_dataset(tmp_path)
    with open_search_dataset(path, expected_manifest_sha256=digest) as snapshot:
        assert snapshot.report["status"] == "validated_synthetic_fixture"
        assert snapshot.report["model_quality"] is None
        assert snapshot.report["splits"]["train"]["grades"] == {"0": 1, "1": 0, "2": 0, "3": 1}
        (tmp_path / "train-0.parquet").write_bytes(b"changed-after-validation")
        snapshot.manifest["files"][0]["path"] = "../unvalidated.parquet"
        assert len([r for batch in snapshot.iter_batches("train", 1) for r in batch.to_pylist()]) == 2
        saved = snapshot.directory
    assert not saved.exists()


def test_multiple_ordered_train_shards_are_complete(tmp_path):
    path, manifest, _ = make_dataset(tmp_path)
    original = pq.read_table(tmp_path / "train-0.parquet")
    first, second = tmp_path / "train-0.parquet", tmp_path / "train-1.parquet"
    pq.write_table(original.slice(0, 1), first)
    pq.write_table(original.slice(1, 1), second)
    manifest["files"][0].update(rows=1, size_bytes=first.stat().st_size,
                                sha256=sha256(first.read_bytes()).hexdigest())
    manifest["files"].insert(1, {"path": second.name, "split": "train", "shard_index": 1,
                                 "rows": 1, "size_bytes": second.stat().st_size,
                                 "sha256": sha256(second.read_bytes()).hexdigest()})
    manifest["splits"][0]["shard_count"] = 2
    rewrite(path, manifest)
    with open_search_dataset(path, expected_manifest_sha256=sha256(path.read_bytes()).hexdigest()) as snapshot:
        assert [r["judgment_id"] for batch in snapshot.iter_batches("train")
                for r in batch.to_pylist()] == ["judgment-1", "judgment-2"]


@pytest.mark.parametrize("mutate,error", [
    (lambda m: m.update(schema_version="sea.training-dataset.v2"), "invalid search manifest"),
    (lambda m: m.update(row_contract="sea.recommend-engagement.v1"), "invalid search manifest"),
    (lambda m: m.update(domain="recommend"), "invalid search manifest"),
    (lambda m: m.update(near_duplicate_scope="document"), "invalid search manifest"),
    (lambda m: m.update(parent_revision=1), "parent revision"),
    (lambda m: m.update(revision=2), "parent revision"),
    (lambda m: m["source"].update(ingest_cutoff=iso(BASE + timedelta(days=5))), "source cutoff"),
    (lambda m: m["source"]["watermarks"].append(deepcopy(m["source"]["watermarks"][0])), "source coverage"),
    (lambda m: m["splits"][1].update(start=m["splits"][0]["start"]), "overlap"),
    (lambda m: m["files"][0].update(path="../train.parquet"), "normalized relative"),
    (lambda m: m["files"][0].update(sha256="0" * 64), "hash mismatch"),
    (lambda m: m["files"][0].update(size_bytes=1), "size mismatch"),
    (lambda m: m["files"][0].update(rows=4) or m["splits"][0].update(rows=4) or m.update(row_count=6), "footer row"),
    (lambda m: m["splits"][0].update(shard_count=2), "missing or duplicate shard"),
    (lambda m: m["files"][0].update(shard_index=1), "missing or duplicate shard"),
    (lambda m: m["files"].reverse(), "split/index order"),
    (lambda m: m["columns"][0].update(arrow_type="uint32"), "row contract"),
])
def test_manifest_and_shard_errors_fail_before_reader_yields(tmp_path, mutate, error):
    path, manifest, _ = make_dataset(tmp_path)
    mutate(manifest)
    rewrite(path, manifest)
    with pytest.raises(DatasetError, match=error):
        with open_search_dataset(path, expected_manifest_sha256=sha256(path.read_bytes()).hexdigest()):
            pytest.fail("unvalidated search row became visible")


@pytest.mark.parametrize("change,error", [
    ({"judged_mask": False}, "unjudged"),
    ({"relevance_grade": 4}, "graded qrel"),
    ({"judgment_revision": 0}, "judgment revision"),
    ({"judgment_source": "human_judgment"}, "data kind"),
    ({"judgment_source_hash": "0"}, "source/content hash"),
    ({"content_available_at": BASE + timedelta(days=1)}, "availability"),
    ({"judged_at": BASE}, "availability"),
    ({"available_at": BASE + timedelta(days=5)}, "availability"),
    ({"revoked_at": BASE + timedelta(days=3)}, "revoked"),
    ({"query_text_sha256": "0" * 64}, "text hash"),
    ({"chunk_text_sha256": "0" * 64}, "text hash"),
    ({"query_time": BASE + timedelta(days=2)}, "query time outside"),
    ({"query_id": ""}, "empty required"),
])
def test_unqualified_qrel_or_temporal_leak_rejected(tmp_path, change, error):
    rows = fixture_rows()
    rows[0].update(change)
    path, _, digest = make_dataset(tmp_path, rows)
    with pytest.raises(DatasetError, match=error):
        with open_search_dataset(path, expected_manifest_sha256=digest):
            pass


@pytest.mark.parametrize("change,error", [
    ({"query_id": "query-1", "query_text": "different query"}, "query or chunk text hash"),
    ({"query_family_id": "family-1"}, "query family or near-duplicate"),
    ({"near_duplicate_cluster_id": "query-cluster-1"}, "query family or near-duplicate"),
    ({"judgment_id": "judgment-1"}, "duplicate or multiple visible"),
    ({"document_id": "document-1", "chunk_id": "chunk-1", "query_id": "query-1"}, "query identity|duplicate or multiple"),
])
def test_query_identity_family_cluster_and_qrel_grain_cannot_cross_splits(tmp_path, change, error):
    rows = fixture_rows()
    rows[2].update(change)
    path, _, digest = make_dataset(tmp_path, rows)
    with pytest.raises(DatasetError, match=error):
        with open_search_dataset(path, expected_manifest_sha256=digest):
            pass


def test_multiple_visible_revisions_of_one_logical_qrel_rejected(tmp_path):
    rows = fixture_rows()
    changed = row(5, "train", 0, 1, query_id="query-1", query_family_id="family-1",
                  near_duplicate_cluster_id="query-cluster-1", query_text=rows[0]["query_text"],
                  query_text_sha256=rows[0]["query_text_sha256"], query_time=rows[0]["query_time"],
                  document_id=rows[0]["document_id"], document_revision=rows[0]["document_revision"],
                  chunk_id=rows[0]["chunk_id"], chunk_text=rows[0]["chunk_text"],
                  chunk_text_sha256=rows[0]["chunk_text_sha256"],
                  content_available_at=rows[0]["content_available_at"], judgment_revision=2)
    rows.append(changed)
    path, _, digest = make_dataset(tmp_path, rows)
    with pytest.raises(DatasetError, match="multiple visible judgment revisions"):
        with open_search_dataset(path, expected_manifest_sha256=digest):
            pass


def test_missing_empty_corrupt_and_unpinned_input_rejected(tmp_path):
    path, _, digest = make_dataset(tmp_path)
    with pytest.raises(DatasetError, match="expected search manifest"):
        with open_search_dataset(path, expected_manifest_sha256=""):
            pass
    with pytest.raises(DatasetError, match="expected search manifest"):
        with open_search_dataset(path, expected_manifest_sha256=None):
            pass
    with pytest.raises(DatasetError, match="manifest hash mismatch"):
        with open_search_dataset(path, expected_manifest_sha256="0" * 64):
            pass
    (tmp_path / "test-0.parquet").unlink()
    with pytest.raises(DatasetError, match="cannot read declared artifact"):
        with open_search_dataset(path, expected_manifest_sha256=digest):
            pytest.fail("a missing later shard exposed earlier train data")
    rows = fixture_rows()[:2] + [fixture_rows()[3]]
    path, _, digest = make_dataset(tmp_path / "empty", rows)
    with pytest.raises(DatasetError, match="invalid search manifest"):
        with open_search_dataset(path, expected_manifest_sha256=digest):
            pass


def test_observed_human_judgment_contract_is_distinct_from_fixture(tmp_path):
    rows = [dict(r, judgment_source="human_judgment") for r in fixture_rows()]
    path, manifest, digest = make_dataset(tmp_path, rows, lambda m: m.update(data_kind="observed"))
    with open_search_dataset(path, expected_manifest_sha256=digest) as snapshot:
        assert snapshot.report["status"] == "validated_manifest_and_rows"
        assert snapshot.report["data_kind"] == "observed"
        assert snapshot.report["model_quality"] is None
    manifest["data_kind"] = "synthetic"
    rewrite(path, manifest)
    with pytest.raises(DatasetError, match="data kind"):
        with open_search_dataset(path, expected_manifest_sha256=sha256(path.read_bytes()).hexdigest()):
            pass


def test_arrow_filesystem_and_parent_lineage(tmp_path):
    path, manifest, _ = make_dataset(tmp_path)
    manifest.update(revision=2, parent_revision=1, parent_manifest_sha256="a" * 64)
    rewrite(path, manifest)
    digest = sha256(path.read_bytes()).hexdigest()
    filesystem = fs.SubTreeFileSystem(str(tmp_path), fs.LocalFileSystem())
    with open_search_dataset("manifest.json", expected_manifest_sha256=digest, filesystem=filesystem) as snapshot:
        assert snapshot.report["validated_rows"] == 4


def test_published_search_qrel_generations_with_original_parquet():
    source = os.environ.get("SEA_SEARCH_QREL_PRODUCER_EVIDENCE")
    if not source:
        pytest.skip("set producer evidence directory for real CH/dbt/S3 export handoff")
    root = Path(source)
    report = json.loads((root / "report.json").read_text())
    assert report["evidence_level"] == "L2_synthetic_fixture_real_CH_dbt_SeaweedFS"
    for version, expected in (("v1", (13, 5, 4, 4)), ("v2", (12, 4, 4, 4))):
        entry = report["generations"][version]
        with open_search_dataset(root / f"dataset-{version}" / "manifest.json",
                                 expected_manifest_sha256=entry["manifest_sha256"]) as snapshot:
            counts = tuple(sum(batch.num_rows for batch in snapshot.iter_batches(split))
                           for split in ("train", "validation", "test"))
            assert (snapshot.report["validated_rows"], *counts) == expected
            assert snapshot.report["status"] == "validated_synthetic_fixture"
