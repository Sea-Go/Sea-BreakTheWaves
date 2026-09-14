"""Small fixed Parquet contract fixtures; never represent production behavior."""

from copy import deepcopy
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
import math
import subprocess
import sys

import pyarrow as pa
import pyarrow.parquet as pq
import pytest

from sea_training.dataset import DatasetError, contract_directory
from recommend.trainer import TrainingError, _gradient, _initial_state, ranker_logit, tower_score, train


BASE = datetime(2026, 9, 10, tzinfo=timezone.utc)


def iso(value):
    return value.isoformat().replace("+00:00", "Z")


def row(index, split, day, label):
    event = BASE + timedelta(days=day, hours=12, seconds=index)
    return {"sample_id": f"s{index}", "request_id": f"r{index}",
            "impression_id": f"e{index}", "authority_id": "fixture",
            "tenant_id": "platform", "subject_id": f"u{index % 3}",
            "item_id": f"i{index % 4}", "content_revision": "r1",
            "request_time": event, "impression_time": event,
            "feature_cutoff": event, "feature_snapshot_ref": f"f{index}",
            "feature_contract_id": "engagement-features.v1",
            "feature_available_at": event - timedelta(seconds=1),
            "user_interest": 0.2 + (index % 3) * 0.2,
            "item_quality": 0.1 + (index % 4) * 0.15,
            "label": label, "label_state": "POSITIVE" if label else "OBSERVED_NEGATIVE",
            "label_revision": 1,
            "label_observation_end": event + timedelta(minutes=30),
            "sampling_probability": 1.0, "split": split}


def fixture(directory, *, rows=None, label_source="synthetic", data_kind="synthetic"):
    directory.mkdir(parents=True)
    if rows is None:
        rows = [row(i, "train", 0, i % 2) for i in range(1, 7)]
        rows += [row(i, "validation", 1, i % 2) for i in range(7, 9)]
        rows += [row(i, "test", 2, i % 2) for i in range(9, 11)]
    columns = json.loads((contract_directory() / "recommend-engagement.columns.v1.json").read_text())
    schema = pa.schema([pa.field(c["name"], pa.timestamp("us", tz="UTC") if
                      c["arrow_type"] == "timestamp[us, tz=UTC]" else pa.type_for_alias(c["arrow_type"]),
                      nullable=c["nullable"]) for c in columns])
    shards = []
    for split in ("train", "validation", "test"):
        path = directory / f"{split}.parquet"
        selected = [record for record in rows if record["split"] == split]
        pq.write_table(pa.Table.from_pylist(selected, schema=schema), path)
        shards.append({"path": path.name, "split": split, "rows": len(selected),
                       "size_bytes": path.stat().st_size, "sha256": sha256(path.read_bytes()).hexdigest()})
    cutoff = iso(BASE + timedelta(days=4))
    manifest = {"schema_version": "sea.training-dataset.v2",
                "row_contract": "sea.recommend-engagement.v1", "dataset_id": "recommend-cpu-fixture",
                "revision": 1, "parent_revision": None, "domain": "recommend", "data_kind": data_kind,
                "created_at": cutoff, "feature_contract_id": "engagement-features.v1",
                "columns": columns,
                "source": {"warehouse_run_id": "fixture-run", "project_revision": "fixture-r1",
                           "generation": "g1", "ingest_cutoff": cutoff,
                           "recipe_sha256": "d" * 64,
                           "batches": [{"batch_id": "b1", "sha256": "a" * 64}],
                           "watermarks": [{"source": "fixture", "partition": "0", "position": 10,
                                           "event_time": cutoff}],
                           "dim_revisions": [{"item_id": item, "content_revision": rev}
                                             for item, rev in sorted({(r["item_id"], r["content_revision"])
                                                                      for r in rows})],
                           "dbt_manifest_sha256": "b" * 64, "dbt_run_results_sha256": "c" * 64},
                "label": {"target": "effective_read", "rule_version": "synthetic-read-30m.v1",
                          "window_seconds": 1800, "window_anchor": "impression_time",
                          "maturity_watermark": cutoff, "source": label_source},
                "splits": [{"name": name, "start": iso(BASE + timedelta(days=day)),
                            "end": iso(BASE + timedelta(days=day + 1))}
                           for day, name in enumerate(("train", "validation", "test"))],
                "files": shards, "row_count": len(rows), "transform_refs": []}
    path = directory / "manifest.json"
    path.write_text(json.dumps(manifest))
    return path, sha256(path.read_bytes()).hexdigest()


