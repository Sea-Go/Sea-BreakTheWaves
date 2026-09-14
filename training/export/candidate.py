"""Freeze a completed recommendation checkpoint as an immutable candidate."""

from __future__ import annotations

import math
import os
from pathlib import Path
import shutil
import sys
import tempfile

from recommend.trainer import (PARAMETERS, RECIPE, TrainingError, _read_checkpoint,
                               _row_fingerprint, _rows, _validate_input, ranker_logit,
                               tower_score)
from sea_training.dataset import DatasetError, open_dataset

from .contract import ExportError, canonical, digest, read_json
from .serving import FEATURES, PREPROCESSING, RANKER_FEATURES, identities, load_candidate, score


PROBES = [{"user_interest": 0.0, "item_quality": 0.0},
          {"user_interest": 0.2, "item_quality": 0.1},
          {"user_interest": 1.0, "item_quality": 0.5}]


def _check_config(config: dict) -> None:
    if (not isinstance(config, dict) or set(config) != {"seed", "epochs", "learning_rate", "momentum"} or
            type(config["seed"]) is not int or config["seed"] < 0 or
            type(config["epochs"]) is not int or config["epochs"] < 1 or
            any(type(config[key]) not in (int, float) or not math.isfinite(config[key])
                for key in ("learning_rate", "momentum")) or
            not 0 < config["learning_rate"] <= 1 or not 0 <= config["momentum"] < 1):
        raise ExportError("invalid expected training configuration")


def _read_preprocessing(path: str | Path) -> dict:
    try:
        value = read_json(Path(path).read_bytes())
    except OSError as exc:
        raise ExportError("missing preprocessing declaration") from exc
    if value != PREPROCESSING:
        raise ExportError("preprocessing is not the trainer's exact identity transform")
    return value


def _check_score(reference: dict, actual: dict) -> None:
    for key in ("user_embedding", "item_embedding"):
        if any(not math.isclose(a, b, rel_tol=0, abs_tol=1e-12)
               for a, b in zip(reference[key], actual[key])):
            raise ExportError("training and serving embedding mismatch")
    for key in ("tower_dot", "ranker_logit"):
        if not math.isclose(reference[key], actual[key], rel_tol=0, abs_tol=1e-12):
            raise ExportError("training and serving score mismatch")


