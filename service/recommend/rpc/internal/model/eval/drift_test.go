// internal/eval/drift_test.go — 行为漂移检测单测（Task 15.4）。
//
// 覆盖：
//   - UpdateBaseline 计算 mean/std/sample 正确性
//   - Detect 漂移检测：mean 偏移超阈值 / std=0 退化 / multi-metric
//   - LoadBaseline/SaveBaseline 持久化（含错误传播、未注入 store）
//   - GetBaseline 线程安全拷贝
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"errors"
	"math"
	"testing"
)

// floatEq 浮点近似比较（已在 metrics_test.go 定义，这里复用）。

// ============================================================================
// 测试用例 — UpdateBaseline
// ============================================================================

// TestDriftDetector_UpdateBaseline_MeanStd 验证 mean/std/sample 计算。
func TestDriftDetector_UpdateBaseline_MeanStd(t *testing.T) {
	det := NewDriftDetector(3.0)
	// values: [0.8, 0.9, 1.0, 1.1, 1.2]
	// mean = 1.0, std = sqrt(((0.04+0.01+0+0.01+0.04)/5)) = sqrt(0.02) ≈ 0.1414
	det.UpdateBaseline("ndcg@10", []float64{0.8, 0.9, 1.0, 1.1, 1.2})

	bl := det.GetBaseline()
	dist, ok := bl["ndcg@10"]
	if !ok {
		t.Fatalf("baseline 缺少 ndcg@10")
	}
	if !floatEq(dist.Mean, 1.0) {
		t.Errorf("Mean = %v, 期望 1.0", dist.Mean)
	}
	wantStd := math.Sqrt(0.02)
	if !floatEq(dist.Std, wantStd) {
		t.Errorf("Std = %v, 期望 %v", dist.Std, wantStd)
	}
	if dist.Sample != 5 {
		t.Errorf("Sample = %d, 期望 5", dist.Sample)
	}
	if dist.Updated == 0 {
		t.Errorf("Updated 应被填充")
	}
}

// TestDriftDetector_UpdateBaseline_EmptyValues 验证空 values 不更新基线。
func TestDriftDetector_UpdateBaseline_EmptyValues(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", nil)
	if len(det.GetBaseline()) != 0 {
		t.Errorf("空 values 不应更新基线")
	}
}

// TestDriftDetector_UpdateBaseline_SingleValue 验证单值时 std=0。
func TestDriftDetector_UpdateBaseline_SingleValue(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{0.5})
	bl := det.GetBaseline()
	dist := bl["ndcg@10"]
	if !floatEq(dist.Mean, 0.5) {
		t.Errorf("Mean = %v, 期望 0.5", dist.Mean)
	}
	if dist.Std != 0 {
		t.Errorf("Std = %v, 期望 0（单值）", dist.Std)
	}
	if dist.Sample != 1 {
		t.Errorf("Sample = %d, 期望 1", dist.Sample)
	}
}

// ============================================================================
// 测试用例 — Detect 漂移检测
// ============================================================================

// TestDriftDetector_Detect_MeanShift 验证 mean 偏移超 std 倍数阈值 → drift。
func TestDriftDetector_Detect_MeanShift(t *testing.T) {
	det := NewDriftDetector(3.0)
	// baseline: mean=1.0, std=0.1414
	det.UpdateBaseline("ndcg@10", []float64{0.8, 0.9, 1.0, 1.1, 1.2})
	// current=0.5 → diff=0.5 > 3*0.1414=0.4243 → drift
	report := det.Detect(map[string]float64{"ndcg@10": 0.5})
	if !report.Detected {
		t.Errorf("mean 偏移 0.5 超过 3σ 应检测到漂移")
	}
	if len(report.Items) != 1 {
		t.Fatalf("Items 长度 = %d, 期望 1", len(report.Items))
	}
	if !report.Items[0].Detected {
		t.Errorf("ndcg@10 应标记 Detected")
	}
	if report.DetectedAt == 0 {
		t.Errorf("DetectedAt 应被填充")
	}
}

// TestDriftDetector_Detect_NoDrift 验证 mean 偏移在阈值内 → 不 drift。
func TestDriftDetector_Detect_NoDrift(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{0.8, 0.9, 1.0, 1.1, 1.2})
	// current=1.05 → diff=0.05 < 3*0.1414=0.4243 且 0.05/1.0=5% < 10% → 不 drift
	report := det.Detect(map[string]float64{"ndcg@10": 1.05})
	if report.Detected {
		t.Errorf("偏移 5%% 在阈值内不应检测到漂移")
	}
}

