"""Verify an immutable pointwise search-qrel Parquet dataset before reading it.

This H10.b contract is separate from recommendation impressions and from the
search trainer's experimental JSONL pair fixture. Validation is not a model
quality result or permission to treat synthetic judgments as observed data.
"""

from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime
from hashlib import sha256
import json
from pathlib import Path
import posixpath
import re
import sqlite3
import tempfile
from typing import Iterator

from jsonschema import Draft202012Validator, FormatChecker
import pyarrow as pa
from pyarrow import fs
import pyarrow.parquet as pq

from .dataset import DatasetError, _copy_verified, _json, artifact_path, contract_directory, utc


SCHEMA_VERSION = "sea.search-qrel-dataset.v1"
ROW_CONTRACT = "sea.search-qrel.v1"
SPLITS = ("train", "validation", "test")
SHA256 = re.compile(r"^[a-f0-9]{64}$")
STRING_COLUMNS = ("judgment_id", "query_id", "query_family_id", "near_duplicate_cluster_id",
                  "query_text", "document_id", "document_revision", "chunk_id", "chunk_text",
                  "judgment_source", "judgment_source_ref", "split")
HASH_COLUMNS = ("query_text_sha256", "chunk_text_sha256", "judgment_source_hash")


def _schemas(schema_dir: Path) -> tuple[dict, list[dict]]:
    try:
        manifest = json.loads((schema_dir / "search-qrel-dataset-manifest.v1.schema.json").read_text())
        columns = json.loads((schema_dir / "search-qrel.columns.v1.json").read_text())
    except (OSError, ValueError) as exc:
        raise DatasetError("search qrel schemas unavailable or invalid") from exc
    return manifest, columns


def _manifest(manifest: dict, schema_dir: Path) -> list[dict]:
    schema, columns = _schemas(schema_dir)
    error = next(Draft202012Validator(schema, format_checker=FormatChecker()).iter_errors(manifest), None)
    if error:
        location = "/".join(str(part) for part in error.absolute_path)
        raise DatasetError(f"invalid search manifest at {location}: {error.validator}")
    if manifest["columns"] != columns:
        raise DatasetError("search Parquet columns differ from versioned row contract")
    parent, parent_hash = manifest["parent_revision"], manifest["parent_manifest_sha256"]
    if (parent is None) != (parent_hash is None) or (parent is None) != (manifest["revision"] == 1):
        raise DatasetError("parent revision and manifest hash must be paired")
    if parent is not None and parent >= manifest["revision"]:
        raise DatasetError("parent revision must precede current revision")
    created, cutoff = utc(manifest["created_at"]), utc(manifest["source"]["ingest_cutoff"])
    if cutoff > created:
        raise DatasetError("source cutoff follows manifest creation")
    batches = [b["batch_id"] for b in manifest["source"]["batches"]]
    if len(batches) != len(set(batches)):
        raise DatasetError("duplicate source batch")
    watermarks = [(w["source"], w["partition"]) for w in manifest["source"]["watermarks"]]
    if len(watermarks) != len(set(watermarks)) or any(
            utc(w["event_time"]) > cutoff for w in manifest["source"]["watermarks"]):
        raise DatasetError("source coverage watermark is duplicate or beyond cutoff")
    splits = manifest["splits"]
    if [s["name"] for s in splits] != list(SPLITS):
        raise DatasetError("search splits must be train/validation/test in order")
    previous_end = None
    for split in splits:
        start, end = utc(split["start"]), utc(split["end"])
        if start >= end or previous_end is not None and start < previous_end:
            raise DatasetError("search split windows overlap or are empty")
        previous_end = end
    paths = [artifact_path(f["path"]) for f in manifest["files"]]
    if len(paths) != len(set(paths)):
        raise DatasetError("duplicate search shard path")
    order = [(SPLITS.index(f["split"]), f["shard_index"]) for f in manifest["files"]]
    if order != sorted(order):
        raise DatasetError("search shards must be in split/index order")
    for split in splits:
        files = [f for f in manifest["files"] if f["split"] == split["name"]]
        if sorted(f["shard_index"] for f in files) != list(range(split["shard_count"])):
            raise DatasetError("search split has a missing or duplicate shard index")
        if sum(f["rows"] for f in files) != split["rows"]:
            raise DatasetError("search split row total differs from shards")
    if sum(s["rows"] for s in splits) != manifest["row_count"]:
        raise DatasetError("search manifest row total differs from splits")
    return columns


def _arrow_type(declared: str) -> pa.DataType:
    return pa.timestamp("us", tz="UTC") if declared == "timestamp[us, tz=UTC]" else pa.type_for_alias(declared)


def _key(values: list[str]) -> str:
    return json.dumps(values, ensure_ascii=False, separators=(",", ":"))


