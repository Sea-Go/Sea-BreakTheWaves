"""Small deterministic two-tower and LR candidate training, never a release path."""

from __future__ import annotations

from hashlib import sha256
import json
import math
import os
from pathlib import Path
import random
import tempfile

from sea_training.dataset import open_dataset


class TrainingError(ValueError):
    """A dataset or checkpoint cannot be used by this recipe."""


RECIPE = "sea.recommend.cpu-towers-lr.v1"
STAGES = ("two_tower", "ranker")
PARAMETERS = {"two_tower": 8, "ranker": 5}


def _canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def _digest(value: object) -> str:
    return sha256(_canonical(value)).hexdigest()


def _tuples(value: object) -> object:
    if isinstance(value, list):
        return tuple(_tuples(item) for item in value)
    return value


def _rows(snapshot, split: str) -> list[dict]:
    return [row for batch in snapshot.iter_batches(split, batch_size=512)
            for row in batch.to_pylist()]


def _row_fingerprint(rows: list[dict]) -> str:
    # The manifest already fixes every file hash; this also fixes the logical reader order.
    return _digest([(r["sample_id"], r["label"], r["sampling_probability"])
                    for r in rows])


def _validate_input(snapshot, splits: dict[str, list[dict]]) -> None:
    manifest = snapshot.manifest
    source = manifest["label"]["source"]
    kind = manifest["data_kind"]
    if (kind, source) not in {("synthetic", "synthetic"),
                               ("observed", "observed_behavior")}:
        raise TrainingError("teacher, human judgment, or mismatched label source is not engagement behavior")
    if manifest["feature_contract_id"] != "engagement-features.v1":
        raise TrainingError("unsupported two-tower and ranker feature contract")
    if not all(name in splits for name in ("train", "validation", "test")):
        raise TrainingError("train/validation/test temporal splits are required")
    if any(len(rows) == 0 for rows in splits.values()):
        raise TrainingError("each temporal split needs at least one mature row")
    train_labels = {row["label"] for row in splits["train"]}
    if train_labels != {0, 1}:
        raise TrainingError("training requires positive and observed-negative examples")
    for rows in splits.values():
        for row in rows:
            if row["sampling_probability"] != 1.0:
                raise TrainingError("this baseline requires full inclusion probability 1.0")
            if row["label_state"] not in ("POSITIVE", "OBSERVED_NEGATIVE"):
                raise TrainingError("unmatured label rejected")


def _initial_state(manifest_sha256: str, row_hash: str, config: dict) -> dict:
    rng = random.Random(config["seed"])
    return {
        "format": RECIPE, "manifest_sha256": manifest_sha256,
        "train_rows_sha256": row_hash, "config": config,
        "status": "training", "stage": "two_tower", "epoch": 0,
        "cursor": 0, "order": [], "updates": 0,
        "random_state": rng.getstate(),
        "models": {"two_tower": [0.1, -0.1, 0.05, 0.05, 0.1, 0.05, -0.1, 0.05],
                   "ranker": [0.0] * 5},
        "optimizer": {"two_tower": [0.0] * 8, "ranker": [0.0] * 5},
    }


