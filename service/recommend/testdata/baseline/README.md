# 回归基线黄金文件（Golden Files）

本目录存放推荐服务 `/api/v2/recommend` 接口的回归基线快照（黄金文件），
用于在 `feature/trpc-agent-go-recommendation-refactor` 破坏式重构后验证 v2 契约稳定性。

## 当前状态

**已生成 20 条合成基线快照**（`case_01.json` ~ `case_20.json`）。

由于本批黄金文件最初生成时服务依赖未完全就绪，因此当前快照为
**基于 v2 接口 schema 合成的代表性快照**（非真实 HTTP 录制）。
所有字段名、类型、嵌套结构均严格对齐：

- 请求 schema：`internal/recommendationv2/types.go` 中的 `RecommendRequest`
  （`tenant_id` / `request_id` / `scenario` / `channel` / `user` / `query` / `context` / `top_k` / `path_mode` / `config` / `debug` / `extension`）
- 响应 schema：`router/response.go` 中的统一 `Resp` 包装
  （`code` / `msg` / `detail?` / `trace_id?` / `data?`），`data` 为 v2 `RecommendResponse`
  （`request_id` / `trace_id` / `path_taken` / `items` / `explanations` / `cost` / `metrics` / `steps` / `fallback` / `extension`）
- `steps` 为 v2 Graph 节点链路，对齐 `RecommendationRuntime` 的 `TraceStep`

> 待代理恢复后，可用 `cmd/recrecorder` 对真实服务发起请求并用真实响应替换本批合成文件；
> 替换时请保持 `{name, request, response}` 三段式结构与文件名不变。

## 黄金文件结构

每个 `case_XX.json` 为单条 JSON，结构如下：

```json
{
  "name": "case_01_dashboard_game_explain",
  "request": {
    "tenant_id": "default",
    "request_id": "...",
    "scenario": "recommend",
    "channel": "...",
    "user": { "user_id": "...", "session_id": "..." },
    "query": "...",
    "top_k": 10,
    "path_mode": "hybrid",
    "debug": true
  },
  "response": {
    "code": 200,
    "msg": "success",
    "data": {
      "request_id": "...",
      "trace_id": "...",
      "path_taken": "hybrid",
      "items": [{"id": "art_1001", "article_id": "art_1001", "score": 1.0, "source": "content", "rank": 1}],
      "explanations": [{"item_id": "art_1001", "text": "..."}],
      "cost": {"estimated_amount": 0.01},
      "metrics": {"item_count": 10, "fallback": false},
      "steps": [{"name": "normalize_request", "status": "ok"}]
    }
  }
}
```

## 20 个测试用例覆盖

请求集合见 `../requests.json`。`cmd/recrecorder` 会把旧测试请求映射为 v2 请求：
`surface` 映射为 `channel`，`rec_request_id` 映射为 `request_id`，默认
`tenant_id=default`、`scenario=recommend`、`path_mode=hybrid`、`top_k=10`。
覆盖 4 种 channel、3 种 period_bucket、开启/关闭 debug、空 query 兜底、
时效性关键词触发 slow/hybrid 链路等场景。

