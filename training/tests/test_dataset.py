from copy import deepcopy
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
from pathlib import Path
import subprocess
import sys

import pyarrow as pa
from pyarrow import fs
import pyarrow.parquet as pq
import pytest

from sea_training.dataset import DatasetError, contract_directory, open_dataset


UTC = timezone.utc
BASE = datetime(2026, 9, 10, tzinfo=UTC)


def iso(value):
    return value.isoformat().replace("+00:00", "Z")


def sample(index, split="train", day=0, **overrides):
    event = BASE + timedelta(days=day, hours=12, seconds=index)
    row = {
        "sample_id": f"sample-{index}", "request_id": f"request-{index}",
        "impression_id": f"impression-{index}", "authority_id": "rtw",
        "tenant_id": "tenant-a", "subject_id": "subject-a", "item_id": f"item-{index}",
        "content_revision": "content-r1", "request_time": event,
        "impression_time": event, "feature_cutoff": event,
        "feature_snapshot_ref": f"snapshot-{index}", "feature_contract_id": "engagement-features.v1",
        "feature_available_at": event - timedelta(seconds=2), "user_interest": 0.2,
        "item_quality": 0.8, "label": index % 2,
        "label_state": "POSITIVE" if index % 2 else "OBSERVED_NEGATIVE",
        "label_revision": 1, "label_observation_end": event + timedelta(minutes=30),
        "sampling_probability": 1.0, "split": split,
    }
    row.update(overrides)
    return row


def make_dataset(directory, rows=None):
    directory.mkdir(parents=True, exist_ok=True)
    if rows is None:
        rows = [sample(1), sample(2), sample(3, "validation", 1), sample(4, "test", 2)]
    columns = json.loads((contract_directory() / "recommend-engagement.columns.v1.json").read_text())
    def datatype(name):
        return pa.timestamp("us", tz="UTC") if name == "timestamp[us, tz=UTC]" else pa.type_for_alias(name)
    schema = pa.schema([pa.field(c["name"], datatype(c["arrow_type"]), nullable=c["nullable"]) for c in columns])
    files, splits = [], []
    for day, name in enumerate(("train", "validation", "test")):
        selected = [r for r in rows if r["split"] == name]
        shard = directory / f"{name}.parquet"
        pq.write_table(pa.Table.from_pylist(selected, schema=schema), shard)
        files.append({"path": shard.name, "split": name, "rows": len(selected),
                      "size_bytes": shard.stat().st_size, "sha256": sha256(shard.read_bytes()).hexdigest()})
        splits.append({"name": name, "start": iso(BASE + timedelta(days=day)),
                       "end": iso(BASE + timedelta(days=day + 1))})
    watermark = iso(BASE + timedelta(days=4))
    manifest = {
        "schema_version": "sea.training-dataset.v2", "row_contract": "sea.recommend-engagement.v1",
        "dataset_id": "dataset-fixture-v1", "revision": 1, "parent_revision": None,
        "domain": "recommend", "data_kind": "synthetic", "created_at": watermark,
        "feature_contract_id": "engagement-features.v1", "columns": columns,
        "source": {"warehouse_run_id": "warehouse-fixture", "project_revision": "fixture-code-r1",
                   "generation": "g1", "ingest_cutoff": watermark, "recipe_sha256": "d" * 64,
                   "batches": [{"batch_id": "source-b1", "sha256": "a" * 64}],
                   "watermarks": [{"source": "product", "partition": "0", "position": 7, "event_time": watermark}],
                   "dim_revisions": [{"item_id": item, "content_revision": revision} for item, revision in sorted({(r["item_id"], r["content_revision"]) for r in rows})], "dbt_manifest_sha256": "b" * 64,
                   "dbt_run_results_sha256": "c" * 64},
        "label": {"target": "effective_read", "rule_version": "fixture-read-30m.v1",
                  "window_seconds": 1800, "window_anchor": "impression_time", "maturity_watermark": watermark, "source": "synthetic"},
        "splits": splits, "files": files, "row_count": len(rows), "transform_refs": [],
    }
    path = directory / "manifest.json"
    path.write_text(json.dumps(manifest, indent=2))
    return path, manifest


def update(path, manifest):
    path.write_text(json.dumps(manifest))


