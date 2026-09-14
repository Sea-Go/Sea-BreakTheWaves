from copy import deepcopy
from hashlib import sha256
import json
from pathlib import Path

import pytest

from search_training.dataset import DatasetError, open_snapshot
from search_training.trainer import TrainConfig, TrainError, train


def _pair(pair_id, query_id, query, positive, negative, *, split):
    return {
        "pair_id": pair_id, "query_id": query_id, "query_family_id": query_id, "query_text": query,
        "judgment_set_id": f"fixture-{split}-qrels-v1", "label_source": "synthetic_fixture",
        "negative_source": "explicit_judged_qrel", "pair_mask": True,
        "sample_weight": 1.0,
        "positive": {"chunk_key": f"article:{pair_id}:positive:r1:c1", "text": positive,
                     "grade": 2, "judgment_id": f"{pair_id}-positive", "judged_mask": True},
        "negative": {"chunk_key": f"article:{pair_id}:negative:r1:c1", "text": negative,
                     "grade": 0, "judgment_id": f"{pair_id}-negative", "judged_mask": True},
    }


def _rows():
    return {
        "train": [
            _pair("p1", "q1", "blue whale", "blue whale swims in deep sea", "brown cat sleeps indoors", split="train"),
            _pair("p2", "q2", "green sea", "green sea plants grow", "dry red sand plain", split="train"),
            _pair("p3", "q3", "quiet ocean", "quiet ocean at dusk", "loud mountain road", split="train"),
        ],
        "validation": [_pair("p4", "q4", "holdoutonly otter", "holdoutonly otter swims", "golden stone rests", split="validation")],
        "test": [_pair("p5", "q5", "cold water", "cold water current", "warm dry air", split="test")],
    }


def _fixture(directory: Path, rows=None, manifest_mutation=None):
    directory.mkdir(parents=True, exist_ok=True)
    rows = rows or _rows()
    files = []
    for split in ("train", "validation", "test"):
        path = f"{split}.jsonl"
        data = b"".join(json.dumps(row, ensure_ascii=False, sort_keys=True).encode() + b"\n" for row in rows[split])
        (directory / path).write_bytes(data)
        files.append({"split": split, "path": path, "sha256": sha256(data).hexdigest(),
                      "size_bytes": len(data), "rows": len(rows[split])})
    manifest = {"contract": "sea.search.experimental-qrel-pairs.v0", "experimental": True,
                "data_kind": "synthetic_fixture", "dataset_id": "fixture-search-001", "revision": 1,
                "files": files}
    if manifest_mutation:
        manifest_mutation(manifest)
    payload = json.dumps(manifest, sort_keys=True).encode()
    path = directory / "manifest.json"
    path.write_bytes(payload)
    return path, sha256(payload).hexdigest()


def test_train_resume_exact_checkpoint_and_candidate(tmp_path):
    manifest, digest = _fixture(tmp_path / "input")
    snapshot = open_snapshot(manifest, expected_manifest_sha256=digest)
    config = TrainConfig(epochs=8, seed=31)
    full_checkpoint, resumed_checkpoint = tmp_path / "full.json", tmp_path / "resumed.json"
    full_candidate, resumed_candidate = tmp_path / "full-model.json", tmp_path / "resumed-model.json"
    full_report = train(snapshot, config, full_checkpoint, candidate_path=full_candidate)
    stopped = train(snapshot, config, resumed_checkpoint, max_steps=4, candidate_path=resumed_candidate)
    assert stopped["status"] == "INTERRUPTED" and stopped["cursor"] == 1
    assert not resumed_candidate.exists()
    resumed_report = train(snapshot, config, resumed_checkpoint, resume=True, candidate_path=resumed_candidate)
    assert full_checkpoint.read_bytes() == resumed_checkpoint.read_bytes()
    assert full_candidate.read_bytes() == resumed_candidate.read_bytes()
    assert full_report == resumed_report
    assert full_report["status"] == "COMPLETED_FIXTURE"
    assert full_report["train_final"]["loss"] < full_report["train_initial"]["loss"]
    assert full_report["validation"]["pairs"] == 1 and full_report["test"]["pairs"] == 1
    assert full_report["step"] == 24 and full_report["epoch"] == 8
    assert full_report["eligible_pairs"] == {"train": 3, "validation": 1, "test": 1}
    checkpoint = json.loads(full_checkpoint.read_bytes())["payload"]
    assert "holdoutonly" not in checkpoint["idf"]  # validation is never used to fit the vocabulary
    assert len(checkpoint["loss_trace"]) == 24
    candidate = json.loads(full_candidate.read_bytes())
    assert candidate["status"] == "candidate_only" and candidate["active"] is False
    assert candidate["manifest_sha256"] == digest


