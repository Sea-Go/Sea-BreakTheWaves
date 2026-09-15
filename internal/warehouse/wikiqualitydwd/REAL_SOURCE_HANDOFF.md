# Wiki FactSet 真源 DWD 冻结同父交接

`TestRTWRealFactSetDWDSourceBundle` 是测试专用第三包。RTW Holder 显式指定 `SEA_BTW_SOURCEPROOF_READER_ROOT` 为含本叶子代码的 BTW 根目录，继续提供原 warehouse 十一键 `SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE` 与 SourceProof 十二键 `SEA_BTW_SOURCEPROOF_REAL_FIXTURE`，并在自己的**持久验收 evidence** 中预建空的普通 `0700` 子目录，将绝对路径传为 `SEA_BTW_WIKI_DWD_EVIDENCE_DIR`。不新增业务 Event、不修改十一键或十二键、不改生产启动。没有新环境变量时，`sourceproof/real-source-acceptance.sh` 保持原来 warehouse→SourceProof 两包顺序，第三包不运行。

脚本只启动一个临时 PostgreSQL：warehouse 真源测试写入完整 ODS 目录及判断前缀并 ACK DataCenter；SourceProof 真 Reader 测试用 RTW Admin 历史 GET、Worker 原 Event、DC 已 ACK 页和同一 ODS PG 重核截止；第三包在 PG **尚未停机**时读取两份原 `0600` fixture 和仓/Reader 原 `0600` 收据，并再调用 `wikiqualitydwd.Freeze`。第三包只读 RTW/DC/ODS，断言截止 `14`、目录 Fact `2`、目标已运输判断 `2`、技术跳过 `10` 与源证据 SHA；冻结后的 `quality_state=not_evaluable`、`evidence_level=rtw_dc_source_proof_only`、`activation=none`。AdminJWT `actor_id` 仍是 Wiki 管理员账号字面值，不能解释为 UserCenter 已验 UID。`facts_complete=true` 仍是管理员对固定 Source scope 的声明，不能作为客观完整性或真人质量验收。

第三包向 caller-owned 子目录执行 `Frozen.Write`，仅创建 `prefix.jsonl`、`catalog-facts.jsonl`、`judgments.jsonl`、`manifest.json` 四个 `O_EXCL 0600` 原文件，立刻逐文件核字节与 SHA。`manifest.json` 原字节 SHA 在测试日志中输出，父轮从保留的四文件独立复算。bundle 目录不加入第五份结果文件，避免 CH loader 的四文件精确入口拒绝；脚本自己的 PG/测试/停库日志仍保留在独立临时目录。父轮后续必须以独立复算的 manifest SHA 和固定四文件调用 CH/dbt loader，重新验收；本第三包没有 CH 行数、Dataset、D07 或生产活跃证据。

普通 `go test -race ./internal/warehouse/wikiqualitydwd` 只验本包纯夹具，真源测试明确 SKIP。真实同父仅由 RTW Holder 调用本叶子 `internal/evaluation/wiki_quality/sourceproof/real-source-acceptance.sh`，且三项包测试均退出成功、`go vet` 成功、`pg_ctl` 最终 `stop-status=3` 才能签真源证据。失败保留最早的原测试日志、四文件可能的部分写入和 PG 现场，不改历史 Golden 或将未运行的 CH loader 记为通过。
