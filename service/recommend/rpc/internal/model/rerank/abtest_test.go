package rerank

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 A/B 测试与外部 rerank 对比（Task 8.7），覆盖：
//   - AssignBucket：FNV-1a 分桶稳定性、分布、默认比例、SetBucketRatio
//   - Rerank：self/external 桶路径、nil self/external 报错
//   - ExternalRerank：直接调用外部 rerank、nil external 报错
//   - RecordOutcome：CTR/延迟/成本累计
//   - ComputeWinRate：self 胜/external 胜/混合场景
//   - GetMetrics：深拷贝隔离
//   - SetMetricsRecorder：监控指标上报
//
// mock 复用说明：
//   - mockExternalReranker（定义于 server_test.go）满足 ExternalReranker interface，
//     本文件直接复用，不重复声明。
//   - mockSelfReranker（本文件定义）满足 domain.Reranker interface，
//     供 reranker_impl_test.go 与 tools_test.go 复用。
// ============================================================================

// ---- mock 实现 ----

// mockSelfReranker 模拟 domain.Reranker（自研 reranker）。
//
// 满足 domain.Reranker interface（Rerank + Name）。
// 默认行为：返回原候选列表，ModelUsed = "self"。
// 可通过 result 字段覆盖默认返回值，通过 err 字段模拟失败。
type mockSelfReranker struct {
	mu      sync.Mutex
	err     error
	called  int
	lastReq domain.RerankRequest
	result  domain.RerankResult
}

func (m *mockSelfReranker) Rerank(_ context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.lastReq = req
	if m.err != nil {
		return domain.RerankResult{}, m.err
	}
	// 若设置了 result.Candidates，返回它；否则返回原候选。
	if m.result.Candidates != nil {
		return m.result, nil
	}
	return domain.RerankResult{Candidates: req.Candidates, ModelUsed: bucketSelf}, nil
}

func (m *mockSelfReranker) Name() string { return "mock_self" }

// mockMetricsRecorder 模拟 MetricsRecorder，记录 Counter/Gauge 调用。
//
// 注：实现中 RecordCounter 传入的是累计快照值（ab.metrics.SelfCost），
// 而非增量 delta，因此本 mock 存储最新值而非累加。
type mockMetricsRecorder struct {
	mu       sync.Mutex
	counters map[string]float64
	gauges   map[string]float64
}

func newMockMetricsRecorder() *mockMetricsRecorder {
	return &mockMetricsRecorder{
		counters: make(map[string]float64),
		gauges:   make(map[string]float64),
	}
}

func (m *mockMetricsRecorder) RecordCounter(name string, value float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name] = value // 存储最新累计快照
}

func (m *mockMetricsRecorder) RecordGauge(name string, value float64, _ map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[name] = value
}

// ---- 辅助函数 ----

// findUserForBucket 找到分配到指定桶的 userID（确定性，遍历最多 10000 个 userID）。
func findUserForBucket(ab *ABTest, wantBucket string) string {
	for i := 0; i < 10000; i++ {
		uid := fmt.Sprintf("user-%d", i)
		if ab.AssignBucket(uid) == wantBucket {
			return uid
		}
	}
	return ""
}

// ---- 测试用例 ----

// TestABTest_AssignBucketStable 验证同一 userID 分桶稳定（FNV-1a 决定性）。
func TestABTest_AssignBucketStable(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	uid := "stable-user-001"
	first := ab.AssignBucket(uid)
	for i := 0; i < 10; i++ {
		if got := ab.AssignBucket(uid); got != first {
			t.Fatalf("分桶不稳定: 首次 = %q, 第 %d 次 = %q", first, i+1, got)
		}
	}
}

// TestABTest_AssignBucketDistribution 验证 ratio=0.5 时约 50/50 分布。
func TestABTest_AssignBucketDistribution(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	total := 1000
	selfCnt := 0
	for i := 0; i < total; i++ {
		uid := fmt.Sprintf("dist-user-%d", i)
		if ab.AssignBucket(uid) == bucketSelf {
			selfCnt++
		}
	}
	// 期望约 50%，容差 ±10%。
	ratio := float64(selfCnt) / float64(total)
	if ratio < 0.4 || ratio > 0.6 {
		t.Errorf("ratio=0.5 分布异常: self 占比 = %.2f, 期望 [0.4, 0.6]", ratio)
	}
}

// TestABTest_AssignBucketInvalidRatio 验证无效比例默认 0.5。
func TestABTest_AssignBucketInvalidRatio(t *testing.T) {
	invalidRatios := []float64{0, 1, -0.5, 1.5, 2}
	for _, r := range invalidRatios {
		ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, r)
		selfCnt := 0
		for i := 0; i < 1000; i++ {
			uid := fmt.Sprintf("invalid-ratio-%f-%d", r, i)
			if ab.AssignBucket(uid) == bucketSelf {
				selfCnt++
			}
		}
		ratio := float64(selfCnt) / 1000
		if ratio < 0.4 || ratio > 0.6 {
			t.Errorf("无效比例 %v 应默认 0.5, 实际 self 占比 = %.2f", r, ratio)
		}
	}
}