def test_cpu_tower_pair_and_lr_train_deterministically_and_resume(tmp_path):
    source, digest = fixture(tmp_path / "source")
    first, resumed, repeated = (tmp_path / f"{name}.json" for name in ("first", "resumed", "repeated"))
    complete = train(source, expected_manifest_sha256=digest, checkpoint=first, epochs=4)
    partial = train(source, expected_manifest_sha256=digest, checkpoint=resumed, epochs=4, max_updates=8)
    assert partial["status"] == "training" and partial["stage"] == "two_tower"
    recovered = train(source, expected_manifest_sha256=digest, checkpoint=resumed, epochs=4, resume=True)
    again = train(source, expected_manifest_sha256=digest, checkpoint=repeated, epochs=4)
    assert complete == recovered == again
    assert first.read_bytes() == resumed.read_bytes() == repeated.read_bytes()
    assert complete["status"] == "candidate" and complete["activation"] == "none"
    assert complete["score_semantics"] == "uncalibrated_logit"
    assert complete["splits"]["train"]["positive"] == 3
    state = json.loads(first.read_text())["state"]
    assert state["updates"] == 4 * 6 * 2
    assert state["optimizer"]["two_tower"] != [0.0] * 8
    assert state["optimizer"]["ranker"] != [0.0] * 5
    assert state["models"]["two_tower"] != [0.1, -0.1, 0.05, 0.05, 0.1, 0.05, -0.1, 0.05]
    assert math.isfinite(tower_score(row(1, "train", 0, 1), state["models"]["two_tower"]))
    assert math.isfinite(ranker_logit(row(1, "train", 0, 1), state["models"]["two_tower"],
                                     state["models"]["ranker"]))


def test_checkpoint_resume_across_stage_boundary(tmp_path):
    source, digest = fixture(tmp_path / "source")
    checkpoint = tmp_path / "candidate.json"
    paused = train(source, expected_manifest_sha256=digest, checkpoint=checkpoint,
                   epochs=1, max_updates=7)
    assert paused["stage"] == "ranker" and paused["cursor"] == 1
    done = train(source, expected_manifest_sha256=digest, checkpoint=checkpoint,
                 epochs=1, resume=True)
    assert done["status"] == "candidate" and done["updates"] == 12


def test_tower_gradient_matches_finite_difference():
    datum = row(1, "train", 0, 1)
    state = _initial_state("a" * 64, "b" * 64,
                           {"seed": 7, "epochs": 1, "learning_rate": 0.05, "momentum": 0.8})
    analytic = _gradient(datum, "two_tower", state)
    weights = state["models"]["two_tower"]

    def loss():
        logit = tower_score(datum, weights)
        return max(0.0, logit) - datum["label"] * logit + math.log1p(math.exp(-abs(logit)))

    for index in range(len(weights)):
        original = weights[index]
        weights[index] = original + 1e-6
        plus = loss()
        weights[index] = original - 1e-6
        minus = loss()
        weights[index] = original
        assert analytic[index] == pytest.approx((plus - minus) / 2e-6, abs=1e-8)


def test_validation_and_test_labels_do_not_change_fitted_weights(tmp_path):
    rows = [row(i, "train", 0, i % 2) for i in range(1, 7)]
    rows += [row(7, "validation", 1, 1), row(8, "test", 2, 0)]
    source_a, hash_a = fixture(tmp_path / "a", rows=rows)
    changed = deepcopy(rows)
    for datum in changed:
        if datum["split"] != "train":
            datum["label"] = 1 - datum["label"]
            datum["label_state"] = "POSITIVE" if datum["label"] else "OBSERVED_NEGATIVE"
    source_b, hash_b = fixture(tmp_path / "b", rows=changed)
    checkpoint_a, checkpoint_b = tmp_path / "a.json", tmp_path / "b.json"
    report_a = train(source_a, expected_manifest_sha256=hash_a, checkpoint=checkpoint_a, epochs=2)
    report_b = train(source_b, expected_manifest_sha256=hash_b, checkpoint=checkpoint_b, epochs=2)
    assert json.loads(checkpoint_a.read_text())["state"]["models"] == json.loads(checkpoint_b.read_text())["state"]["models"]
    assert report_a["splits"]["test"] != report_b["splits"]["test"]


@pytest.mark.parametrize("source_kind,source_label", [
    ("synthetic", "teacher"), ("observed", "teacher"),
    ("observed", "human_judgment"), ("synthetic", "observed_behavior")])
