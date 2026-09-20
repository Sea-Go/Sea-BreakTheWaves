// Package agent model_tier_test.go — ModelTierManager 单元测试（Task 13.10）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - GetModel 按 Agent 名返回模型（小/大模型）
//   - RegisterRouting 注册/覆盖路由
//   - EstimateCost 成本估算
//   - CostBreakdown 成本分解
//   - OptimizeRouting 优化建议
//
// stub 命名加 MT（ModelTier）前缀避免与已有 stub 冲突。
package agent

import (
	"testing"
)

// ----------------------------------------------------------------------------
// GetModel 测试
// ----------------------------------------------------------------------------

// TestGetModel_SmallAgents 验证小模型 Agent 返回小模型配置。
func TestGetModel_SmallAgents(t *testing.T) {
	m := NewModelTierManager()
	for _, name := range []string{"intent", "recall_planner", "understand", "profile", "channel"} {
		model := m.GetModel(name)
		if model.Size != string(ModelTierSmall) {
			t.Errorf("Agent %q model.Size = %q, 期望 %q", name, model.Size, string(ModelTierSmall))
		}
	}
}

// TestGetModel_LargeAgents 验证大模型 Agent 返回大模型配置。
func TestGetModel_LargeAgents(t *testing.T) {
	m := NewModelTierManager()
	for _, name := range []string{"rerank", "quality", "explain", "graph", "orchestrator"} {
		model := m.GetModel(name)
		if model.Size != string(ModelTierLarge) {
			t.Errorf("Agent %q model.Size = %q, 期望 %q", name, model.Size, string(ModelTierLarge))
		}
	}
}

// TestGetModel_UnknownAgent 验证未知 Agent 默认返回小模型。
func TestGetModel_UnknownAgent(t *testing.T) {
	m := NewModelTierManager()
	model := m.GetModel("unknown_agent")
	if model.Size != string(ModelTierSmall) {
		t.Errorf("未知 Agent model.Size = %q, 期望 %q", model.Size, string(ModelTierSmall))
	}
}

// TestGetModel_EnableCache 验证返回的模型 EnableCache 默认为 true。
func TestGetModel_EnableCache(t *testing.T) {
	m := NewModelTierManager()
	small := m.GetModel("intent")
	if !small.EnableCache {
		t.Error("小模型 EnableCache 应为 true")
	}
	large := m.GetModel("rerank")
	if !large.EnableCache {
		t.Error("大模型 EnableCache 应为 true")
	}
}

// ----------------------------------------------------------------------------
// RegisterRouting 测试
// ----------------------------------------------------------------------------

// TestRegisterRouting_Override 验证 RegisterRouting 覆盖默认路由。
func TestRegisterRouting_Override(t *testing.T) {
	m := NewModelTierManager()
	// intent 默认是小模型，覆盖为大模型。
	m.RegisterRouting("intent", string(ModelTierLarge))
	model := m.GetModel("intent")
	if model.Size != string(ModelTierLarge) {
		t.Errorf("覆盖后 intent model.Size = %q, 期望 %q", model.Size, string(ModelTierLarge))
	}
}

// TestRegisterRouting_NewAgent 验证 RegisterRouting 注册新 Agent。
func TestRegisterRouting_NewAgent(t *testing.T) {
	m := NewModelTierManager()
	// 注册新 Agent 为大模型。
	m.RegisterRouting("custom_agent", string(ModelTierLarge))
	model := m.GetModel("custom_agent")
	if model.Size != string(ModelTierLarge) {
		t.Errorf("custom_agent model.Size = %q, 期望 %q", model.Size, string(ModelTierLarge))
	}
}

// TestRegisterRouting_InvalidTier 验证未知 tier 默认按小模型。
func TestRegisterRouting_InvalidTier(t *testing.T) {
	m := NewModelTierManager()
	m.RegisterRouting("test_agent", "invalid_tier")
	model := m.GetModel("test_agent")
	if model.Size != string(ModelTierSmall) {
		t.Errorf("未知 tier 时 model.Size = %q, 期望 %q", model.Size, string(ModelTierSmall))
	}
}

// TestRegisterRouting_EmptyName 验证空名不注册。
func TestRegisterRouting_EmptyName(t *testing.T) {
	m := NewModelTierManager()
	m.RegisterRouting("", string(ModelTierLarge))
	// 不应 panic。
}

// ----------------------------------------------------------------------------
// EstimateCost 测试
// ----------------------------------------------------------------------------