def export_candidate(manifest_path: str | Path, *, expected_manifest_sha256: str,
                     checkpoint_path: str | Path, expected_config: dict,
                     preprocessing_path: str | Path, output_dir: str | Path) -> dict:
    """Reverify frozen input and candidate, then write a never-active serving package."""
    _check_config(expected_config)
    preprocessing = _read_preprocessing(preprocessing_path)
    if (not isinstance(expected_manifest_sha256, str) or len(expected_manifest_sha256) != 64 or
            any(c not in "0123456789abcdef" for c in expected_manifest_sha256)):
        raise ExportError("frozen input hash is required")
    if sys.version_info[:2] != (3, 12):
        raise ExportError("candidate export requires Python 3.12")
    try:
        with open_dataset(manifest_path, expected_manifest_sha256=expected_manifest_sha256) as snapshot:
            if snapshot.manifest["transform_refs"]:
                raise ExportError("trainer does not consume referenced transformations")
            if snapshot.manifest["feature_contract_id"] != "engagement-features.v1":
                raise ExportError("unsupported feature contract")
            splits = {name: _rows(snapshot, name) for name in ("train", "validation", "test")}
            _validate_input(snapshot, splits)
            row_hash = _row_fingerprint(splits["train"])
            checkpoint_bytes = Path(checkpoint_path).read_bytes()
            read_json(checkpoint_bytes)
            state = _read_checkpoint(Path(checkpoint_path), snapshot.manifest_sha256,
                                     row_hash, expected_config, len(splits["train"]))
            if Path(checkpoint_path).read_bytes() != checkpoint_bytes:
                raise ExportError("checkpoint changed during export")
            if state["status"] != "candidate" or state["stage"] != "done":
                raise ExportError("training checkpoint has not completed as candidate")
            weights = state["models"]
            if set(weights) != set(PARAMETERS):
                raise ExportError("candidate model keys mismatch")
            weights = {name: weights[name] for name in ("two_tower", "ranker")}
            pre_bytes = canonical(preprocessing)
            pair_id, space_id = identities(RECIPE, weights, preprocessing)
            config = {"recipe": RECIPE, "feature_contract_id": "engagement-features.v1",
                      "feature_order": FEATURES, "ranker_feature_order": RANKER_FEATURES,
                      "preprocessing_sha256": digest(pre_bytes),
                      "embedding_dimension": 2, "metric": "dot",
                      "score_semantics": "uncalibrated_logit", "calibration": "none",
                      "pair_id": pair_id, "space_id": space_id}
            probes = []
            for raw in PROBES:
                row = dict(raw)
                trainer = {"tower_dot": tower_score(row, weights["two_tower"]),
                           "ranker_logit": ranker_logit(row, weights["two_tower"], weights["ranker"])}
                tw = weights["two_tower"]
                trainer["user_embedding"] = [tw[0] * raw["user_interest"] + tw[1],
                                             tw[2] * raw["user_interest"] + tw[3]]
                trainer["item_embedding"] = [tw[4] * raw["item_quality"] + tw[5],
                                             tw[6] * raw["item_quality"] + tw[7]]
                actual = score(weights, preprocessing, raw)
                _check_score(trainer, actual)
                probes.append({"input": raw, "training_output": trainer,
                               "serving_output": actual})
            files = {"weights.json": canonical(weights),
                     "preprocessing.json": pre_bytes,
                     "model_config.json": canonical(config),
                     "probes.json": canonical({"cases": probes, "tolerance_absolute": 1e-12})}
            serving_manifest = {
                "format": "sea.recommend.serving-candidate.v1", "status": "candidate",
                "activation": "none", "registration": "none", "recipe": RECIPE,
                "pair_id": pair_id, "space_id": space_id,
                "dataset_manifest_sha256": snapshot.manifest_sha256,
                "dataset_id": snapshot.manifest["dataset_id"],
                "dataset_revision": snapshot.manifest["revision"],
                "source_data_kind": snapshot.manifest["data_kind"],
                "environment": {"python": "3.12", "runtime": "cpu-json",
                                "uv_lock_sha256": digest((Path(__file__).resolve().parents[1] /
                                                          "uv.lock").read_bytes())},
                "train_rows_sha256": row_hash,
                "checkpoint_sha256": digest(checkpoint_bytes),
                "training_config": expected_config,
                "files": {name: {"sha256": digest(data), "size_bytes": len(data)}
                          for name, data in files.items()},
            }
    except (TrainingError, DatasetError, OSError, KeyError, TypeError, ValueError) as exc:
        if isinstance(exc, ExportError):
            raise
        raise ExportError("checkpoint or frozen input verification failed") from exc

    destination = Path(output_dir)
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists():
        raise ExportError("immutable candidate destination already exists")
    temp = Path(tempfile.mkdtemp(prefix=f".{destination.name}.", dir=destination.parent))
    try:
        for name, data in files.items():
            (temp / name).write_bytes(data)
        (temp / "manifest.json").write_bytes(canonical(serving_manifest))
        loaded, loaded_weights, loaded_preprocessing = load_candidate(temp)
        if loaded != serving_manifest:
            raise ExportError("candidate package validation mismatch")
        for probe in probes:
            _check_score(probe["training_output"], score(loaded_weights,
                                                          loaded_preprocessing, probe["input"]))
        try:
            destination.mkdir()
        except FileExistsError as exc:
            raise ExportError("immutable candidate destination already exists") from exc
        complete = False
        try:
            for name in files:
                os.link(temp / name, destination / name)
            # The manifest is the last file: a reader cannot accept a partial directory.
            os.link(temp / "manifest.json", destination / "manifest.json")
            complete = True
        finally:
            if not complete:
                shutil.rmtree(destination)
    finally:
        shutil.rmtree(temp)
    return serving_manifest