def _row(row: dict, split: dict, manifest: dict, keys: sqlite3.Connection, stats: dict) -> None:
    for field in STRING_COLUMNS:
        value = row[field]
        if not isinstance(value, str) or not value.strip():
            raise DatasetError(f"empty required search identity: {field}")
    for field in HASH_COLUMNS:
        if not isinstance(row[field], str) or not SHA256.fullmatch(row[field]):
            raise DatasetError(f"invalid search source/content hash: {field}")
    if sha256(row["query_text"].encode("utf-8")).hexdigest() != row["query_text_sha256"] or \
            sha256(row["chunk_text"].encode("utf-8")).hexdigest() != row["chunk_text_sha256"]:
        raise DatasetError("query or chunk text hash mismatch")
    if row["judged_mask"] is not True or type(row["relevance_grade"]) is not int or \
            row["relevance_grade"] not in (0, 1, 2, 3):
        raise DatasetError("unjudged or invalid graded qrel")
    if type(row["judgment_revision"]) is not int or row["judgment_revision"] < 1:
        raise DatasetError("invalid judgment revision")
    required_source = "human_judgment" if manifest["data_kind"] == "observed" else "synthetic_fixture"
    if row["judgment_source"] != required_source:
        raise DatasetError("judgment source differs from data kind")
    query_time, content_time = row["query_time"], row["content_available_at"]
    judged, available, revoked = row["judged_at"], row["available_at"], row["revoked_at"]
    cutoff = utc(manifest["source"]["ingest_cutoff"])
    for value in (query_time, content_time, judged, available, revoked):
        if value is not None and (not isinstance(value, datetime) or value.tzinfo is None or
                                  value.utcoffset().total_seconds() != 0):
            raise DatasetError("search qrel time must be UTC")
    if not utc(split["start"]) <= query_time < utc(split["end"]):
        raise DatasetError("query time outside declared split")
    if not content_time <= query_time <= judged <= available <= cutoff:
        raise DatasetError("search qrel content/judgment availability crosses cutoff")
    if revoked is not None and revoked <= cutoff:
        raise DatasetError("revoked judgment is not visible at cutoff")
    query_key = row["query_id"]
    query_value = (row["split"], row["query_family_id"], row["near_duplicate_cluster_id"],
                   row["query_text_sha256"], query_time.isoformat())
    old = keys.execute("SELECT split,family,cluster,text_hash,query_time FROM queries WHERE query_id=?", (query_key,)).fetchone()
    if old is not None and old != query_value:
        raise DatasetError("query identity changes or crosses splits")
    keys.execute("INSERT OR IGNORE INTO queries VALUES (?,?,?,?,?,?)", (query_key, *query_value))
    for table, column, value in (("families", "family_id", row["query_family_id"]),
                                 ("clusters", "cluster_id", row["near_duplicate_cluster_id"])):
        found = keys.execute(f"SELECT split FROM {table} WHERE {column}=?", (value,)).fetchone()
        if found is not None and found[0] != row["split"]:
            raise DatasetError("query family or near-duplicate cluster crosses splits")
        keys.execute(f"INSERT OR IGNORE INTO {table} VALUES (?,?)", (value, row["split"]))
    chunk_key = _key([row["document_id"], row["document_revision"], row["chunk_id"]])
    old = keys.execute("SELECT text_hash,available_at FROM chunks WHERE chunk_key=?", (chunk_key,)).fetchone()
    chunk_value = (row["chunk_text_sha256"], content_time.isoformat())
    if old is not None and old != chunk_value:
        raise DatasetError("same document revision/chunk has conflicting content")
    keys.execute("INSERT OR IGNORE INTO chunks VALUES (?,?,?)", (chunk_key, *chunk_value))
    logical_key = _key([row["query_id"], row["document_id"], row["document_revision"], row["chunk_id"]])
    try:
        keys.execute("INSERT INTO qrels VALUES (?,?,?)", (logical_key, row["judgment_id"], row["judgment_revision"]))
    except sqlite3.IntegrityError as exc:
        raise DatasetError("duplicate or multiple visible judgment revisions for one qrel") from exc
    stats["rows"] += 1
    stats["grades"][str(row["relevance_grade"])] += 1


