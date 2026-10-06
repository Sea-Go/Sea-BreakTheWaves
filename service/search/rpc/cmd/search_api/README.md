# search_api——B1 btw-search-api 的 dev 形态

把端到端检索管线（`internal/pipeline`：B2 判档规划 → B3 三路检索 →
B5 EvidencePack → B6 摘要/引用）暴露为 HTTP 端点，让前端在真实 A1
网关就绪前即可按 **C-3 检索契约** 联调，把 chat 页的 BFF 503 退化为
可交互。

- **不引 go-zero 框架**：纯 `net/http`，保持 dev 轻量；装配与
  `cmd/search_demo` 同构（启动时 `devseed.LoadCorpus`（冻结种子集，
  20 篇文档）→ `retrieval.Store` → `pipeline.NewDefaultPipeline`，
  FakeEncoder + 确定性摘要 stub，全链路无外部依赖、同输入同输出）。
- **CORS 全开放**（`Access-Control-Allow-Origin: *`）：允许
  `http://localhost:3000` 的前端页直连。
- 真实化路径：换 go-zero 网关 + 真 Encoder/Summarizer（经 D1）时，
  本进程整体退役，契约不变。

## 启动

仓库根执行（种子集路径相对仓库根解析，与 search_demo 同口径）：

```bash
go run ./service/search/rpc/cmd/search_api            # 默认 :8300
go run ./service/search/rpc/cmd/search_api --port 8400 # 覆盖端口
go run ./service/search/rpc/cmd/search_api --seeds testdata/index/seeds
```

启动横幅（stderr 日志）：

```
search_api dev: listening on :8300 seeds=testdata/index/seeds docs=20 encoder=fake-encoder-v1 manifest_id=…
```

## 端点

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/search` | C-3 检索：`{query, tier, delivery}` → JSON 交付 |
| GET | `/health` | 存活探测 → `{"status":"ok"}` |

### 请求（C-3 的 dev 子集）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `query` | string | 是 | 查询文本（TrimSpace 后非空，空则 400） |
| `tier` | string | 否 | `fast\|balanced\|deep`；缺省 `balanced`；未知值 400 |
| `delivery` | string | 否 | `summary\|tools`；缺省 `summary`；未知值 400 |

C-3 请求侧的 `module_scope`/`budget{max_ms,max_model_calls}` 在 dev
形态**接受但忽略**（宽松解码，不报未知字段错）。

### 响应

- **summary 交付**：`{evidence_pack, session_id, citations, answer, formatted_answer}`
  - `evidence_pack`：B5 证据包（`query_id` + 候选 `doc_key/rrf_score/lanes/evidence[]`）；
  - `session_id`：会话标识 `s-<12hex>`（逐请求生成）；
  - `citations`：引用列表（C-3 字段 `doc_key/revision_id/locator/quote` +
    前端挂锚点用的 `index`，对应答案内 `[n]` 角标与 `#cit-n`）；
  - `answer`：B6 答案，内嵌 `[n]` 行内角标；
  - `formatted_answer`：Markdown 形态（`[n]` → `[n](#cit-n)` 锚点链接）。
- **tools 交付**：`{evidence_pack}`（B5 直返，不经 B6 摘要）。
- 错误信封（与 RTW 前端 BFF 同形态）：`{"code":<int>,"msg":"…","data":null}`；
  空查询/未知档位/未知交付/坏 JSON → 400，请求体超 1MiB → 413，
  方法不符 → 405，管线失败 → 500。

## curl 示例

```bash
# 存活探测
curl -s http://localhost:8300/health
# → {"status":"ok"}

# summary 交付（balanced 档；种子集语料为「海洋观测/城市交通/气候能源」研究纪要）
curl -s -X POST http://localhost:8300/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"海洋观测中观测网络建设的核心要点有哪些？","tier":"balanced","delivery":"summary"}'
```

summary 响应样例（`evidence_pack.candidates` 较长，此处折叠）：