def test_accepts_all_shards_and_preserves_verified_bytes(tmp_path):
    path, manifest = make_dataset(tmp_path / "original")
    original_hash = sha256(path.read_bytes()).hexdigest()
    with open_dataset(path, expected_manifest_sha256=original_hash) as dataset:
        assert dataset.report["validated_rows"] == 4
        assert dataset.report["splits"]["train"] == {"rows": 2, "positive": 1, "negative": 1}
        # Source updates cannot change the already validated training snapshot.
        (path.parent / "train.parquet").write_bytes(b"changed-after-validation")
        manifest["revision"] = 2
        update(path, manifest)
        actual = [r for b in dataset.iter_batches("train", batch_size=1) for r in b.to_pylist()]
        assert [r["sample_id"] for r in actual] == ["sample-1", "sample-2"]
        snapshot_dir = dataset.directory
    assert not snapshot_dir.exists()


@pytest.mark.parametrize("mutation,expected", [
    (lambda m: m.update(schema_version="sea.training-dataset.v3"), "invalid manifest"),
    (lambda m: m.update(row_count=7), "row totals"),
    (lambda m: m["files"][0].update(path="../train.parquet"), "normalized relative"),
    (lambda m: m["files"][0].update(sha256="d" * 64), "hash mismatch"),
    (lambda m: m["files"][0].update(size_bytes=1), "size mismatch"),
    (lambda m: m["files"][0].update(rows=9) or m.update(row_count=11), "footer row"),
    (lambda m: m["splits"][1].update(start=m["splits"][0]["start"]), "overlap"),
    (lambda m: m["source"]["watermarks"].append(deepcopy(m["source"]["watermarks"][0])), "duplicate source"),
    (lambda m: m["source"]["watermarks"][0].update(event_time=iso(BASE)), "source coverage"),
    (lambda m: m["columns"][0].update(arrow_type="int64"), "row contract"),
    (lambda m: m.update(parent_revision=1), "parent_revision"),
    (lambda m: m.update(data_kind="observed"), "synthetic labels"),
    (lambda m: m["files"].append(deepcopy(m["files"][0])), "duplicate artifact"),
])
def test_rejects_invalid_manifest_or_shard(tmp_path, mutation, expected):
    path, manifest = make_dataset(tmp_path)
    mutation(manifest)
    update(path, manifest)
    with pytest.raises(DatasetError, match=expected):
        with open_dataset(path):
            pytest.fail("invalid dataset was exposed to training")


@pytest.mark.parametrize("changes,expected", [
    ({"feature_available_at": BASE + timedelta(days=5)}, "future feature"),
    ({"label_state": "PENDING"}, "label/state"),
    ({"label": 7}, "label/state"),
    ({"sampling_probability": 0.0}, "sampling probability"),
    ({"user_interest": float("nan")}, "non-finite"),
    ({"item_quality": float("inf")}, "non-finite"),
    ({"feature_contract_id": "other"}, "feature contract"),
    ({"authority_id": ""}, "empty required"),
    ({"label_observation_end": BASE + timedelta(days=5)}, "immature"),
])
def test_rejects_semantically_bad_rows_even_with_correct_hash(tmp_path, changes, expected):
    path, _ = make_dataset(tmp_path, [sample(1, **changes)])
    with pytest.raises(DatasetError, match=expected):
        with open_dataset(path):
            pytest.fail("bad row exposed")


def test_rejects_duplicate_exposure_even_with_distinct_sample_ids(tmp_path):
    path, _ = make_dataset(tmp_path, [sample(1), sample(2, impression_id="impression-1")])
    with pytest.raises(DatasetError, match="duplicate sample or exposure"):
        with open_dataset(path):
            pass


def test_identity_includes_authority_and_tenant(tmp_path):
    rows = [sample(1), sample(2, impression_id="impression-1", tenant_id="tenant-b")]
    path, _ = make_dataset(tmp_path, rows)
    with open_dataset(path) as dataset:
        assert dataset.report["validated_rows"] == 2


def test_read_window_starts_at_impression_and_features_at_actual_cutoff(tmp_path):
    row = sample(1)
    request = row["request_time"]
    row.update(feature_cutoff=request + timedelta(seconds=3),
               feature_available_at=request + timedelta(seconds=2),
               impression_time=request + timedelta(seconds=20),
               label_observation_end=request + timedelta(seconds=20, minutes=30))
    path, _ = make_dataset(tmp_path, [row])
    with open_dataset(path) as dataset:
        assert dataset.report["validated_rows"] == 1


