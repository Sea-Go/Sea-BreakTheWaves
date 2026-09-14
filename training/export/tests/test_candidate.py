"""Frozen input, checkpoint, and independent serving score acceptance."""

from copy import deepcopy
from hashlib import sha256
import json
import math
import subprocess
import sys

import pytest

from recommend.tests.test_trainer import fixture
from recommend.trainer import train
from export.candidate import export_candidate
from export.contract import ExportError, canonical, digest
from export.serving import PREPROCESSING, load_candidate, score
from export.search import assess_search_candidate


CONFIG = {"seed": 7, "epochs": 2, "learning_rate": 0.05, "momentum": 0.8}


def prepared(tmp_path, *, partial=False):
    manifest, source_hash = fixture(tmp_path / "dataset")
    checkpoint = tmp_path / "checkpoint.json"
    train(manifest, expected_manifest_sha256=source_hash, checkpoint=checkpoint,
          **CONFIG, max_updates=2 if partial else None)
    preprocess = tmp_path / "identity.json"
    preprocess.write_bytes(canonical(PREPROCESSING))
    return manifest, source_hash, checkpoint, preprocess, tmp_path / "serving"


def export(source):
    manifest, source_hash, checkpoint, preprocess, destination = source
    return export_candidate(manifest, expected_manifest_sha256=source_hash,
                            checkpoint_path=checkpoint, expected_config=CONFIG,
                            preprocessing_path=preprocess, output_dir=destination)


def tamper_checkpoint(checkpoint, mutate):
    envelope = json.loads(checkpoint.read_text())
    mutate(envelope["state"])
    envelope["sha256"] = sha256(json.dumps(envelope["state"], sort_keys=True,
                                           separators=(",", ":"), allow_nan=False).encode()).hexdigest()
    checkpoint.write_bytes(canonical(envelope))


def test_completed_resume_exports_immutable_candidate_and_exact_scores(tmp_path):
    source = prepared(tmp_path, partial=True)
    manifest, source_hash, checkpoint, _, destination = source
    with pytest.raises(ExportError, match="not completed"):
        export(source)
    train(manifest, expected_manifest_sha256=source_hash, checkpoint=checkpoint,
          **CONFIG, resume=True)
    result = export(source)
    assert result["status"] == "candidate"
    assert result["activation"] == result["registration"] == "none"
    assert result["dataset_manifest_sha256"] == source_hash
    assert result["checkpoint_sha256"] == digest(checkpoint.read_bytes())
    assert result["environment"]["python"] == "3.12"
    assert result["pair_id"].startswith("sea.recommend.pair.")
    assert result["space_id"].startswith("sea.recommend.space.")
    assert set(destination.iterdir()) == {destination / name for name in
                                          (*result["files"], "manifest.json")}
    assert b"optimizer" not in (destination / "weights.json").read_bytes()
    assert b"random_state" not in (destination / "weights.json").read_bytes()
    loaded, weights, preprocessing = load_candidate(destination)
    assert loaded == result
    for case in json.loads((destination / "probes.json").read_text())["cases"]:
        assert score(weights, preprocessing, case["input"]) == case["serving_output"]
        for key in ("ranker_logit", "tower_dot"):
            assert math.isclose(case["training_output"][key], case["serving_output"][key],
                                rel_tol=0, abs_tol=1e-12)
    with pytest.raises(ExportError, match="already exists"):
        export(source)


def test_same_checkpoint_and_config_give_byte_identical_package(tmp_path):
    source = prepared(tmp_path)
    first = export(source)
    second_source = (*source[:-1], tmp_path / "serving-two")
    second = export(second_source)
    assert first == second
    for name in (*first["files"], "manifest.json"):
        assert (source[-1] / name).read_bytes() == (second_source[-1] / name).read_bytes()


