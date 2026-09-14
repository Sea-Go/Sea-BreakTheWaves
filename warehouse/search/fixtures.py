"""Deterministic, explicitly synthetic search judgments for WS07-D gates."""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parent
UTC = timezone.utc


def encoded(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()


def digest(value: bytes) -> str:
    return sha256(value).hexdigest()


def iso(value: datetime) -> str:
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def judgment(query: tuple[str, str, str, str, int], document: tuple[str, int], sequence: int,
             *, revision: int = 1, grade: int | None = None, retracted: bool = False) -> dict:
    query_id, family, cluster, query_text, day = query
    document_id, initial_grade = document
    query_time = datetime(2026, 9, day, 8, tzinfo=UTC)
    # A published document revision has one immutable availability time even
    # when several queries later judge the same chunk.
    content_time = datetime(2026, 8, 1, tzinfo=UTC)
    judged_at = query_time + timedelta(hours=1) if revision == 1 else datetime(2026, 9, 8, 8, tzinfo=UTC)
    available_at = judged_at + timedelta(minutes=10)
    chunk_id = document_id + ":chunk-1"
    chunk_text = f"Synthetic published passage about {document_id}; evidence is fixture only."
    judgment_id = f"judge.{query_id}.{document_id}"
    source_ref = f"synthetic-search-qrel/{judgment_id}/r{revision}"
    proof = {"judgment_id": judgment_id, "revision": revision, "source_ref": source_ref,
             "grade": None if retracted else initial_grade if grade is None else grade,
             "retracted": retracted}
    result = {
        "event_id": f"{judgment_id}.r{revision}", "judgment_id": judgment_id,
        "judgment_revision": revision, "status": "retracted" if retracted else "active",
        "query_id": query_id, "query_family_id": family, "near_duplicate_cluster_id": cluster,
        "query_text": query_text, "query_text_sha256": digest(query_text.encode()),
        "document_id": document_id, "document_revision": "r1", "chunk_id": chunk_id,
        "chunk_text": chunk_text, "chunk_text_sha256": digest(chunk_text.encode()),
        "relevance_grade": None if retracted else initial_grade if grade is None else grade,
        "judged_mask": not retracted,
        "judgment_source": "synthetic_fixture", "judgment_source_ref": source_ref,
        "judgment_source_hash": digest(encoded(proof)), "query_time": iso(query_time),
        "content_available_at": iso(content_time), "judged_at": iso(judged_at),
        "available_at": iso(available_at), "revoked_at": iso(available_at) if retracted else None,
        "source_partition": "fixture-0", "source_sequence": sequence,
    }
    return result


def batch(name: str, rows: list[dict]) -> bytes:
    lines = []
    for row in rows:
        event = {"batch_id": name, **row}
        event["payload_hash"] = digest(encoded(row))
        lines.append(encoded(event))
    return b"\n".join(lines) + b"\n"


def main() -> None:
    queries = [
        ("q-train-apple", "family-apple", "near-train", "apple pie recipe", 1),
        ("q-train-banana", "family-banana", "near-train", "banana dessert guide", 2),
        ("q-val-citrus", "family-citrus", "near-val", "citrus marmalade", 3),
        ("q-val-orange", "family-orange", "near-val", "orange jam steps", 4),
        ("q-test-coffee", "family-coffee", "near-test", "coffee bean guide", 5),
        ("q-test-espresso", "family-espresso", "near-test", "espresso grind size", 6),
    ]
    documents = [[("apple-guide", 3), ("banana-guide", 0), ("apple-history", 1)],
                 [("banana-guide", 3), ("apple-guide", 0)],
                 [("citrus-guide", 3), ("coffee-guide", 0)],
                 [("orange-guide", 3), ("banana-guide", 0)],
                 [("coffee-guide", 3), ("orange-guide", 0)],
                 [("espresso-guide", 3), ("citrus-guide", 0)]]
    first = [judgment(query, document, sequence)
             for sequence, (query, document) in enumerate(
                 ((query, document) for query, docs in zip(queries, documents) for document in docs), start=1)]
    second = [judgment(queries[0], documents[0][2], len(first) + 1, revision=2, retracted=True),
              judgment(queries[2], documents[2][1], len(first) + 2, revision=2, grade=1)]
    destination = ROOT / "fixtures"
    destination.mkdir(exist_ok=True)
    for name, rows in (("v1", first), ("v2", second)):
        (destination / f"{name}.jsonl").write_bytes(batch(name, rows))


if __name__ == "__main__":
    main()
