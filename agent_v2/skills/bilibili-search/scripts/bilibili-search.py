#!/usr/bin/env python
"""Bilibili video search skill backed by bilibili-api-python."""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
from html import unescape
from typing import Any, Dict, NoReturn

MAX_SUMMARY_CHARS = 700

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")


def print_usage() -> None:
    print(
        "Usage:\n"
        "  python bilibili-search.py --query '成都旅游攻略' --count 10\n"
        "  python bilibili-search.py "
        '\'{"query":"成都旅游攻略","count":10}\'\n\n'
        "Environment:\n"
        "  BILIBILI_COOKIE   Optional raw Bilibili cookie string\n"
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
            if i + 1 >= len(argv):
                die("query is required")
            payload["query"] = argv[i + 1]
            i += 2
            continue
        if arg in {"--count", "-c"}:
            if i + 1 >= len(argv):
                die("count value is required")
            payload["count"] = argv[i + 1]
            i += 2
            continue
        die(f"Unknown argument: {arg}")
    return payload


def parse_query(payload: Dict[str, Any]) -> str:
    query = payload.get("query") or payload.get("Query") or ""
    if not isinstance(query, str) or not query.strip():
        die("query is required")
    return query.strip()


def parse_count(payload: Dict[str, Any]) -> int:
    raw = payload.get("count", payload.get("Count", 10))
    try:
        count = int(raw)
    except (TypeError, ValueError):
        count = 10
    return max(1, min(20, count))


def clean_text(value: Any) -> str:
    text = str(value or "")
    text = re.sub(r"<[^>]+>", "", text)
    text = unescape(text)
    text = re.sub(r"\s+", " ", text).strip()
    if len(text) > MAX_SUMMARY_CHARS:
        text = f"{text[:MAX_SUMMARY_CHARS].rstrip()}..."
    return text


def as_int(value: Any) -> int:
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)):
        return int(value)
    text = str(value or "").strip().lower()
    if not text:
        return 0
    multiplier = 1
    if text.endswith("万"):
        multiplier = 10000
        text = text[:-1]
    try:
        return int(float(text) * multiplier)
    except ValueError:
        return 0


def first_value(item: Dict[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in item and item[key] not in (None, ""):
            return item[key]
    return ""


def normalize_item(item: Dict[str, Any]) -> Dict[str, Any]:
    bvid = str(first_value(item, "bvid", "bvid_str", "id")).strip()
    arcurl = str(first_value(item, "arcurl", "url")).strip()
    if arcurl.startswith("//"):
        arcurl = "https:" + arcurl
    if not arcurl and bvid.startswith("BV"):
        arcurl = "https://www.bilibili.com/video/" + bvid

    summary = clean_text(first_value(item, "description", "desc", "content", "tag"))
    title = clean_text(first_value(item, "title", "name"))

    return {
        "title": title,
        "url": arcurl,
        "bvid": bvid,
        "author_name": clean_text(first_value(item, "author", "uname", "up_name")),
        "summary": summary,
        "view_count": as_int(first_value(item, "play", "view", "stat_view")),
        "danmaku_count": as_int(first_value(item, "danmaku", "dm", "stat_danmaku")),
        "like_count": as_int(first_value(item, "like", "stat_like")),
        "favorite_count": as_int(first_value(item, "favorites", "favorite", "stat_favorite")),
        "publish_time": as_int(first_value(item, "pubdate", "senddate", "created")),
        "duration": str(first_value(item, "duration")).strip(),
        "cover_url": str(first_value(item, "pic", "cover")).strip(),
    }


def extract_items(resp: Any) -> list[Dict[str, Any]]:
    if isinstance(resp, dict):
        data = resp.get("result")
        if isinstance(data, list):
            return [item for item in data if isinstance(item, dict)]
        data = resp.get("data")
        if isinstance(data, dict):
            result = data.get("result")
            if isinstance(result, list):
                return [item for item in result if isinstance(item, dict)]
            items = data.get("items")
            if isinstance(items, list):
                return [item for item in items if isinstance(item, dict)]
    return []


def build_credential() -> Any | None:
    cookie = os.getenv("BILIBILI_COOKIE", "").strip()
    if not cookie:
        return None
    try:
        from bilibili_api import Credential
    except Exception:
        return None

    parts: Dict[str, str] = {}
    for chunk in cookie.split(";"):
        if "=" in chunk:
            key, value = chunk.split("=", 1)
            parts[key.strip()] = value.strip()
    return Credential(
        sessdata=parts.get("SESSDATA", ""),
        bili_jct=parts.get("bili_jct", ""),
        buvid3=parts.get("buvid3", ""),
        dedeuserid=parts.get("DedeUserID", ""),
        ac_time_value=parts.get("ac_time_value", ""),
    )


async def request_bilibili(query: str, count: int) -> Dict[str, Any]:
    try:
        from bilibili_api import search
    except Exception as err:
        die("bilibili-api-python is not installed; run pip install bilibili-api-python", body=str(err))

    page_size = min(20, count)
    credential = build_credential()
    search_type = getattr(search, "SearchObjectType", None)
    video_type = getattr(search_type, "VIDEO", None) if search_type is not None else None

    kwargs: Dict[str, Any] = {"keyword": query, "page": 1}
    if video_type is not None:
        kwargs["search_type"] = video_type
    if credential is not None:
        kwargs["credential"] = credential

    try:
        resp = await search.search_by_type(**kwargs)
    except TypeError:
        kwargs.pop("credential", None)
        resp = await search.search_by_type(**kwargs)
    except Exception as err:
        die("Bilibili search failed", body=str(err))

    items = [normalize_item(item) for item in extract_items(resp)]
    items = [item for item in items if item["title"] or item["bvid"] or item["url"]]
    return {
        "code": 0,
        "message": "success",
        "item_count": min(len(items), page_size),
        "items": items[:page_size],
    }


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
    result = asyncio.run(request_bilibili(query, count))
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
