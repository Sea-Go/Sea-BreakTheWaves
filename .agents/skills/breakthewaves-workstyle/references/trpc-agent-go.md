# tRPC-Agent-Go 在 BTW 中的开发规范

本页规定搜索、推荐与离线内容理解怎样使用框架。go-zero 承接协议、工程与服务装配，tRPC-Agent-Go 承接模型、工具、编排和运行事件；二者的衔接按 [联合开发规范](framework-integration.md) 执行。

以下 API 与关键行为于 2026-09-23 按项目解析的核心模块 `v1.10.0` 核对；当前不加载 Session Postgres 子模块。后续以实际解析版本为准；框架能力、项目要求和当前已装配实现分别说明，不把版本号或目录存在当成功能启用证明。

## 1. 先把算法任务映射到框架能力

| BTW 要完成的工作 | 使用什么 | 如何与原算法结合 |
| --- | --- | --- |
| 过滤、融合、特征、确定性排序 | 原领域实现直接执行 | 不为每步计算建立聊天 Agent，也不为了框架调用再走自身 HTTP 接口 |
| 查询理解、内容语义抽取、基于证据生成 | `model.Model`／`llmagent`，应用交互经 Runner 执行 | 输入来自所属用例，输出回到物品／检索／交付消费者；需要工具循环时使用 LLMAgent |
| 让模型调用搜索、排序、原文能力 | Function Tool／明确 ToolSet | 包装已有算法的调用边界，返回候选、分数或原文位置，不复制算法 |
| 专家处理子问题后由主 Agent 综合 | AgentTool | 子结果回父 Agent；需要哪些上下文和工具显式传入 |
| 当前轮交由另一 Agent 接管 | SubAgents／transfer | 明确控制权和结果归属，不把它当作“调用后返回”的普通工具 |
| 固定顺序、独立并行或有界修订 | 普通调用，或已有 Chain／Parallel／Cycle Agent | 只有确需 Agent 级组合才用内置编排；并行聚合与循环终止有明确责任 |
| 有状态分支、节点汇合、检查点恢复 | Graph／GraphAgent | 节点调用已有能力，图组织状态和依赖；Graph 工作流不等同于知识图谱检索 |
| 在既有检索上提供知识工具 | `knowledge.Knowledge`／Knowledge Tool 或本域 Function Tool | 适配已有全文／三路检索，不为了框架默认导入流程重建 chunk 物品 |
| 运行之外的持久内容／训练任务 | 现有 Async Worker 与领域任务 | 任务中的语义步骤可用 Agent；一次后台 goroutine 或框架内存状态不等于可恢复 Worker |

## Agent 能力落位

在线“本域”指 Search／Recommend 的 `rpc/internal`。统一 API svc 经两个域的 `rpc/engine` 装配能力。离线 Agent 由 Async `content.NewAnalyzer` 连接 `internal/processing`，在 Worker 中运行。共用框架技术集中于 `service/common/agentx`，不搬入场景决策。

| 组件 | 当前位置与新增方式 | 必须接通 |
| --- | --- | --- |
| Model／Runner／会话适配 | `common/agentx`；工厂接显式配置与 Tool | 供应商配置 → 实际 Agent／Runner → 每轮运行 → owner 关闭 |
| 在线 Agent／Prompt／Tool | `search/rpc/internal/search` 的证据生成能力；Recommend 选择交付 | 查询、Ontology、候选与原文 → 模型 → 完整业务结果 |
| 离线理解／教师判断 | `async/rpc/internal/processing`；content 和 Worker 装配 | 自然结构全文 → 有出处的对象、关系、样本与覆盖记录 |
| Graph／Planner | 当前无必须引入的 Graph；确有状态依赖时落所属领域 | 真实节点、增量、分支及终止；不要预建空目录 |
| Callback／Extension／Plugin | 所属 Agent 工厂，公共观测复用 telemetry | 注册到实际实例、准确作用域、保留失败语义 |
| Session／Memory | `agentx/session.go` 为每轮临时会话和事件同步；无用户 Memory 生产 | 请求隔离、实际清理、race 回归；不修改外部 Ontology |
| Event／遥测 | agentx 转业务事件，API handler/stream 写 SSE；common/telemetry 记录 | 模型增量、工具过程和完整终态，与原始协议解耦 |
| 运行时 Skill／执行器 | 当前未启用；只在明确执行需求下装配 | 可调用工具和明确边界，不能恢复旧字符串 Skill 转发 |

