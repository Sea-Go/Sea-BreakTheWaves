"""Search-local frozen fixture reader; not the H10.b production dataset contract."""

from __future__ import annotations

from dataclasses import dataclass
from hashlib import sha256
import json
import math
from pathlib import Path, PurePosixPath


CONTRACT = "sea.search.experimental-qrel-pairs.v0"
SPLITS = ("train", "validation", "test")


class DatasetError(ValueError):
    pass


def _unique(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise DatasetError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _constant(value: str):
    raise DatasetError(f"non-finite JSON constant: {value}")


def _parse(payload: bytes) -> dict:
    try:
        result = json.loads(payload, object_pairs_hook=_unique, parse_constant=_constant)
    except (UnicodeError, ValueError) as exc:
        raise DatasetError("invalid JSON") from exc
    if not isinstance(result, dict):
        raise DatasetError("JSON object required")
    return result


def _keys(value: dict, expected: set[str], label: str) -> None:
    if set(value) != expected:
        raise DatasetError(f"invalid {label} fields")


def _string(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise DatasetError(f"invalid {label}")
    return value


def _path(value: object) -> Path:
    value = _string(value, "artifact path")
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or "\\" in value or ":" in value or str(path) != value:
        raise DatasetError("artifact path must be normalized and relative")
    return Path(value)


def _candidate(value: object, role: str) -> dict:
    if not isinstance(value, dict):
        raise DatasetError(f"invalid {role}")
    _keys(value, {"chunk_key", "text", "grade", "judgment_id", "judged_mask"}, role)
    _string(value["chunk_key"], "chunk_key")
    _string(value["text"], "text")
    _string(value["judgment_id"], "judgment_id")
    if value["judged_mask"] is not True:
        raise DatasetError("unjudged candidate cannot become a training negative")
    grade = value["grade"]
    if type(grade) is not int or grade not in (0, 1, 2):
        raise DatasetError("invalid qrel grade")
    if role == "positive" and grade != 2:
        raise DatasetError("positive requires relevant qrel grade 2")
    if role == "negative" and grade != 0:
        raise DatasetError("negative requires judged qrel grade 0")
    return value


def _pair(value: dict) -> dict:
    _keys(value, {"pair_id", "query_id", "query_family_id", "query_text", "judgment_set_id",
                  "label_source", "negative_source", "positive", "negative", "pair_mask", "sample_weight"}, "pair")
    for field in ("pair_id", "query_id", "query_family_id", "query_text", "judgment_set_id"):
        _string(value[field], field)
    if value["label_source"] != "synthetic_fixture" or value["negative_source"] != "explicit_judged_qrel":
        raise DatasetError("fixture source or negative provenance mismatch")
    if value["pair_mask"] is not True:
        raise DatasetError("unqualified pair mask")
    weight = value["sample_weight"]
    if type(weight) not in (int, float) or not math.isfinite(weight) or not 0 < weight <= 1:
        raise DatasetError("invalid sample weight")
    positive = _candidate(value["positive"], "positive")
    negative = _candidate(value["negative"], "negative")
    if positive["chunk_key"] == negative["chunk_key"]:
        raise DatasetError("same chunk cannot be both positive and negative")
    if positive["judgment_id"] == negative["judgment_id"]:
        raise DatasetError("positive and negative judgment IDs must differ")
    return value


@dataclass(frozen=True)
class Snapshot:
    manifest: dict
    manifest_sha256: str
    rows: dict[str, tuple[dict, ...]]


def open_snapshot(manifest_path: str | Path, *, expected_manifest_sha256: str) -> Snapshot:
    """Read every declared byte before returning an immutable-in-practice snapshot.

    The v0 reader is intentionally local and bounded; production S3/Parquet is WS09-A.
    """
    if not isinstance(expected_manifest_sha256, str) or len(expected_manifest_sha256) != 64:
        raise DatasetError("expected manifest SHA-256 required")
    source = Path(manifest_path)
    try:
        payload = source.read_bytes()
    except OSError as exc:
        raise DatasetError("cannot read manifest") from exc
    digest = sha256(payload).hexdigest()
    if digest != expected_manifest_sha256:
        raise DatasetError("manifest hash mismatch")
    manifest = _parse(payload)
    _keys(manifest, {"contract", "experimental", "data_kind", "dataset_id", "revision", "files"}, "manifest")
    if manifest["contract"] != CONTRACT or manifest["experimental"] is not True or manifest["data_kind"] != "synthetic_fixture":
        raise DatasetError("only explicit experimental synthetic search fixtures are accepted")
    _string(manifest["dataset_id"], "dataset_id")
    if type(manifest["revision"]) is not int or manifest["revision"] < 1:
        raise DatasetError("invalid revision")
    files = manifest["files"]
    if not isinstance(files, list) or len(files) != len(SPLITS):
        raise DatasetError("all three split files must be declared")
    rows: dict[str, tuple[dict, ...]] = {}
    all_pair_ids: set[str] = set()
    query_splits: dict[str, str] = {}
    family_splits: dict[str, str] = {}
    query_texts: dict[str, str] = {}
    judgments: dict[tuple[str, str], tuple[int, str]] = {}
    seen_paths: set[Path] = set()
    for index, item in enumerate(files):
        if not isinstance(item, dict):
            raise DatasetError("invalid file declaration")
        _keys(item, {"split", "path", "sha256", "size_bytes", "rows"}, "file")
        split = SPLITS[index]
        if item["split"] != split:
            raise DatasetError("files must be train/validation/test order")
        path = _path(item["path"])
        if path in seen_paths:
            raise DatasetError("duplicate artifact path")
        seen_paths.add(path)
        hash_value = item["sha256"]
        if not isinstance(hash_value, str) or len(hash_value) != 64 or any(c not in "0123456789abcdef" for c in hash_value):
            raise DatasetError("invalid artifact SHA-256")
        if type(item["size_bytes"]) is not int or item["size_bytes"] < 0 or type(item["rows"]) is not int or item["rows"] < 0:
            raise DatasetError("invalid file count")
        target = source.parent / path
        try:
            data = target.read_bytes()
        except OSError as exc:
            raise DatasetError("cannot read declared artifact") from exc
        if len(data) != item["size_bytes"] or sha256(data).hexdigest() != hash_value:
            raise DatasetError("artifact size/hash mismatch")
        parsed: list[dict] = []
        for line in data.splitlines():
            record = _pair(_parse(line))
            pair_id, query_id = record["pair_id"], record["query_id"]
            family_id = record["query_family_id"]
            if pair_id in all_pair_ids:
                raise DatasetError("duplicate pair ID")
            all_pair_ids.add(pair_id)
            if query_id in query_splits and query_splits[query_id] != split:
                raise DatasetError("query group crosses splits")
            if family_id in family_splits and family_splits[family_id] != split:
                raise DatasetError("query rewrite family crosses splits")
            if query_id in query_texts and query_texts[query_id] != record["query_text"]:
                raise DatasetError("query text changed for same query ID")
            query_splits[query_id] = split
            family_splits[family_id] = split
            query_texts[query_id] = record["query_text"]
            for role in ("positive", "negative"):
                candidate = record[role]
                judgment_key = (query_id, candidate["chunk_key"])
                judgment = (candidate["grade"], candidate["judgment_id"])
                if judgment_key in judgments and judgments[judgment_key] != judgment:
                    raise DatasetError("conflicting qrel for query/chunk")
                judgments[judgment_key] = judgment
            parsed.append(record)
        if len(parsed) != item["rows"]:
            raise DatasetError("declared row count mismatch")
        rows[split] = tuple(parsed)
    if not rows["train"]:
        raise DatasetError("empty training split")
    return Snapshot(manifest, digest, rows)
