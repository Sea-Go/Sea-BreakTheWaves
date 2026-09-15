# Wiki 编制技术结果引用：typed JCS 构造器

状态：**默认未接线的 v1 本机合同候选**。本切片只新增 `BuildWikiCompileResultRef(ctx, WikiCompileCompletionEvidence, artifacts.Store) (jobs.ResultRef,error)`。它在 RTW `AcceptCompile` 业务接纳及调用方 `GetJob/GetCompile/GetRevision` 回读之后构造技术结果对象；不调用 DC Complete、不建立 Wiki 修订或人工 Release，也不修改现有 Worker 和 `cmd/worker`。

## 来源与13键结果

typed 输入恰为 `Job jobs.Job`、`Accepted ridethewind.Compile`、`Revision ridethewind.Revision`（RTW GetRevision 已回填正文）、`Candidate content.WikiCompileCandidate` 和 `CandidateObject corpus.Ref`。构造器复用当前 `DecodeWikiCompileClaim` 的 RTW 12 键票据、整个 DC Submit 的 JCS Job hash、JobType/producer/resource、当前 running worker/attempt/epoch/cancel 及租约/期限门禁；另核 RTW `ACCEPTED` 与 Job 同 Compile、module/page/base、source IDs、Guidance 原 UTF-8 SHA、业务 InputHash、generation、attempt/epoch/cancel和**同一租约期限**。`Job.InputHash`、`Compile.InputHash`、Markdown 内容 SHA、RTW opaque `ResultHash` 仍是四个独立域。

WikiRevision 必须是 RTW 已接纳的 `revision_id`，未撤回、`kind=wiki`、同 module/page/base、标题与候选一字不差、`media_type=text/markdown`、`created_by=btw.compile/<CompileID>`，正文原字节 SHA/ObjectKey 和候选完全一致；SourceRefs 逐项与候选相同、只引用冻结 SourceRevisionID、无重复且定位形状为 `paragraph:N`。实际来源段落是否存在由早先 BTW Source Tool/RTW Accept 与新鲜 RTW GetRevision 证明；此纯构造器没有 Source 正文输入，不借旧页或当前 head 猜段落。

在任何 manifest Put 之前，构造器用注入的对象 Store `Get(CandidateObject)` 读候选 Markdown 原字节，并再次核 hash/正文；因此错误DC版本/旧job、错误RTW generation、撤回或错Wiki修订、假来源、伪内容哈希和坏对象字节都不会写技术结果对象。再固定生成恰好13个小写 JSON 键，generation/lease_epoch/cancel_version 为无前导零十进制**字符串**，JCS 变换后的字节是唯一 Put 字节，按返回 `sha256/<SHA>` 键/Get 验字节，得到 `jobs.ResultRef{URI:"sha256:<SHA>",Hash:<SHA>,MediaType:"application/vnd.sea.wiki-compile-result+json"}`。结果对象不含 Wiki 原文、Source 正文、模型凭据、时间戳或会话 ID；同证据重试得到同 URI/SHA。

## 局部验收

固定 BTW 开发集成基头 `394f154` → 构造器/test 头 `6f31347`；真实任务专属 `artifacts.Local` Put/Get 与**独立的同根 Local 第二 Reader**按 `sha256/<hash>` 读取、校原字节/JCS及恰好13键。静态跨语言合同 golden SHA-256 `b0a0447d2736bf4880c946a8e6dfb6602c2de5f889fb17bef1d0db26579eb63f`；同输入两次 Put 返回同 ResultRef和同存储字节。15类假证据（旧DC state/epoch/cancel、错DC Submit hash、旧RTW generation、撤回、错base/字节、假/重复SourceRef、错候选hash/key、对象读回错、租约过期等）均在 manifest Put 前拒绝且 Put计数0。

```bash
go test -race -p 1 -mod=readonly -count=1 -run '^TestWikiCompileResultRef' ./internal/app
go vet -p 1 -mod=readonly ./internal/app
go mod verify
git diff --check
```

上述命令在固定代码头退出0，完整 race 测试日志 SHA-256 `fe5ca3210db68acd2ba2d73bb398db31812083ad0fe6eda25837d5395dfc5fd0`。测试只用 `t.TempDir()` 的 Local 对象，没有启动 PG/S3/DC/RTW 进程，因此无服务停止日志或线上凭据；临时对象由测试生命周期清理。

## 后续交接限制

`WikiCompileCompletionEvidence.Job` 和 RTW 修订是调用方刚取得的快照，构造器只能在本次请求前后核本地时钟和内容；DC Cancel/RTW Withdraw 仍可能与 Store Put 并发。接线方在 DC Complete 前必须重新 GetJob/GetCompile/GetRevision，交由 DC lease CAS 做最终技术接纳。现 Worker 的 `CompletionRef=nil` 和命令默认拒启保持原样；跨进程候选丢失后的恢复账本、真实 RTW 同进程 Wiki 接纳、共享 S3 同桶原字节读回、DC 成功 ResultRef/Complete 和人工发布均**未在本切片签收**。本页的 `ACCEPTED` 指 RTW 编辑 head 已有待维护修订，不代表活动已发布 Release。
