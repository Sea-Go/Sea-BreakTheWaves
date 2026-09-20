// Package agent model.go — 模型分级配置。
//
// 该文件定义模型分级（小/大模型）与配置，对应 trpc-agent-go 的
// WithOptimizeForCache(true) 等能力。所有 Agent 通过 ModelConfig 选择模型，
// EnableCache 默认开启以降低 token 成本。
//
// 模型分级策略：
//   - 小模型（ModelTierSmall）：意图/召回规划/画像/频道等轻量任务
//   - 大模型（ModelTierLarge）：图谱/重排/质量/解释/编排等复杂任务
//
// 二开扩展点：
//   - 修改 defaultSmallModel/defaultLargeModel 切换模型版本
//   - 通过 AgentOptions.Model 覆盖默认模型
package agent

// ModelConfig 模型配置，描述一个 LLM 模型的名称、规模与调用参数。
type ModelConfig struct {
	// Name 模型名（如 "gpt-4o-mini" / "gpt-4o"）。
	Name string
	// Size 模型规模（"small" / "large"）。
	Size string
	// EnableCache 是否启用 Prompt Cache（对应 trpc-agent-go 的 WithOptimizeForCache(true)）。
	EnableCache bool
	// Temperature 采样温度。
	Temperature float64
	// MaxTokens 最大输出 token 数。
	MaxTokens int
}

// ModelTier 模型分级常量类型。
type ModelTier string

const (
	// ModelTierSmall 小模型，用于意图/召回规划/画像/频道等轻量任务。
	ModelTierSmall ModelTier = "small"
	// ModelTierLarge 大模型，用于图谱/重排/质量/解释/编排等复杂任务。
	ModelTierLarge ModelTier = "large"
)

// defaultSmallModel 默认小模型配置：低温度、短输出，适合结构化意图与召回规划。
var defaultSmallModel = ModelConfig{
	Name:        "small-model",
	Size:        string(ModelTierSmall),
	EnableCache: true,
	Temperature: 0.3,
	MaxTokens:   1024,
}

// defaultLargeModel 默认大模型配置：较高温度、长输出，适合图谱推理与解释生成。
var defaultLargeModel = ModelConfig{
	Name:        "large-model",
	Size:        string(ModelTierLarge),
	EnableCache: true,
	Temperature: 0.7,
	MaxTokens:   4096,
}

// NewModel 根据分级创建模型配置，EnableCache 固定为 true。
// tier 为 ModelTierSmall 或 ModelTierLarge，未知值默认返回小模型。
func NewModel(tier string) ModelConfig {
	switch tier {
	case string(ModelTierLarge):
		return defaultLargeModel
	default:
		return defaultSmallModel
	}
}

// NewSmallModel 创建小模型配置（意图/召回规划/画像/频道）。
func NewSmallModel() ModelConfig {
	return NewModel(string(ModelTierSmall))
}

// NewLargeModel 创建大模型配置（图谱/重排/质量/解释/编排）。
func NewLargeModel() ModelConfig {
	return NewModel(string(ModelTierLarge))
}

// ToLLMOptions 将 ModelConfig 转换为 LLMOptions，供 LLMClient.Complete 使用。
func (m ModelConfig) ToLLMOptions() LLMOptions {
	return LLMOptions{
		Temperature: m.Temperature,
		MaxTokens:   m.MaxTokens,
		Model:       m.Name,
		EnableCache: m.EnableCache,
	}
}