def _read_checkpoint(path: Path, manifest_hash: str, row_hash: str, config: dict,
                     count: int) -> dict:
    try:
        envelope = json.loads(path.read_bytes())
        state = envelope["state"]
        if set(envelope) != {"state", "sha256"} or envelope["sha256"] != _digest(state):
            raise TrainingError("checkpoint digest mismatch")
        if state["format"] != RECIPE or state["manifest_sha256"] != manifest_hash \
                or state["train_rows_sha256"] != row_hash or state["config"] != config:
            raise TrainingError("checkpoint frozen input or recipe mismatch")
        if state["status"] not in ("training", "candidate") or state["stage"] not in (*STAGES, "done"):
            raise TrainingError("invalid checkpoint stage")
        if type(state["epoch"]) is not int or not 0 <= state["epoch"] <= config["epochs"]:
            raise TrainingError("invalid checkpoint epoch")
        if type(state["cursor"]) is not int or not 0 <= state["cursor"] <= count:
            raise TrainingError("invalid checkpoint cursor")
        order = state["order"]
        if (not isinstance(order, list) or
                (order and (len(order) != count or any(type(index) is not int for index in order)
                            or set(order) != set(range(count)))) or
                (not order and state["cursor"] != 0) or
                (order and state["cursor"] == 0)):
            raise TrainingError("invalid checkpoint reader order")
        if (type(state["updates"]) is not int or state["updates"] < 0 or
                (state["stage"] == "done" and
                 (state["epoch"] != config["epochs"] or state["cursor"] != 0 or order)) or
                (state["stage"] != "done" and state["epoch"] == config["epochs"])):
            raise TrainingError("invalid checkpoint progress")
        for name, length in PARAMETERS.items():
            for field in ("models", "optimizer"):
                values = state[field][name]
                if len(values) != length or any(type(v) not in (int, float) or not math.isfinite(v) for v in values):
                    raise TrainingError("invalid checkpoint model or optimizer shape")
        rng = random.Random()
        rng.setstate(_tuples(state["random_state"]))
        if state["stage"] == "done" and state["status"] != "candidate":
            raise TrainingError("invalid completed checkpoint status")
        return state
    except TrainingError:
        raise
    except (OSError, ValueError, TypeError, KeyError, IndexError) as exc:
        raise TrainingError("invalid or damaged checkpoint") from exc


def _save_checkpoint(path: Path, state: dict) -> None:
    payload = _canonical({"state": state, "sha256": _digest(state)})
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def _tower_parts(row: dict, weights: list[float]) -> tuple[list[float], list[float], float]:
    u, i = row["user_interest"], row["item_quality"]
    user = [weights[0] * u + weights[1], weights[2] * u + weights[3]]
    item = [weights[4] * i + weights[5], weights[6] * i + weights[7]]
    return user, item, sum(a * b for a, b in zip(user, item))


def tower_score(row: dict, weights: list[float]) -> float:
    return _tower_parts(row, weights)[2]


def ranker_features(row: dict, tower_weights: list[float]) -> list[float]:
    u, i = row["user_interest"], row["item_quality"]
    return [1.0, u, i, u * i, tower_score(row, tower_weights)]


def ranker_logit(row: dict, tower_weights: list[float], ranker_weights: list[float]) -> float:
    return sum(x * w for x, w in zip(ranker_features(row, tower_weights), ranker_weights))


def _gradient(row: dict, stage: str, state: dict) -> list[float]:
    towers = state["models"]["two_tower"]
    if stage == "two_tower":
        user, item, logit = _tower_parts(row, towers)
        u, i = row["user_interest"], row["item_quality"]
        derivatives = [item[0] * u, item[0], item[1] * u, item[1],
                       user[0] * i, user[0], user[1] * i, user[1]]
    else:
        derivatives = ranker_features(row, towers)
        logit = sum(x * w for x, w in zip(derivatives, state["models"]["ranker"]))
    # Stable sigmoid derivative of BCE, for candidate raw scores only.
    sigmoid = 1 / (1 + math.exp(-abs(logit)))
    probability = sigmoid if logit >= 0 else 1 - sigmoid
    return [(probability - row["label"]) * derivative for derivative in derivatives]


def _step(row: dict, stage: str, state: dict) -> None:
    config = state["config"]
    gradient = _gradient(row, stage, state)
    weights = state["models"][stage]
    momentum = state["optimizer"][stage]
    for index, value in enumerate(gradient):
        momentum[index] = config["momentum"] * momentum[index] + value
        weights[index] -= config["learning_rate"] * momentum[index]
        if not math.isfinite(weights[index]):
            raise TrainingError("non-finite training parameter")


