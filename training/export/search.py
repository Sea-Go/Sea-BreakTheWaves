"""Declare the experimental lexical search candidate outside DC serving scope."""

from __future__ import annotations

from pathlib import Path

from .contract import ExportError, read_json


def assess_search_candidate(path: str | Path) -> dict:
    try:
        candidate = read_json(Path(path).read_bytes())
    except OSError as exc:
        raise ExportError("search candidate is unavailable") from exc
    if (candidate.get("status") != "candidate_only" or candidate.get("active") is not False or
            candidate.get("experimental") is not True or
            candidate.get("feature_version") != "lexical-pairwise-v0"):
        raise ExportError("not the declared experimental lexical search candidate")
    return {"status": "NOT_SUPPORTED", "serving_export": False,
            "dc_registration": False,
            "reason": "lexical pairwise ranker is not a Dense, learned Sparse, or Multi-vector encoder"}
