// internal/eval/regression_test.go — 回归测试单测（Task 15.3）。
//
// 覆盖：
//   - RegressionRunner.Run 全流程（stub Recommender）
//   - 漂移检测（超阈值标记 drift / 未超不标记）
//   - 报告字段正确性（Cases/Passed/Failed/Metrics/Drift/Duration）
//   - 无用例时返回空报告
//   - Recommender 为 nil 时不 panic
//   - baseline 为 0 时的退化处理
//
// 使用 stdlib 手写 stub，不依赖 testify。
package eval

import (
	"context"
	"errors"
	"math"
	"testing"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubRecommender 测试用 Recommender，可配置返回结果与错误。
type stubRecommender struct {
	results map[string][]string // query → results
	err     error
	calls   int
}

func (s *stubRecommender) Recommend(_ context.Context, query string) ([]string, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.results[query], nil
}

// makeBaselineCases 构造若干 EvalCase 并塞入 store。
func makeBaselineCases(store *MemoryCaseStore) []EvalCase {
	ctx := context.Background()
	cases := []EvalCase{
		{ID: "c1", Query: "q1", Expected: []string{"a", "b"}, Surface: "home"},
		{ID: "c2", Query: "q2", Expected: []string{"c", "d"}, Surface: "home"},
	}
	for _, c := range cases {
		_ = store.Create(ctx, c)
	}
	return cases
}

// ============================================================================
// 测试用例 — Run 全流程
// ============================================================================

// TestRegressionRunner_Run_Basic 验证 Run 全流程：调用 Recommender + metrics 聚合。
func TestRegressionRunner_Run_Basic(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v1", Metrics: map[string]float64{}}

	rec := &stubRecommender{
		results: map[string][]string{
			"q1": {"a", "x"}, // 命中 1/2
			"q2": {"c", "d"}, // 命中 2/2
		},
	}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec).WithK(2)

	report, err := runner.Run(context.Background(), CaseFilter{})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if report.Cases != 2 {
		t.Errorf("Cases = %d, 期望 2", report.Cases)
	}
	if report.Passed != 2 {
		t.Errorf("Passed = %d, 期望 2（NDCG>0）", report.Passed)
	}
	if report.Failed != 0 {
		t.Errorf("Failed = %d, 期望 0", report.Failed)
	}
	if rec.calls != 2 {
		t.Errorf("Recommender 调用 %d 次, 期望 2 次", rec.calls)
	}
	// 验证 metrics 存在。
	if _, ok := report.Metrics["ndcg@2"]; !ok {
		t.Errorf("metrics 缺少 ndcg@2, 实际 %v", report.Metrics)
	}
	if _, ok := report.Metrics["recall@2"]; !ok {
		t.Errorf("metrics 缺少 recall@2, 实际 %v", report.Metrics)
	}
	if report.Duration <= 0 {
		t.Errorf("Duration 应 > 0, 实际 %d", report.Duration)
	}
}

// TestRegressionRunner_Run_NilRecommender 验证 Recommender 为 nil 时不 panic。
func TestRegressionRunner_Run_NilRecommender(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v1"}

	runner := NewRegressionRunner(store, calc, baseline)
	report, err := runner.Run(context.Background(), CaseFilter{})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if report.Cases != 2 {
		t.Errorf("Cases = %d, 期望 2", report.Cases)
	}
	if report.Passed != 0 {
		t.Errorf("nil Recommender 时 Passed 应为 0, 实际 %d", report.Passed)
	}
	// metrics 应全部为 0。
	for k, v := range report.Metrics {
		if v != 0 {
			t.Errorf("metric %s = %v, 期望 0", k, v)
		}
	}
}

// TestRegressionRunner_Run_NoCases 验证无用例时返回空报告。
func TestRegressionRunner_Run_NoCases(t *testing.T) {
	store := NewMemoryCaseStore()
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v1"}
	runner := NewRegressionRunner(store, calc, baseline)

	report, err := runner.Run(context.Background(), CaseFilter{})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if report.Cases != 0 {
		t.Errorf("Cases = %d, 期望 0", report.Cases)
	}
	if len(report.Metrics) != 0 {
		t.Errorf("无用例时 metrics 应为空, 实际 %v", report.Metrics)
	}
}

// TestRegressionRunner_Run_RecommenderError 验证 Recommender 错误时该 case 计为失败。
func TestRegressionRunner_Run_RecommenderError(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v1"}

	rec := &stubRecommender{err: errors.New("rpc error")}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec)

	report, err := runner.Run(context.Background(), CaseFilter{})
	if err != nil {
		t.Fatalf("Run 不应返回错误: %v", err)
	}
	if report.Passed != 0 {
		t.Errorf("Recommender 错误时 Passed 应为 0, 实际 %d", report.Passed)
	}
	if report.Failed != 2 {
		t.Errorf("Failed = %d, 期望 2", report.Failed)
	}
}

// ============================================================================
// 测试用例 — 漂移检测
// ============================================================================

