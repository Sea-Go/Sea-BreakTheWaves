// Package agent model_tier.go — 模型分级降本提速（Task 13.10）。
//
// 该文件实现 ModelTierManager：按 Agent 名路由到小/大模型，估算成本，
// 生成成本报告与优化建议。
//
// 职责：
//   - GetModel：按 Agent 名返回对应模型配置（小/大模型）
//   - RegisterRouting：注册/覆盖 Agent 路由
//   - EstimateCost：估算单次 Agent 调用成本（元）
//   - CostBreakdown：批量生成成本报告
//   - OptimizeRouting：根据成本报告生成优化建议
//
// 模型分级策略：
//   - 小模型（intent/recall_planner/understand/profile/channel）：fast, low-cost
//   - 大模型（rerank/quality/explain/graph/orchestrator）：high-quality
//
// 二开扩展点：
//   - 通过 NewModelTierManager() 创建实例
//   - 通过 RegisterRouting 注册/覆盖 Agent 路由
//   - 通过 OptimizeRouting 获取优化建议
//
// 不直接 import trpc-agent-go / testify，所有依赖通过 interface 注入。
package agent

// ----------------------------------------------------------------------------
// 模型分级常量
// ----------------------------------------------------------------------------

// 小模型 Agent 名列表（fast, low-cost）。
var smallModelAgents = map[string]bool{
	"intent":         true,
	"recall_planner": true,
	"understand":     true,
	"profile":        true,
	"channel":        true,
}

// 大模型 Agent 名列表（high-quality）。
var largeModelAgents = map[string]bool{
	"rerank":       true,
	"quality":      true,
	"explain":      true,
	"graph":        true,
	"orchestrator": true,
}

// 模型成本（元/1K tokens）。
const (
	// smallModelInputCostPer1K 小模型输入成本（元/1K input tokens）。
	smallModelInputCostPer1K = 0.001
	// smallModelOutputCostPer1K 小模型输出成本（元/1K output tokens）。
	smallModelOutputCostPer1K = 0.002
	// largeModelInputCostPer1K 大模型输入成本（元/1K input tokens）。
	largeModelInputCostPer1K = 0.01
	// largeModelOutputCostPer1K 大模型输出成本（元/1K output tokens）。
	largeModelOutputCostPer1K = 0.03
)

// ----------------------------------------------------------------------------
// 成本报告与优化建议
// ----------------------------------------------------------------------------

// ModelCostReport 单个 Agent 的成本报告。
//
// 字段语义：
//   - AgentName：Agent 名
//   - Tier：模型分级（small/large）
//   - InputTokens：输入 token 数
//   - OutputTokens：输出 token 数
//   - CostYuan：估算成本（元）
//   - Model：模型名
type ModelCostReport struct {
	// AgentName Agent 名。
	AgentName string
	// Tier 模型分级。
	Tier string
	// InputTokens 输入 token 数。
	InputTokens int
	// OutputTokens 输出 token 数。
	OutputTokens int
	// CostYuan 估算成本（元）。
	CostYuan float64
	// Model 模型名。
	Model string
}

// AgentCall 单次 Agent 调用记录。
//
// 字段语义：
//   - AgentName：Agent 名
//   - InputTokens：输入 token 数
//   - OutputTokens：输出 token 数
//   - CachedTokens：命中 Prompt Cache 的 token 数
type AgentCall struct {
	// AgentName Agent 名。
	AgentName string
	// InputTokens 输入 token 数。
	InputTokens int
	// OutputTokens 输出 token 数。
	OutputTokens int
	// CachedTokens 命中 Prompt Cache 的 token 数。
	CachedTokens int
}

// RoutingOptimization 路由优化建议。
//
// 字段语义：
//   - Agent：Agent 名
//   - CurrentTier：当前模型分级
//   - SuggestedTier：建议的模型分级
//   - Reason：优化原因
//   - Savings：预估节省成本（元）
type RoutingOptimization struct {
	// Agent Agent 名。
	Agent string
	// CurrentTier 当前模型分级。
	CurrentTier string
	// SuggestedTier 建议的模型分级。
	SuggestedTier string
	// Reason 优化原因。
	Reason string
	// Savings 预估节省成本（元）。
	Savings float64
}

// ----------------------------------------------------------------------------
// ModelTierManager
// ----------------------------------------------------------------------------