// TestEstimateCost_SmallModel 验证小模型成本估算。
func TestEstimateCost_SmallModel(t *testing.T) {
	m := NewModelTierManager()
	// 小模型：1000 input + 1000 output = 0.001 + 0.002 = 0.003 元。
	cost := m.EstimateCost("intent", 1000, 1000)
	expected := 0.001 + 0.002
	if !floatEquals(cost, expected) {
		t.Errorf("小模型成本 = %v, 期望 %v", cost, expected)
	}
}

// TestEstimateCost_LargeModel 验证大模型成本估算。
func TestEstimateCost_LargeModel(t *testing.T) {
	m := NewModelTierManager()
	// 大模型：1000 input + 1000 output = 0.01 + 0.03 = 0.04 元。
	cost := m.EstimateCost("rerank", 1000, 1000)
	expected := 0.01 + 0.03
	if !floatEquals(cost, expected) {
		t.Errorf("大模型成本 = %v, 期望 %v", cost, expected)
	}
}

// TestEstimateCost_ZeroTokens 验证 0 token 时成本为 0。
func TestEstimateCost_ZeroTokens(t *testing.T) {
	m := NewModelTierManager()
	cost := m.EstimateCost("intent", 0, 0)
	if cost != 0 {
		t.Errorf("0 token 成本 = %v, 期望 0", cost)
	}
}

// TestEstimateCost_PartialTokens 验证非整千 token 成本计算。
func TestEstimateCost_PartialTokens(t *testing.T) {
	m := NewModelTierManager()
	// 小模型：500 input + 500 output = 0.0005 + 0.001 = 0.0015 元。
	cost := m.EstimateCost("intent", 500, 500)
	expected := 0.0005 + 0.001
	if !floatEquals(cost, expected) {
		t.Errorf("小模型部分 token 成本 = %v, 期望 %v", cost, expected)
	}
}

// ----------------------------------------------------------------------------
// CostBreakdown 测试
// ----------------------------------------------------------------------------

// TestCostBreakdown_Basic 验证基本成本分解。
func TestCostBreakdown_Basic(t *testing.T) {
	m := NewModelTierManager()
	calls := []AgentCall{
		{AgentName: "intent", InputTokens: 1000, OutputTokens: 500, CachedTokens: 0},
		{AgentName: "rerank", InputTokens: 2000, OutputTokens: 1000, CachedTokens: 0},
	}
	reports := m.CostBreakdown(calls)
	if len(reports) != 2 {
		t.Fatalf("reports 长度 = %d, 期望 2", len(reports))
	}
	// 验证 intent（小模型）。
	if reports[0].AgentName != "intent" {
		t.Errorf("reports[0].AgentName = %q, 期望 intent", reports[0].AgentName)
	}
	if reports[0].Tier != string(ModelTierSmall) {
		t.Errorf("reports[0].Tier = %q, 期望 %q", reports[0].Tier, string(ModelTierSmall))
	}
	expectedCost := 0.001 + 0.001 // 1000 input + 500 output（小模型）
	if !floatEquals(reports[0].CostYuan, expectedCost) {
		t.Errorf("reports[0].CostYuan = %v, 期望 %v", reports[0].CostYuan, expectedCost)
	}
	// 验证 rerank（大模型）。
	if reports[1].AgentName != "rerank" {
		t.Errorf("reports[1].AgentName = %q, 期望 rerank", reports[1].AgentName)
	}
	if reports[1].Tier != string(ModelTierLarge) {
		t.Errorf("reports[1].Tier = %q, 期望 %q", reports[1].Tier, string(ModelTierLarge))
	}
}

// TestCostBreakdown_CachedTokens 验证 CachedTokens 不计入成本。
func TestCostBreakdown_CachedTokens(t *testing.T) {
	m := NewModelTierManager()
	calls := []AgentCall{
		{AgentName: "intent", InputTokens: 1000, OutputTokens: 500, CachedTokens: 500},
	}
	reports := m.CostBreakdown(calls)
	if len(reports) != 1 {
		t.Fatalf("reports 长度 = %d, 期望 1", len(reports))
	}
	// billable input = 1000 - 500 = 500。
	// 小模型：500/1000 * 0.001 + 500/1000 * 0.002 = 0.0005 + 0.001 = 0.0015。
	expectedCost := 0.0005 + 0.001
	if !floatEquals(reports[0].CostYuan, expectedCost) {
		t.Errorf("CachedTokens 不计入成本, CostYuan = %v, 期望 %v", reports[0].CostYuan, expectedCost)
	}
}