// TestDriftDetector_Detect_StdZero_Degenerate 验证 std=0 退化绝对阈值。
func TestDriftDetector_Detect_StdZero_Degenerate(t *testing.T) {
	det := NewDriftDetector(3.0)
	// 单值基线 → std=0
	det.UpdateBaseline("ndcg@10", []float64{0.5})
	// current=0.65 → diff=0.15 > 0.1（绝对阈值） → drift
	report := det.Detect(map[string]float64{"ndcg@10": 0.65})
	if !report.Detected {
		t.Errorf("std=0 时 diff=0.15 > 0.1 应检测到漂移")
	}
}

// TestDriftDetector_Detect_StdZero_NoDrift 验证 std=0 时小偏移不 drift。
func TestDriftDetector_Detect_StdZero_NoDrift(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{0.5})
	// current=0.54 → diff=0.04 < 0.1（绝对阈值） 且 0.04/0.5=8% < 10%（相对阈值） → 不 drift
	report := det.Detect(map[string]float64{"ndcg@10": 0.54})
	if report.Detected {
		t.Errorf("std=0 + diff=0.04 < 0.1 不应检测到漂移")
	}
}

// TestDriftDetector_Detect_RelativeChange 验证 10% 相对变化规则。
func TestDriftDetector_Detect_RelativeChange(t *testing.T) {
	det := NewDriftDetector(3.0)
	// baseline: mean=0.9, std=0.05（小 std）
	det.UpdateBaseline("ndcg@10", []float64{0.85, 0.9, 0.95})
	// current=0.75 → diff=0.15 > 3*0.05=0.15（不大于，规则1 不触发）
	//              但 0.15/0.9 = 16.7% > 10%（规则2 触发） → drift
	report := det.Detect(map[string]float64{"ndcg@10": 0.75})
	if !report.Detected {
		t.Errorf("相对变化 16.7%% > 10%% 应检测到漂移")
	}
}

// TestDriftDetector_Detect_MultiMetric 验证多 metric 同时检测。
func TestDriftDetector_Detect_MultiMetric(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{1.0, 1.0, 1.0}) // std=0
	det.UpdateBaseline("latency", []float64{100, 100, 100}) // std=0
	det.UpdateBaseline("ctr", []float64{0.1, 0.1, 0.1})     // std=0

	// std=0 时退化为绝对阈值 0.1：|current - mean| > 0.1 → drift
	// 同时 10% 相对变化也会触发 drift。
	current := map[string]float64{
		"ndcg@10": 0.5,    // diff=0.5 > 0.1 → drift
		"latency": 100.05, // diff=0.05 < 0.1 且 0.05/100=0.05% < 10% → 不 drift
		"ctr":     0.1001, // diff=0.0001 < 0.1 且 0.0001/0.1=0.1% < 10% → 不 drift
	}
	report := det.Detect(current)
	if !report.Detected {
		t.Errorf("应检测到漂移")
	}
	drifted := 0
	for _, item := range report.Items {
		if item.Detected {
			drifted++
		}
	}
	if drifted != 1 {
		t.Errorf("漂移指标数 = %d, 期望 1（仅 ndcg@10）", drifted)
	}
}

// TestDriftDetector_Detect_NoBaseline 验证无基线时返回空报告。
func TestDriftDetector_Detect_NoBaseline(t *testing.T) {
	det := NewDriftDetector(3.0)
	report := det.Detect(map[string]float64{"ndcg@10": 0.5})
	if report.Detected {
		t.Errorf("无基线时不应检测到漂移")
	}
	if len(report.Items) != 0 {
		t.Errorf("无基线时 Items 应为空, 实际 %v", report.Items)
	}
}

// TestDriftDetector_Detect_MissingCurrent 验证 current 缺失该指标时跳过。
func TestDriftDetector_Detect_MissingCurrent(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{1.0, 1.0, 1.0})
	report := det.Detect(map[string]float64{"other": 0.5})
	if report.Detected {
		t.Errorf("current 缺失该指标时不应检测漂移")
	}
	if len(report.Items) != 0 {
		t.Errorf("current 缺失时 Items 应为空, 实际 %v", report.Items)
	}
}

