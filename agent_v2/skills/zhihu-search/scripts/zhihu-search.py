#!/usr/bin/env python
"""Zhihu search skill script (Python stdlib only)."""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any, Dict, NoReturn
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


DEFAULT_BASE_URL = "https://developer.zhihu.com"
REQUEST_TIMEOUT_SECONDS = 5
MAX_SUMMARY_CHARS = 900

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")


def print_usage() -> None:
    print(
        "Usage:\n"
        "  python zhihu-search.py --query '如何理解 rave 文化' --count 5\n"
        "  python zhihu-search.py "
        '\'{"query":"如何理解 rave 文化","count":5}\'\n\n'
        "Environment:\n"
        "  ZHIHU_ACCESS_SECRET      Bearer auth token\n"
        "  ZHIHU_OPENAPI_BASE_URL   Optional, default https://developer.zhihu.com\n"
        "  ZHIHU_ZHIHU_SEARCH_URL   Optional full endpoint override\n"
    )


def die(message: str, *, body: Any | None = None) -> NoReturn:
    payload: Dict[str, Any] = {"error": message, "code": 1}
    if body is not None:
        payload["body"] = body
    print(json.dumps(payload, ensure_ascii=False))
    raise SystemExit(1)


def parse_payload(raw: str) -> Dict[str, Any]:
    try:
        data = json.loads(raw)
    except json.JSONDecodeError:
        die("Invalid JSON payload")
    if not isinstance(data, dict):
        die("Invalid JSON payload")
    return data


def parse_args(argv: list[str]) -> Dict[str, Any]:
    if len(argv) == 1 and argv[0].lstrip().startswith("{"):
        return parse_payload(argv[0])

    payload: Dict[str, Any] = {}
    i = 0
    while i < len(argv):
        arg = argv[i]
        if arg in {"--query", "-q"}:
            if i+1 >= len(argv):
                die("query is required")
            payload["query"] = argv[i+1]
            i += 2
            continue
        if arg in {"--count", "-c"}:
            if i+1 >= len(argv):
                die("count value is required")
            payload["count"] = argv[i+1]
            i += 2
            continue
        die(f"Unknown argument: {arg}")
    return payload


def parse_query(payload: Dict[str, Any]) -> str:
    query = payload.get("query") or payload.get("Query") or ""
    if not isinstance(query, str) or not query.strip():
        die("query is required")
    query = query.strip()
    return query


def parse_count(payload: Dict[str, Any]) -> int:
    raw = payload.get("count", payload.get("Count", 10))
    try:
        count = int(raw)
    except (TypeError, ValueError):
        count = 10
    return max(1, min(10, count))


def get_endpoint() -> str:
    explicit = os.getenv("ZHIHU_ZHIHU_SEARCH_URL", "").strip()
    if explicit:
        return explicit
    base_url = os.getenv("ZHIHU_OPENAPI_BASE_URL", DEFAULT_BASE_URL).strip()
    return f"{base_url.rstrip('/')}/api/v1/content/zhihu_search"


def build_result(api_resp: Dict[str, Any]) -> Dict[str, Any]:
    data = api_resp.get("Data") if isinstance(api_resp.get("Data"), dict) else {}
    items = data.get("Items") if isinstance(data.get("Items"), list) else []
    normalized_items = []
    for item in items:
        if not isinstance(item, dict):
            continue
        summary = str(item.get("ContentText", "") or "")
        if len(summary) > MAX_SUMMARY_CHARS:
            summary = f"{summary[:MAX_SUMMARY_CHARS].rstrip()}..."

        normalized_items.append(
            {
                "title": item.get("Title", ""),
                "url": item.get("Url", ""),
                "author_name": item.get("AuthorName", ""),
                "summary": summary,
                "vote_up_count": item.get("VoteUpCount", 0),
                "comment_count": item.get("CommentCount", 0),
                "edit_time": item.get("EditTime", 0),
            }
        )

    return {
        "code": api_resp.get("Code", -1),
        "message": api_resp.get("Message", ""),
        "item_count": len(normalized_items),
        "items": normalized_items,
    }


def request_zhihu(query: str, count: int) -> Dict[str, Any]:
    secret = os.getenv("ZHIHU_ACCESS_SECRET", "").strip()
    if not secret:
        die("Set ZHIHU_ACCESS_SECRET first (Bearer auth only)")

    params = urlencode({"Query": query, "Count": str(count)})
    url = f"{get_endpoint()}?{params}"
    req = Request(
        url=url,
        method="GET",
        headers={
            "Authorization": f"Bearer {secret}",
            "Accept": "application/json",
            "X-Request-Timestamp": str(int(time.time())),
        },
    )

    try:
        with urlopen(req, timeout=REQUEST_TIMEOUT_SECONDS) as resp:
            body_text = resp.read().decode("utf-8", errors="replace")
    except HTTPError as err:
        body_text = err.read().decode("utf-8", errors="replace")
        die(f"HTTP {err.code}", body=body_text)
    except (URLError, TimeoutError) as err:
        die("HTTP request failed (timeout or network error)", body=str(err))

    try:
        return json.loads(body_text)
    except json.JSONDecodeError:
        die("Non-JSON response from API", body=body_text[:500])


def main() -> None:
    if len(sys.argv) >= 2 and sys.argv[1] in {"-h", "--help"}:
        print_usage()
        return

    if len(sys.argv) < 2:
        print_usage()
        raise SystemExit(1)

    payload = parse_args(sys.argv[1:])
    query = parse_query(payload)
    count = parse_count(payload)

    api_resp = request_zhihu(query, count)

    result = build_result(api_resp)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