// ModelTierManager 模型分级管理器。
//
// 字段语义：
//   - smallModel：小模型配置
//   - largeModel：大模型配置
//   - routing：Agent 名 → 模型分级（"small"/"large"）映射
//
// 二开扩展点：通过 NewModelTierManager() 创建实例；
// 通过 RegisterRouting 注册/覆盖路由。
type ModelTierManager struct {
	smallModel ModelConfig
	largeModel ModelConfig
	routing    map[string]string
}

// NewModelTierManager 构造 ModelTierManager。
//
// 初始化小/大模型配置（用 model.go 的 defaultSmallModel/defaultLargeModel），
// 与默认路由（smallModelAgents → small，largeModelAgents → large）。
// 返回 *ModelTierManager。
func NewModelTierManager() *ModelTierManager {
	m := &ModelTierManager{
		smallModel: NewSmallModel(),
		largeModel: NewLargeModel(),
		routing:    make(map[string]string),
	}
	// 初始化默认路由。
	for name := range smallModelAgents {
		m.routing[name] = string(ModelTierSmall)
	}
	for name := range largeModelAgents {
		m.routing[name] = string(ModelTierLarge)
	}
	return m
}

// GetModel 按 Agent 名返回模型配置。
//
// 路由顺序：
//  1. routing 表中已注册的 Agent 名 → 对应分级
//  2. smallModelAgents 中的 Agent 名 → 小模型
//  3. largeModelAgents 中的 Agent 名 → 大模型
//  4. 未匹配的 Agent 名 → 默认小模型（保守策略）
func (m *ModelTierManager) GetModel(agentName string) ModelConfig {
	if m == nil {
		return NewSmallModel()
	}
	tier, ok := m.routing[agentName]
	if !ok {
		// 未注册路由，检查默认列表。
		if smallModelAgents[agentName] {
			tier = string(ModelTierSmall)
		} else if largeModelAgents[agentName] {
			tier = string(ModelTierLarge)
		} else {
			// 默认小模型。
			tier = string(ModelTierSmall)
		}
	}
	switch tier {
	case string(ModelTierLarge):
		return m.largeModel
	default:
		return m.smallModel
	}
}

// RegisterRouting 注册/覆盖 Agent 路由。
//
// agentName Agent 名；tier 模型分级（"small"/"large"）。
// 未知 tier 默认按小模型处理。
func (m *ModelTierManager) RegisterRouting(agentName string, tier string) {
	if m == nil || agentName == "" {
		return
	}
	if tier != string(ModelTierSmall) && tier != string(ModelTierLarge) {
		tier = string(ModelTierSmall)
	}
	m.routing[agentName] = tier
}

// EstimateCost 估算单次 Agent 调用成本（元）。
//
// 小模型：0.001 元/1K input + 0.002 元/1K output
// 大模型：0.01 元/1K input + 0.03 元/1K output
//
// inputTokens 输入 token 数；outputTokens 输出 token 数。
func (m *ModelTierManager) EstimateCost(agentName string, inputTokens, outputTokens int) float64 {
	if m == nil {
		return 0
	}
	tier := m.GetTier(agentName)
	return computeCost(tier, inputTokens, outputTokens)
}

// GetTier 返回 Agent 对应的模型分级。
//
// 优先查 routing 表；未注册时按默认列表；仍未匹配返回 small。
func (m *ModelTierManager) GetTier(agentName string) string {
	if m == nil {
		return string(ModelTierSmall)
	}
	if tier, ok := m.routing[agentName]; ok {
		return tier
	}
	if smallModelAgents[agentName] {
		return string(ModelTierSmall)
	}
	if largeModelAgents[agentName] {
		return string(ModelTierLarge)
	}
	return string(ModelTierSmall)
}

// computeCost 按分级与 token 数计算成本（元）。
func computeCost(tier string, inputTokens, outputTokens int) float64 {
	inputCostPer1K := smallModelInputCostPer1K
	outputCostPer1K := smallModelOutputCostPer1K
	if tier == string(ModelTierLarge) {
		inputCostPer1K = largeModelInputCostPer1K
		outputCostPer1K = largeModelOutputCostPer1K
	}
	return float64(inputTokens)/1000*inputCostPer1K + float64(outputTokens)/1000*outputCostPer1K
}

