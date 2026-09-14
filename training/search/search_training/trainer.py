"""Deterministic CPU T1 pairwise logistic text-ranker baseline."""

from __future__ import annotations

from collections import Counter
from dataclasses import asdict, dataclass
from hashlib import sha256
import json
import math
import os
from pathlib import Path
import random
import re
import tempfile
from typing import TYPE_CHECKING

from .dataset import Snapshot

if TYPE_CHECKING:
    from .authoritative import AuthoritativePairs


FIXTURE_PAIR_POLICY = "sea.search.experimental-qrel-pairs.v0"


FEATURE_VERSION = "lexical-pairwise-v0"
FEATURES = ("term_frequency_overlap", "idf_overlap", "exact_token_phrase", "negative_log_document_length")
_TOKEN = re.compile(r"[a-z0-9]+|[\u4e00-\u9fff]")


class TrainError(ValueError):
    pass


@dataclass(frozen=True)
class TrainConfig:
    epochs: int = 5
    learning_rate: float = 0.05
    l2: float = 0.0
    seed: int = 7
    beta1: float = 0.9
    beta2: float = 0.999
    epsilon: float = 1e-8

    def validate(self) -> None:
        if type(self.epochs) is not int or self.epochs < 1 or type(self.seed) is not int or self.seed < 0:
            raise TrainError("invalid epochs/seed")
        for name in ("learning_rate", "l2", "beta1", "beta2", "epsilon"):
            value = getattr(self, name)
            if type(value) not in (int, float) or not math.isfinite(value):
                raise TrainError(f"invalid {name}")
        if not 0 < self.learning_rate <= 1 or self.epsilon <= 0 or not 0 <= self.l2 <= 1 or not 0 <= self.beta1 < 1 or not 0 <= self.beta2 < 1:
            raise TrainError("invalid optimizer range")


def _tokens(text: str) -> list[str]:
    return _TOKEN.findall(text.casefold())


def _fit_idf(rows: tuple[dict, ...]) -> dict[str, float]:
    documents = [candidate["text"] for row in rows for candidate in (row["positive"], row["negative"])]
    df: Counter[str] = Counter()
    for doc in documents:
        df.update(set(_tokens(doc)))
    return {term: math.log((len(documents) + 1) / (count + 1)) + 1 for term, count in sorted(df.items())}


def _features(query: str, document: str, idf: dict[str, float]) -> list[float]:
    query_tokens, doc_tokens = _tokens(query), _tokens(document)
    q, d = Counter(query_tokens), Counter(doc_tokens)
    overlap = sum(min(count, d[token]) for token, count in q.items())
    denominator = sum(q.values()) or 1
    unique = set(q)
    idf_total = sum(idf.get(token, 1.0) for token in unique) or 1
    weighted = sum(idf.get(token, 1.0) for token in unique if d[token]) / idf_total
    phrase = float(bool(query_tokens) and any(doc_tokens[i:i + len(query_tokens)] == query_tokens
                                                 for i in range(len(doc_tokens) - len(query_tokens) + 1)))
    return [overlap / denominator, weighted, phrase, -math.log1p(len(doc_tokens)) / 10]


def _difference(row: dict, idf: dict[str, float]) -> list[float]:
    pos = _features(row["query_text"], row["positive"]["text"], idf)
    neg = _features(row["query_text"], row["negative"]["text"], idf)
    return [a - b for a, b in zip(pos, neg, strict=True)]


def _softplus(value: float) -> float:
    return value + math.log1p(math.exp(-value)) if value > 0 else math.log1p(math.exp(value))


def _sigmoid_negative(value: float) -> float:
    if value >= 0:
        value = math.exp(-value)
        return value / (1 + value)
    return 1 / (1 + math.exp(value))


def _loss(row: dict, weights: list[float], idf: dict[str, float]) -> float:
    delta = sum(w * x for w, x in zip(weights, _difference(row, idf), strict=True))
    return row["sample_weight"] * _softplus(-delta)


def _canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8")


def _write_atomic(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".search-checkpoint-", delete=False) as file:
        temporary = Path(file.name)
        file.write(data)
        file.flush()
        os.fsync(file.fileno())
    os.replace(temporary, path)


def _tuple(value: object):
    if isinstance(value, list):
        return tuple(_tuple(child) for child in value)
    return value


def _save(path: Path, state: dict) -> None:
    payload = _canonical(state)
    _write_atomic(path, _canonical({"payload": state, "sha256": sha256(payload).hexdigest()}))


