# WS08-E 局部验收

日期：2026-09-14；基线 BTW `8e1f56a`，分支 `feat/recommend-item-candidates-20260914`。工作区 `[W0]` 本独立树；`[W1]` `internal/recommend/`、`internal/app/recommend_rtw*`、`migrations/recommend/`；`[R1]` Sea-Docs WS08-E/H08.b/H10.c/H11.b、RTW 当前发布/撤回 Worker 合同、BTW usermodel/warehouse/旧 recommendation 模块；`[D1]` 根 go.mod 锁定的 Go/pgx/tRPC 模块缓存；`[G1]` 无生成文件；`[X1]` 只在临时 PG16 与 HTTP fixture 写入，无共享/生产状态；`[N1]` root 集成树、原业务脏树、Sea-Docs 与 H11 配对发布；`[T1]` test-postgres.sh 创建的隔离 PG 数据目录。主职责 `[C2:APPLICATION]` 与 `[C4:PERSISTENCE]`，跨 `[C1]` RTW Worker HTTP、`[C7]` 固定发布/item 特征/DWS资格合同、`[C8]` 验收。

`RECOMMEND_KEEP_EVIDENCE=1 bash internal/recommend/test-postgres.sh` 最终退出 0；隔离 PG16、`-race -count=1` 及局部 vet 均通过，证据 `/tmp/sea-btw-ws08e-pg-final2-20260914.log` 和 `/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-recommend-acceptance.2vn7dM`。根模块 `go test -mod=readonly -race -count=1 ./...`、`go vet ./...`、`go mod verify` 亦退出 0；时间门禁补正后目标包 race 与隔离 PG 全门禁再次通过。覆盖同指针并发重建幂等、不可变行 UPDATE 拒绝、新发布保留旧代/活动头递增、发布在 PG 提交前改变整事务回滚、RTW 撤回后查询不服务旧池、1–100 条输出上界。`TestRTWItemSourceConsumesWorkerSnapshotAndRevisionRefs` 使用真实 BTW RTW HTTP 客户端访问隔离 Worker 协议 fixture，核对当前发布三路 ref 与修订 metadata 进入池，正文不进特征；它不是 RTW 真 PG 服务联验。

ItemCF 手算 `u1={A,B}`、`u2={A,B,C}` 得 `sim(A,B)=0.75` 且 TopK、固定输入 hash 可重现；同主体对同 item 的第二次合格展示不增加相似度和支持人数。成熟观察负例不连边，旧修订不连边，未展示/PENDING/无 impression/同主体重复 impression/窗口或标签可用时间超水位被拒。**这些 DWS 行是合成单测输入，当前没有真实 CH/S3 DWS 提供者，也未激活邻居或热门池。**

状态为 `LOCAL_VERIFIED/PARTIAL`。H08.a 真请求/阶段候选事件、H10.c 正式 item 特征产品、WS07-C 真实成熟行为与可靠热门分母、ItemCF 耐久准入、H08.b 用户 bundle 配对消费、H11.b 训练 user/item 空间、真实 RTW PG 内容发布联验、生产索引与 Collector 均未验收。

真实 RTW 发布到 BTW 候选 PG 的可选联验入口已加到 `internal/app/recommend_rtw_real_test.go`。RTW 父测试应在人工发布后提供 0600 JSON（`rtw_base,worker_token,pg_dsn,module_id,revision_id,item_id`），对 BTW `go test -c -race -o <bin> ./internal/app` 编出的子进程设置 `SEA_RTW_ITEM_POOL_FIXTURE=<path>` 并运行 `-test.run=^TestRTWRealItemPoolHandoff$`。子进程在 RTW 隔离 PG 内创建自己独立 schema，执行显式迁移、RTW 当前快照/逐修订读取、PG Rebuild 和有界查询，核指定 item 同版与不可变行一次。**此门禁目前只编译/跳过，尚未实际收到 RTW 父 fixture。**