// TestABTest_SetBucketRatio 验证 SetBucketRatio 调整分桶比例。
func TestABTest_SetBucketRatio(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	// 调整为 0.1（self 占 10%）。
	ab.SetBucketRatio(0.1)
	selfCnt := 0
	for i := 0; i < 1000; i++ {
		uid := fmt.Sprintf("set-ratio-%d", i)
		if ab.AssignBucket(uid) == bucketSelf {
			selfCnt++
		}
	}
	ratio := float64(selfCnt) / 1000
	if ratio > 0.2 {
		t.Errorf("ratio=0.1 时 self 占比 = %.2f, 期望 <= 0.2", ratio)
	}
}

// TestABTest_SetBucketRatioInvalid 验证无效比例被忽略（不修改原比例）。
func TestABTest_SetBucketRatioInvalid(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	ab.SetBucketRatio(0.3)
	// 设置无效值应被忽略。
	ab.SetBucketRatio(0)
	ab.SetBucketRatio(1)
	ab.SetBucketRatio(-1)
	// 验证比例仍为 0.3（约 30% self）。
	selfCnt := 0
	for i := 0; i < 1000; i++ {
		uid := fmt.Sprintf("set-invalid-%d", i)
		if ab.AssignBucket(uid) == bucketSelf {
			selfCnt++
		}
	}
	ratio := float64(selfCnt) / 1000
	if ratio < 0.2 || ratio > 0.4 {
		t.Errorf("无效 SetBucketRatio 后比例应保持 0.3, 实际 = %.2f", ratio)
	}
}

// TestABTest_RerankSelf 验证 self 桶调用 selfReranker。
func TestABTest_RerankSelf(t *testing.T) {
	self := &mockSelfReranker{}
	external := &mockExternalReranker{}
	ab := NewABTest(self, external, 0.5)

	uid := findUserForBucket(ab, bucketSelf)
	if uid == "" {
		t.Fatal("未找到 self 桶 userID")
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: uid},
		Candidates: []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}},
		TopK:       2,
	}

	result, err := ab.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank self 失败: %v", err)
	}
	if self.called != 1 {
		t.Errorf("selfReranker 调用次数 = %d, 期望 1", self.called)
	}
	if external.called != 0 {
		t.Errorf("externalReranker 不应被调用, called = %d", external.called)
	}
	if result.ModelUsed != bucketSelf {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, bucketSelf)
	}
}

// TestABTest_RerankExternal 验证 external 桶调用 externalReranker。
func TestABTest_RerankExternal(t *testing.T) {
	self := &mockSelfReranker{}
	external := &mockExternalReranker{}
	ab := NewABTest(self, external, 0.5)

	uid := findUserForBucket(ab, bucketExternal)
	if uid == "" {
		t.Fatal("未找到 external 桶 userID")
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: uid},
		Candidates: []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}},
		Query:      "测试",
		TopK:       2,
	}

	result, err := ab.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank external 失败: %v", err)
	}
	if external.called != 1 {
		t.Errorf("externalReranker 调用次数 = %d, 期望 1", external.called)
	}
	if self.called != 0 {
		t.Errorf("selfReranker 不应被调用, called = %d", self.called)
	}
	if result.ModelUsed != bucketExternal {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, bucketExternal)
	}
}

// TestABTest_RerankNilSelf 验证 self 桶但 self reranker 未配置时报错。
func TestABTest_RerankNilSelf(t *testing.T) {
	external := &mockExternalReranker{}
	ab := NewABTest(nil, external, 0.5)

	uid := findUserForBucket(ab, bucketSelf)
	if uid == "" {
		t.Fatal("未找到 self 桶 userID")
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: uid},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		TopK:       1,
	}

	_, err := ab.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("self reranker 未配置时应返回错误")
	}
}

// TestABTest_RerankNilExternal 验证 external 桶但 external reranker 未配置时报错。
func TestABTest_RerankNilExternal(t *testing.T) {
	self := &mockSelfReranker{}
	ab := NewABTest(self, nil, 0.5)

	uid := findUserForBucket(ab, bucketExternal)
	if uid == "" {
		t.Fatal("未找到 external 桶 userID")
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: uid},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		TopK:       1,
	}

	_, err := ab.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("external reranker 未配置时应返回错误")
	}
}