// TestCostBreakdown_AllCached 验证全部 cached 时 input 成本为 0。
func TestCostBreakdown_AllCached(t *testing.T) {
	m := NewModelTierManager()
	calls := []AgentCall{
		{AgentName: "intent", InputTokens: 1000, OutputTokens: 0, CachedTokens: 1000},
	}
	reports := m.CostBreakdown(calls)
	if len(reports) != 1 {
		t.Fatalf("reports 长度 = %d, 期望 1", len(reports))
	}
	if reports[0].CostYuan != 0 {
		t.Errorf("全部 cached 时成本应 = 0, 实际 %v", reports[0].CostYuan)
	}
}

// TestCostBreakdown_EmptyCalls 验证空 calls 返回 nil。
func TestCostBreakdown_EmptyCalls(t *testing.T) {
	m := NewModelTierManager()
	reports := m.CostBreakdown(nil)
	if reports != nil {
		t.Errorf("空 calls 应返回 nil, 实际 %v", reports)
	}
}

// TestCostBreakdown_ModelField 验证报告 Model 字段。
func TestCostBreakdown_ModelField(t *testing.T) {
	m := NewModelTierManager()
	calls := []AgentCall{
		{AgentName: "intent", InputTokens: 100, OutputTokens: 50},
	}
	reports := m.CostBreakdown(calls)
	if reports[0].Model == "" {
		t.Error("Model 字段不应为空")
	}
}

// ----------------------------------------------------------------------------
// OptimizeRouting 测试
// ----------------------------------------------------------------------------

// TestOptimizeRouting_SmallModelHighCost 验证小模型 Agent 成本占比 > 30% 时建议优化。
func TestOptimizeRouting_SmallModelHighCost(t *testing.T) {
	m := NewModelTierManager()
	// 小模型成本占比 > 30%：intent 0.003 + rerank 0.04 = 0.043 总。
	// 小模型占比 = 0.003 / 0.043 ≈ 7%（不够 30%）。
	// 改为：intent 大量调用，让小模型占比 > 30%。
	reports := []ModelCostReport{
		{AgentName: "intent", Tier: string(ModelTierSmall), InputTokens: 10000, OutputTokens: 5000, CostYuan: 0.02},
		{AgentName: "rerank", Tier: string(ModelTierLarge), InputTokens: 1000, OutputTokens: 500, CostYuan: 0.025},
	}
	// 小模型占比 = 0.02 / (0.02 + 0.025) = 0.444 > 30%。
	optimizations := m.OptimizeRouting(reports)
	if len(optimizations) == 0 {
		t.Error("小模型占比 > 30% 应有优化建议")
	}
	// 验证建议针对小模型 Agent。
	found := false
	for _, opt := range optimizations {
		if opt.Agent == "intent" {
			found = true
			if opt.Savings <= 0 {
				t.Errorf("Savings 应 > 0, 实际 %v", opt.Savings)
			}
		}
	}
	if !found {
		t.Error("应有针对 intent 的优化建议")
	}
}

// TestOptimizeRouting_LargeModelHighCost 验证大模型 Agent 单次成本 > 0.1 元时建议降级。
func TestOptimizeRouting_LargeModelHighCost(t *testing.T) {
	m := NewModelTierManager()
	reports := []ModelCostReport{
		{AgentName: "rerank", Tier: string(ModelTierLarge), InputTokens: 5000, OutputTokens: 5000, CostYuan: 0.2},
	}
	optimizations := m.OptimizeRouting(reports)
	if len(optimizations) == 0 {
		t.Fatal("大模型单次成本 > 0.1 应有优化建议")
	}
	opt := optimizations[0]
	if opt.Agent != "rerank" {
		t.Errorf("Agent = %q, 期望 rerank", opt.Agent)
	}
	if opt.CurrentTier != string(ModelTierLarge) {
		t.Errorf("CurrentTier = %q, 期望 %q", opt.CurrentTier, string(ModelTierLarge))
	}
	if opt.SuggestedTier != string(ModelTierSmall) {
		t.Errorf("SuggestedTier = %q, 期望 %q", opt.SuggestedTier, string(ModelTierSmall))
	}
	if opt.Savings <= 0 {
		t.Errorf("Savings 应 > 0, 实际 %v", opt.Savings)
	}
}

