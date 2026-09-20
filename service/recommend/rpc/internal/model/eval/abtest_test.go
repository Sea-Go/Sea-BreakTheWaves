// internal/eval/abtest_test.go — 在线 A/B 测试单测（Task 15.6）。
//
// 覆盖：
//   - CreateExperiment 校验（ID/Variant 数量/Weight）
//   - AssignVariant 分桶稳定性（同 user 永远同 bucket）+ 加权分布
//   - RecordMetric 指标记录（impression/click/conversion）
//   - GetResults 报告生成（CTR/CVR/延迟/成本）
//   - CompareVariants 显著性检验（显著/不显著/样本不足）
//   - Stop 停止实验
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"testing"
)

// ============================================================================
// stub / 工具
// ============================================================================

// makeExperiment 构造一个标准 2-variant 实验。
func makeExperiment(id string) Experiment {
	return Experiment{
		ID:   id,
		Name: "test-exp",
		Variants: []Variant{
			{ID: "control", Name: "control", Weight: 50, Config: map[string]any{}},
			{ID: "treatment", Name: "treatment", Weight: 50, Config: map[string]any{}},
		},
	}
}

// ============================================================================
// 测试用例 — CreateExperiment
// ============================================================================

// TestABTestService_CreateExperiment_Success 验证成功创建实验。
func TestABTestService_CreateExperiment_Success(t *testing.T) {
	svc := NewABTestService()
	exp := makeExperiment("exp-1")
	if err := svc.CreateExperiment(context.Background(), exp); err != nil {
		t.Fatalf("CreateExperiment 错误: %v", err)
	}
}

// TestABTestService_CreateExperiment_Duplicate 验证重复创建报错。
func TestABTestService_CreateExperiment_Duplicate(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	exp := makeExperiment("exp-1")
	_ = svc.CreateExperiment(ctx, exp)
	if err := svc.CreateExperiment(ctx, exp); err == nil {
		t.Errorf("重复创建应报错")
	}
}

// TestABTestService_CreateExperiment_EmptyID 验证空 ID 报错。
func TestABTestService_CreateExperiment_EmptyID(t *testing.T) {
	svc := NewABTestService()
	exp := makeExperiment("")
	if err := svc.CreateExperiment(context.Background(), exp); err == nil {
		t.Errorf("空 ID 应报错")
	}
}

// TestABTestService_CreateExperiment_TooFewVariants 验证 variant 数量 < 2 报错。
func TestABTestService_CreateExperiment_TooFewVariants(t *testing.T) {
	svc := NewABTestService()
	exp := Experiment{
		ID:       "exp-1",
		Variants: []Variant{{ID: "v1", Weight: 1}},
	}
	if err := svc.CreateExperiment(context.Background(), exp); err == nil {
		t.Errorf("variant < 2 应报错")
	}
}

// TestABTestService_CreateExperiment_ZeroWeight 验证 weight <= 0 报错。
func TestABTestService_CreateExperiment_ZeroWeight(t *testing.T) {
	svc := NewABTestService()
	exp := Experiment{
		ID: "exp-1",
		Variants: []Variant{
			{ID: "v1", Weight: 0},
			{ID: "v2", Weight: 1},
		},
	}
	if err := svc.CreateExperiment(context.Background(), exp); err == nil {
		t.Errorf("weight <= 0 应报错")
	}
}

// ============================================================================
// 测试用例 — AssignVariant 分桶稳定性
// ============================================================================

// TestABTestService_AssignVariant_Stability 验证同 user 永远分到同 bucket。
func TestABTestService_AssignVariant_Stability(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))

	v1, err := svc.AssignVariant(ctx, "exp-1", "user-123")
	if err != nil {
		t.Fatalf("AssignVariant 错误: %v", err)
	}
	// 多次调用应返回相同 variant。
	for i := 0; i < 10; i++ {
		v, err := svc.AssignVariant(ctx, "exp-1", "user-123")
		if err != nil {
			t.Fatalf("AssignVariant 错误: %v", err)
		}
		if v.ID != v1.ID {
			t.Errorf("同 user 第 %d 次分桶 variant = %s, 期望 %s", i, v.ID, v1.ID)
		}
	}
}