def _validate_rows(directory: Path, manifest: dict, columns: list[dict]) -> dict:
    stats = {name: {"rows": 0, "grades": {str(grade): 0 for grade in range(4)}} for name in SPLITS}
    splits = {s["name"]: s for s in manifest["splits"]}
    with sqlite3.connect(directory.parent / "search-validation.sqlite") as keys:
        keys.execute("CREATE TABLE queries (query_id TEXT PRIMARY KEY,split TEXT,family TEXT,cluster TEXT,text_hash TEXT,query_time TEXT)")
        keys.execute("CREATE TABLE families (family_id TEXT PRIMARY KEY,split TEXT)")
        keys.execute("CREATE TABLE clusters (cluster_id TEXT PRIMARY KEY,split TEXT)")
        keys.execute("CREATE TABLE chunks (chunk_key TEXT PRIMARY KEY,text_hash TEXT,available_at TEXT)")
        keys.execute("CREATE TABLE qrels (logical_key TEXT PRIMARY KEY,judgment_id TEXT UNIQUE,revision INTEGER)")
        for item in manifest["files"]:
            try:
                shard = pq.ParquetFile(directory / item["path"])
            except (OSError, pa.ArrowException) as exc:
                raise DatasetError("invalid search Parquet shard") from exc
            with shard:
                if shard.metadata.num_rows != item["rows"]:
                    raise DatasetError("search Parquet footer row count mismatch")
                if shard.schema_arrow.names != [c["name"] for c in columns]:
                    raise DatasetError("search Parquet column order mismatch")
                for field, declared in zip(shard.schema_arrow, columns):
                    if field.type != _arrow_type(declared["arrow_type"]) or field.nullable != declared["nullable"]:
                        raise DatasetError("search Parquet column type/nullability mismatch")
                decoded = 0
                for batch in shard.iter_batches(batch_size=8192):
                    for row in batch.to_pylist():
                        decoded += 1
                        if row["split"] != item["split"]:
                            raise DatasetError("search row/file split mismatch")
                        _row(row, splits[item["split"]], manifest, keys, stats[item["split"]])
                if decoded != item["rows"]:
                    raise DatasetError("search decoded row count mismatch")
        keys.commit()
    for split in manifest["splits"]:
        if stats[split["name"]]["rows"] != split["rows"]:
            raise DatasetError("search split has missing or excess Parquet rows")
    return stats


@dataclass(frozen=True)
class SearchDatasetSnapshot:
    manifest: dict
    manifest_sha256: str
    directory: Path
    report: dict
    _files: tuple[tuple[str, str], ...]

    def iter_batches(self, split: str, batch_size: int = 8192) -> Iterator[pa.RecordBatch]:
        if split not in SPLITS:
            raise DatasetError("undeclared search split")
        if batch_size < 1:
            raise DatasetError("batch_size must be positive")
        for file_split, path in self._files:
            if file_split == split:
                with pq.ParquetFile(self.directory / path) as shard:
                    yield from shard.iter_batches(batch_size=batch_size)


@contextmanager
def open_search_dataset(manifest_uri: str | Path, *, expected_manifest_sha256: str,
                        schema_dir: Path | None = None,
                        filesystem: fs.FileSystem | None = None) -> Iterator[SearchDatasetSnapshot]:
    """Copy and verify every declared shard before exposing one search batch."""
    if not isinstance(expected_manifest_sha256, str) or not SHA256.fullmatch(expected_manifest_sha256):
        raise DatasetError("expected search manifest SHA-256 required")
    uri = str(manifest_uri)
    try:
        if filesystem is None:
            if "://" not in uri:
                uri = str(Path(uri).resolve())
            filesystem, manifest_path = fs.FileSystem.from_uri(uri)
        else:
            manifest_path = uri
    except (OSError, ValueError, pa.ArrowException) as exc:
        raise DatasetError("unsupported search manifest location") from exc
    try:
        with filesystem.open_input_file(manifest_path) as file:
            payload = file.read()
    except (OSError, pa.ArrowException) as exc:
        raise DatasetError("cannot read search manifest") from exc
    digest = sha256(payload).hexdigest()
    if digest != expected_manifest_sha256:
        raise DatasetError("search manifest hash mismatch")
    manifest = _json(payload)
    columns = _manifest(manifest, schema_dir or contract_directory())
    base = posixpath.dirname(manifest_path)
    with tempfile.TemporaryDirectory(prefix="sea-search-qrel-") as directory:
        dest = Path(directory) / "artifacts"
        for item in manifest["files"]:
            _copy_verified(filesystem, posixpath.join(base, item["path"]), dest / item["path"],
                           item["sha256"], item["size_bytes"])
        stats = _validate_rows(dest, manifest, columns)
        status = "validated_synthetic_fixture" if manifest["data_kind"] == "synthetic" else "validated_manifest_and_rows"
        report = {"dataset_id": manifest["dataset_id"], "revision": manifest["revision"],
                  "manifest_sha256": digest, "data_kind": manifest["data_kind"], "row_contract": ROW_CONTRACT,
                  "validated_rows": sum(s["rows"] for s in stats.values()), "splits": stats, "status": status,
                  "model_quality": None}
        files = tuple((item["split"], item["path"]) for item in manifest["files"])
        yield SearchDatasetSnapshot(manifest, digest, dest, report, files)
