"""Exact, finite JSON and digest utilities shared by export and serving verification."""

from __future__ import annotations

from hashlib import sha256
import json


class ExportError(ValueError):
    """A candidate or its serving artifact fails the export contract."""


def canonical(value: object) -> bytes:
    try:
        return json.dumps(value, sort_keys=True, separators=(",", ":"),
                          ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise ExportError("non-finite or unsupported artifact value") from exc


def digest(value: bytes) -> str:
    return sha256(value).hexdigest()


def read_json(data: bytes) -> dict:
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ExportError("duplicate artifact key")
            result[key] = value
        return result

    def finite(_):
        raise ExportError("non-finite artifact value")

    try:
        value = json.loads(data, object_pairs_hook=unique, parse_constant=finite)
    except (UnicodeError, ValueError) as exc:
        raise ExportError("invalid artifact JSON") from exc
    if not isinstance(value, dict):
        raise ExportError("artifact must be an object")
    return value
