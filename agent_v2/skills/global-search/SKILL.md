---
name: global-search
description: 搜索全网内容，返回脚本整理后的结构化结果（标题、链接、作者、摘要等）
homepage: ../../docs/global_search.md
metadata: {"openclaw":{"emoji":"🌐","requires":{"bins":["python3"]}}}
---

# Global Search Skill

## 概述
本 Skill 用于调用知乎开放平台的 `global_search` API。
完整的 API 文档请参考：https://developer.zhihu.com/docs

通过 OpenAPI Platform `GET /api/v1/content/global_search` 检索全网内容，并把响应整理为适合 agent 消费的精简 JSON 结构。

## 认证
使用环境变量 `ZHIHU_ACCESS_SECRET` 进行认证
用户可以在知乎开放平台控制台获取 Access Secret

可选配置：

- `ZHIHU_OPENAPI_BASE_URL`（默认：`https://developer.zhihu.com`）
- `ZHIHU_GLOBAL_SEARCH_URL`（完整 endpoint 覆盖；设置后优先于 `ZHIHU_OPENAPI_BASE_URL` + 默认 path）

## 快速开始

`{baseDir}` 是 agent 框架在运行时自动替换的变量，指向当前 skill 目录的绝对路径（即 `skills/global-search/`）。

```bash
python3 {baseDir}/scripts/global-search.py '{"query":"如何理解 rave 文化","count":8}'
```

## 输入约定

传入一个 JSON 参数：

```json
{"query":"...", "count":10}
```

规则：

- `query` 必填，且不能是空字符串。
- `count` 可选；脚本会自动限制到 1-20（接口上限）。

## 输出约定

### 成功

返回 JSON，包括：

- `code`, `message`
- `item_count`
- `items[]`，包含 `title`, `summary`, `url`, `author_name`, `edit_time`

### 失败

`error` 字段为动态错误描述，常见情况：

```json
{"error":"query is required","code":1}
{"error":"Invalid JSON payload","code":1}
{"error":"Set ZHIHU_ACCESS_SECRET first (Bearer auth only)","code":1}
{"error":"HTTP request failed (timeout or network error)","code":1}
```

HTTP 非 2xx 时额外携带 `body`：

```json
{"error":"HTTP 403","body":"Forbidden","code":1}
```