## 2. Model、Agent 与 Runner 的装配

- 在 svc／所属 runtime 工厂组合稳定依赖：模型、检索／存储、工具、Prompt、Agent、Runner。普通 API handler 只接入用例，不构造另一套共享 Agent。
- 使用公开 Model 实现或适配所需公开接口；模型名称、端点、凭据、生成参数和场景预算来自配置。Embedding／排序模型按各自能力接入，不将所有模型调用都当作聊天工具循环。
- Agent 的固定 instruction、工具集合及实例级选项在装配期确定；查询、Ontology、证据、会话与本轮参数由 Run 输入及公开 RunOption 传入。不能通过修改共享 Agent 字段切换请求。
- `WithTools` 和 `WithToolSets` 在本次核对版本覆盖列表：先汇总该 Agent 允许的工具再设置，避免新增一个工具后丢失旧工具。`WithExtensions` 可累加，但只作用于安装它的 Agent，不能假设自动传到子 Agent。
- 应用运行通常通过 `Runner.Run(ctx, userID, sessionID, message, runOptions...)`。直接调用 `Agent.Run` 的需求必须承担 Invocation、事件消费与完成通知责任；不能为省一层调用把框架生命周期悄悄移除。
- `WithMaxLLMCalls`／`WithMaxToolIterations` 在当前版本按本 Agent 的 invocation 生效，非正数不限次数。场景需明确预算，并通过总 Context 期限约束整次业务；子 Agent 各自的局部限额不等于整棵调用树的总预算。

## 3. Tool 与检索能力的接入

- Function Tool 使用框架所需的 typed 输入／输出和 `func(context.Context, Input) (Output, error)` 适配，再交给 `function.NewFunctionTool` 生成 schema 与解码；这是工具调用合同，不是新增一套 HTTP 请求模型。
- 模型可见参数只包含完成任务所需的查询、物品／原文位置等。服务器确定的权限范围、允许物品类型和凭据由宿主注入，模型参数不能把这些约束放宽。
- Tool 直接调用既有领域能力，保留 Context、真实结果和失败原因；不吞错返回“没有结果”，不将生成内容冒充原文，不用另一个字符串 Skill 分发层转发普通计算。
- 只读检索可在依赖支持时并行；写操作先明确并发、事务与幂等。框架并发选项不能代替底层数据保证；模型重试、Tool 重试和 SDK 重试不叠加产生副作用。
- 使用 Knowledge 接口时，查询增强、范围过滤、召回与重排复用实际搜索链。避免 Agent 已改写一次、Knowledge 又无条件改写一次；来源读写按 BTW 的整物品与原文语义，不照搬固定 chunk 推荐结构。
- 外部 MCP 是具体 ToolSet 依赖，装配处负责连接、就绪、schema 与关闭。工具出现在列表不能证明远端可调用，也不能证明其有权访问当前资料。
- 工具验证从实际框架调用边界进入：参数影响结果、非法／越权输入无副作用、错误与取消到达依赖、证据位置可回读。权限落实在装配和执行入口，Prompt 不替代它。

## 4. Prompt、证据与结构化输出

- Prompt 在所属领域实现中管理任务目标与输出合同；动态 Ontology、查询、排序证据与原文由本次用例提供。保留证据标识与原文位置，避免把所有资料复制到多个 Agent 的长期 instruction。
- 区分“搜索结果摘要”与“读取过的正文”。需要原文依据的回答／计划通过原文工具补足证据；生成后按输出边界解析业务结果和引用，证据不足明确表达，不凭回答流畅度认定检索成功。
- 当前版本 `WithOutputSchema` 只允许回答，不能与工具／RAG／transfer 混用；不要为了得到 JSON 把检索能力意外禁用。
- 当前版本另有 `WithStructuredOutputJSONSchema`／`WithStructuredOutputJSON`，使用模型原生结构化输出，可与工具配置组合，但供应商是否支持须实际核对。若不支持，采用明确的取证后生成流程或已支持的输出方案，不宣称配置 schema 即已保证结果。
- 公共计划／问答结果由 BTW 合同表达：展示内容、结构化数据、证据引用和不足状态分别有真实来源。工具中间结果、模型草稿与最终结果不混写；一次 schema 解析通过也不证明引用支持结论。