```json
{
  "evidence_pack": {
    "query_id": "q-21086475ff61",
    "candidates": [
      { "doc_key": "doc-02", "rrf_score": 0.0164,
        "lanes": { "dense": 0.198, "sparse": 0.0, "multi": 0.0, "rerank": 0.0 },
        "evidence": [ { "revision_id": "rev-…", "section_path": ["…"], "para_index": 0, "quote": "…" } ] }
    ]
  },
  "session_id": "s-5128711fb13f",
  "citations": [
    { "index": 1, "doc_key": "doc-00", "revision_id": "rev-fac0fe30931e879a",
      "locator": { "revision_id": "rev-fac0fe30931e879a", "section_path": ["海洋观测研究纪要（第 1 卷）", "海洋观测的背景与约束"], "para_index": 0, "quote": "海洋观测的第 1 个要点是建立长期稳定的观测基线……" },
      "quote": "海洋观测的第 1 个要点是建立长期稳定的观测基线……" }
  ],
  "answer": "根据 20 篇文档……「海洋观测的第 1 个要点是建立长期稳定的观测基线……」[1]",
  "formatted_answer": "根据 20 篇文档……「海洋观测的第 1 个要点是建立长期稳定的观测基线……」[1](#cit-1)"
}
```

```bash
# tools 交付（Agent 工具口径：EvidencePack 直返，顶层只有 evidence_pack）
curl -s -X POST http://localhost:8300/api/v1/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"城市交通的要点","tier":"fast","delivery":"tools"}' | python3 -m json.tool
# 顶层键 = ['evidence_pack']，candidates=11

# 校验失败
curl -s -X POST http://localhost:8300/api/v1/search \
  -H 'Content-Type: application/json' -d '{"query":"","tier":"fast","delivery":"summary"}'
# → {"code":400,"data":null,"msg":"query 不能为空"}
curl -s -X POST http://localhost:8300/api/v1/search \
  -H 'Content-Type: application/json' -d '{"query":"x","tier":"turbo"}'
# → {"code":400,"data":null,"msg":"未知档位 \"turbo\"（合法值 fast|balanced|deep）"}

# CORS 预检（前端 localhost:3000 直连）
curl -s -i -X OPTIONS http://localhost:8300/api/v1/search \
  -H 'Origin: http://localhost:3000' -H 'Access-Control-Request-Method: POST'
# → 204 + Access-Control-Allow-Origin: *（GET/POST/OPTIONS；Content-Type/Authorization）
```

## 前端接线（Sea-RideTheWind-Fronted）

1. **消除 BFF 503**：前端 `.env`（或启动环境）设置
   `SEA_PRODUCT_API_SERVER_URL=http://localhost:8300`
   （根地址，不带 `/api/v1`）。未设置时 `/api/chat`、`/api/sea/*` 返回
   `503 {"code":503,"msg":"产品服务尚未配置，请设置 SEA_PRODUCT_API_SERVER_URL","data":null}`；
   设置后 BFF 即开始向本服务转发。
2. **直连检索端点**（推荐，零网关改动）：浏览器/客户端直接
   `POST http://localhost:8300/api/v1/search`（CORS 已放行）：

   ```ts
   const res = await fetch("http://localhost:8300/api/v1/search", {
     method: "POST",
     headers: { "Content-Type": "application/json" },
     body: JSON.stringify({ query, tier, delivery: "summary" }),
   });
   const { answer, formatted_answer, citations, evidence_pack, session_id } = await res.json();
   // formatted_answer 的 [n](#cit-n) 锚点 ↔ citations[].index ↔ CitationCard 侧栏
   ```

   chat 页消费建议：`formatted_answer` 渲染正文（角标可点）、
   `citations` 填引用侧栏、`evidence_pack.candidates` 填证据面板。
3. **经 BFF 代理的路径**：`/api/sea/[...path]` 的白名单当前为
   `learning/…|intelligence/search`（转发为 `${base}/v1/<path>`），与
   `/api/v1/search` 不同路径；`/api/chat` 期待上游 `POST /api/v1/chat`
   （SSE：`message {id,role,part,seq?}` / `done {}` / `error {message}`），
   本 dev 服务**未实现**该端点（summary 管线非流式）。设置 env 后 chat 页
   从 503 变为可交互 UI（上游 404 以错误横幅呈现、可重试）；把 chat 流
   接到本端点需前端侧改调 `/api/v1/search`（或后续给 dev 服务补
   `/api/v1/chat` SSE 垫片，事件契约见 ChatStream）。

## 测试

```bash
go test -race ./service/search/rpc/cmd/search_api/
```

覆盖：summary/tools 两交付正例（响应形态断言到顶层键集合）、空查询/
未知档位/未知交付/坏 JSON → 400 信封、health 200、CORS 预检与
Allow-Origin 头、非 POST 方法 405、tier/delivery 缺省值。