def test_feature_cutoff_after_impression_is_rejected(tmp_path):
    row = sample(1)
    row["feature_cutoff"] = row["impression_time"] + timedelta(seconds=1)
    path, _ = make_dataset(tmp_path, [row])
    with pytest.raises(DatasetError, match="ordering"):
        with open_dataset(path):
            pass


def test_request_group_cannot_cross_time_splits(tmp_path):
    rows = [sample(1), sample(2, "validation", 1, request_id="request-1")]
    path, _ = make_dataset(tmp_path, rows)
    with pytest.raises(DatasetError, match="request group crosses"):
        with open_dataset(path):
            pass


def test_checks_all_shards_before_yielding_any(tmp_path):
    path, _ = make_dataset(tmp_path)
    (tmp_path / "test.parquet").unlink()
    with pytest.raises(DatasetError, match="cannot read"):
        with open_dataset(path):
            pytest.fail("validation yielded before checking test shard")


def test_duplicate_json_keys_and_manifest_digest_rejected(tmp_path):
    path, _ = make_dataset(tmp_path)
    with pytest.raises(DatasetError, match="manifest hash"):
        with open_dataset(path, expected_manifest_sha256="0" * 64):
            pass
    original = path.read_text()
    path.write_text(original.replace('"revision": 1,', '"revision": 1, "revision": 2,'))
    with pytest.raises(DatasetError, match="invalid manifest JSON"):
        with open_dataset(path):
            pass


def test_custom_arrow_filesystem_and_cli(tmp_path):
    path, _ = make_dataset(tmp_path)
    with open_dataset("manifest.json", filesystem=fs.SubTreeFileSystem(str(tmp_path), fs.LocalFileSystem())) as dataset:
        assert dataset.report["validated_rows"] == 4
    result = subprocess.run([sys.executable, "-m", "sea_training", str(path)], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout)["manifest_sha256"] == sha256(path.read_bytes()).hexdigest()


def test_transform_artifact_integrity_and_train_only_fit(tmp_path):
    path, manifest = make_dataset(tmp_path)
    transform = tmp_path / "normalization.json"
    transform.write_text('{"mean": 0.5}')
    manifest["transform_refs"] = [{"name": "normalization", "path": transform.name,
                                  "sha256": sha256(transform.read_bytes()).hexdigest(), "fit_split": "train"}]
    update(path, manifest)
    with open_dataset(path):
        pass
    manifest["transform_refs"][0]["fit_split"] = "test"
    update(path, manifest)
    with pytest.raises(DatasetError, match="invalid manifest"):
        with open_dataset(path):
            pass


def test_unsupported_location_is_a_structured_rejection():
    with pytest.raises(DatasetError, match="manifest location"):
        with open_dataset("unknown-scheme://manifest.json"):
            pass


def test_missing_schema_is_a_structured_rejection(tmp_path):
    path, _ = make_dataset(tmp_path)
    with pytest.raises(DatasetError, match="schemas unavailable"):
        with open_dataset(path, schema_dir=tmp_path / "absent"):
            pass


@pytest.mark.parametrize("revisions", [[], [{"item_id": "unrelated", "content_revision": "content-r1"}],
                                        [{"item_id": "item-1", "content_revision": "wrong"}]])
def test_manifest_dim_set_must_cover_each_sample(tmp_path, revisions):
    path, manifest = make_dataset(tmp_path, [sample(1)])
    manifest["source"]["dim_revisions"] = revisions
    update(path, manifest)
    with pytest.raises(DatasetError, match="absent from fixed DIM"):
        with open_dataset(path):
            pass


def test_dimension_references_are_structured_not_delimiter_keys(tmp_path):
    rows = [sample(1, item_id="a:b", content_revision="c"),
            sample(2, item_id="a", content_revision="b:c")]
    path, manifest = make_dataset(tmp_path, rows)
    with open_dataset(path) as dataset:
        assert dataset.report["validated_rows"] == 2
    manifest["source"]["dim_revisions"] = [{"item_id": "a:b", "content_revision": "c"}]
    update(path, manifest)
    with pytest.raises(DatasetError, match="absent from fixed DIM"):
        with open_dataset(path):
            pass


def test_manifest_v1_requires_explicit_producer_upgrade(tmp_path):
    path, manifest = make_dataset(tmp_path)
    manifest["schema_version"] = "sea.training-dataset.v1"
    manifest["source"]["dim_revisions"] = ["ambiguous:legacy"]
    update(path, manifest)
    with pytest.raises(DatasetError, match="invalid manifest"):
        with open_dataset(path):
            pass