## 5. Graph 与多 Agent 的状态规范

- Graph 只组织本任务需要的状态依赖、分支或恢复。检索、特征、排序仍调用原算法实现；不为每个普通函数补同名节点、Agent 或状态审批。
- 框架 key 使用框架定义；业务 state key 明确类型与所属节点。读取时处理缺失／错类型；节点返回自己负责的增量，并为并行汇合选择合适 reducer。
- 输入 State 视为只读。当前 `State.Clone()` 只复制顶层 map；需要修改嵌套 slice/map 时按所有权复制，不能由方法名推断深拷贝。
- 边描述执行依赖，数据通过 State 和明确的输入／输出映射传递；连上两个 Agent 节点不意味着后者已收到前者的证据。
- AgentTool、transfer 与 Graph 节点分清控制权和输出归属。并行 Agent 的事件按调用与作者关联，不把不同分支文本连成一个答案；循环有停止条件、最大轮次与总预算。
- 检查点只代表框架状态可恢复；数据库写入、消息确认和索引更新仍由原任务所有者保证幂等。Session、Graph State 和任务表不能同时充当三份权威加工状态。

## 6. Callback、Extension 与 Plugin

| 需要改变的行为 | 框架位置 | 在 BTW 中的要求 |
| --- | --- | --- |
| 某次模型／工具调用的边界逻辑 | 相应 Callback | 只作用于所需阶段；已有算法与权限仍由原所有者负责 |
| 同一个 Agent 的可复用工具与回调组合 | Extension | 在实际 Agent 装配；每轮状态不放共享实例 |
| Runner 调用范围的统一观测或策略 | Plugin | 明确 Runner／Run 作用域，不再复制协议中间件 |
| 图节点前后观测与恢复 | Graph NodeCallbacks | 区分记录、短路、替换结果和恢复失败 |

仅观测的 Graph Before／After 回调按合同返回 `nil, nil`；非 nil 空 map 仍可能跳过节点或覆盖结果。恢复失败要有显式策略与测试，不能在日志回调中把失败改成成功。`OnEvent` 等观测位置不自动成为可靠的权限拒绝边界。

## 7. Runner 事件、结果与取消

| 框架信号 | 消费规则 | 对公共交付的含义 |
| --- | --- | --- |
| Run 返回 Go error | 处理启动／准备失败，无流时不得等待 channel | 未开始或启动失败，不发送业务成功 |
| Event／Response 为空 | 读取内容前判空；Graph state 等按其合同处理 | 不凭空补完成文本 |
| Delta | 按本次响应关联记录增量 | 用于过程展示，不与完整消息重复拼接 |
| 完整 Message／结构化结果 | 替换或解析所属结果 | 供最终答案／计划使用，工具消息不冒充最终答案 |
| 普通 Done／IsFinalResponse | 可能只是模型或局部步骤完成 | 不终止整个工具循环或多 Agent 用例 |
| IsError／IsTerminalError | 区分可恢复观测错误和整次运行失败，保留终态原因 | 后续完成事件不得将失败改成成功 |
| IsRunnerCompletion | 内部 Runner 运行边界 | 结合业务结果、取消及提交状态判断成功 |
| channel 关闭／Context 取消 | 无预期终态则按中断；取消保留取消结果 | 不能因连接结束就补一个成功终态 |

