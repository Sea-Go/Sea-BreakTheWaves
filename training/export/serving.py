"""Small independent serving reader; never imports the training scorer."""

from __future__ import annotations

import math
from pathlib import Path

from .contract import ExportError, canonical, digest, read_json


FEATURES = ["user_interest", "item_quality"]
RANKER_FEATURES = ["bias", "user_interest", "item_quality", "interaction", "tower_dot"]
PREPROCESSING = {"version": "sea.recommend.identity-f64.v1", "inputs": FEATURES,
                 "operation": "identity", "missing": "reject", "non_finite": "reject"}


def _vector(value: object, length: int) -> list[float]:
    if not isinstance(value, list) or len(value) != length or any(
            type(number) not in (float, int) or not math.isfinite(number) for number in value):
        raise ExportError("serving weight shape or finite value mismatch")
    return value


def score(weights: dict, preprocessing: dict, raw: dict) -> dict:
    if preprocessing != PREPROCESSING:
        raise ExportError("preprocessing contract mismatch")
    if not isinstance(raw, dict) or set(raw) != set(FEATURES):
        raise ExportError("serving input feature order/shape mismatch")
    u, i = (raw[name] for name in FEATURES)
    if any(type(v) not in (float, int) or not math.isfinite(v) for v in (u, i)):
        raise ExportError("non-finite or missing serving input")
    if set(weights) != {"two_tower", "ranker"}:
        raise ExportError("serving weight keys mismatch")
    tw = _vector(weights["two_tower"], 8)
    rw = _vector(weights["ranker"], 5)
    user = [tw[0] * u + tw[1], tw[2] * u + tw[3]]
    item = [tw[4] * i + tw[5], tw[6] * i + tw[7]]
    dot = user[0] * item[0] + user[1] * item[1]
    logit = sum(a * b for a, b in zip([1.0, u, i, u * i, dot], rw))
    if not all(math.isfinite(v) for v in (*user, *item, dot, logit)):
        raise ExportError("non-finite serving output")
    return {"user_embedding": user, "item_embedding": item,
            "tower_dot": dot, "ranker_logit": logit}


def identities(recipe: str, weights: dict, preprocessing: dict) -> tuple[str, str]:
    if preprocessing != PREPROCESSING or set(weights) != {"two_tower", "ranker"}:
        raise ExportError("cannot derive pair and space from incompatible weights")
    towers = _vector(weights["two_tower"], 8)
    _vector(weights["ranker"], 5)
    basis = {"recipe": recipe, "two_tower": towers,
             "preprocessing_sha256": digest(canonical(preprocessing)),
             "dimension": 2, "metric": "dot"}
    space_id = "sea.recommend.space." + digest(canonical(basis))
    pair_id = "sea.recommend.pair." + digest(canonical({"space_id": space_id,
                                                       "two_tower": towers}))
    return pair_id, space_id


def load_candidate(directory: str | Path) -> tuple[dict, dict, dict]:
    root = Path(directory)
    try:
        manifest = read_json((root / "manifest.json").read_bytes())
        if (set(manifest) != {"format", "status", "activation", "registration", "recipe",
                             "pair_id", "space_id", "dataset_manifest_sha256", "dataset_id",
                             "dataset_revision", "source_data_kind", "environment", "train_rows_sha256",
                             "checkpoint_sha256", "training_config", "files"} or
                manifest["format"] != "sea.recommend.serving-candidate.v1" or
                manifest["recipe"] != "sea.recommend.cpu-towers-lr.v1" or
                manifest["status"] != "candidate" or manifest["activation"] != "none" or
                manifest["registration"] != "none" or
                any(not isinstance(manifest[key], str) or len(manifest[key]) != 64 or
                    any(c not in "0123456789abcdef" for c in manifest[key])
                    for key in ("dataset_manifest_sha256", "train_rows_sha256", "checkpoint_sha256"))):
            raise ExportError("not a candidate-only serving artifact")
        environment = manifest["environment"]
        if (not isinstance(environment, dict) or set(environment) != {"python", "runtime", "uv_lock_sha256"} or
                environment["python"] != "3.12" or environment["runtime"] != "cpu-json" or
                not isinstance(environment["uv_lock_sha256"], str) or
                len(environment["uv_lock_sha256"]) != 64 or
                any(c not in "0123456789abcdef" for c in environment["uv_lock_sha256"])):
            raise ExportError("candidate export environment mismatch")
        files = manifest["files"]
        if set(files) != {"weights.json", "preprocessing.json", "model_config.json", "probes.json"}:
            raise ExportError("serving file set mismatch")
        payloads = {}
        for name in files:
            data = (root / name).read_bytes()
            if digest(data) != files[name]["sha256"] or len(data) != files[name]["size_bytes"]:
                raise ExportError("serving artifact hash or size mismatch")
            payloads[name] = read_json(data)
    except (OSError, KeyError, TypeError) as exc:
        raise ExportError("incomplete serving artifact") from exc
    weights, preprocessing = payloads["weights.json"], payloads["preprocessing.json"]
    config = payloads["model_config.json"]
    if preprocessing != PREPROCESSING:
        raise ExportError("preprocessing contract mismatch")
    pair_id, space_id = identities(manifest["recipe"], weights, preprocessing)
    if (set(config) != {"recipe", "feature_contract_id", "feature_order",
                        "ranker_feature_order", "preprocessing_sha256", "embedding_dimension",
                        "metric", "score_semantics", "calibration", "pair_id", "space_id"} or
            config["recipe"] != "sea.recommend.cpu-towers-lr.v1" or
            config["feature_contract_id"] != "engagement-features.v1" or
            config["preprocessing_sha256"] != digest(canonical(preprocessing)) or
            config["calibration"] != "none" or
            config["feature_order"] != FEATURES or config["ranker_feature_order"] != RANKER_FEATURES or
            config["embedding_dimension"] != 2 or config["metric"] != "dot" or
            config["score_semantics"] != "uncalibrated_logit" or
            config["pair_id"] != pair_id or config["space_id"] != space_id or
            manifest["pair_id"] != pair_id or manifest["space_id"] != space_id):
        raise ExportError("serving model configuration mismatch")
    score(weights, preprocessing, {"user_interest": 0.0, "item_quality": 0.0})
    probes = payloads["probes.json"]
    if (set(probes) != {"cases", "tolerance_absolute"} or
            probes["tolerance_absolute"] != 1e-12 or
            not isinstance(probes["cases"], list) or len(probes["cases"]) != 3):
        raise ExportError("serving probe contract mismatch")
    for case in probes["cases"]:
        if not isinstance(case, dict) or set(case) != {"input", "training_output", "serving_output"}:
            raise ExportError("serving probe shape mismatch")
        actual = score(weights, preprocessing, case["input"])
        for expected in (case["training_output"], case["serving_output"]):
            if not isinstance(expected, dict) or set(expected) != set(actual):
                raise ExportError("serving probe output shape mismatch")
            for key, value in actual.items():
                candidates = expected[key] if isinstance(value, list) else [expected[key]]
                reference = value if isinstance(value, list) else [value]
                if (not isinstance(candidates, list) or len(candidates) != len(reference) or
                        any(type(a) not in (int, float) or not math.isfinite(a) or
                            not math.isclose(a, b, rel_tol=0, abs_tol=1e-12)
                            for a, b in zip(candidates, reference))):
                    raise ExportError("serving probe score mismatch")
    return manifest, weights, preprocessing