// TestABTest_ExternalRerank 验证直接调用外部 rerank（不做分桶）。
func TestABTest_ExternalRerank(t *testing.T) {
	external := &mockExternalReranker{}
	ab := NewABTest(&mockSelfReranker{}, external, 0.5)

	cands := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	result, err := ab.ExternalRerank(context.Background(), "测试", cands, 2)
	if err != nil {
		t.Fatalf("ExternalRerank 失败: %v", err)
	}
	if external.called != 1 {
		t.Errorf("externalReranker 调用次数 = %d, 期望 1", external.called)
	}
	if len(result) != 2 {
		t.Errorf("返回候选数 = %d, 期望 2", len(result))
	}
}

// TestABTest_ExternalRerankNil 验证 external 未配置时 ExternalRerank 报错。
func TestABTest_ExternalRerankNil(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, nil, 0.5)
	_, err := ab.ExternalRerank(context.Background(), "测试", []domain.Candidate{{ArticleID: "a1"}}, 1)
	if err == nil {
		t.Fatal("external 未配置时 ExternalRerank 应返回错误")
	}
}

// TestABTest_RecordOutcomeCTR 验证 CTR 与延迟均值计算。
func TestABTest_RecordOutcomeCTR(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)

	// self 桶：文章 a1，4 次曝光，2 次点击 → CTR = 0.5。
	ab.RecordOutcome(bucketSelf, "a1", true, 100*time.Millisecond, 0.01)
	ab.RecordOutcome(bucketSelf, "a1", false, 200*time.Millisecond, 0.01)
	ab.RecordOutcome(bucketSelf, "a1", true, 100*time.Millisecond, 0.01)
	ab.RecordOutcome(bucketSelf, "a1", false, 200*time.Millisecond, 0.01)

	// external 桶：文章 a1，4 次曝光，1 次点击 → CTR = 0.25。
	ab.RecordOutcome(bucketExternal, "a1", true, 50*time.Millisecond, 0.02)
	ab.RecordOutcome(bucketExternal, "a1", false, 50*time.Millisecond, 0.02)
	ab.RecordOutcome(bucketExternal, "a1", false, 50*time.Millisecond, 0.02)
	ab.RecordOutcome(bucketExternal, "a1", false, 50*time.Millisecond, 0.02)

	m := ab.GetMetrics()

	// CTR 验证。
	if got := m.SelfCTR["a1"]; got != 0.5 {
		t.Errorf("SelfCTR[a1] = %.2f, 期望 0.50", got)
	}
	if got := m.ExternalCTR["a1"]; got != 0.25 {
		t.Errorf("ExternalCTR[a1] = %.2f, 期望 0.25", got)
	}

	// 延迟均值验证：self (100+200+100+200)/4 = 150ms = 0.15s。
	if got := m.SelfLatency["a1"]; got != 0.15 {
		t.Errorf("SelfLatency[a1] = %.4f, 期望 0.1500", got)
	}
	// external (50*4)/4 = 50ms = 0.05s。
	if got := m.ExternalLatency["a1"]; got != 0.05 {
		t.Errorf("ExternalLatency[a1] = %.4f, 期望 0.0500", got)
	}

	// 成本验证：self 0.01*4=0.04, external 0.02*4=0.08。
	if got := m.SelfCost; got != 0.04 {
		t.Errorf("SelfCost = %.2f, 期望 0.04", got)
	}
	if got := m.ExternalCost; got != 0.08 {
		t.Errorf("ExternalCost = %.2f, 期望 0.08", got)
	}
}

// TestABTest_RecordOutcomeWinRateSelfWins 验证 self 胜率为 1.0（self CTR 全胜）。
func TestABTest_RecordOutcomeWinRateSelfWins(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)

	// 文章 a1：self CTR 1.0, external CTR 0.0 → self 胜。
	for i := 0; i < 5; i++ {
		ab.RecordOutcome(bucketSelf, "a1", true, 10*time.Millisecond, 0)
		ab.RecordOutcome(bucketExternal, "a1", false, 10*time.Millisecond, 0)
	}

	if got := ab.ComputeWinRate(); got != 1.0 {
		t.Errorf("ComputeWinRate = %.2f, 期望 1.0（self 全胜）", got)
	}
}

// TestABTest_RecordOutcomeWinRateExternalWins 验证 self 胜率为 0.0（external 全胜）。
func TestABTest_RecordOutcomeWinRateExternalWins(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)

	// 文章 a1：self CTR 0.0, external CTR 1.0 → external 胜。
	for i := 0; i < 5; i++ {
		ab.RecordOutcome(bucketSelf, "a1", false, 10*time.Millisecond, 0)
		ab.RecordOutcome(bucketExternal, "a1", true, 10*time.Millisecond, 0)
	}

	if got := ab.ComputeWinRate(); got != 0.0 {
		t.Errorf("ComputeWinRate = %.2f, 期望 0.0（external 全胜）", got)
	}
}

