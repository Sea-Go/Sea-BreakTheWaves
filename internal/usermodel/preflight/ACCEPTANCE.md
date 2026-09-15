# BTW SubjectRef v2 阶段一 usermodel 预检证据

固定输入：BTW 开发集成 `f51723e0a1db3b5029342ef8207fb2255be79cec`，七份 `migrations/usermodel/001..006` 源 SHA-256 为 `fdb6615e728a8a0714cb8b5c491199fa67b0e1d84f8f9d8facbb1f5545e4ab97`。隔离 PostgreSQL 16.14 部署这七份 SQL 后，由 `pg_catalog` 获得 23 表列/PK/FK/唯一索引 oracle，catalog SHA-256 为 `89cb6c0c8447389c617a4d7531a80db052d0da51de448cf8c6ec265c21e9e5d1`。其中 19 表含三字段主体、1 表为外部未映射事件、2 表仅有 authority/tenant、1 表是无主体 coverage prefix；详细键清单见 `SCHEMA_INVENTORY.md`。

本地验证命令：`bash internal/usermodel/preflight/test-postgres.sh`。脚本创建并停止 `/tmp` 隔离 PG16，运行 `GOMAXPROCS=2 go test -mod=readonly -p=2 -count=1 -v` 与同范围 `go vet`，构建一次 CLI，验证默认离线拒绝、stderr 每行可解析为共享字段的 JSON、显式只读 CLI 审计。测试与 CLI 均退出 0；未跑 BTW 全仓或生产 E2E，以免在低磁盘下做无关构建。

| fixture | 匿名 report SHA-256 | 结果与负例 |
| --- | --- | --- |
| 七份 migration 空库 | `6feed342e4361ed7e74a6093625a699de69b899e168fca985bbf206656bbc564` | L1=0、L2=0、L3=0；contract 与 live catalog SHA 相同，CLI 与包测试报告相同。|
| synthetic/冲突/断键 | `11ce826cf16f99682e794c853ed67151e0a7e5076ed11d9770bc7a19a2b2f66d` | L1=9、L2=3、L3=0；早期 synthetic 非规范主体被阻断；正 int64 边界含零、符号、前导零、溢出与最大合法值；跨 tenant 同 UID 在旧 PK 允许、去槽后碰撞；移除旧 FK 后识别 schema 缺键与 orphan；状态版本与 Outbox 间有空洞；活动 Serving pointer 的 pair 不等于 bundle pair。两次只读报告 SHA 一致，fixture 行数不变。|

L1/L2 是各规则对各表的**命中实例数之和**，不是去重用户数，也不展示主体值。输出 JSON 不含 DSN、UID、turn、event_body、payload；测试中的 synthetic 字符串仅存在隔离 fixture 源码，不在报告。匿名 SHA 证明的是同一输入与同一规则集的结果字节，不能当作生产数据验证。

局部状态：`LOCAL_VERIFIED`（物理结构、PG16 只读扫描、CLI 门禁与受控 JSON 日志）。真实 BTW 生产表、线上 RTW UID 签发、外部 Recommend pair 批准、旧不可变工件/hash、触发器行为、Collector→DC 下钻和完整 OBS-r3 链路均为 `NOT_VERIFIED`。本阶段没有迁表、双写、v2 行或 serving 切换；由集成负责人审核并另行决定后续阶段。
