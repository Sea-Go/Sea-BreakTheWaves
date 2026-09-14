# 真实 BGE-M3 三路索引到 RTW 产品答案的隔离验收

日期：2026-09-14。BTW 基线 `a72adc0`，RTW 基线 `5c20dc0`，DC 基线 `e8c8cd68`。状态：**隔离环境真实 BGE-M3 → BTW 三路 local-exact 工件 → RTW 人工发布 → BTW 实际三路召回 → RTW 有引用产品答案子链 `INTEGRATED`；H06/H07/H02 整体仍 `PARTIAL`。**

工作区域：`[W0]` 本 BTW 独立工作树；`[W1]` `internal/clients/datacenter/representation_gate*`、本目录 opt-in 测试及说明、`cmd/api` 配置/装配/说明；`[R1]` DC 与 RTW 各自集成树及固定接口；`[D1]` Go 锁定模块和 BGE-M3 固定模型缓存；`[G1]` 无；`[X1]` 测试进程自建的回环 DC/RTW/User Center、隔离 PostgreSQL 和本地对象目录；`[N1]` 原始业务脏树、共享/生产数据库和 Docs；`[T1]` 两边测试脚本的随机临时目录。主职责 `[C8:VERIFY]`，跨 `[C6:INFRA]` 模型并发、`[C2:APPLICATION]` 搜索、`[C4:PERSISTENCE]` RTW 接纳与 `[C5:AGENT_ADAPTER]` 根 Runner。

## 实际运行的链路

DC `cmd/server` 既有真实 BGE 测试启动 hash 锁定的官方 `BAAI/bge-m3@5617a9f61b02` CPU Provider，在隔离 PG 中注册三个不同的 typed embedding 配置/调用点，经原生身份、控制面和网关给出 1024 维 Dense、250002 词表学习型 Sparse、1024 维 token matrix（`mean_maxsim`）。它把**仅供测试的**网关地址、短期令牌和配置 ID 写入 0600 `runtime.json`，等待消费者完成，不将令牌写入 Git 或验收说明。

RTW 的 opt-in HTTP/PG 测试用三个 DC profile 创建真实 release/build，固定同一 `build_id`、`generation` 和 release manifest hash。测试语料是一个来源段落 `Evidence` 与一个 Wiki 段落 `My interpretation`；两个 chunk 均携带 RTW 修订、对象 hash、locator、原文范围和正确的 encoding key。BTW 独立子进程从 RTW/BTW 共享的本地 SHA256 对象目录读取这一固定 chunk manifest，用真实 DC typed 客户端分别构建 Dense、Sparse、Multi-vector 工件，逐片执行三路自检，并输出含三路不可变 ref 的同代 index manifest。RTW 从自己的对象存储读回、校验每个 ref，然后按既有 `AcceptBuild` 变为 READY，并由管理员手动切换发布指针。

产品阶段 RTW 经真实 User Center 核定 UID，签发 `rtw.identity/platform/<UID>` 和固定发布快照；透明 HTTP 中继把原始 HMAC 范围送到 BTW。BTW 先验签，之后 `search.Service.Execute` 使用 RTW 发布的**实际三路 ref**及同一问题 `Evidence` 向三个本地 exact 索引查询。断言三路都执行、都有候选且排序首位为 RTW 来源 chunk，其融合候选保留三路 provenance；没有向搜索执行器注入候选。随后复核 RTW 当前有效修订、读取同版原文并取得耐久引用收据。固定测试模型只在收据存在后返回带真实 evidence ID 的 JSON；原生 tRPC-Agent-Go Graph/LLMAgent/Runner 提交产品轮次，RTW 从自身 PG 验证答案和引用后才返回 `succeeded`。

第一次联验暴露了真实容量冲突：搜索并行执行三路查询，单并发本地 BGE Provider 返回 429，两路失败，BTW 按 `ErrLane` 拒收，RTW 返回可重试 503。BTW 现在提供 `datacenter.RepresentationGate`，正式 `cmd/api` 从 `BTW_SEARCH_REPRESENTATION_MAX_IN_FLIGHT` 显式取得 1–32 的本进程共享门限；此次单并发 Provider 使用 `1`。排队取消在上游调用前结束。该门限不改变请求身份、错误或使用量收据；多进程总体容量仍须 DC/部署层治理。

复验命令在 DC 集成树启动 `BGE_HOLD_FOR_CONSUMER=1 BGE_SERVING_ROOT=<锁定 serving 根目录> BGE_MODEL_DIRECTORY=<已核验模型快照> bash scripts/test-bge-representations.sh`；待其隔离目录生成 `runtime.json` 后，在 RTW 独立工作树执行：

```bash
KNOWLEDGE_KEEP_EVIDENCE=1 KNOWLEDGE_REAL_USER_GATE=1 \
SEA_DC_BGE_RUNTIME=<DC 隔离目录>/runtime.json \
SEA_BTW_PRODUCT_SEARCH_ROOT=<本 BTW 独立工作树绝对路径> \
bash service/knowledge/scripts/acceptance.sh
```

第二次完整 RTW 脚本**退出码 0**；`TestRealHTTPKnowledgeWorkflowWithUserCenter` 与同协议 gRPC 替身版本分别 PASS（54.07s、20.16s），`go test -race ./service/knowledge/...`、`go vet ./service/knowledge/...`、真实 User Center 的 race/vet 均通过。DC 原有真实控制面/Provider 测试在消费者释放后 PASS；第二轮三路查询在 DC HTTP 日志中均为 200，未再出现新 429。BTW 定向 `go test -mod=readonly -race -count=1 ./internal/clients/datacenter ./internal/transport/http/search ./cmd/api` 与定向 vet 通过。隔离日志位于测试脚本打印的 evidence directory，目录和令牌不入 Git。

BTW 根模块的 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...` 和 `go mod verify` 也均退出 0。RTW 在未设置 opt-in 环境变量时的原结构性验收脚本再次退出 0；该基线结果不与本次真实模型子链混同。

本验收没有运行正式 `cmd/api` socket，也没有通过 `content.IndexCoordinator`/DC 技术 job 完整建索引；发布前的两片段 manifest 是测试固定语料。它没有证明 Milvus、生产对象存储、多实例容量、真实大模型总结质量、搜索相关性/规模、详搜/中高智能、Tools/SSE/两端客户端或 Collector 下钻。固定模型仅验证接纳时序。产品全量 H02/H07 与构建全量 H06 仍按各自清单继续验收。
