# Wiki FactSet 真源 DWD 冻结同父交接

`TestRTWRealFactSetDWDSourceBundle` 是测试专用第三包。RTW Holder 显式指定 `SEA_BTW_SOURCEPROOF_READER_ROOT` 为含本叶子代码的 BTW 根目录，继续提供原 warehouse 十一键 `SEA_RTW_REAL_WIKI_FACT_SET_FIXTURE` 与 SourceProof 十二键 `SEA_BTW_SOURCEPROOF_REAL_FIXTURE`，并在自己的**持久验收 evidence** 中预建空的普通 `0700` 子目录，将绝对路径传为 `SEA_BTW_WIKI_DWD_EVIDENCE_DIR`。不新增业务 Event、不修改十一键或十二键、不改生产启动。没有新环境变量时，`sourceproof/real-source-acceptance.sh` 保持原来 warehouse→SourceProof 两包顺序，第三包不运行。

脚本只启动一个临时 PostgreSQL：warehouse 真源测试写入完整 ODS 目录及判断前缀并 ACK DataCenter；SourceProof 真 Reader 测试用 RTW Admin 历史 GET、Worker 原 Event、DC 已 ACK 页和同一 ODS PG 重核截止；第三包在 PG **尚未停机**时读取两份原 `0600` fixture 和仓/Reader 原 `0600` 收据，并再调用 `wikiqualitydwd.Freeze`。第三包只读 RTW/DC/ODS，断言截止 `14`、目录 Fact `2`、目标已运输判断 `2`、技术跳过 `10` 与源证据 SHA；冻结后的 `quality_state=not_evaluable`、`evidence_level=rtw_dc_source_proof_only`、`activation=none`。AdminJWT `actor_id` 仍是 Wiki 管理员账号字面值，不能解释为 UserCenter 已验 UID。`facts_complete=true` 仍是管理员对固定 Source scope 的声明，不能作为客观完整性或真人质量验收。

第三包向 caller-owned 子目录执行 `Frozen.Write`，仅创建 `prefix.jsonl`、`catalog-facts.jsonl`、`judgments.jsonl`、`manifest.json` 四个 `O_EXCL 0600` 原文件，立刻逐文件核字节与 SHA。`manifest.json` 原字节 SHA 在测试日志中输出，父轮从保留的四文件独立复算。bundle 目录不加入第五份结果文件，避免 CH loader 的四文件精确入口拒绝；脚本自己的 PG/测试/停库日志仍保留在独立临时目录。父轮后续必须以独立复算的 manifest SHA 和固定四文件调用 CH/dbt loader，重新验收；本第三包没有 CH 行数、Dataset、D07 或生产活跃证据。

普通 `go test -race ./internal/warehouse/wikiqualitydwd` 只验本包纯夹具，真源测试明确 SKIP。真实同父仅由 RTW Holder 调用本叶子 `internal/evaluation/wiki_quality/sourceproof/real-source-acceptance.sh`，且三项包测试均退出成功、`go vet` 成功、`pg_ctl` 最终 `stop-status=3` 才能签真源证据。失败保留最早的原测试日志、四文件可能的部分写入和 PG 现场，不改历史 Golden 或将未运行的 CH loader 记为通过。


## 内容开发固定头真来源PG→CH验收

独立叶子`feat/wiki-sourceproof-dwd-real-20260916@f4f03639d349fbf7da3448893dd2d6eced561b7f`两笔`a9052b4/f4f0363`远端同HEAD/洁，主任务精确cherry到已有BTW内容开发为`074670e/eef9a40`，原质量ODS/SourceProof、旧11键和12键合同不改。RTW父桥独立代码`a93e40c`先完成一轮受控RTW×DC`ebea5f8`×BTW开发`eef9a40`真实双PG16＋CH，14 source事件、目录1/判定3（目标2＋旧基线1）/技术skip10/同qualityconsumer ACK14；BTW同PG Warehouse、Reader、DWD第三项各PASS、四文件原SHA与Manifest在RTW持久Evidence核，真实隔离CH/dbt4模型＋4测试通过、stopstatus3/CH进程退出。这轮初始RTW叶子报告`offline_dwd_parent_verified=true`的源SHA和限制见RTW`service/knowledge/docs/acceptance/wiki-fact-set-dwd-source.md`。

同行只读复审未见来源/ACK/CH阻断P0–P2，P3指出BTW三包失败日志原只写RTW会清的`t.TempDir`；RTW父桥后续`0cf22b9`在DWD模式保留私有`0600`持久原日志。主任务从最终三个**开发固定代码头**RTW`27580d7`×DC`ebea5f8`×本BTW`eef9a40`又各开新PG16和新CH generation完成最终父轮exit0：RTW testSHA=`b772b36a7a56befa125c2955014a077c94a85a9dd0f69388a2aa1bf20a3150d8`、reportv4SHA=`74d0d44480d616004538135a1d1763866f52147eac2d19f9595443325bd31b2a`，持久三包原脚本SHA=`1042783a68d05661460420bc167d0775ec52286e1bdd327130d745bbf4a2a239`，BTW同PG Warehouse测试SHA=`9f6817b582c851cde5666b96aa7cfb7d681e6edcde775c789a4cbdeadfc7368b`、ReaderSHA=`dd6fccec4e7e82ad039de01df892b4546bc9e3d883466f5a7291c9189bfccaba`、DWD测试SHA=`ae77cd1fe2513f8def6b34ba70cb6140fe840e46e044210e2597f5fad994e723`、vet空e3b0，两PG stopSHA=`ca19178a35ab4153b75b494963b66ce8243e1b94c173107c87b44d09db23652d`/status3，RTW Evidence`sea-knowledge-acceptance.aMpbws`和BTW独立`sea-wiki-sourceproof-real.ryXRYO`。

新父Evidence下`observability/wiki-factset-dwd-bundle`**现在确有**四个原`0600`文件；manifest SHA=`706ab718bae794ef10dc34edcab1b485ad2e0fe29abd53d5ebceb20e7e8222dc`、完整prefix14/目录事实2/目标Judgment2/skip10与RTW原Event/DCACK/ODS原行SourceScope同SHA。Caller-pinned CH report SHA=`033eaf62ce4167cdccd7a1a634793b8427cf28fa9a8f3b61816d322c57333128`、进程停机SHA=`719f747e91eff9b2704b89fdd29a4a9a1bc9d42cd8b002021e1bacef8580bd37`、4模型＋4data test pass、同generation重插拒。CH loader报告`source_authority_independently_verified_here=false`是它自己的局部资格；RTW父报告跨RTW/DC/PG/CH已另证`offline_dwd_parent_verified=true`。两者均`quality_state=not_evaluable,human_catalog_verified=false,d07_evaluable=false,production_verified=false`，管理员Grade仍只为source claim，旧11/12结果原字节随RTW`t.TempDir()`删除但四个DWD原工件、CH报告和三包脚本日志留存可复验。

此切片只签受控本机**真实来源PG→CH DWD/ADS L3**，没有将RTW当前head快照当DC历史截止as-of、没有真人FactCatalog漏项裁决、整页D07、Dataset/Serving正式发布或生产上线。SourceProof Reader和离线出口继续默认无正式业务激活指针。
