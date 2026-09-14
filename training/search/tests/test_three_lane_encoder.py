from __future__ import annotations

from copy import deepcopy
from hashlib import sha256
import json
import math
import os
from pathlib import Path

import pytest

from three_lane_encoder import BGE_ROOT, EncodingError, encode, file_digest, freeze, validate_representations


def small_profiles() -> dict:
    return {"dense": {"representation_contract": {"dimensions": 4}},
            "sparse": {"representation_contract": {"dimensions": 100, "max_nonzero": 4}},
            "token_matrix": {"representation_contract": {"dimensions": 4, "max_tokens": 3}}}


def valid_small_representation() -> dict:
    # Only a validator fixture. Released artifacts always come from Provider.
    return {"dense": {"values": [1.0, 0.0, 0.0, 0.0]},
            "sparse": {"indices": [3, 10], "weights": [0.25, 0.5]},
            "token_matrix": {"shape": [1, 4], "values": [[0.0, 1.0, 0.0, 0.0]], "mask": [True]}}


@pytest.mark.parametrize("change", [
    lambda r: r["dense"]["values"].pop(),
    lambda r: r["dense"]["values"].__setitem__(0, math.nan),
    lambda r: r["dense"]["values"].__setitem__(0, 0.0),
    lambda r: r["sparse"]["indices"].__setitem__(1, 3),
    lambda r: r["sparse"]["weights"].__setitem__(0, math.inf),
    lambda r: r["sparse"]["weights"].__setitem__(0, 0.0),
    lambda r: r["token_matrix"]["shape"].__setitem__(0, 2),
    lambda r: r["token_matrix"]["mask"].__setitem__(0, False),
    lambda r: r["token_matrix"]["values"][0].__setitem__(1, 0.0),
])
def test_wrong_head_shape_finiteness_or_mask_rejected(change) -> None:
    value = deepcopy(valid_small_representation())
    change(value)
    with pytest.raises(EncodingError):
        validate_representations(value, small_profiles())


def test_immutable_content_addressed_file_rejects_conflict(tmp_path: Path) -> None:
    source = b'{"status":"candidate_default_off"}'
    target = freeze(tmp_path, "shards/queries/one.jsonl", source)
    assert target.read_bytes() == source
    freeze(tmp_path, "shards/queries/one.jsonl", source)
    with pytest.raises(EncodingError, match="immutable representation object conflicts"):
        freeze(tmp_path, "shards/queries/one.jsonl", b"changed")
    assert target.read_bytes() == source


def real_inputs() -> tuple[str, str, Path, Path] | None:
    fields = ("SEA_BGE_QREL_MANIFEST", "SEA_BGE_QREL_MANIFEST_SHA256",
              "SEA_BGE_MODEL_DIRECTORY", "SEA_BGE_PYTHON")
    if not all(os.environ.get(field) for field in fields):
        return None
    return (os.environ[fields[0]], os.environ[fields[1]],
            Path(os.environ[fields[2]]), Path(os.environ[fields[3]]))


def test_actual_locked_bge_three_heads_and_numeric_fingerprint(tmp_path: Path) -> None:
    inputs = real_inputs()
    if inputs is None:
        pytest.skip("opt in with locked local BGE cache and frozen H10.b revision 2")
    qrel, expected, model, python = inputs
    report = encode(qrel, expected, model, python, tmp_path / "representation")
    manifest_path = Path(report["manifest_path"])
    raw = manifest_path.read_bytes()
    manifest = json.loads(raw)
    assert report["status"] == manifest["status"] == "candidate_default_off"
    assert sha256(raw).hexdigest() == manifest_path.stem == report["manifest_sha256"]
    assert report["trained_weights"] is False and report["model_quality"] is None
    assert manifest["model_revision"] == "5617a9f61b028005a4858fdac845db406aefb181"
    assert manifest["model_lock_sha256"] == file_digest(BGE_ROOT / "model.lock.json")
    assert manifest["profiles_sha256"] == file_digest(BGE_ROOT / "profiles.json")
    assert manifest["dataset_manifest_sha256"] == expected and manifest["dataset_revision"] == 2
    assert manifest["query_count"] == manifest["chunk_count"] == 6
    assert [s["kind"] for s in manifest["shards"]] == ["query", "chunk"]
    for shard in manifest["shards"]:
        body = manifest_path.parents[1].joinpath(shard["path"]).read_bytes()
        assert body.endswith(b"\n") and len(body) == shard["size_bytes"]
        assert sha256(body).hexdigest() == shard["sha256"]
        rows = [json.loads(line) for line in body.splitlines()]
        assert len(rows) == shard["rows"] == 6
        for row in rows:
            assert sha256(row["text"].encode()).hexdigest() == row["text_sha256"]
            validate_representations(row["representations"], manifest["lanes"])
    first_query = json.loads(manifest_path.parents[1].joinpath(manifest["shards"][0]["path"]).read_bytes().splitlines()[0])
    assert first_query["query_id"] == "q-test-coffee"
    dense = first_query["representations"]["dense"]["values"]
    sparse = first_query["representations"]["sparse"]
    token = first_query["representations"]["token_matrix"]
    assert dense[:4] == pytest.approx([-0.02440527268, -0.01478208695, -0.07838745415, -0.03442910314], abs=1e-5)
    assert sparse["indices"] == [66, 186, 17997, 79497]
    assert sparse["weights"][:2] == pytest.approx([0.11247785389, 0.13680353761], abs=1e-5)
    assert token["shape"] == [5, 1024] and token["mask"] == [True] * 5
    assert token["values"][0][:2] == pytest.approx([-0.00643265108, 0.02525801770], abs=1e-5)


def test_no_source_sha_or_missing_cached_model_never_starts_worker(tmp_path: Path) -> None:
    inputs = real_inputs()
    if inputs is None:
        pytest.skip("opt in with frozen H10.b manifest")
    qrel, expected, _, python = inputs
    with pytest.raises(ValueError, match="manifest hash mismatch"):
        encode(qrel, "0" * 64, tmp_path / "missing", python, tmp_path / "bad-source")
    with pytest.raises(EncodingError, match="cached locked BGE model files missing"):
        encode(qrel, expected, tmp_path / "missing", python, tmp_path / "no-model")
    assert not (tmp_path / "no-model").exists()
