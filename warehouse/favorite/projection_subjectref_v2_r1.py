"""Build a new immutable CH row set from frozen ODS and approved PG sidecar rows."""
from __future__ import annotations

import hashlib
import json
import re

ODS_FIELDS = ("producer", "source_offset", "event_id", "event_type", "authority_id",
              "tenant_id", "subject_id", "favorite_id", "folder_id", "target_type",
              "target_id", "target_revision", "operation", "predecessor_event_id",
              "event_time", "available_at", "dc_received_at", "source_event_hash",
              "technical_receipt", "event_spec")
MAPPING_FIELDS = ("producer", "source_offset", "event_id", "authority_id", "tenant_id",
                  "subject_id", "issuer", "subject_uid")
UID = re.compile(r"[1-9][0-9]*\Z")


def strict_json(value: bytes | str) -> object:
    def unique_pairs(pairs: list[tuple[str, object]]) -> dict:
        result = {}
        for key, item in pairs:
            if key in result:
                raise ValueError("JSON contains a duplicate key")
            result[key] = item
        return result

    def invalid_constant(value: str) -> None:
        raise ValueError(f"JSON contains non-finite number {value}")

    return json.loads(value, object_pairs_hook=unique_pairs,
                      parse_constant=invalid_constant)


def digest(body: bytes) -> str:
    return hashlib.sha256(body).hexdigest()


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"),
                      allow_nan=False).encode("utf-8")


def parse_lines(body: bytes, fields: tuple[str, ...]) -> list[dict]:
    if not body or not body.endswith(b"\n"):
        raise ValueError("JSONL must have a final newline")
    values = [strict_json(line) for line in body.splitlines()]
    if not values or any(not isinstance(v, dict) or set(v) != set(fields) for v in values):
        raise ValueError("row shape differs from pinned contract")
    if any(type(v["source_offset"]) is not int or v["source_offset"] < 1 for v in values):
        raise ValueError("source_offset must be a positive JSON integer")
    return values


def original_proof(row: dict) -> None:
    if row["producer"] != "rtw.community.favorite" or row["source_offset"] < 1:
        raise ValueError("favorite source identity differs")
    if row["authority_id"] != "rtw.identity" or row["tenant_id"] != "platform":
        raise ValueError("historic v1 identity slot differs")
    uid = row["subject_id"]
    if not isinstance(uid, str) or not UID.fullmatch(uid) or int(uid) > 2**63 - 1:
        raise ValueError("historic RTW UID is not canonical positive int64")
    if not isinstance(row["event_spec"], str) or not isinstance(row["technical_receipt"], str):
        raise ValueError("frozen source proof must be JSON text")
    spec = strict_json(row["event_spec"])
    receipt = strict_json(row["technical_receipt"])
    if (not isinstance(spec, dict) or not isinstance(receipt, dict) or
            type(receipt.get("offset")) is not int):
        raise ValueError("frozen EventSpec or receipt structure differs")
    payload = spec.get("payload", {})
    if digest(canonical(spec)) != row["source_event_hash"] or \
            receipt.get("input_hash") != row["source_event_hash"] or \
            receipt.get("offset") != row["source_offset"] or \
            receipt.get("event_id") != row["event_id"] or \
            receipt.get("producer") != row["producer"] or \
            receipt.get("received_at") != row["dc_received_at"] or \
            receipt.get("technical_status") != "accepted" or \
            spec.get("event_id") != row["event_id"] or \
            spec.get("producer") != row["producer"] or \
            spec.get("event_type") != row["event_type"] or \
            payload.get("subject_ref") != {
                "authority_id": row["authority_id"], "tenant_id": row["tenant_id"],
                "subject_id": uid} or \
            spec.get("aggregate_id") != row["favorite_id"] or \
            spec.get("occurred_at") != row["event_time"] or \
            payload.get("event_id") != row["event_id"] or \
            payload.get("operation") != row["operation"] or \
            payload.get("favorite_id") != row["favorite_id"] or \
            payload.get("folder_id") != row["folder_id"] or \
            payload.get("target_type") != row["target_type"] or \
            payload.get("target_id") != row["target_id"] or \
            payload.get("target_revision") != row["target_revision"] or \
            payload.get("event_time") != row["event_time"] or \
            payload.get("available_at") != row["available_at"]:
        raise ValueError("frozen EventSpec/JCS or technical receipt differs")


def project(ods: bytes, mappings: bytes) -> bytes:
    """The caller must supply mappings exported after the official locked PG preflight.

    This function checks the immutable row and every PG anchor; it does not
    authorize an unverified PG database or manufacture missing mappings.
    """
    old = parse_lines(ods, ODS_FIELDS)
    sidecar = parse_lines(mappings, MAPPING_FIELDS)
    if len(old) != len(sidecar):
        raise ValueError("sidecar count differs from frozen ODS")
    index: dict[tuple[str, int, str], dict] = {}
    source_offsets: set[tuple[str, int]] = set()
    event_ids: set[tuple[str, str]] = set()
    for row in old:
        original_proof(row)
        key = row["producer"], row["source_offset"], row["event_id"]
        if key in index or key[:2] in source_offsets or (key[0], key[2]) in event_ids:
            raise ValueError("old source event or offset duplicated")
        index[key] = row
        source_offsets.add(key[:2])
        event_ids.add((key[0], key[2]))
    for row in old:
        if row["operation"] == "assert":
            if row["predecessor_event_id"] or row["event_type"] != "rtw.favorite.assert":
                raise ValueError("assert predecessor differs")
            continue
        if row["operation"] != "retract" or row["event_type"] != "rtw.favorite.retract":
            raise ValueError("favorite operation differs")
        predecessor = next((a for a in old if a["producer"] == row["producer"] and
                            a["event_id"] == row["predecessor_event_id"]), None)
        if (predecessor is None or predecessor["operation"] != "assert" or
                predecessor["source_offset"] >= row["source_offset"] or
                any(predecessor[name] != row[name] for name in
                    ("authority_id", "tenant_id", "subject_id", "favorite_id",
                     "folder_id", "target_type", "target_id", "target_revision"))):
            raise ValueError("favorite retract predecessor differs")
    projections: dict[tuple[str, int, str], dict] = {}
    for mapping in sidecar:
        key = mapping["producer"], mapping["source_offset"], mapping["event_id"]
        row = index.get(key)
        if row is None or key in projections:
            raise ValueError("sidecar source anchor missing or duplicated")
        if any(mapping[name] != row[name] for name in MAPPING_FIELDS[:6]):
            raise ValueError("sidecar original identity differs")
        if mapping["issuer"] != "rtw.identity" or mapping["subject_uid"] != row["subject_id"]:
            raise ValueError("sidecar canonical issuer/UID differs")
        projections[key] = dict(row, issuer=mapping["issuer"],
                                subject_uid=mapping["subject_uid"],
                                origin_ods_sha256=digest(ods))
    if set(projections) != set(index):
        raise ValueError("sidecar mapping is incomplete")
    return b"".join(canonical(projections[key]) + b"\n" for key in sorted(projections))


def fixture_mapping(ods: bytes) -> bytes:
    """Only for isolated CH/dbt tests; not an export from the PG sidecar."""
    return b"".join(canonical({name: row[name] for name in MAPPING_FIELDS[:6]} |
                               {"issuer": "rtw.identity", "subject_uid": row["subject_id"]}) +
                    b"\n" for row in parse_lines(ods, ODS_FIELDS))