- 内部按该 Runner 的完成协议收尾；公共 SSE 客户端仍消费到 EOF 并检查终态。这是不同层的结束条件。
- 提前停止展示或写出失败时取消本次运行，并按 owner 责任完成退出。消费者不关闭框架生产的 channel，不手动重复 Runner 管理的完成通知。
- 本版本 `Runner.Close()` 会取消活跃运行，并关闭自己拥有的 Session 与相关插件；它不是应用全部 goroutine 的等待屏障，也不会自动关闭所有外部注入的服务。应用等待其拥有的请求退出后再关闭借给 Runner 的数据库等资源。
- 公共事件需要稳定业务映射，不能把某个框架 helper 或单 choice 例子的行为当成所有 Graph／多 Agent 场景的结果合同。详细错误与 RPC/SSE 桥接见 [联合流式规范](framework-integration.md#错误终态与跨进程事件)。

## 8. Session、Memory、Artifact 与运行时 Skill

- Session 管调用历史／框架状态，Memory 管明确授权的记忆能力，Artifact 管框架产物引用；它们不替代 BTW 原文库、物品索引和外部用户 Ontology。
- app/user/session 的来源和隔离由用例与可信身份建立，不能用共享固定 session 混合用户。启用后端须进入实际 Runner 装配；引入 Postgres 依赖并不代表使用了持久 Session。
- 当前 `service/common/agentx` 使用每次运行独立的 in-memory Session，运行后清理。Session 公共接口适配保护 v1.10.0 的事件／时间戳并发访问，升级框架时重新核对对应 race 测试。需要持久会话时实现并装配所选后端，验证重启后的读取、隔离和关闭；框架后端 schema 由框架负责。
- 当前任务约定用户塔和画像由外部负责，不能启用自动记忆更新后把它当成 BTW 自有用户画像生产链。临时证据、对话历史和权威业务事实的写入责任分别定义。
- 运行时 Skill 只是 Agent 的资源／能力组织方式，不能绕开明确工具集合。它与这里给 Codex 使用的开发技能不同。
- 当前版本 `WithSkills` 未显式选执行器或限制时存在本地执行器便利回退；只需知识指导时使用已支持的显式 knowledge-only 选项，并检查最终工具集合。已有显式 executor 不会因该 profile 自动移除。涉及执行能力时按实际业务需要配置环境，不让接入说明文件暗中扩大能力。
- 原书、训练分片和大型计算产物继续使用业务对象存储；子 Agent 历史或临时 workspace 不提供业务持久化保证。

## 9. 验收两类事实

**框架行为**：组件真实进入装配；Tool 从框架入口执行；Graph 数据与并行状态隔离；事件不会过早完成或吞掉错误；Session 真正按配置保存；取消与关闭责任成立。

**算法／Agent 效果**：召回与排序按固定条件比较，Agent 的证据支持、引用和任务结果有效，模型／工具成本来自真实调用。模拟模型可以证明框架行为，不能证明实际模型质量、原文真实性或生产吞吐。

## 必须实际加载的原始规范

在技能清单找到已有 `$trpc-agent-go`，读取其 `SKILL.md`、`references/code-style.md`、`references/version-and-sources.md`；本机入口是 `~/.codex/skills/trpc-agent-go/SKILL.md`，换环境使用实际清单路径。按修改追加：

- Model／Runner／Tool／流式：`references/runtime-and-tools.md`。
- Graph／多 Agent：`references/orchestration.md`。
- Knowledge／Session／Memory／Artifact／Skill：`references/state-knowledge-execution.md`。
- 协议服务／观测／评测：`references/quality-and-serving.md`。
- 依赖／公开接口：`references/engineering.md`。

用 `go list -m -json trpc.group/trpc-go/trpc-agent-go` 及相关子模块确定 Dir／Replace，再查看对应源码、AGENTS.md、示例和公开 API。技能快照、网站最新版本与项目版本不能混用；这里的详细规范不取代加载来源，也不自动触发升级。

本次同版本证据：[LLMAgent Option](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/agent/llmagent/option.go)、[Function Tool](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/tool/function/function_tool.go)、[Runner](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/runner/runner.go)、[Event](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/event/event.go)、[Graph State](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/graph/state.go)、[Graph Callback](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/graph/callbacks.go)、[Knowledge](https://github.com/trpc-group/trpc-agent-go/blob/v1.10.0/knowledge/knowledge.go)。上游仓库贡献流程不自动成为 BTW 开发流程。