// ============================================================================
// 测试用例 — 持久化
// ============================================================================

// TestDriftDetector_SaveAndLoad 验证 Save + Load 持久化全流程。
func TestDriftDetector_SaveAndLoad(t *testing.T) {
	store := NewMemoryBaselineStore()
	det := NewDriftDetector(3.0).WithStore(store)

	det.UpdateBaseline("ndcg@10", []float64{0.8, 0.9, 1.0})
	if err := det.SaveBaseline(context.Background()); err != nil {
		t.Fatalf("SaveBaseline 错误: %v", err)
	}
	if store.SaveCount() != 1 {
		t.Errorf("Save 调用 %d 次, 期望 1 次", store.SaveCount())
	}

	// 新建 detector，加载基线。
	det2 := NewDriftDetector(3.0).WithStore(store)
	if err := det2.LoadBaseline(context.Background()); err != nil {
		t.Fatalf("LoadBaseline 错误: %v", err)
	}
	if store.LoadCount() != 1 {
		t.Errorf("Load 调用 %d 次, 期望 1 次", store.LoadCount())
	}

	bl := det2.GetBaseline()
	dist, ok := bl["ndcg@10"]
	if !ok {
		t.Fatalf("加载后基线缺少 ndcg@10")
	}
	if !floatEq(dist.Mean, 0.9) {
		t.Errorf("加载后 Mean = %v, 期望 0.9", dist.Mean)
	}
}

// TestDriftDetector_Load_NilStore 验证未注入 store 时 LoadBaseline 不报错。
func TestDriftDetector_Load_NilStore(t *testing.T) {
	det := NewDriftDetector(3.0)
	if err := det.LoadBaseline(context.Background()); err != nil {
		t.Errorf("未注入 store 时 LoadBaseline 应返回 nil, 实际 %v", err)
	}
}

// TestDriftDetector_Save_NilStore 验证未注入 store 时 SaveBaseline 不报错。
func TestDriftDetector_Save_NilStore(t *testing.T) {
	det := NewDriftDetector(3.0)
	if err := det.SaveBaseline(context.Background()); err != nil {
		t.Errorf("未注入 store 时 SaveBaseline 应返回 nil, 实际 %v", err)
	}
}

// TestDriftDetector_Load_Error 验证 Load 错误传播。
func TestDriftDetector_Load_Error(t *testing.T) {
	store := NewMemoryBaselineStore().WithLoadErr(errors.New("load failed"))
	det := NewDriftDetector(3.0).WithStore(store)
	if err := det.LoadBaseline(context.Background()); err == nil {
		t.Errorf("Load 错误应传播")
	}
}

// TestDriftDetector_Save_Error 验证 Save 错误传播。
func TestDriftDetector_Save_Error(t *testing.T) {
	store := NewMemoryBaselineStore().WithSaveErr(errors.New("save failed"))
	det := NewDriftDetector(3.0).WithStore(store)
	if err := det.SaveBaseline(context.Background()); err == nil {
		t.Errorf("Save 错误应传播")
	}
}

// TestDriftDetector_GetBaseline_Copy 验证 GetBaseline 返回拷贝（修改不影响内部）。
func TestDriftDetector_GetBaseline_Copy(t *testing.T) {
	det := NewDriftDetector(3.0)
	det.UpdateBaseline("ndcg@10", []float64{1.0, 1.0, 1.0})
	bl := det.GetBaseline()
	bl["ndcg@10"] = Distribution{Mean: 999}
	bl["new"] = Distribution{Mean: 0}

	bl2 := det.GetBaseline()
	if bl2["ndcg@10"].Mean == 999 {
		t.Errorf("GetBaseline 应返回拷贝, 修改不应影响内部")
	}
	if _, ok := bl2["new"]; ok {
		t.Errorf("GetBaseline 拷贝修改不应向内部添加新 key")
	}
}

// TestDriftDetector_DefaultThreshold 验证 threshold<=0 时使用默认值。
func TestDriftDetector_DefaultThreshold(t *testing.T) {
	det := NewDriftDetector(0)
	if det.threshold != defaultDriftStdThreshold {
		t.Errorf("threshold = %v, 期望默认值 %v", det.threshold, defaultDriftStdThreshold)
	}
}
