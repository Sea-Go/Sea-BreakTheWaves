"""Deterministic graded pairs from a pinned H10.b search-qrel Parquet snapshot.

The independent sea_training reader owns manifest, source, split and physical
Parquet validation. This module only selects same-query judged preferences for
the small lexical T1 candidate; it never publishes a retrieval model.
"""

from __future__ import annotations

from collections import defaultdict
from contextlib import contextmanager
from dataclasses import dataclass
from hashlib import sha256
import json
from pathlib import Path
from typing import Iterator

from pyarrow import fs

from sea_training.dataset import DatasetError
from sea_training.search_dataset import SPLITS, open_search_dataset


PAIR_POLICY_ID = "sea.search.graded-judged-pairs.v1"
MAX_PAIRS_PER_SPLIT = 200_000
HIGH_GRADES = frozenset((2, 3))
LOW_GRADES = frozenset((0, 1))


def _canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False,
                      separators=(",", ":"), allow_nan=False).encode("utf-8")


def _chunk_key(row: dict) -> str:
    return json.dumps([row["document_id"], row["document_revision"], row["chunk_id"]],
                      ensure_ascii=False, separators=(",", ":"))


def _candidate(row: dict) -> dict:
    return {"chunk_key": _chunk_key(row), "text": row["chunk_text"],
            "text_sha256": row["chunk_text_sha256"], "grade": row["relevance_grade"],
            "judged_mask": row["judged_mask"], "judgment_id": row["judgment_id"],
            "judgment_revision": row["judgment_revision"], "judgment_source": row["judgment_source"],
            "judgment_source_ref": row["judgment_source_ref"],
            "judgment_source_hash": row["judgment_source_hash"],
            "content_available_at": row["content_available_at"].isoformat(),
            "judged_at": row["judged_at"].isoformat(), "available_at": row["available_at"].isoformat()}


def _pair(high: dict, low: dict, split: str, manifest: dict, manifest_hash: str) -> dict:
    if high["query_id"] != low["query_id"] or high["query_family_id"] != low["query_family_id"] or \
            high["near_duplicate_cluster_id"] != low["near_duplicate_cluster_id"] or \
            high["query_text_sha256"] != low["query_text_sha256"] or high["query_time"] != low["query_time"] or \
            high["judgment_source"] != low["judgment_source"]:
        raise DatasetError("graded pair crosses query identity, group or source")
    if high["judged_mask"] is not True or low["judged_mask"] is not True or \
            high["relevance_grade"] not in HIGH_GRADES or low["relevance_grade"] not in LOW_GRADES or \
            high["relevance_grade"] <= low["relevance_grade"] or _chunk_key(high) == _chunk_key(low):
        raise DatasetError("unjudged, non-graded or duplicate-chunk search pair")
    key = [manifest_hash, PAIR_POLICY_ID, split, high["query_id"],
           high["judgment_id"], high["judgment_revision"],
           low["judgment_id"], low["judgment_revision"]]
    return {"pair_id": "graded-" + sha256(_canonical(key)).hexdigest(), "split": split,
            "query_id": high["query_id"], "query_family_id": high["query_family_id"],
            "near_duplicate_cluster_id": high["near_duplicate_cluster_id"],
            "query_text": high["query_text"], "query_text_sha256": high["query_text_sha256"],
            "query_time": high["query_time"].isoformat(),
            "judgment_set_id": manifest["judgment_policy_id"] + "@" + str(manifest["revision"]),
            "label_source": high["judgment_source"], "negative_source": "explicit_judged_lower_qrel",
            "positive": _candidate(high), "negative": _candidate(low),
            "pair_mask": True, "grade_gap": high["relevance_grade"] - low["relevance_grade"],
            "sample_weight": (high["relevance_grade"] - low["relevance_grade"]) / 3}


@dataclass(frozen=True)
class AuthoritativePairs:
    manifest: dict
    manifest_sha256: str
    rows: dict[str, tuple[dict, ...]]
    pair_policy_id: str
    pair_snapshot_sha256: str
    reader_report: dict
    unpairable_queries: dict[str, int]


@contextmanager
def open_authoritative_pairs(manifest_uri: str | Path, *, expected_manifest_sha256: str,
                             schema_dir: Path | None = None,
                             filesystem: fs.FileSystem | None = None) -> Iterator[AuthoritativePairs]:
    """Verify all declared S3 Parquet bytes, then freeze same-query graded pairs.

    Only grade 2/3 vs grade 0/1 is used for T1 fitting. A grade 1 row remains
    grade 1 in the pair; it is never relabelled as an irrelevant grade 0.
    """
    with open_search_dataset(manifest_uri, expected_manifest_sha256=expected_manifest_sha256,
                             schema_dir=schema_dir, filesystem=filesystem) as source:
        pairs: dict[str, tuple[dict, ...]] = {}
        unpairable: dict[str, int] = {}
        for split in SPLITS:
            groups: dict[str, list[dict]] = defaultdict(list)
            for batch in source.iter_batches(split, batch_size=8192):
                for row in batch.to_pylist():
                    groups[row["query_id"]].append(row)
            built: list[dict] = []
            skipped = 0
            for query_id in sorted(groups):
                judged = sorted(groups[query_id], key=lambda row: (row["judgment_id"], row["judgment_revision"]))
                high = [row for row in judged if row["judged_mask"] is True and row["relevance_grade"] in HIGH_GRADES]
                low = [row for row in judged if row["judged_mask"] is True and row["relevance_grade"] in LOW_GRADES]
                if not high or not low:
                    skipped += 1
                    continue
                if len(high) * len(low) > MAX_PAIRS_PER_SPLIT - len(built):
                    raise DatasetError("search pair materialization exceeds bounded T1 candidate")
                for upper in high:
                    for lower in low:
                        built.append(_pair(upper, lower, split, source.manifest, source.manifest_sha256))
            if not built:
                raise DatasetError(f"no same-query judged high/low pairs in {split}")
            if len({row["pair_id"] for row in built}) != len(built):
                raise DatasetError("duplicate graded search pair identity")
            pairs[split] = tuple(built)
            unpairable[split] = skipped
        material = {"manifest_sha256": source.manifest_sha256, "pair_policy_id": PAIR_POLICY_ID,
                    "rows": {split: pairs[split] for split in SPLITS}}
        yield AuthoritativePairs(manifest=source.manifest, manifest_sha256=source.manifest_sha256,
                                 rows=pairs, pair_policy_id=PAIR_POLICY_ID,
                                 pair_snapshot_sha256=sha256(_canonical(material)).hexdigest(),
                                 reader_report=source.report, unpairable_queries=unpairable)