// TestRegressionRunner_Run_DriftDetected 验证漂移检测：metrics 下降 > 阈值标记 drift。
func TestRegressionRunner_Run_DriftDetected(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	// 基线 ndcg@2=1.0，当前会降至 0.x → drift。
	baseline := RegressionBaseline{
		Version: "v1",
		Metrics: map[string]float64{
			"ndcg@2": 1.0, // 当前约 0.75（c1 命中 1/2 + c2 命中 2/2 的均值），下降 25% → drift
		},
	}

	rec := &stubRecommender{
		results: map[string][]string{
			"q1": {"a", "x"}, // NDCG@2 = 1/log2(2) / 1/log2(2) + 1/log2(3) = 1/(1+0.6309) ≈ 0.613
			"q2": {"c", "d"}, // NDCG@2 = 1.0
		},
	}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec).WithK(2).WithThreshold(0.1)

	report, _ := runner.Run(context.Background(), CaseFilter{})
	if len(report.Drift) == 0 {
		t.Fatalf("期望检测到漂移, Drift 为空")
	}
	found := false
	for _, item := range report.Drift {
		if item.Metric == "ndcg@2" && item.Detected {
			found = true
		}
	}
	if !found {
		t.Errorf("ndcg@2 下降 > 10%% 应标记 drift, 实际 %v", report.Drift)
	}
}

// TestRegressionRunner_Run_NoDrift 验证 metrics 在阈值内不标记 drift。
func TestRegressionRunner_Run_NoDrift(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	// 基线 ndcg@2=1.0，当前实际也是 1.0（c1 与 c2 都完美命中）→ 不 drift。
	baseline := RegressionBaseline{
		Version: "v1",
		Metrics: map[string]float64{
			"ndcg@2": 1.0,
		},
	}

	rec := &stubRecommender{
		results: map[string][]string{
			"q1": {"a", "b"},
			"q2": {"c", "d"},
		},
	}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec).WithK(2).WithThreshold(0.1)

	report, _ := runner.Run(context.Background(), CaseFilter{})
	for _, item := range report.Drift {
		if item.Detected {
			t.Errorf("完美命中时不应标记 drift, 实际 %v 检测到漂移", item.Metric)
		}
	}
}

// TestRegressionRunner_Run_ZeroBaseline 验证 baseline 为 0 时 current 非零标记 drift。
func TestRegressionRunner_Run_ZeroBaseline(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{
		Version: "v1",
		Metrics: map[string]float64{
			"ndcg@2": 0, // baseline=0, current 非零 → drift
		},
	}

	rec := &stubRecommender{
		results: map[string][]string{
			"q1": {"a", "b"},
			"q2": {"c", "d"},
		},
	}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec).WithK(2)

	report, _ := runner.Run(context.Background(), CaseFilter{})
	found := false
	for _, item := range report.Drift {
		if item.Metric == "ndcg@2" && item.Detected {
			found = true
		}
	}
	if !found {
		t.Errorf("baseline=0 + current 非零应标记 drift, 实际 %v", report.Drift)
	}
}

// TestRegressionRunner_Run_BaselineVersion 验证报告携带 baseline version。
func TestRegressionRunner_Run_BaselineVersion(t *testing.T) {
	store := NewMemoryCaseStore()
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v2.3.4"}
	runner := NewRegressionRunner(store, calc, baseline)

	report, _ := runner.Run(context.Background(), CaseFilter{})
	if report.BaselineVersion != "v2.3.4" {
		t.Errorf("BaselineVersion = %q, 期望 v2.3.4", report.BaselineVersion)
	}
}

// TestRegressionRunner_Run_Filter 验证 CaseFilter 生效。
func TestRegressionRunner_Run_Filter(t *testing.T) {
	store := NewMemoryCaseStore()
	ctx := context.Background()
	_ = store.Create(ctx, EvalCase{ID: "c1", Query: "q1", Expected: []string{"a"}, Surface: "home"})
	_ = store.Create(ctx, EvalCase{ID: "c2", Query: "q2", Expected: []string{"b"}, Surface: "feed"})

	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{Version: "v1"}
	rec := &stubRecommender{results: map[string][]string{"q1": {"a"}}}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec)

	report, _ := runner.Run(ctx, CaseFilter{Surface: "home"})
	if report.Cases != 1 {
		t.Errorf("Filter Surface=home 时 Cases = %d, 期望 1", report.Cases)
	}
}

// TestRegressionRunner_DriftPercent 验证 DriftPercent 计算正确。
func TestRegressionRunner_DriftPercent(t *testing.T) {
	store := NewMemoryCaseStore()
	makeBaselineCases(store)
	calc := NewMetricsCalculator()
	baseline := RegressionBaseline{
		Version: "v1",
		Metrics: map[string]float64{
			"ndcg@2": 1.0,
		},
	}
	rec := &stubRecommender{
		results: map[string][]string{
			"q1": {"a", "x"},
			"q2": {"c", "d"},
		},
	}
	runner := NewRegressionRunner(store, calc, baseline).WithRecommender(rec).WithK(2)

	report, _ := runner.Run(context.Background(), CaseFilter{})
	for _, item := range report.Drift {
		if item.Metric == "ndcg@2" {
			expected := (report.Metrics["ndcg@2"] - 1.0) / 1.0 * 100
			if math.Abs(item.DriftPercent-expected) > 1e-6 {
				t.Errorf("DriftPercent = %v, 期望 %v", item.DriftPercent, expected)
			}
		}
	}
}