// TestABTestService_AssignVariant_Distribution 验证分桶分布合理（加权）。
func TestABTestService_AssignVariant_Distribution(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))

	counts := map[string]int{}
	N := 1000
	for i := 0; i < N; i++ {
		userID := "user-" + itoa(i)
		v, err := svc.AssignVariant(ctx, "exp-1", userID)
		if err != nil {
			t.Fatalf("AssignVariant 错误: %v", err)
		}
		counts[v.ID]++
	}
	// 50/50 权重，每个 variant 应在 [400, 600] 之间。
	for _, id := range []string{"control", "treatment"} {
		if counts[id] < 400 || counts[id] > 600 {
			t.Errorf("variant %s 计数 = %d, 期望 400-600", id, counts[id])
		}
	}
}

// TestABTestService_AssignVariant_NotExist 验证实验不存在报错。
func TestABTestService_AssignVariant_NotExist(t *testing.T) {
	svc := NewABTestService()
	if _, err := svc.AssignVariant(context.Background(), "nope", "u1"); err == nil {
		t.Errorf("实验不存在应报错")
	}
}

// TestABTestService_AssignVariant_Stopped 验证已停止实验报错。
func TestABTestService_AssignVariant_Stopped(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))
	_ = svc.Stop(ctx, "exp-1")
	if _, err := svc.AssignVariant(ctx, "exp-1", "u1"); err == nil {
		t.Errorf("已停止实验应报错")
	}
}

// ============================================================================
// 测试用例 — RecordMetric
// ============================================================================

// TestABTestService_RecordMetric_Success 验证指标记录成功。
func TestABTestService_RecordMetric_Success(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))

	// 记录 impression + click + conversion。
	events := []MetricEvent{
		{Type: string(MetricEventImpression), VariantID: "control", LatencyMs: 50, CostYuan: 0.01},
		{Type: string(MetricEventImpression), VariantID: "control", LatencyMs: 100, CostYuan: 0.02},
		{Type: string(MetricEventClick), VariantID: "control"},
		{Type: string(MetricEventConversion), VariantID: "control"},
	}
	for _, e := range events {
		if err := svc.RecordMetric(ctx, "exp-1", e.VariantID, e); err != nil {
			t.Fatalf("RecordMetric 错误: %v", err)
		}
	}

	report, _ := svc.GetResults(ctx, "exp-1")
	if len(report.Variants) != 2 {
		t.Fatalf("Variants 长度 = %d, 期望 2", len(report.Variants))
	}
	var control *VariantResult
	for i := range report.Variants {
		if report.Variants[i].VariantID == "control" {
			control = &report.Variants[i]
		}
	}
	if control == nil {
		t.Fatalf("报告中缺少 control")
	}
	if control.Metrics.Impressions != 2 {
		t.Errorf("Impressions = %d, 期望 2", control.Metrics.Impressions)
	}
	if control.Metrics.Clicks != 1 {
		t.Errorf("Clicks = %d, 期望 1", control.Metrics.Clicks)
	}
	if control.Metrics.Conversions != 1 {
		t.Errorf("Conversions = %d, 期望 1", control.Metrics.Conversions)
	}
	if control.Metrics.LatencyMs != 150 {
		t.Errorf("LatencyMs = %d, 期望 150", control.Metrics.LatencyMs)
	}
	if !floatEq(control.Metrics.CostYuan, 0.03) {
		t.Errorf("CostYuan = %v, 期望 0.03", control.Metrics.CostYuan)
	}
	// CTR = 1/2 = 0.5, CVR = 1/1 = 1.0。
	if !floatEq(control.Metrics.CTR, 0.5) {
		t.Errorf("CTR = %v, 期望 0.5", control.Metrics.CTR)
	}
	if !floatEq(control.Metrics.CVR, 1.0) {
		t.Errorf("CVR = %v, 期望 1.0", control.Metrics.CVR)
	}
	if control.Sample != 2 {
		t.Errorf("Sample = %d, 期望 2", control.Sample)
	}
}

