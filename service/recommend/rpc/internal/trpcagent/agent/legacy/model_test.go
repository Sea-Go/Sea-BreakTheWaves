// Package agent model_test.go — 模型分级配置测试。
//
// 该文件测试 internal/agent/model.go 的 ModelConfig/NewModel/NewSmallModel/
// NewLargeModel/ToLLMOptions，覆盖小/大模型/未知 tier/EnableCache 始终为 true 等场景。
package agent

import "testing"

// TestNewModel_Small 验证小模型配置。
func TestNewModel_Small(t *testing.T) {
	m := NewModel(string(ModelTierSmall))
	if m.Size != string(ModelTierSmall) {
		t.Errorf("Size = %q, 期望 small", m.Size)
	}
	if !m.EnableCache {
		t.Errorf("EnableCache 应为 true")
	}
	if m.Temperature <= 0 {
		t.Errorf("Temperature 应为正数, 实际 %f", m.Temperature)
	}
	if m.MaxTokens <= 0 {
		t.Errorf("MaxTokens 应为正数, 实际 %d", m.MaxTokens)
	}
	if m.Name == "" {
		t.Errorf("Name 不应为空")
	}
}

// TestNewModel_Large 验证大模型配置。
func TestNewModel_Large(t *testing.T) {
	m := NewModel(string(ModelTierLarge))
	if m.Size != string(ModelTierLarge) {
		t.Errorf("Size = %q, 期望 large", m.Size)
	}
	if !m.EnableCache {
		t.Errorf("EnableCache 应为 true")
	}
	if m.MaxTokens <= 0 {
		t.Errorf("MaxTokens 应为正数, 实际 %d", m.MaxTokens)
	}
}

// TestNewModel_UnknownTier 验证未知 tier 默认返回小模型。
func TestNewModel_UnknownTier(t *testing.T) {
	m := NewModel("unknown")
	if m.Size != string(ModelTierSmall) {
		t.Errorf("未知 tier 应默认 small, 实际 %q", m.Size)
	}
}

// TestNewSmallModel 验证 NewSmallModel 返回小模型。
func TestNewSmallModel(t *testing.T) {
	m := NewSmallModel()
	if m.Size != string(ModelTierSmall) {
		t.Errorf("Size = %q, 期望 small", m.Size)
	}
}

// TestNewLargeModel 验证 NewLargeModel 返回大模型。
func TestNewLargeModel(t *testing.T) {
	m := NewLargeModel()
	if m.Size != string(ModelTierLarge) {
		t.Errorf("Size = %q, 期望 large", m.Size)
	}
}

// TestNewModel_EnableCacheAlwaysTrue 验证所有 tier 的 EnableCache 始终为 true。
func TestNewModel_EnableCacheAlwaysTrue(t *testing.T) {
	for _, tier := range []string{string(ModelTierSmall), string(ModelTierLarge), "unknown"} {
		m := NewModel(tier)
		if !m.EnableCache {
			t.Errorf("tier %q: EnableCache 应始终为 true", tier)
		}
	}
}

// TestNewModel_LargeBiggerThanSmall 验证大模型 MaxTokens 大于小模型。
func TestNewModel_LargeBiggerThanSmall(t *testing.T) {
	small := NewSmallModel()
	large := NewLargeModel()
	if large.MaxTokens <= small.MaxTokens {
		t.Errorf("大模型 MaxTokens (%d) 应大于小模型 (%d)", large.MaxTokens, small.MaxTokens)
	}
}

// TestModelConfig_ToLLMOptions 验证 ToLLMOptions 字段映射正确。
func TestModelConfig_ToLLMOptions(t *testing.T) {
	m := ModelConfig{
		Name:        "test-model",
		Size:        "small",
		EnableCache: true,
		Temperature: 0.5,
		MaxTokens:   2048,
	}
	opts := m.ToLLMOptions()
	if opts.Model != "test-model" {
		t.Errorf("Model = %q, 期望 test-model", opts.Model)
	}
	if opts.Temperature != 0.5 {
		t.Errorf("Temperature = %f, 期望 0.5", opts.Temperature)
	}
	if opts.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, 期望 2048", opts.MaxTokens)
	}
	if !opts.EnableCache {
		t.Errorf("EnableCache 应为 true")
	}
}

// TestModelConfig_ToLLMOptions_CacheDisabled 验证 EnableCache=false 时映射正确。
func TestModelConfig_ToLLMOptions_CacheDisabled(t *testing.T) {
	m := ModelConfig{
		Name:        "no-cache-model",
		EnableCache: false,
		Temperature: 0.1,
		MaxTokens:   100,
	}
	opts := m.ToLLMOptions()
	if opts.EnableCache {
		t.Errorf("EnableCache 应为 false")
	}
	if opts.Model != "no-cache-model" {
		t.Errorf("Model = %q, 期望 no-cache-model", opts.Model)
	}
}