@pytest.mark.parametrize("change,error", [
    (lambda rows: rows["train"][0]["negative"].update(judged_mask=False), "unjudged"),
    (lambda rows: rows["train"][0]["negative"].update(grade=1), "grade 0"),
    (lambda rows: rows["train"][0].update(pair_mask=False), "mask"),
    (lambda rows: rows["validation"][0].update(query_id="q1"), "crosses splits"),
    (lambda rows: rows["validation"][0].update(query_family_id="q1"), "rewrite family crosses splits"),
    (lambda rows: rows["train"][0].update(negative_source="unjudged"), "negative provenance"),
    (lambda rows: rows["train"][0]["negative"].update(chunk_key=rows["train"][0]["positive"]["chunk_key"]), "same chunk"),
])
def test_ineligible_or_leaking_qrels_rejected(tmp_path, change, error):
    rows = deepcopy(_rows())
    change(rows)
    manifest, digest = _fixture(tmp_path, rows)
    with pytest.raises(DatasetError, match=error):
        open_snapshot(manifest, expected_manifest_sha256=digest)


def test_corrupt_missing_or_unpinned_input_rejected(tmp_path):
    manifest, digest = _fixture(tmp_path)
    with pytest.raises(DatasetError, match="manifest hash mismatch"):
        open_snapshot(manifest, expected_manifest_sha256="0" * 64)
    (tmp_path / "train.jsonl").write_text("tampered\n")
    with pytest.raises(DatasetError, match="artifact size/hash mismatch"):
        open_snapshot(manifest, expected_manifest_sha256=digest)
    (tmp_path / "train.jsonl").unlink()
    with pytest.raises(DatasetError, match="cannot read declared artifact"):
        open_snapshot(manifest, expected_manifest_sha256=digest)


def test_non_authoritative_or_wrong_split_manifest_rejected(tmp_path):
    path, digest = _fixture(tmp_path / "observed", manifest_mutation=lambda m: m.update(data_kind="observed"))
    with pytest.raises(DatasetError, match="experimental synthetic"):
        open_snapshot(path, expected_manifest_sha256=digest)
    path, digest = _fixture(tmp_path / "split", manifest_mutation=lambda m: m["files"][1].update(split="train"))
    with pytest.raises(DatasetError, match="train/validation/test"):
        open_snapshot(path, expected_manifest_sha256=digest)


def test_resume_rejects_corruption_config_and_new_input(tmp_path):
    manifest, digest = _fixture(tmp_path / "first")
    snapshot = open_snapshot(manifest, expected_manifest_sha256=digest)
    checkpoint = tmp_path / "checkpoint.json"
    train(snapshot, TrainConfig(), checkpoint, max_steps=1)
    with pytest.raises(TrainError, match="input/config/feature mismatch"):
        train(snapshot, TrainConfig(seed=8), checkpoint, resume=True)
    second, second_hash = _fixture(tmp_path / "second", manifest_mutation=lambda m: m.update(revision=2))
    with pytest.raises(TrainError, match="input/config/feature mismatch"):
        train(open_snapshot(second, expected_manifest_sha256=second_hash), TrainConfig(), checkpoint, resume=True)
    checkpoint.write_bytes(checkpoint.read_bytes().replace(b"fixture-search-001", b"fixture-search-002"))
    with pytest.raises(TrainError, match="corrupt or unavailable checkpoint"):
        train(snapshot, TrainConfig(), checkpoint, resume=True)
