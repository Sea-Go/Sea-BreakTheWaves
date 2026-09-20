// Package internal 是推荐系统重构（refactor-reco-trpc-agent-go）的内部实现根。
//
// 该包本身不包含可执行代码，仅承载中文注释规范示例（见本文件）。
// 所有业务实现位于 internal 下的子包：domain/agent/tool/recall/rank/rerank/
// graph/repo/obs/event/session/quality/channel/search/cf/profile/cpu/skill/
// server/eval/config。
//
// ============================================================================
// 中文注释规范示例（适用于 internal/ 下所有子包）
// ============================================================================
//
// 【1. 包注释格式】（写在每个包的 doc.go 文件顶部）
//
//   // Package <name> <一句话职责>。
//   //
//   // 包职责:
//   //
//   // <1-3 句中文描述本包做什么，覆盖哪些能力>
//   //
//   // 核心 interface:
//   //   - <Interface1>: <职责>
//   //   - <Interface2>: <职责>
//   //
//   // 二开扩展点:
//   //   - <扩展点1>: <说明>
//   //   - <扩展点2>: <说明>
//   package <name>
//
// 示例：
//
//   // Package recall 实现传统推荐快路径的召回能力。
//   //
//   // 包职责:
//   //
//   // 该包实现 Recaller interface，提供传统推荐快路径的召回源：
//   // RuleRecaller/ContentRecaller/CFRecaller/GraphRecaller/ChannelRecaller/
//   // HybridRecaller。召回零 LLM token，P99 < 100ms。
//   //
//   // 核心 interface:
//   //   - Recaller: 召回 interface（定义于 domain 包）
//   //
//   // 二开扩展点:
//   //   - 注册新 Recaller: 实现 Recaller interface 并通过 Register 注入
//   //   - 业务规则注入: 实现 RuleRecaller 注入活动期间等业务规则
//   package recall
//
// 【2. interface 注释格式】（写在 interface 声明上方）
//
//   // <InterfaceName> <职责一句话>。
//   //
//   // 职责：<详细描述>。
//   // 实现：<实现类列表与位置>。
//   //
//   // 二开扩展点：<如何二开，列出实现 interface + Register 注入的方式>。
//   type <InterfaceName> interface {
//       // <Method> <做什么>。
//       // <参数说明>；<返回值说明>；<错误条件>。
//       <Method>(...) (..., error)
//   }
//
// 示例：
//
//   // Recaller 召回 interface，传统推荐快路径核心契约。
//   //
//   // 职责：从多数据源召回候选文章，零 LLM token。
//   // 实现：RuleRecaller/ContentRecaller/CFRecaller/GraphRecaller/...
//   //
//   // 二开扩展点：实现该 interface 并通过 recall.Register(name, r) 注入，
//   // OrchestratorAgent 即可在召回规划中调用该召回器。
//   type Recaller interface {
//       // Recall 执行召回。
//       // ctx 上下文；req 召回请求。
//       // 返回召回结果与 error。
//       Recall(ctx context.Context, req RecallRequest) (RecallResult, error)
//   }
//
// 【3. 函数/方法注释格式】（写在函数/方法声明上方）
//
//   // <FuncName> <做什么>。
//   // <参数说明>；<返回值说明>；<错误条件>。
//   func <FuncName>(...) (..., error) { ... }
//
// 示例：
//
//   // Register 注册召回器到注册表。
//   // name 召回器名称（如 "rule"/"content"/"cf"）；r 召回器实现。
//   // 返回 error（名称冲突时返回错误）。
//   func Register(name string, r Recaller) error { ... }
//
// 【4. 二开点注释格式】（写在二开相关的 interface/函数/类型上方或行尾）
//
//   // 二开：<如何二开，列出实现 interface + Register 注入的方式>
//
// 示例（写在 interface 注释块中）：
//
//   // 二开扩展点：实现该 interface 并通过 Register 注入。
//
// 示例（写在函数注释中）：
//
//   // Register 注册召回器。二开：实现 Recaller 并调用本函数注入。
//
// 示例（写在类型注释中）：
//
//   // RecommendConfig 推荐配置，所有可二开项通过该结构暴露。
//   type RecommendConfig struct { ... }
//
// 【5. 类型注释格式】（写在类型声明上方）
//
//   // <TypeName> <职责一句话>。
//   // <字段说明按需补充>。
//   // 二开：<如何二开（如适用）>。
//   type <TypeName> struct { ... }
//
// 示例：
//
//   // ArticleQuality 文章质量 6 维评分。
//   // 二开：通过 RecommendConfig.QualityModel 切换评判模型。
//   type ArticleQuality struct {
//       // Authority 权威性（0-1）。
//       Authority float64
//   }
//
// ============================================================================
// 规范要点
// ============================================================================
//
// 1. 所有注释使用中文。
// 2. 包注释写在 doc.go（不写在业务 .go 文件顶部）。
// 3. interface 注释必含"职责"+"二开扩展点"。
// 4. 函数注释说明"做什么"+"参数"+"返回值"+"错误条件"。
// 5. 二开点用"二开："或"二开扩展点："显式标注。
// 6. 依赖注入：构造函数接收 interface，不依赖具体实现，便于 mock 与二开。
// 7. 配置驱动：所有可二开项通过 config.yaml + RecommendConfig 暴露，无硬编码。
package internal
