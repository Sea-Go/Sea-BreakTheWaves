# fast/low/summary 真 RTW + DC 本机模型功能验收

日期：2026-09-15。状态：**一格功能 L3 通过，答案语义质量门禁未通过；默认关闭，不可上线**。本切片只涉及 `fast/low/summary`，其余搜索产品格没有据此标记已交付。

RTW 测试管理员通过真实 User Center、知识 HTTP 与 PostgreSQL 发布双块 release；BTW 正式 `cmd/api` 从 RTW 当前发布获取三路索引 ref，经真实 DataCenter 锁定 BGE-M3 模型将查询发到三条 local-exact 召回，再由 RTW Worker HTTP 读取同修订正文、提交 `EvidencePack` 与耐久引用。单根 tRPC-Agent-Go Graph/LLMAgent 调用 DataCenter 本机网关路由到 Ollama `qwen3.5:2b`，不是固定模型响应。通过完整 Graph 事件、引用 JSON 校验后才提交 RTW 耐久答案；RTW 父测试还核 PG 答案/答案引用/搜索引用各一行、产品历史及跨主体读面，且 OTLP 收到原生 `search_summary_root` span。

真实 DC 网关要求幂等键和显式输出预算。BTW `gatewayModel` 保留官方 tRPC-Agent-Go OpenAI adapter，按**RTW 签发 Subject + SearchID + AnswerID + 发布快照 SHA + 完整规范 Agent 请求 SHA + 模型名**生成稳定键：同一操作重试同键，不同主体/搜索/快照异键；缺业务上下文时在发网前拒绝。`cmd/api` 当前只支持 `fast/low`，显式给此装配 `MaxOutputTokens=512`，通用 Summary 构造器不再隐式锁定其他档位；DC 另以当前完整请求估计输入上界。低档调用发 `reasoning_effort:none`，以防所用 Ollama OpenAI 模型将全部输出预算花在 reasoning 而不给用户内容。Summary 指令是通用“只根据原文、不补外部背景、引用 ID 去重”，确定性引用校验保持最终门禁。

同轮失败证据均保留，没有把失败当验收：首轮 DC 返回 `400 invalid-idempotency-key`；次轮补键后返回 `400 model-budget-required`；第三轮 RTW 测试 HTTP 5 秒上界先取消、BTW 返回 `CANCELLED`；第四轮 DC 200/21.751 秒、1653/512 tokens，但 512 tokens 全在 reasoning 且 `content` 空，Graph 拒绝；第五轮 DC 200/6.921 秒、1648/139 tokens，模型重复同一 evidence ID 并加无来源定义，Graph 拒绝。第六轮在较窄指令下首次成功，但为避免单词测试特调，改成通用证据指令并单独注入当前档位预算后完整重跑第七轮。

第七轮 RTW 真 User Center/PG 与 BTW/DC/Ollama 同源联验退出 0；[RTW 耐久答案原报告](/private/tmp/sea-rtw-live-summary-result-r7-20260915/summary-search_bcd52eec-7e05-4b9a-8821-a524b4de98e5.json) SHA `8047eda3f04cdea933d9d7327bb76f10040e7eabd8e55ddd0f9e399ca910d343`。BTW 的 [DC↔RTW 精确绑定报告](/private/tmp/sea-rtw-live-summary-result-r7-20260915/combined-model-usage.json) SHA `8139455aef3b381a5f15c3b82e1f2dbac7fefa9bbc32b2561c9bf69ebc1c3fdb`：使用同一 RTW SearchID 在 DC `telemetry.model_interaction.request_body` 定位**唯一**完成调用，核 `configuration_id=76bd4360-0c32-4cd3-b0e8-d66044e3f881`、物理模型、捕获响应原文中的 `answer` 与唯一 evidence ID 和 RTW 已接受内容逐字一致。Ollama 原生响应给 `prompt_tokens=1683, completion_tokens=56, total_tokens=1739`；DC 实测模型延时 `4295ms`，RTW 端到端 `10632ms`。两种延时只属本地功能夹具，不是性能达标结论。DC [用量收据](/var/folders/f_/l5hv3b1d6sx8zwr_cc8fkjkm0000gn/T/sea-dc-ws05a.ZRhoW1/consumer-usage.json) 还保留失败轮调用和 provider usage；RTW 同轮日志 `/private/tmp/sea-rtw-live-summary-acceptance-r7-20260915.log`。收据命令的 `--quality-gate=not_passed_unsupported_interpretation` 是本次人工审阅的显式输入；数值与内容绑定由程序独立核验，质量状态并非程序自动推断。

**语义质量仍失败**：已发布可引用片段原文只有 `Evidence`，模型的已接受答案是 `"Evidence" is the title of a source.`；“title”属性没有从这段片段得到支持。这证明现有结构性 `validate_answer` 足以拒绝坏 JSON、错误/重复引用，但不足以判定句子是否忠于来源。用户侧激活维持关闭，后续须加独立语义忠实度门禁、丰富测试原文与人工评审。没有冻结 qrel/完整判断范围，S09/S11/S12 不可评。正式 fast/low 默认 SLO 没改：本轮仅在真模型**测试注入**将 RTW fast 上限调至 60 秒、测试 HTTP 客户端调至 95 秒，生产预算与性能仍待另验。

复核：BTW `go test -mod=readonly -race -count=1 ./cmd/api ./internal/search ./internal/transport/http/search ./cmd/search-summary-usage-acceptance` 与 `go vet`；RTW 真 User Center 跨仓隔离 PG/HTTP 测试及 go vet；DC WS05-A 真 Ollama/PG 网关测试与锁定 BGE provider 测试。所有保活测试进程已收到 release 并退出 0，原有工作树与服务未部署。
