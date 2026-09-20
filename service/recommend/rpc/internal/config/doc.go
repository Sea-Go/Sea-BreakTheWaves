// Package config 实现配置驱动体系（双路径/灰度/模型分级）。
//
// 包职责:
//
// 该包定义推荐系统的全部配置结构，所有可二开项通过 config.yaml +
// RecommendConfig 暴露，无硬编码。包含子结构：RecommendConfig（TopK/
// RecallStrategy/RerankModel/RerankWeights/AgentLoopEnabled/FallbackEnabled/
// MaxToolCalls/PromptCacheEnabled/SummaryEnabled/BestOfN/QualityThreshold/
// CFEnabled/SearchEnabled/TemporalDecayEnabled/PathMode）、ChannelConfig、
// PromptCacheConfig、SummaryConfig、QualityConfig、BestOfNConfig、AGUIConfig、
// EvalConfig、SearchConfig、CFConfig、RerankSelfConfig、CPUConfig、
// TemporalProfileConfig、GraphConfig、SkillConfig、PathModeConfig。
// 支持双路径（auto/fast/slow/hybrid）、灰度切换、模型分级、A/B 分桶。
//
// 核心 interface:
//   - Config: 配置根 interface，加载/校验/热更新
//   - Provider: 配置源 interface（文件/环境/远程）
//   - Validator: 配置校验 interface
//
// 二开扩展点:
//   - 自定义配置源: 实现 Provider interface 接入远程配置中心
//   - 新增配置项: 在对应子结构新增字段并通过 yaml 标签暴露
//   - 校验规则: 实现 Validator interface 注入业务校验逻辑
package config