// TestABTest_RecordOutcomeWinRateMixed 验证混合场景胜率。
func TestABTest_RecordOutcomeWinRateMixed(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)

	// 文章 a1：self CTR 1.0, external CTR 0.0 → self 胜。
	for i := 0; i < 5; i++ {
		ab.RecordOutcome(bucketSelf, "a1", true, 10*time.Millisecond, 0)
		ab.RecordOutcome(bucketExternal, "a1", false, 10*time.Millisecond, 0)
	}
	// 文章 a2：self CTR 0.0, external CTR 1.0 → external 胜。
	for i := 0; i < 5; i++ {
		ab.RecordOutcome(bucketSelf, "a2", false, 10*time.Millisecond, 0)
		ab.RecordOutcome(bucketExternal, "a2", true, 10*time.Millisecond, 0)
	}

	if got := ab.ComputeWinRate(); got != 0.5 {
		t.Errorf("ComputeWinRate = %.2f, 期望 0.5（1 胜 / 2 总）", got)
	}
}

// TestABTest_ComputeWinRateNoData 验证无对比数据时胜率为 0。
func TestABTest_ComputeWinRateNoData(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	if got := ab.ComputeWinRate(); got != 0 {
		t.Errorf("无数据时 ComputeWinRate = %.2f, 期望 0", got)
	}
}

// TestABTest_ComputeWinRateSingleSide 验证仅有 self 数据时胜率为 0。
func TestABTest_ComputeWinRateSingleSide(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	// 仅 self 桶有数据，external 无 → 无对比 → 胜率 0。
	ab.RecordOutcome(bucketSelf, "a1", true, 10*time.Millisecond, 0)
	if got := ab.ComputeWinRate(); got != 0 {
		t.Errorf("仅 self 数据时 ComputeWinRate = %.2f, 期望 0", got)
	}
}

// TestABTest_GetMetricsDeepCopy 验证 GetMetrics 返回深拷贝（修改不影响内部状态）。
func TestABTest_GetMetricsDeepCopy(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	ab.RecordOutcome(bucketSelf, "a1", true, 100*time.Millisecond, 0.01)

	m1 := ab.GetMetrics()
	// 修改返回的快照。
	m1.SelfCTR["a1"] = 999.0
	m1.SelfCost = 999.0

	// 再次获取，验证内部状态未被修改。
	m2 := ab.GetMetrics()
	if got := m2.SelfCTR["a1"]; got != 1.0 {
		t.Errorf("深拷贝失效: SelfCTR[a1] = %.2f, 期望 1.0", got)
	}
	if got := m2.SelfCost; got != 0.01 {
		t.Errorf("深拷贝失效: SelfCost = %.2f, 期望 0.01", got)
	}
}

// TestABTest_SetMetricsRecorder 验证监控指标上报。
func TestABTest_SetMetricsRecorder(t *testing.T) {
	rec := newMockMetricsRecorder()
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	ab.SetMetricsRecorder(rec)

	// self 桶记录：cost 0.01。
	ab.RecordOutcome(bucketSelf, "a1", true, 100*time.Millisecond, 0.01)
	// external 桶记录：cost 0.02。
	ab.RecordOutcome(bucketExternal, "a1", true, 50*time.Millisecond, 0.02)

	// 验证 Counter 上报。
	if got := rec.counters[metricSelfCostTotal]; got != 0.01 {
		t.Errorf("metricSelfCostTotal = %.4f, 期望 0.01", got)
	}
	if got := rec.counters[metricExternalCostTotal]; got != 0.02 {
		t.Errorf("metricExternalCostTotal = %.4f, 期望 0.02", got)
	}
	// 验证 Gauge 上报（self 胜率：self CTR 1.0 > external CTR 1.0 → 0.0，未胜）。
	if _, ok := rec.gauges[metricSelfWinRate]; !ok {
		t.Error("metricSelfWinRate 未上报")
	}
}

// TestABTest_RecordOutcomeInvalidBucket 验证无效桶名被忽略（不 panic、不影响指标）。
func TestABTest_RecordOutcomeInvalidBucket(t *testing.T) {
	ab := NewABTest(&mockSelfReranker{}, &mockExternalReranker{}, 0.5)
	// 无效桶名不应 panic。
	ab.RecordOutcome("invalid_bucket", "a1", true, 10*time.Millisecond, 0.01)

	m := ab.GetMetrics()
	if m.SelfCost != 0 || m.ExternalCost != 0 {
		t.Errorf("无效桶名不应影响成本指标: SelfCost=%.2f, ExternalCost=%.2f", m.SelfCost, m.ExternalCost)
	}
}