| 序号 | 文件 | surface | 频道方向 | period_bucket | explain | 覆盖点 |
|------|------|---------|----------|---------------|---------|--------|
| 01 | case_01.json | dashboard_recommend | 游戏 | d1 | true | 默认首页推荐 + 完整解释链路 + 出池副作用 |
| 02 | case_02.json | dashboard_recommend | 旅行 | w1 | false | 周期桶 w1，无 explain_trace |
| 03 | case_03.json | search | 美食 | d1 | true | 搜索 surface + 解释 + 周期池触发 refill |
| 04 | case_04.json | search | 数码科技 | weekend | false | 周末桶，无 explain_trace |
| 05 | case_05.json | profile | 音乐 | d1 | true | 个人页 surface + 短期池 refill |
| 06 | case_06.json | profile | 电影 | w1 | false | 周期桶 w1，无 explain_trace |
| 07 | case_07.json | channel | 游戏 | d1 | true | 频道页 surface + 周期池 refill |
| 08 | case_08.json | channel | 旅行 | weekend | false | 周末桶频道页，无 explain_trace |
| 09 | case_09.json | dashboard_recommend | （空 query） | d1 | true | **空 query 走 fast_fallback 路径**，status=fallback |
| 10 | case_10.json | search | 时效新闻 | d1 | true | **含“最新/今日/价格/库存”触发 RAG+TOOL**，must_cite_sources=true |
| 11 | case_11.json | profile | 历史人文 | w1 | false | 长文读书类，无 explain_trace |
| 12 | case_12.json | channel | 美食 | d1 | true | 频道页美食 + 短期池 refill |
| 13 | case_13.json | dashboard_recommend | 户外运动 | weekend | true | 周末户外场景 + 出池副作用 |
| 14 | case_14.json | search | 价格库存 | d1 | true | **时效性关键词触发 RAG+TOOL + must_cite_sources** |
| 15 | case_15.json | profile | 摄影 | w1 | false | 兴趣类内容，无 explain_trace |
| 16 | case_16.json | channel | 科技数码 | d1 | true | 频道页数码 + 周期池 refill |
| 17 | case_17.json | dashboard_recommend | 读书 | w1 | false | 长周期阅读，无 explain_trace |
| 18 | case_18.json | search | 健身减脂 | weekend | true | 周末桶搜索 + 短期/周期池 refill |
| 19 | case_19.json | profile | 编程开发 | d1 | true | 技术类内容 + 长期池 refill |
| 20 | case_20.json | channel | 电影 | w1 | false | 频道页影视，无 explain_trace |

> 说明：当前 `RecommendRequest` 结构体只含 `surface / query / user_id / session_id /
> explain / period_bucket / rec_request_id` 字段，没有独立的 channel / tag / config
> override 字段。因此“频道方向”与“标签过滤”通过 `query` 文本前缀表达，
> “是否带额外配置”以 `explain` 开关近似覆盖，确保请求对真实类型结构合法。

## 对比回归差异

使用 `cmd/regrdiff` 工具对比基线与新响应：

```bash
# 在 recommendation/ 目录下执行

# 1) 重构后，先用同一批请求录制候选响应
go run ./cmd/recrecorder --addr http://localhost:20721 --out testdata/candidate

# 2) 与基线对比（默认即忽略 trace_id 等易变字段）
go run ./cmd/regrdiff -baseline testdata/baseline -new testdata/candidate

# 3) 追加忽略额外字段（如 latency_ms），并打印完整差异
go run ./cmd/regrdiff -baseline testdata/baseline -new testdata/candidate \
    -ignore latency_ms -verbose
```

### CLI 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-baseline <dir>` | `testdata/baseline` | 基线目录（黄金文件所在目录） |
| `-new <dir>` | （必填） | 新响应目录，待对比的响应所在目录 |
| `-ignore <fields>` | （空） | 额外忽略的字段名，逗号分隔（如 `trace_id,timestamp,latency_ms`） |
| `-verbose` | false | 打印完整逐字段差异（默认仅打印差异条数） |

### 对比时忽略的易变字段

即使不传 `-ignore`，以下字段也会被自动忽略（每次请求都会变化）：
- `trace_id`、`rec_request_id`、`request_id`、`search_request_id`
- `timestamp`、`created_at`、`updated_at`

> 注意：推荐结果内容字段 `items`、`items[].id`、`items[].article_id` 和 `items[].rank`
> **不会**被忽略，它们才是 v2 回归关注的核心。

### 退出码

- `0`：所有配对文件在忽略指定字段后完全一致（回归通过）
- `1`：存在差异、缺少配对文件或发生错误（回归失败）

### 自检（基线对自身）

```bash
# 把基线复制一份作为“新响应”，对比应全部通过（退出码 0）
cp -r testdata/baseline /tmp/reco_new
go run ./cmd/regrdiff -baseline testdata/baseline -new /tmp/reco_new -verbose
```