def _report(splits: dict[str, list[dict]], state: dict) -> dict:
    towers = state["models"]["two_tower"]
    ranker = state["models"]["ranker"]
    result = {}
    for name, rows in splits.items():
        logits = [ranker_logit(row, towers, ranker) for row in rows]
        # Empirical logistic objective, not a calibrated production probability.
        loss = sum(max(0.0, z) - y * z + math.log1p(math.exp(-abs(z)))
                   for z, y in zip(logits, (row["label"] for row in rows))) / len(rows)
        result[name] = {"rows": len(rows), "positive": sum(row["label"] for row in rows),
                        "observed_negative": sum(1 - row["label"] for row in rows),
                        "empirical_logloss": loss}
    return {"status": state["status"], "recipe": RECIPE,
            "manifest_sha256": state["manifest_sha256"], "train_rows_sha256": state["train_rows_sha256"],
            "updates": state["updates"], "stage": state["stage"], "epoch": state["epoch"],
            "cursor": state["cursor"], "score_semantics": "uncalibrated_logit",
            "sampling": "full_inclusion_probability_1", "splits": result,
            "activation": "none"}


def train(manifest: str | Path, *, expected_manifest_sha256: str, checkpoint: str | Path,
          seed: int = 7, epochs: int = 3, learning_rate: float = 0.05,
          momentum: float = 0.8, max_updates: int | None = None,
          resume: bool = False) -> dict:
    """Train a fixed synthetic/observed candidate; checkpoint after every SGD update."""
    if len(expected_manifest_sha256) != 64 or any(c not in "0123456789abcdef" for c in expected_manifest_sha256):
        raise TrainingError("expected frozen manifest sha256 is required")
    if type(seed) is not int or type(epochs) is not int or epochs < 1 or seed < 0:
        raise TrainingError("invalid seed or epochs")
    if not math.isfinite(learning_rate) or not 0 < learning_rate <= 1:
        raise TrainingError("invalid learning rate")
    if not math.isfinite(momentum) or not 0 <= momentum < 1:
        raise TrainingError("invalid momentum")
    if max_updates is not None and (type(max_updates) is not int or max_updates < 0):
        raise TrainingError("invalid update limit")
    config = {"seed": seed, "epochs": epochs, "learning_rate": learning_rate,
              "momentum": momentum}
    with open_dataset(manifest, expected_manifest_sha256=expected_manifest_sha256) as snapshot:
        splits = {name: _rows(snapshot, name) for name in ("train", "validation", "test")}
        _validate_input(snapshot, splits)
        train_rows = splits["train"]
        row_hash = _row_fingerprint(train_rows)
        path = Path(checkpoint)
        if resume:
            state = _read_checkpoint(path, snapshot.manifest_sha256, row_hash, config, len(train_rows))
        else:
            if path.exists():
                raise TrainingError("checkpoint already exists; use explicit resume")
            state = _initial_state(snapshot.manifest_sha256, row_hash, config)
        rng = random.Random()
        rng.setstate(_tuples(state["random_state"]))
        completed = 0
        while state["stage"] != "done" and (max_updates is None or completed < max_updates):
            if not state["order"]:
                state["order"] = list(range(len(train_rows)))
                rng.shuffle(state["order"])
                state["random_state"] = rng.getstate()
            index = state["order"][state["cursor"]]
            _step(train_rows[index], state["stage"], state)
            state["cursor"] += 1
            state["updates"] += 1
            completed += 1
            if state["cursor"] == len(train_rows):
                state["cursor"] = 0
                state["order"] = []
                state["epoch"] += 1
                if state["epoch"] == epochs:
                    if state["stage"] == "two_tower":
                        state["stage"] = "ranker"
                        state["epoch"] = 0
                    else:
                        state["stage"] = "done"
                        state["status"] = "candidate"
            _save_checkpoint(path, state)
        if max_updates == 0 and not path.exists():
            _save_checkpoint(path, state)
        return _report(splits, state)