def test_rejects_teacher_and_mismatched_provenance(tmp_path, source_kind, source_label):
    source, digest = fixture(tmp_path / "source", data_kind=source_kind, label_source=source_label)
    with pytest.raises(TrainingError, match="label source"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")


def test_accepts_structurally_observed_behavior_without_claiming_live_validation(tmp_path):
    source, digest = fixture(tmp_path / "source", data_kind="observed",
                             label_source="observed_behavior")
    report = train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json", epochs=1)
    assert report["status"] == "candidate" and report["sampling"] == "full_inclusion_probability_1"


def test_rejects_nonunit_sampling_before_training(tmp_path):
    rows = [row(i, "train", 0, i % 2) for i in range(1, 7)]
    rows += [row(7, "validation", 1, 1), row(8, "test", 2, 0)]
    rows[0]["sampling_probability"] = 0.5
    source, digest = fixture(tmp_path / "source", rows=rows)
    with pytest.raises(TrainingError, match="inclusion probability"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")


def test_rejects_missing_positive_and_missing_split(tmp_path):
    rows = [row(i, "train", 0, 0) for i in range(1, 7)]
    rows += [row(7, "validation", 1, 1), row(8, "test", 2, 0)]
    source, digest = fixture(tmp_path / "one", rows=rows)
    with pytest.raises(TrainingError, match="positive and observed-negative"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")
    rows = [row(i, "train", 0, i % 2) for i in range(1, 7)]
    rows += [row(7, "validation", 1, 1)]
    source, digest = fixture(tmp_path / "two", rows=rows)
    with pytest.raises(TrainingError, match="at least one mature row"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")


def test_reader_rejects_future_feature_and_cross_split_request(tmp_path):
    rows = [row(i, "train", 0, i % 2) for i in range(1, 7)]
    rows += [row(7, "validation", 1, 1), row(8, "test", 2, 0)]
    rows[0]["feature_available_at"] = rows[0]["impression_time"] + timedelta(seconds=1)
    source, digest = fixture(tmp_path / "future", rows=rows)
    with pytest.raises(DatasetError, match="future feature"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")
    rows = deepcopy(rows)
    rows[0]["feature_available_at"] = rows[0]["impression_time"] - timedelta(seconds=1)
    rows[-2]["request_id"] = rows[0]["request_id"]
    rows[-2]["subject_id"] = rows[0]["subject_id"]
    source, digest = fixture(tmp_path / "cross", rows=rows)
    with pytest.raises(DatasetError, match="request group crosses"):
        train(source, expected_manifest_sha256=digest, checkpoint=tmp_path / "out.json")


def test_corruption_shape_wrong_dataset_and_explicit_resume(tmp_path):
    source, digest = fixture(tmp_path / "source")
    checkpoint = tmp_path / "candidate.json"
    train(source, expected_manifest_sha256=digest, checkpoint=checkpoint, max_updates=1)
    with pytest.raises(TrainingError, match="already exists"):
        train(source, expected_manifest_sha256=digest, checkpoint=checkpoint)
    with pytest.raises(TrainingError, match="frozen input"):
        train(source, expected_manifest_sha256=digest, checkpoint=checkpoint,
              seed=9, resume=True)
    original = checkpoint.read_bytes()
    checkpoint.write_bytes(original[:-1] + b"0")
    with pytest.raises(TrainingError, match="invalid or damaged checkpoint"):
        train(source, expected_manifest_sha256=digest, checkpoint=checkpoint, resume=True)
    checkpoint.write_bytes(original)
    envelope = json.loads(original)
    envelope["state"]["models"]["ranker"].pop()
    envelope["sha256"] = sha256(json.dumps(envelope["state"], sort_keys=True,
                                         separators=(",", ":"), allow_nan=False).encode()).hexdigest()
    checkpoint.write_text(json.dumps(envelope))
    with pytest.raises(TrainingError, match="shape"):
        train(source, expected_manifest_sha256=digest, checkpoint=checkpoint, resume=True)
    checkpoint.write_bytes(original)
    with pytest.raises(DatasetError, match="manifest hash"):
        train(source, expected_manifest_sha256="0" * 64, checkpoint=checkpoint, resume=True)


def test_cli_emits_candidate_report_only(tmp_path):
    source, digest = fixture(tmp_path / "source")
    checkpoint = tmp_path / "candidate.json"
    result = subprocess.run([sys.executable, "-m", "recommend", str(source),
                             "--expected-sha256", digest,
                             "--checkpoint", str(checkpoint), "--epochs", "1"],
                            capture_output=True, text=True, check=True)
    report = json.loads(result.stdout)
    assert report["status"] == "candidate" and report["activation"] == "none"
    assert checkpoint.exists()
