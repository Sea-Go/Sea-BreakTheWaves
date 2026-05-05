---
name: zhihu-search
description: 搜索知乎站内内容，并返回适合 Agent 消费的结构化 JSON 结果，包括标题、链接、作者、摘要、赞同数、评论数和编辑时间。Use when the user asks to search Zhihu, find answers/articles on 知乎, or retrieve Zhihu content by keyword.
---

# Zhihu Search Skill

## 概述
使用本 skill 调用知乎开放平台的 `zhihu_search` API，检索知乎站内内容，并把响应整理为精简 JSON。

当用户需要“搜知乎”“找知乎回答/文章”“查知乎上关于某个主题的内容”时，先加载本 skill，然后运行脚本。

## 认证
使用环境变量 `ZHIHU_ACCESS_SECRET` 进行 Bearer 认证。用户需要先在知乎开放平台控制台获取 Access Secret。

可选配置：

- `ZHIHU_OPENAPI_BASE_URL`（默认：`https://developer.zhihu.com`）
- `ZHIHU_ZHIHU_SEARCH_URL`（完整 endpoint 覆盖；设置后优先于 `ZHIHU_OPENAPI_BASE_URL` + 默认 path，适用于预发/代理/自定义网关）

本地开发时可以先加载仓库根目录的 `.env.local`：

```bash
source .env.local
```

## 快速开始

在 `skill_run` 中从 skill 工作目录执行：

```bash
python scripts/zhihu-search.py --query "如何理解 rave 文化" --count 5
```
注意：运行命令必须使用 `python`，不要使用 `python3`。

## 输入约定

推荐使用命令行参数：

```bash
python scripts/zhihu-search.py --query "..." --count 5
```

也兼容传入一个 JSON 参数：

```json
{"query":"...", "count":10}
```

规则：

- `query` 必填，且不能是空字符串（会自动 `strip`）。
- `count` 可选；脚本会自动限制到 1-10。
- 字段名同时兼容小写和知乎 API 风格大写：`query`/`Query`、`count`/`Count`。

## 输出约定

### 成功

只输出 JSON，字段包括：

- `code`, `message`
- `item_count`
- `items[]`，包含 `title`, `summary`, `url`, `author_name`, `vote_up_count`, `comment_count`, `edit_time`

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

## 使用建议

- 优先把用户问题改写成短搜索词；不要把很长的上下文原样塞进 `query`。
- 默认 `count` 用 5；用户明确要求更多时最多传 10。
- 如果结果为空，换一组更短、更核心的关键词再试一次。
- 向用户总结结果时，保留标题和链接，不要伪造脚本没有返回的信息。

## 参考文档

更多 API 字段和脚本行为见 `references/zhihu-search-api.md`。