// TestABTestService_RecordMetric_NotExist 验证实验不存在报错。
func TestABTestService_RecordMetric_NotExist(t *testing.T) {
	svc := NewABTestService()
	err := svc.RecordMetric(context.Background(), "nope", "v1", MetricEvent{Type: string(MetricEventImpression)})
	if err == nil {
		t.Errorf("实验不存在应报错")
	}
}

// TestABTestService_RecordMetric_UnknownEvent 验证未知事件类型报错。
func TestABTestService_RecordMetric_UnknownEvent(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))
	err := svc.RecordMetric(ctx, "exp-1", "control", MetricEvent{Type: "unknown"})
	if err == nil {
		t.Errorf("未知事件类型应报错")
	}
}

// TestABTestService_RecordMetric_VariantNotExist 验证 variant 不存在报错。
func TestABTestService_RecordMetric_VariantNotExist(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))
	err := svc.RecordMetric(ctx, "exp-1", "nope", MetricEvent{Type: string(MetricEventImpression)})
	if err == nil {
		t.Errorf("variant 不存在应报错")
	}
}

// ============================================================================
// 测试用例 — GetResults
// ============================================================================

// TestABTestService_GetResults_NotExist 验证实验不存在报错。
func TestABTestService_GetResults_NotExist(t *testing.T) {
	svc := NewABTestService()
	if _, err := svc.GetResults(context.Background(), "nope"); err == nil {
		t.Errorf("实验不存在应报错")
	}
}

// TestABTestService_GetResults_EmptyMetrics 验证无事件时返回空指标。
func TestABTestService_GetResults_EmptyMetrics(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))

	report, err := svc.GetResults(ctx, "exp-1")
	if err != nil {
		t.Fatalf("GetResults 错误: %v", err)
	}
	if len(report.Variants) != 2 {
		t.Fatalf("Variants 长度 = %d, 期望 2", len(report.Variants))
	}
	for _, v := range report.Variants {
		if v.Metrics.Impressions != 0 {
			t.Errorf("无事件时 Impressions 应为 0, 实际 %d", v.Metrics.Impressions)
		}
		if v.Sample != 0 {
			t.Errorf("无事件时 Sample 应为 0, 实际 %d", v.Sample)
		}
	}
	// 无样本时 winner 应为空。
	if report.Winner != "" {
		t.Errorf("无样本时 Winner 应为空, 实际 %q", report.Winner)
	}
}

// ============================================================================
// 测试用例 — CompareVariants 显著性检验
// ============================================================================

// TestCompareVariants_Significant 验证显著差异时返回 winner。
func TestCompareVariants_Significant(t *testing.T) {
	// control: 1000 impressions, 100 clicks → CTR=0.1
	// treatment: 1000 impressions, 200 clicks → CTR=0.2
	// Z = (0.2 - 0.1) / sqrt(0.15 * 0.85 * (1/1000 + 1/1000)) ≈ 6.26 → 显著
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "control", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 100, CTR: 0.1}},
			{VariantID: "treatment", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 200, CTR: 0.2}},
		},
	}
	winner, conf := CompareVariants(report)
	if winner != "treatment" {
		t.Errorf("Winner = %q, 期望 treatment", winner)
	}
	if conf < 0.95 {
		t.Errorf("Confidence = %v, 期望 ≥ 0.95", conf)
	}
}

// TestCompareVariants_NotSignificant 验证无显著差异时返回空 winner。
func TestCompareVariants_NotSignificant(t *testing.T) {
	// control: 1000 impressions, 100 clicks → CTR=0.1
	// treatment: 1000 impressions, 105 clicks → CTR=0.105
	// 差异 0.005 → Z 很小 → 不显著
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "control", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 100, CTR: 0.1}},
			{VariantID: "treatment", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 105, CTR: 0.105}},
		},
	}
	winner, _ := CompareVariants(report)
	if winner != "" {
		t.Errorf("不显著时 Winner 应为空, 实际 %q", winner)
	}
}

// TestCompareVariants_InsufficientSample 验证样本不足返回空 winner。
func TestCompareVariants_InsufficientSample(t *testing.T) {
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "control", Sample: 0, Metrics: VariantMetrics{CTR: 0}},
			{VariantID: "treatment", Sample: 0, Metrics: VariantMetrics{CTR: 0}},
		},
	}
	winner, conf := CompareVariants(report)
	if winner != "" {
		t.Errorf("样本不足时 Winner 应为空, 实际 %q", winner)
	}
	if conf != 0 {
		t.Errorf("样本不足时 Confidence 应为 0, 实际 %v", conf)
	}
}