// TestOptimizeRouting_NoOptimization 验证无优化场景。
func TestOptimizeRouting_NoOptimization(t *testing.T) {
	m := NewModelTierManager()
	reports := []ModelCostReport{
		{AgentName: "intent", Tier: string(ModelTierSmall), InputTokens: 100, OutputTokens: 50, CostYuan: 0.0002},
		{AgentName: "rerank", Tier: string(ModelTierLarge), InputTokens: 100, OutputTokens: 50, CostYuan: 0.0025},
	}
	// 小模型占比 0.0002 / 0.0027 ≈ 7% < 30%，大模型单次 0.0025 < 0.1。
	optimizations := m.OptimizeRouting(reports)
	if len(optimizations) != 0 {
		t.Errorf("无优化场景应返回空, 实际 %d 个建议", len(optimizations))
	}
}

// TestOptimizeRouting_EmptyReports 验证空报告返回 nil。
func TestOptimizeRouting_EmptyReports(t *testing.T) {
	m := NewModelTierManager()
	if opts := m.OptimizeRouting(nil); opts != nil {
		t.Errorf("空报告应返回 nil, 实际 %v", opts)
	}
}

// ----------------------------------------------------------------------------
// GetTier 测试
// ----------------------------------------------------------------------------

// TestGetTier 验证 GetTier 返回正确分级。
func TestGetTier(t *testing.T) {
	m := NewModelTierManager()
	if tier := m.GetTier("intent"); tier != string(ModelTierSmall) {
		t.Errorf("intent tier = %q, 期望 %q", tier, string(ModelTierSmall))
	}
	if tier := m.GetTier("rerank"); tier != string(ModelTierLarge) {
		t.Errorf("rerank tier = %q, 期望 %q", tier, string(ModelTierLarge))
	}
	if tier := m.GetTier("unknown"); tier != string(ModelTierSmall) {
		t.Errorf("unknown tier = %q, 期望 %q", tier, string(ModelTierSmall))
	}
}

// ----------------------------------------------------------------------------
// 零值与 nil 测试
// ----------------------------------------------------------------------------

// TestModelCostReport_ZeroValue 验证零值安全。
func TestModelCostReport_ZeroValue(t *testing.T) {
	var r ModelCostReport
	if r.AgentName != "" || r.Tier != "" || r.CostYuan != 0 {
		t.Errorf("零值 ModelCostReport 不正确: %+v", r)
	}
}

// TestAgentCall_ZeroValue 验证零值安全。
func TestAgentCall_ZeroValue(t *testing.T) {
	var c AgentCall
	if c.AgentName != "" || c.InputTokens != 0 || c.OutputTokens != 0 || c.CachedTokens != 0 {
		t.Errorf("零值 AgentCall 不正确: %+v", c)
	}
}

// TestRoutingOptimization_ZeroValue 验证零值安全。
func TestRoutingOptimization_ZeroValue(t *testing.T) {
	var r RoutingOptimization
	if r.Agent != "" || r.CurrentTier != "" || r.SuggestedTier != "" || r.Savings != 0 {
		t.Errorf("零值 RoutingOptimization 不正确: %+v", r)
	}
}

// TestModelTierManager_NilReceiver 验证 nil receiver 安全处理。
func TestModelTierManager_NilReceiver(t *testing.T) {
	var m *ModelTierManager
	// GetModel 不应 panic。
	model := m.GetModel("intent")
	if model.Size != string(ModelTierSmall) {
		t.Errorf("nil receiver GetModel 应返回小模型, 实际 %q", model.Size)
	}
	// RegisterRouting 不应 panic。
	m.RegisterRouting("test", string(ModelTierLarge))
	// EstimateCost 应返回 0。
	if cost := m.EstimateCost("intent", 1000, 1000); cost != 0 {
		t.Errorf("nil receiver EstimateCost 应返回 0, 实际 %v", cost)
	}
	// CostBreakdown 应返回 nil。
	if reports := m.CostBreakdown(nil); reports != nil {
		t.Errorf("nil receiver CostBreakdown 应返回 nil, 实际 %v", reports)
	}
	// OptimizeRouting 应返回 nil。
	if opts := m.OptimizeRouting(nil); opts != nil {
		t.Errorf("nil receiver OptimizeRouting 应返回 nil, 实际 %v", opts)
	}
	// GetTier 应返回 small。
	if tier := m.GetTier("intent"); tier != string(ModelTierSmall) {
		t.Errorf("nil receiver GetTier 应返回 small, 实际 %q", tier)
	}
}

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// floatEquals 比较两个 float64 是否近似相等（避免浮点精度问题）。
func floatEquals(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
