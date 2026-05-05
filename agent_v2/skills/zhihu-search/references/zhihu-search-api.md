# Zhihu Search API Reference

## Endpoint

Default endpoint:

```text
GET https://developer.zhihu.com/api/v1/content/zhihu_search
```

The script can override the endpoint with:

- `ZHIHU_ZHIHU_SEARCH_URL`: full endpoint URL.
- `ZHIHU_OPENAPI_BASE_URL`: base URL used with `/api/v1/content/zhihu_search`.

## Authentication

The script sends:

```text
Authorization: Bearer ${ZHIHU_ACCESS_SECRET}
X-Request-Timestamp: <unix timestamp>
```

If `ZHIHU_ACCESS_SECRET` is missing, the script exits with JSON:

```json
{"error":"Set ZHIHU_ACCESS_SECRET first (Bearer auth only)","code":1}
```

## Query Parameters

- `Query`: required search text.
- `Count`: optional result count. The wrapper clamps it to `1..10`.

## Normalized Output

The script normalizes upstream `Data.Items` into:

```json
{
  "code": 0,
  "message": "success",
  "item_count": 1,
  "items": [
    {
      "title": "...",
      "url": "...",
      "author_name": "...",
      "summary": "...",
      "vote_up_count": 0,
      "comment_count": 0,
      "edit_time": 0
    }
  ]
}
```

The wrapper intentionally keeps only stable fields useful for an agent answer.