@pytest.mark.parametrize("kind,mutation", [
    ("shape", lambda s: s["models"]["ranker"].pop()),
    ("nonfinite", lambda s: s["models"]["two_tower"].append(float("inf"))),
    ("input_hash", lambda s: s.update({"manifest_sha256": "a" * 64})),
])
def test_bad_checkpoint_shape_value_or_input_hash_is_rejected(tmp_path, kind, mutation):
    source = prepared(tmp_path)
    checkpoint = source[2]
    if kind == "nonfinite":
        # JSON NaN/Infinity never enters the packaged artifact.
        envelope = json.loads(checkpoint.read_text())
        mutation(envelope["state"])
        checkpoint.write_text(json.dumps(envelope))
    else:
        tamper_checkpoint(checkpoint, mutation)
    with pytest.raises(ExportError, match="invalid artifact JSON" if kind == "nonfinite" else
                       "verification failed"):
        export(source)
    assert not source[-1].exists()


def test_wrong_frozen_manifest_hash_or_training_config_is_rejected(tmp_path):
    source = prepared(tmp_path)
    with pytest.raises(ExportError, match="verification failed"):
        export_candidate(source[0], expected_manifest_sha256="a" * 64,
                         checkpoint_path=source[2], expected_config=CONFIG,
                         preprocessing_path=source[3], output_dir=source[4])
    wrong = dict(CONFIG, seed=8)
    with pytest.raises(ExportError, match="verification failed"):
        export_candidate(source[0], expected_manifest_sha256=source[1],
                         checkpoint_path=source[2], expected_config=wrong,
                         preprocessing_path=source[3], output_dir=source[4])


@pytest.mark.parametrize("change", [
    lambda p: p.pop("operation"),
    lambda p: p.update({"operation": "zscore"}),
    lambda p: p.update({"inputs": ["item_quality", "user_interest"]}),
])
def test_missing_or_wrong_preprocessing_is_rejected(tmp_path, change):
    source = prepared(tmp_path)
    preprocess = deepcopy(PREPROCESSING)
    change(preprocess)
    source[3].write_bytes(canonical(preprocess))
    with pytest.raises(ExportError, match="preprocessing"):
        export(source)
    assert not source[-1].exists()


def test_missing_preprocessing_file_is_rejected(tmp_path):
    source = prepared(tmp_path)
    source[3].unlink()
    with pytest.raises(ExportError, match="missing preprocessing"):
        export(source)


def test_artifact_hash_and_nonfinite_serving_input_fail_closed(tmp_path):
    source = prepared(tmp_path)
    export(source)
    _, weights, preprocessing = load_candidate(source[-1])
    with pytest.raises(ExportError, match="non-finite"):
        score(weights, preprocessing, {"user_interest": float("nan"), "item_quality": 0.1})
    with pytest.raises(ExportError, match="shape"):
        score(weights, preprocessing, {"user_interest": 0.1})
    (source[-1] / "weights.json").write_bytes(b"{}")
    with pytest.raises(ExportError, match="hash or size"):
        load_candidate(source[-1])


def test_pair_space_and_preprocessing_bind_to_weights(tmp_path):
    source = prepared(tmp_path)
    manifest = export(source)
    config_path = source[-1] / "model_config.json"
    config = json.loads(config_path.read_text())
    config["pair_id"] = "sea.recommend.pair.other"
    config_path.write_bytes(canonical(config))
    manifest["files"]["model_config.json"] = {"sha256": digest(config_path.read_bytes()),
                                                "size_bytes": config_path.stat().st_size}
    (source[-1] / "manifest.json").write_bytes(canonical(manifest))
    with pytest.raises(ExportError, match="configuration mismatch"):
        load_candidate(source[-1])