// CostBreakdown 批量生成成本报告。
//
// 对每个 AgentCall 生成 ModelCostReport：
//   - 查 Agent 对应模型分级与配置
//   - 估算成本（CachedTokens 不计入成本，仅 InputTokens + OutputTokens）
//   - 报告含 AgentName/Tier/InputTokens/OutputTokens/CostYuan/Model
//
// nil 入参返回 nil（区别于空切片）。
func (m *ModelTierManager) CostBreakdown(agentCalls []AgentCall) []ModelCostReport {
	if m == nil || agentCalls == nil {
		return nil
	}
	reports := make([]ModelCostReport, 0, len(agentCalls))
	for _, call := range agentCalls {
		tier := m.GetTier(call.AgentName)
		var model ModelConfig
		if tier == string(ModelTierLarge) {
			model = m.largeModel
		} else {
			model = m.smallModel
		}
		// CachedTokens 不计入成本（命中 Prompt Cache 不计费）。
		billableInput := call.InputTokens - call.CachedTokens
		if billableInput < 0 {
			billableInput = 0
		}
		cost := computeCost(tier, billableInput, call.OutputTokens)
		reports = append(reports, ModelCostReport{
			AgentName:    call.AgentName,
			Tier:         tier,
			InputTokens:  call.InputTokens,
			OutputTokens: call.OutputTokens,
			CostYuan:     cost,
			Model:        model.Name,
		})
	}
	return reports
}

// OptimizeRouting 根据成本报告生成优化建议。
//
// 优化策略：
//  1. 计算小模型 Agent 与大模型 Agent 的总成本占比
//  2. 若小模型 Agent 成本占比 > 30%，建议进一步优化（如启用 Prompt Cache、
//     减少输入 token、合并 Agent 调用等）
//  3. 若大模型 Agent 单次成本 > 阈值（如 0.1 元），建议评估是否可降级到小模型
//
// 返回 RoutingOptimization 列表。
func (m *ModelTierManager) OptimizeRouting(report []ModelCostReport) []RoutingOptimization {
	if m == nil || len(report) == 0 {
		return nil
	}
	// 计算总成本与小模型成本。
	var totalCost float64
	var smallModelCost float64
	agentCostMap := make(map[string]float64) // 每个 Agent 的累计成本
	for _, r := range report {
		totalCost += r.CostYuan
		agentCostMap[r.AgentName] += r.CostYuan
		if r.Tier == string(ModelTierSmall) {
			smallModelCost += r.CostYuan
		}
	}
	var optimizations []RoutingOptimization
	// 小模型 Agent 成本占比 > 30%：建议优化。
	if totalCost > 0 {
		smallRatio := smallModelCost / totalCost
		if smallRatio > 0.30 {
			// 找出成本最高的小模型 Agent。
			var topAgent string
			var topCost float64
			for _, r := range report {
				if r.Tier == string(ModelTierSmall) && r.CostYuan > topCost {
					topAgent = r.AgentName
					topCost = r.CostYuan
				}
			}
			if topAgent != "" {
				optimizations = append(optimizations, RoutingOptimization{
					Agent:         topAgent,
					CurrentTier:   string(ModelTierSmall),
					SuggestedTier: string(ModelTierSmall),
					Reason:        "小模型 Agent 成本占比超 30%，建议启用 Prompt Cache 或减少输入 token",
					Savings:       topCost * 0.5, // 估算节省 50%
				})
			}
		}
	}
	// 大模型 Agent 单次成本 > 0.1 元：建议评估降级。
	for _, r := range report {
		if r.Tier == string(ModelTierLarge) && r.CostYuan > 0.1 {
			// 估算降级到小模型可节省的成本。
			smallCost := computeCost(string(ModelTierSmall), r.InputTokens, r.OutputTokens)
			savings := r.CostYuan - smallCost
			if savings > 0 {
				optimizations = append(optimizations, RoutingOptimization{
					Agent:         r.AgentName,
					CurrentTier:   string(ModelTierLarge),
					SuggestedTier: string(ModelTierSmall),
					Reason:        "大模型 Agent 单次成本 > 0.1 元，建议评估是否可降级到小模型",
					Savings:       savings,
				})
			}
		}
	}
	return optimizations
}

// ----------------------------------------------------------------------------
// 编译期断言
// ----------------------------------------------------------------------------

// 编译期断言：ModelTierManager 与配置结构自检。
var _ = NewModelTierManager