def _load(path: Path) -> dict:
    try:
        envelope = json.loads(path.read_bytes(), object_pairs_hook=_no_duplicates)
        if set(envelope) != {"payload", "sha256"} or sha256(_canonical(envelope["payload"])).hexdigest() != envelope["sha256"]:
            raise TrainError("checkpoint checksum mismatch")
        return envelope["payload"]
    except (OSError, ValueError, TypeError, KeyError) as exc:
        raise TrainError("corrupt or unavailable checkpoint") from exc


def _no_duplicates(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise TrainError("duplicate checkpoint key")
        result[key] = value
    return result


def _summary(rows: tuple[dict, ...], weights: list[float], idf: dict[str, float]) -> dict:
    if not rows:
        return {"pairs": 0, "loss": None, "pair_accuracy": None}
    deltas = [sum(w * x for w, x in zip(weights, _difference(row, idf), strict=True)) for row in rows]
    return {"pairs": len(rows), "loss": sum(_loss(row, weights, idf) for row in rows) / len(rows),
            "pair_accuracy": sum(delta > 0 for delta in deltas) / len(rows)}


def train(snapshot: Snapshot | AuthoritativePairs, config: TrainConfig, checkpoint_path: str | Path, *,
          resume: bool = False, max_steps: int | None = None, candidate_path: str | Path | None = None) -> dict:
    """Train or resume; max_steps is a total update count, useful for interruption tests."""
    config.validate()
    if max_steps is not None and (type(max_steps) is not int or max_steps < 1):
        raise TrainError("invalid max_steps")
    checkpoint = Path(checkpoint_path)
    idf = _fit_idf(snapshot.rows["train"])
    config_dict = asdict(config)
    pair_policy = getattr(snapshot, "pair_policy_id", FIXTURE_PAIR_POLICY)
    computed_pair_hash = sha256(_canonical({"manifest_sha256": snapshot.manifest_sha256,
                                            "pair_policy_id": pair_policy, "rows": snapshot.rows})).hexdigest()
    pair_hash = getattr(snapshot, "pair_snapshot_sha256", computed_pair_hash)
    if pair_hash != computed_pair_hash:
        raise TrainError("pair snapshot hash differs from training rows")
    data_kind = snapshot.manifest["data_kind"]
    authoritative = pair_policy != FIXTURE_PAIR_POLICY
    if resume:
        state = _load(checkpoint)
        if state.get("contract") != FEATURE_VERSION or state.get("dataset_id") != snapshot.manifest["dataset_id"] or state.get("revision") != snapshot.manifest["revision"] or state.get("manifest_sha256") != snapshot.manifest_sha256 or state.get("pair_policy_id") != pair_policy or state.get("pair_snapshot_sha256") != pair_hash or state.get("data_kind") != data_kind or state.get("config") != config_dict or state.get("idf") != idf:
            raise TrainError("checkpoint input/config/feature mismatch")
        try:
            rng = random.Random()
            rng.setstate(_tuple(state["rng_state"]))
            if len(state["weights"]) != len(FEATURES) or len(state["m"]) != len(FEATURES) or len(state["v"]) != len(FEATURES):
                raise ValueError("bad optimizer shape")
            if any(not math.isfinite(number) for key in ("weights", "m", "v") for number in state[key]):
                raise ValueError("non-finite optimizer")
            if sorted(state["order"]) != list(range(len(snapshot.rows["train"]))) or not 0 <= state["cursor"] < len(state["order"]):
                if not (state["epoch"] == config.epochs and state["cursor"] == 0):
                    raise ValueError("bad cursor")
            if not 0 <= state["epoch"] <= config.epochs or state["step"] != len(state["loss_trace"]):
                raise ValueError("bad progress")
        except (KeyError, TypeError, ValueError, IndexError) as exc:
            raise TrainError("invalid checkpoint state") from exc
    else:
        if checkpoint.exists():
            raise TrainError("checkpoint exists; use resume explicitly")
        rng = random.Random(config.seed)
        order = list(range(len(snapshot.rows["train"])))
        rng.shuffle(order)
        state = {"contract": FEATURE_VERSION, "dataset_id": snapshot.manifest["dataset_id"],
                 "revision": snapshot.manifest["revision"], "manifest_sha256": snapshot.manifest_sha256,
                 "pair_policy_id": pair_policy, "pair_snapshot_sha256": pair_hash,
                 "data_kind": data_kind, "config": config_dict, "idf": idf,
                 "weights": [0.0] * len(FEATURES), "m": [0.0] * len(FEATURES),
                 "v": [0.0] * len(FEATURES), "rng_state": rng.getstate(),
                 "order": order, "epoch": 0, "cursor": 0, "step": 0, "loss_trace": []}
    initial = _summary(snapshot.rows["train"], [0.0] * len(FEATURES), idf)
    while state["epoch"] < config.epochs and (max_steps is None or state["step"] < max_steps):
        row = snapshot.rows["train"][state["order"][state["cursor"]]]
        diff = _difference(row, idf)
        delta = sum(w * x for w, x in zip(state["weights"], diff, strict=True))
        state["loss_trace"].append(row["sample_weight"] * _softplus(-delta))
        gradient = [-row["sample_weight"] * _sigmoid_negative(delta) * x + config.l2 * w
                    for x, w in zip(diff, state["weights"], strict=True)]
        state["step"] += 1
        for i, value in enumerate(gradient):
            state["m"][i] = config.beta1 * state["m"][i] + (1 - config.beta1) * value
            state["v"][i] = config.beta2 * state["v"][i] + (1 - config.beta2) * value * value
            estimate_m = state["m"][i] / (1 - config.beta1 ** state["step"])
            estimate_v = state["v"][i] / (1 - config.beta2 ** state["step"])
            state["weights"][i] -= config.learning_rate * estimate_m / (math.sqrt(estimate_v) + config.epsilon)
        state["cursor"] += 1
        if state["cursor"] == len(state["order"]):
            state["epoch"] += 1
            state["cursor"] = 0
            if state["epoch"] < config.epochs:
                state["order"] = list(range(len(snapshot.rows["train"])))
                rng.shuffle(state["order"])
        state["rng_state"] = rng.getstate()
        _save(checkpoint, state)
    complete = state["epoch"] == config.epochs
    complete_status = "COMPLETED_FIXTURE" if not authoritative else (
        "COMPLETED_CANDIDATE_SYNTHETIC" if data_kind == "synthetic" else "COMPLETED_CANDIDATE_OBSERVED")
    report = {"status": complete_status if complete else "INTERRUPTED", "experimental": True,
              "data_kind": snapshot.manifest["data_kind"], "dataset_id": snapshot.manifest["dataset_id"],
              "manifest_sha256": snapshot.manifest_sha256, "pair_policy_id": pair_policy,
              "pair_snapshot_sha256": pair_hash, "feature_version": FEATURE_VERSION,
              "loss": "pairwise_logistic", "loss_interpretation": "optimization_diagnostic_only",
              "model_quality": None,
              "negative_policy": ("same-query graded judged high 2/3 > low 0/1" if authoritative else
                                  "explicit judged qrel grade 0 only"),
              "mask": ("higher and lower judged, pair_mask=true" if authoritative else
                       "positive and negative judged, pair_mask=true"), "seed": config.seed,
              "eligible_pairs": {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")},
              "step": state["step"], "epoch": state["epoch"], "cursor": state["cursor"],
              "train_initial": initial, "train_final": _summary(snapshot.rows["train"], state["weights"], idf),
              "validation": _summary(snapshot.rows["validation"], state["weights"], idf) if complete else None,
              "test": _summary(snapshot.rows["test"], state["weights"], idf) if complete else None,
              "loss_trace_sha256": sha256(_canonical(state["loss_trace"])).hexdigest()}
    if authoritative:
        report["judged_higher"] = {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")}
        report["judged_lower"] = {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")}
        report["reader_status"] = snapshot.reader_report["status"]
        report["unpairable_queries"] = snapshot.unpairable_queries
    else:
        report["judged_positive"] = {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")}
        report["judged_negative"] = {split: len(snapshot.rows[split]) for split in ("train", "validation", "test")}
    if candidate_path is not None and complete:
        candidate = {"status": "candidate_only", "active": False, "experimental": True,
                     "data_kind": state["data_kind"], "model_quality": None,
                     "dataset_id": state["dataset_id"], "manifest_sha256": state["manifest_sha256"],
                     "pair_policy_id": state["pair_policy_id"], "pair_snapshot_sha256": state["pair_snapshot_sha256"],
                     "feature_version": FEATURE_VERSION, "features": FEATURES, "idf": idf,
                     "weights": state["weights"], "config": config_dict,
                     "checkpoint_sha256": sha256(checkpoint.read_bytes()).hexdigest()}
        _write_atomic(Path(candidate_path), _canonical(candidate))
    return report
