// Package quality 实现文章质量评判：LLM Verifier + 6 维评分 + 反馈闭环。
//
// 包职责:
//
// 该包实现 QualityJudger interface（定义于 domain 包），提供文章多维质量
// 评分（6 维：Authority/Depth/Freshness/Completeness/Readability/Citation）。
// 候选 Agent 用 WithStructuredOutputJSONSchema 输出 ArticleQuality；裁判
// Agent 用 Logprobs=true/TopLogprobs=20 获取质量标签概率分布；Best-of-N
// 编排（WithAttempts(3)/SelectionModePairwise/llm_verifier_pairwise）选择
// 最高质量。配合 RecommendConfig.QualityThreshold 过滤、validate 真实
// grounding、反馈闭环与图谱 QualityReport 节点。
//
// 核心 interface:
//   - QualityJudger: 质量评判 interface（定义于 domain 包），Judge
//   - Rubric: 质量评判标准（accuracy/authority/depth/freshness/...）
//   - FeedbackHook: 质量反馈 Hook，写入 ArticleQualityFeedback 表 + 图谱
//
// 二开扩展点:
//   - 自定义 rubric: 实现 Rubric interface 注入业务评判标准
//   - 替换评判 Agent: 实现 QualityJudger interface 接入自研评判模型
//   - 反馈校准: 通过 FeedbackHook 反馈定期校准 rubrics
package quality
