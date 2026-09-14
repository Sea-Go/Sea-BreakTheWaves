"""Consume only frozen, verified warehouse shards; never discover a latest table."""

from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
import math
from pathlib import Path, PurePosixPath
import posixpath
import sqlite3
import tempfile
from typing import Iterator

from jsonschema import Draft202012Validator, FormatChecker
import pyarrow as pa
from pyarrow import fs
import pyarrow.parquet as pq


class DatasetError(ValueError):
    """A manifest or shard fails the published dataset contract."""


def contract_directory() -> Path:
    bundled = Path(__file__).resolve().parent / "schemas"
    if (bundled / "training-dataset-manifest.v1.schema.json").is_file():
        return bundled
    # Editable checkout fallback; canonical schemas are packaged without a copy in source.
    for parent in Path(__file__).resolve().parents:
        candidate = parent / "contracts" / "jsonschema"
        if (candidate / "training-dataset-manifest.v1.schema.json").is_file():
            return candidate
    raise DatasetError("dataset schemas unavailable; provide schema_dir explicitly")


def utc(value: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (TypeError, ValueError) as exc:
        raise DatasetError("invalid timestamp") from exc
    if parsed.tzinfo is None or parsed.utcoffset() != timedelta(0):
        raise DatasetError("timestamps must carry UTC timezone")
    return parsed.astimezone(timezone.utc)


def artifact_path(value: str) -> str:
    path = PurePosixPath(value)
    if (path.is_absolute() or ".." in path.parts or "\\" in value
            or ":" in value or not path.parts or str(path) != value):
        raise DatasetError("artifact path must be a normalized relative path")
    return value


def _json(payload: bytes) -> dict:
    def constant(value: str):
        raise DatasetError(f"non-finite JSON constant: {value}")

    def unique_pairs(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise DatasetError(f"duplicate JSON key: {key}")
            result[key] = value
        return result

    try:
        return json.loads(payload, parse_constant=constant, object_pairs_hook=unique_pairs)
    except (ValueError, UnicodeError) as exc:
        raise DatasetError("invalid manifest JSON") from exc


def _validate_manifest(manifest: dict, schema_dir: Path) -> None:
    try:
        schema = json.loads((schema_dir / "training-dataset-manifest.v2.schema.json").read_text())
        expected = json.loads((schema_dir / "recommend-engagement.columns.v1.json").read_text())
    except (OSError, ValueError) as exc:
        raise DatasetError("dataset schemas unavailable or invalid") from exc
    validator = Draft202012Validator(schema, format_checker=FormatChecker())
    error = next(validator.iter_errors(manifest), None)
    if error:
        location = "/".join(str(part) for part in error.absolute_path)
        raise DatasetError(f"invalid manifest at {location}: {error.validator}")
    if manifest["domain"] != "recommend":
        raise DatasetError("row contract currently supports recommend only")
    if manifest["columns"] != expected:
        raise DatasetError("columns do not match the versioned row contract")
    parent = manifest["parent_revision"]
    if parent is not None and parent >= manifest["revision"]:
        raise DatasetError("parent_revision must precede revision")
    created = utc(manifest["created_at"])
    source = manifest["source"]
    cutoff = utc(source["ingest_cutoff"])
    maturity = utc(manifest["label"]["maturity_watermark"])
    if cutoff > created or maturity > cutoff:
        raise DatasetError("invalid ingest/creation/maturity ordering")
    identities = [(w["source"], w["partition"]) for w in source["watermarks"]]
    if len(identities) != len(set(identities)):
        raise DatasetError("duplicate source partition watermark")
    if any(maturity > utc(w["event_time"]) or utc(w["event_time"]) > cutoff
           for w in source["watermarks"]):
        raise DatasetError("label watermark exceeds source coverage or ingest cutoff")
    batches = [b["batch_id"] for b in source["batches"]]
    if len(batches) != len(set(batches)):
        raise DatasetError("duplicate source batch")
    dim_keys = [(d["item_id"], d["content_revision"]) for d in source["dim_revisions"]]
    if len(dim_keys) != len(set(dim_keys)):
        raise DatasetError("duplicate dimension revision")
    if manifest["data_kind"] == "observed" and manifest["label"]["source"] == "synthetic":
        raise DatasetError("observed data cannot have synthetic labels")
    if manifest["label"]["target"] != "effective_read":
        raise DatasetError("unsupported target for engagement row contract")

    splits = manifest["splits"]
    names = [s["name"] for s in splits]
    if len(names) != len(set(names)) or "train" not in names:
        raise DatasetError("splits require unique names and a training window")
    order = {name: index for index, name in enumerate(("train", "validation", "test"))}
    previous_end = None
    previous_order = -1
    for split in splits:
        start, end = utc(split["start"]), utc(split["end"])
        if start >= end or (previous_end is not None and start < previous_end):
            raise DatasetError("split windows overlap or are empty")
        if order[split["name"]] <= previous_order:
            raise DatasetError("splits must be in temporal train/validation/test order")
        previous_end, previous_order = end, order[split["name"]]
    paths = [artifact_path(f["path"]) for f in manifest["files"]]
    paths += [artifact_path(f["path"]) for f in manifest["transform_refs"]]
    if len(paths) != len(set(paths)):
        raise DatasetError("duplicate artifact path")
    if any(f["split"] not in names for f in manifest["files"]):
        raise DatasetError("file references an undeclared split")
    if set(f["split"] for f in manifest["files"]) != set(names):
        raise DatasetError("every declared split must have an explicit shard, including empty splits")
    if sum(f["rows"] for f in manifest["files"]) != manifest["row_count"]:
        raise DatasetError("manifest row totals disagree")


@dataclass(frozen=True)
class DatasetSnapshot:
    """Private local snapshot of all validated bytes, stable for this context."""

    manifest: dict
    manifest_sha256: str
    directory: Path
    report: dict

    def iter_batches(self, split: str, batch_size: int = 8192) -> Iterator[pa.RecordBatch]:
        if split not in {s["name"] for s in self.manifest["splits"]}:
            raise DatasetError("undeclared split")
        if batch_size < 1:
            raise DatasetError("batch_size must be positive")
        for item in self.manifest["files"]:
            if item["split"] == split:
                with pq.ParquetFile(self.directory / item["path"]) as shard:
                    yield from shard.iter_batches(batch_size=batch_size)


def _copy_verified(filesystem: fs.FileSystem, remote_path: str, dest: Path,
                   expected_hash: str, expected_size: int | None = None) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    digest, size = sha256(), 0
    try:
        with filesystem.open_input_stream(remote_path) as source, dest.open("xb") as target:
            while chunk := source.read(1024 * 1024):
                digest.update(chunk)
                size += len(chunk)
                target.write(chunk)
    except (OSError, pa.ArrowException) as exc:
        raise DatasetError("cannot read declared artifact") from exc
    if expected_size is not None and size != expected_size:
        raise DatasetError("artifact size mismatch")
    if digest.hexdigest() != expected_hash:
        raise DatasetError("artifact hash mismatch")


def _rows(snapshot: DatasetSnapshot) -> dict:
    manifest = snapshot.manifest
    splits = {s["name"]: (utc(s["start"]), utc(s["end"])) for s in manifest["splits"]}
    maturity = utc(manifest["label"]["maturity_watermark"])
    window = timedelta(seconds=manifest["label"]["window_seconds"])
    dim_keys = {(d["item_id"], d["content_revision"]) for d in manifest["source"]["dim_revisions"]}
    stats = {name: {"rows": 0, "positive": 0, "negative": 0} for name in splits}
    # Exact cross-shard identity checks use disk, not an unbounded Python ID set.
    with sqlite3.connect(snapshot.directory.parent / "validation.sqlite") as keys:
        keys.execute("CREATE TABLE samples (id TEXT PRIMARY KEY)")
        keys.execute("CREATE TABLE requests (id TEXT PRIMARY KEY, split TEXT NOT NULL)")
        keys.execute("CREATE TABLE impressions (id TEXT PRIMARY KEY)")
        for item in manifest["files"]:
            path = snapshot.directory / item["path"]
            try:
                shard = pq.ParquetFile(path)
            except (OSError, pa.ArrowException) as exc:
                raise DatasetError("invalid Parquet shard") from exc
            with shard:
                if shard.metadata.num_rows != item["rows"]:
                    raise DatasetError("Parquet footer row count mismatch")
                if shard.schema_arrow.names != [c["name"] for c in manifest["columns"]]:
                    raise DatasetError("Parquet columns/order mismatch")
                for field, column in zip(shard.schema_arrow, manifest["columns"]):
                    declared = column["arrow_type"]
                    expected_type = pa.timestamp("us", tz="UTC") if declared == "timestamp[us, tz=UTC]" else pa.type_for_alias(declared)
                    if field.type != expected_type:
                        raise DatasetError(f"Parquet type mismatch: {field.name}")
                read_rows = 0
                for batch in shard.iter_batches(batch_size=8192):
                    for row in batch.to_pylist():
                        read_rows += 1
                        for column in manifest["columns"]:
                            value = row[column["name"]]
                            if value is None and not column["nullable"]:
                                raise DatasetError(f"null required value: {column['name']}")
                            if isinstance(value, str) and not value:
                                raise DatasetError(f"empty required identity: {column['name']}")
                        if row["split"] != item["split"]:
                            raise DatasetError("row/file split mismatch")
                        request = row["request_time"]
                        impression = row["impression_time"]
                        cutoff = row["feature_cutoff"]
                        available = row["feature_available_at"]
                        observed_end = row["label_observation_end"]
                        start, end = splits[item["split"]]
                        if not start <= request < end:
                            raise DatasetError("request outside split window")
                        if not request <= cutoff <= impression:
                            raise DatasetError("invalid request/feature cutoff/impression ordering")
                        if available > cutoff:
                            raise DatasetError("future feature in historical request")
                        if observed_end != impression + window or observed_end > maturity or observed_end > end:
                            raise DatasetError("label is immature or crosses the split boundary")
                        if (row["item_id"], row["content_revision"]) not in dim_keys:
                            raise DatasetError("row content revision absent from fixed DIM set")
                        if row["feature_contract_id"] != manifest["feature_contract_id"]:
                            raise DatasetError("row feature contract mismatch")
                        label = row["label"]
                        expected_state = {0: "OBSERVED_NEGATIVE", 1: "POSITIVE"}.get(label)
                        if expected_state is None or row["label_state"] != expected_state:
                            raise DatasetError("label/state mismatch or non-mature sample")
                        if row["label_revision"] < 1:
                            raise DatasetError("invalid label revision")
                        for field in ("user_interest", "item_quality", "sampling_probability"):
                            if not math.isfinite(row[field]):
                                raise DatasetError(f"non-finite numeric feature: {field}")
                        if not 0 < row["sampling_probability"] <= 1:
                            raise DatasetError("invalid sampling probability")
                        subject = [row[k] for k in ("authority_id", "tenant_id", "subject_id")]
                        request_key = json.dumps(subject + [row["request_id"]], separators=(",", ":"))
                        impression_key = json.dumps(subject + [row["impression_id"]], separators=(",", ":"))
                        try:
                            keys.execute("INSERT INTO samples VALUES (?)", (row["sample_id"],))
                            keys.execute("INSERT INTO impressions VALUES (?)", (impression_key,))
                        except sqlite3.IntegrityError as exc:
                            raise DatasetError("duplicate sample or exposure target") from exc
                        previous = keys.execute("SELECT split FROM requests WHERE id=?", (request_key,)).fetchone()
                        if previous is not None and previous[0] != item["split"]:
                            raise DatasetError("request group crosses dataset splits")
                        keys.execute("INSERT OR IGNORE INTO requests VALUES (?, ?)", (request_key, item["split"]))
                        stats[item["split"]]["rows"] += 1
                        stats[item["split"]]["positive" if label else "negative"] += 1
                if read_rows != item["rows"]:
                    raise DatasetError("decoded row count mismatch")
        keys.commit()
    return stats


@contextmanager
def open_dataset(manifest_uri: str | Path, *, schema_dir: Path | None = None,
                 filesystem: fs.FileSystem | None = None,
                 expected_manifest_sha256: str | None = None) -> Iterator[DatasetSnapshot]:
    """Verify every declared shard before exposing training batches.

    A custom Arrow filesystem supports S3-compatible endpoints; credentials and
    endpoint settings are supplied by the caller, outside the manifest.
    """
    uri = str(manifest_uri)
    try:
        if filesystem is None:
            if "://" not in uri:
                uri = str(Path(uri).resolve())
            filesystem, manifest_path = fs.FileSystem.from_uri(uri)
        else:
            manifest_path = uri
    except (OSError, ValueError, pa.ArrowException) as exc:
        raise DatasetError("unsupported or invalid manifest location") from exc
    try:
        with filesystem.open_input_file(manifest_path) as file:
            payload = file.read()
    except (OSError, pa.ArrowException) as exc:
        raise DatasetError("cannot read manifest") from exc
    digest = sha256(payload).hexdigest()
    if expected_manifest_sha256 is not None and digest != expected_manifest_sha256:
        raise DatasetError("manifest hash mismatch")
    manifest = _json(payload)
    _validate_manifest(manifest, schema_dir or contract_directory())
    base = posixpath.dirname(manifest_path)
    with tempfile.TemporaryDirectory(prefix="sea-dataset-") as directory:
        dest = Path(directory) / "artifacts"
        for item in manifest["files"]:
            _copy_verified(filesystem, posixpath.join(base, item["path"]), dest / item["path"],
                           item["sha256"], item["size_bytes"])
        for transform in manifest["transform_refs"]:
            _copy_verified(filesystem, posixpath.join(base, transform["path"]), dest / transform["path"],
                           transform["sha256"])
        snapshot = DatasetSnapshot(manifest, digest, dest, {})
        stats = _rows(snapshot)
        report = {"dataset_id": manifest["dataset_id"], "revision": manifest["revision"],
                  "manifest_sha256": digest, "data_kind": manifest["data_kind"],
                  "validated_rows": sum(s["rows"] for s in stats.values()), "splits": stats,
                  "row_contract": manifest["row_contract"], "status": "validated"}
        yield DatasetSnapshot(manifest, digest, dest, report)