def test_probe_numbers_are_rechecked_even_if_metadata_is_rehashed(tmp_path):
    source = prepared(tmp_path)
    manifest = export(source)
    probe_path = source[-1] / "probes.json"
    probe = json.loads(probe_path.read_text())
    probe["cases"][0]["training_output"]["ranker_logit"] += 0.1
    probe_path.write_bytes(canonical(probe))
    manifest["files"]["probes.json"] = {"sha256": digest(probe_path.read_bytes()),
                                         "size_bytes": probe_path.stat().st_size}
    (source[-1] / "manifest.json").write_bytes(canonical(manifest))
    with pytest.raises(ExportError, match="probe score mismatch"):
        load_candidate(source[-1])


def test_experimental_search_candidate_has_explicit_unsupported_disposition(tmp_path):
    path = tmp_path / "search-candidate.json"
    path.write_bytes(canonical({"status": "candidate_only", "active": False,
                                "experimental": True, "feature_version": "lexical-pairwise-v0"}))
    result = assess_search_candidate(path)
    assert result["status"] == "NOT_SUPPORTED"
    assert result["serving_export"] is False and result["dc_registration"] is False
    path.write_bytes(canonical({"status": "candidate_only", "active": True,
                                "experimental": True, "feature_version": "lexical-pairwise-v0"}))
    with pytest.raises(ExportError):
        assess_search_candidate(path)


def test_declared_but_unconsumed_dataset_transform_is_rejected(tmp_path):
    source = prepared(tmp_path)
    manifest_path, _, checkpoint, preprocess, destination = source
    transform = manifest_path.parent / "transform.json"
    transform.write_bytes(canonical({"kind": "identity"}))
    manifest = json.loads(manifest_path.read_text())
    manifest["transform_refs"] = [{"name": "training-transform", "path": transform.name,
                                   "sha256": digest(transform.read_bytes()), "fit_split": "train"}]
    manifest_path.write_bytes(canonical(manifest))
    new_hash = digest(manifest_path.read_bytes())
    checkpoint.unlink()
    train(manifest_path, expected_manifest_sha256=new_hash, checkpoint=checkpoint, **CONFIG)
    with pytest.raises(ExportError, match="does not consume referenced transformations"):
        export_candidate(manifest_path, expected_manifest_sha256=new_hash,
                         checkpoint_path=checkpoint, expected_config=CONFIG,
                         preprocessing_path=preprocess, output_dir=destination)
    assert not destination.exists()


def test_cli_writes_candidate_only_and_machine_readable_result(tmp_path):
    source = prepared(tmp_path)
    options = tmp_path / "training-config.json"
    options.write_bytes(canonical(CONFIG))
    completed = subprocess.run([sys.executable, "-m", "export", str(source[0]),
                                "--expected-manifest-sha256", source[1],
                                "--checkpoint", str(source[2]),
                                "--expected-config", str(options),
                                "--preprocessing", str(source[3]),
                                "--output-dir", str(source[4])],
                               capture_output=True, text=True, check=True)
    assert json.loads(completed.stdout)["status"] == "candidate"
    records = [json.loads(line) for line in completed.stderr.splitlines()]
    assert [record["event"] for record in records] == ["training.export.started",
                                                      "training.export.completed"]
    assert all(record["service"] == "sea-breakthewaves-training" for record in records)
    load_candidate(source[4])


def test_cli_rejection_emits_structured_failure_without_package(tmp_path):
    source = prepared(tmp_path, partial=True)
    options = tmp_path / "training-config.json"
    options.write_bytes(canonical(CONFIG))
    completed = subprocess.run([sys.executable, "-m", "export", str(source[0]),
                                "--expected-manifest-sha256", source[1],
                                "--checkpoint", str(source[2]),
                                "--expected-config", str(options),
                                "--preprocessing", str(source[3]),
                                "--output-dir", str(source[4])],
                               capture_output=True, text=True)
    assert completed.returncode == 2 and completed.stdout == ""
    records = [json.loads(line) for line in completed.stderr.splitlines()]
    assert records[-1]["event"] == "training.export.rejected"
    assert records[-1]["status"] == "rejected"
    assert not source[-1].exists()