// TestCompareVariants_SingleVariant 验证单 variant 返回空。
func TestCompareVariants_SingleVariant(t *testing.T) {
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "v1", Sample: 100, Metrics: VariantMetrics{CTR: 0.1}},
		},
	}
	winner, conf := CompareVariants(report)
	if winner != "" {
		t.Errorf("单 variant 时 Winner 应为空, 实际 %q", winner)
	}
	if conf != 0 {
		t.Errorf("单 variant 时 Confidence 应为 0, 实际 %v", conf)
	}
}

// TestCompareVariants_MultiVariant 验证多于 2 个 variant 时取 CTR 最高的两个对比。
func TestCompareVariants_MultiVariant(t *testing.T) {
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "a", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 100, CTR: 0.1}},
			{VariantID: "b", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 200, CTR: 0.2}},
			{VariantID: "c", Sample: 1000, Metrics: VariantMetrics{Impressions: 1000, Clicks: 150, CTR: 0.15}},
		},
	}
	winner, conf := CompareVariants(report)
	if winner != "b" {
		t.Errorf("Winner = %q, 期望 b（CTR 最高且与第二差距显著）", winner)
	}
	if conf < 0.95 {
		t.Errorf("Confidence = %v, 期望 ≥ 0.95", conf)
	}
}

// TestCompareVariants_AllZeroCTR 验证全 0 CTR 时返回空。
func TestCompareVariants_AllZeroCTR(t *testing.T) {
	report := ABTestReport{
		Variants: []VariantResult{
			{VariantID: "a", Sample: 100, Metrics: VariantMetrics{Impressions: 100, Clicks: 0, CTR: 0}},
			{VariantID: "b", Sample: 100, Metrics: VariantMetrics{Impressions: 100, Clicks: 0, CTR: 0}},
		},
	}
	winner, _ := CompareVariants(report)
	if winner != "" {
		t.Errorf("全 0 CTR 时 Winner 应为空, 实际 %q", winner)
	}
}

// ============================================================================
// 测试用例 — Stop
// ============================================================================

// TestABTestService_Stop 验证停止实验。
func TestABTestService_Stop(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))
	if err := svc.Stop(ctx, "exp-1"); err != nil {
		t.Fatalf("Stop 错误: %v", err)
	}
	// 停止后 AssignVariant 应报错。
	if _, err := svc.AssignVariant(ctx, "exp-1", "u1"); err == nil {
		t.Errorf("停止后 AssignVariant 应报错")
	}
	// 停止后 RecordMetric 应报错。
	if err := svc.RecordMetric(ctx, "exp-1", "control", MetricEvent{Type: string(MetricEventImpression)}); err == nil {
		t.Errorf("停止后 RecordMetric 应报错")
	}
}

// TestABTestService_Stop_NotExist 验证停止不存在的实验报错。
func TestABTestService_Stop_NotExist(t *testing.T) {
	svc := NewABTestService()
	if err := svc.Stop(context.Background(), "nope"); err == nil {
		t.Errorf("停止不存在的实验应报错")
	}
}

// ============================================================================
// 测试用例 — 并发安全（-race 检测）
// ============================================================================

// TestABTestService_Concurrent 并发 AssignVariant + RecordMetric 验证无 race。
func TestABTestService_Concurrent(t *testing.T) {
	svc := NewABTestService()
	ctx := context.Background()
	_ = svc.CreateExperiment(ctx, makeExperiment("exp-1"))

	done := make(chan struct{})
	// 并发 AssignVariant。
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_, _ = svc.AssignVariant(ctx, "exp-1", "u"+itoa(i))
		}
	}()
	// 并发 RecordMetric。
	for i := 0; i < 100; i++ {
		_ = svc.RecordMetric(ctx, "exp-1", "control", MetricEvent{Type: string(MetricEventImpression)})
	}
	<-done
	// 不报错即可（race 检测在 -race 模式下生效）。
}
