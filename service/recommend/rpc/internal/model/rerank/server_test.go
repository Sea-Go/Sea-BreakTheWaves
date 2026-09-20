package rerank

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试在线 inference 服务 Server，覆盖：
//   - Rerank self 路径（CrossEncoder + TwoTower + LambdaMART 协同）
//   - Rerank external 路径
//   - Rerank self 失败 → 单次降级到 external
//   - 降级触发：失败率超阈值 → circuit breaker → 自动走 external
//   - 模型热加载（HotReloadModel）
//   - 健康检查（HealthCheck）
// ============================================================================

// ---- mock 实现 ----

// mockCrossEncoder 模拟 Cross-encoder 模型。
type mockCrossEncoder struct {
	mu        sync.Mutex
	scores    []float64
	err       error
	called    int
	lastQuery string
}

func (m *mockCrossEncoder) Score(_ context.Context, query string, cands []domain.Candidate) ([]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.lastQuery = query
	if m.err != nil {
		return nil, m.err
	}
	if m.scores != nil {
		return m.scores, nil
	}
	// 默认返回按 index 递增的分数。
	out := make([]float64, len(cands))
	for i := range out {
		out[i] = float64(len(cands) - i) // 降序，第一个最高
	}
	return out, nil
}

// mockTwoTower 模拟双塔模型。
type mockTwoTower struct {
	mu         sync.Mutex
	scores     []float64
	err        error
	called     int
	lastUserID string
}

func (m *mockTwoTower) Score(_ context.Context, userID string, cands []domain.Candidate) ([]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.lastUserID = userID
	if m.err != nil {
		return nil, m.err
	}
	if m.scores != nil {
		return m.scores, nil
	}
	out := make([]float64, len(cands))
	for i := range out {
		out[i] = 0.5
	}
	return out, nil
}

// mockLambdaMART 模拟 LambdaMART 排序模型。
type mockLambdaMART struct {
	mu     sync.Mutex
	err    error
	called int
}

func (m *mockLambdaMART) Rank(_ context.Context, cands []domain.Candidate) ([]domain.Candidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	if m.err != nil {
		return nil, m.err
	}
	// 按 Score 降序排序。
	out := make([]domain.Candidate, len(cands))
	copy(out, cands)
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].Score < out[j].Score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// mockExternalReranker 模拟外部 rerank 服务。
type mockExternalReranker struct {
	mu       sync.Mutex
	err      error
	called   int
	lastTopK int
}

func (m *mockExternalReranker) Rerank(_ context.Context, query string, cands []domain.Candidate, topK int) ([]domain.Candidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	m.lastTopK = topK
	if m.err != nil {
		return nil, m.err
	}
	out := make([]domain.Candidate, 0, topK)
	for i := 0; i < topK && i < len(cands); i++ {
		c := cands[i]
		c.Score = 1.0 - float64(i)*0.1
		out = append(out, c)
	}
	return out, nil
}

func (m *mockExternalReranker) Name() string { return "mock_external" }

// ---- 测试用例 ----

// TestServer_Name 验证重排器名称。
func TestServer_Name(t *testing.T) {
	s := NewServer(nil, nil, nil, nil)
	if got := s.Name(); got != "rerank_server" {
		t.Errorf("Name() = %q, 期望 rerank_server", got)
	}
}

// TestServer_RerankSelf 验证 self 路径：CrossEncoder + TwoTower + LambdaMART 协同。
func TestServer_RerankSelf(t *testing.T) {
	ce := &mockCrossEncoder{}
	tt := &mockTwoTower{}
	ltr := &mockLambdaMART{}
	external := &mockExternalReranker{}
	s := NewServer(ce, tt, ltr, external)

	cands := []domain.Candidate{
		{ArticleID: "a1", Score: 0.5},
		{ArticleID: "a2", Score: 0.3},
		{ArticleID: "a3", Score: 0.8},
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: cands,
		Query:      "测试查询",
		Model:      "self",
		TopK:       2,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank self 失败: %v", err)
	}

	if result.ModelUsed != "self" {
		t.Errorf("ModelUsed = %q, 期望 self", result.ModelUsed)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("返回候选数 = %d, 期望 2 (TopK)", len(result.Candidates))
	}
	if ce.called != 1 {
		t.Errorf("CrossEncoder 调用次数 = %d, 期望 1", ce.called)
	}
	if tt.called != 1 {
		t.Errorf("TwoTower 调用次数 = %d, 期望 1", tt.called)
	}
	if ltr.called != 1 {
		t.Errorf("LambdaMART 调用次数 = %d, 期望 1", ltr.called)
	}
	if external.called != 0 {
		t.Errorf("external 不应被调用, called = %d", external.called)
	}
}

// TestServer_RerankSelfEmptyCandidates 验证空候选列表。
func TestServer_RerankSelfEmptyCandidates(t *testing.T) {
	s := NewServer(&mockCrossEncoder{}, nil, nil, nil)

	req := domain.RerankRequest{
		Model:      "self",
		Candidates: nil,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("空候选列表 Rerank 失败: %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Errorf("空候选列表应返回空结果, got %d", len(result.Candidates))
	}
}

// TestServer_RerankExternal 验证 external 路径。
func TestServer_RerankExternal(t *testing.T) {
	ce := &mockCrossEncoder{}
	external := &mockExternalReranker{}
	s := NewServer(ce, nil, nil, external)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}

	req := domain.RerankRequest{
		Candidates: cands,
		Query:      "测试",
		Model:      "external",
		TopK:       2,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank external 失败: %v", err)
	}

	if result.ModelUsed != "external" {
		t.Errorf("ModelUsed = %q, 期望 external", result.ModelUsed)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("返回候选数 = %d, 期望 2", len(result.Candidates))
	}
	if ce.called != 0 {
		t.Errorf("self 模型不应被调用, ce.called = %d", ce.called)
	}
	if external.called != 1 {
		t.Errorf("external 调用次数 = %d, 期望 1", external.called)
	}
}

// TestServer_RerankSelfFallbackToExternal 验证 self 失败时单次降级到 external。
func TestServer_RerankSelfFallbackToExternal(t *testing.T) {
	ce := &mockCrossEncoder{err: errors.New("model crashed")}
	external := &mockExternalReranker{}
	s := NewServer(ce, nil, nil, external)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: cands,
		Query:      "测试",
		Model:      "self",
		TopK:       2,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("self 失败应降级到 external, 不应返回错误, got: %v", err)
	}
	if result.ModelUsed != "external" {
		t.Errorf("降级后 ModelUsed = %q, 期望 external", result.ModelUsed)
	}
	if external.called != 1 {
		t.Errorf("external 调用次数 = %d, 期望 1", external.called)
	}
}

// TestServer_RerankSelfFailExternalAlsoFails 验证 self 和 external 均失败时返回错误。
func TestServer_RerankSelfFailExternalAlsoFails(t *testing.T) {
	ce := &mockCrossEncoder{err: errors.New("self error")}
	external := &mockExternalReranker{err: errors.New("external error")}
	s := NewServer(ce, nil, nil, external)

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "测试",
		Model:      "self",
		TopK:       1,
	}

	_, err := s.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("self 和 external 均失败时应返回错误")
	}
}

// TestServer_DegradeOnHighFailureRate 验证失败率超阈值时触发降级。
func TestServer_DegradeOnHighFailureRate(t *testing.T) {
	ce := &mockCrossEncoder{err: errors.New("model unavailable")}
	external := &mockExternalReranker{}
	s := NewServer(ce, nil, nil, external)
	// 设置低阈值便于触发降级。
	s.SetDegradeThreshold(0.3)

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "测试",
		Model:      "self",
		TopK:       1,
	}

	ctx := context.Background()
	// 发送足够多的失败请求触发降级（需 >= minRequestsForDegrade=10）。
	for i := 0; i < 12; i++ {
		_, _ = s.Rerank(ctx, req)
	}

	// 验证已进入降级状态。
	if !s.IsDegraded() {
		t.Fatal("失败率超阈值后应处于降级状态")
	}

	// 降级状态下直接走 external，不调用 self。
	ce.called = 0 // 重置计数
	external.called = 0

	result, err := s.Rerank(ctx, req)
	if err != nil {
		t.Fatalf("降级状态下 Rerank 失败: %v", err)
	}
	if result.ModelUsed != "external" {
		t.Errorf("降级时 ModelUsed = %q, 期望 external", result.ModelUsed)
	}
	if ce.called != 0 {
		t.Errorf("降级时 self 不应被调用, ce.called = %d", ce.called)
	}
	if external.called != 1 {
		t.Errorf("降级时 external 应被调用, called = %d", external.called)
	}
}

// TestServer_DegradeRecovery 验证降级冷却期过后恢复 self。
func TestServer_DegradeRecovery(t *testing.T) {
	ce := &mockCrossEncoder{err: errors.New("temp error")}
	external := &mockExternalReranker{}
	s := NewServer(ce, nil, nil, external)
	s.SetDegradeThreshold(0.3)
	// 设置极短冷却期便于测试。
	s.SetDegradeCooldown(50 * time.Millisecond)

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "测试",
		Model:      "self",
		TopK:       1,
	}

	ctx := context.Background()
	// 触发降级。
	for i := 0; i < 12; i++ {
		_, _ = s.Rerank(ctx, req)
	}
	if !s.IsDegraded() {
		t.Fatal("应处于降级状态")
	}

	// 等待冷却期过期。
	time.Sleep(60 * time.Millisecond)

	// 恢复 self 模型（修复错误）。
	ce.mu.Lock()
	ce.err = nil
	ce.mu.Unlock()

	// 冷却期过后应恢复 self。
	result, err := s.Rerank(ctx, req)
	if err != nil {
		t.Fatalf("冷却期后 Rerank 失败: %v", err)
	}
	if result.ModelUsed != "self" {
		t.Errorf("冷却期后 ModelUsed = %q, 期望 self", result.ModelUsed)
	}
}

// TestServer_HotReloadModel 验证模型热加载。
func TestServer_HotReloadModel(t *testing.T) {
	s := NewServer(&mockCrossEncoder{}, nil, nil, nil)

	if s.GetCurrentModel() != "self" {
		t.Errorf("初始模型 = %q, 期望 self", s.GetCurrentModel())
	}

	err := s.HotReloadModel(context.Background(), "/data/models/v2.onnx", "v20260701_120000")
	if err != nil {
		t.Fatalf("HotReloadModel 失败: %v", err)
	}

	if s.GetCurrentModel() != "v20260701_120000" {
		t.Errorf("热加载后模型 = %q, 期望 v20260701_120000", s.GetCurrentModel())
	}
}

// TestServer_SetModels 验证原子替换模型实例。
func TestServer_SetModels(t *testing.T) {
	oldCE := &mockCrossEncoder{}
	s := NewServer(oldCE, nil, nil, nil)

	newCE := &mockCrossEncoder{}
	s.SetModels(newCE, nil, nil, nil)

	// 验证模型已替换（通过调用确认不 panic）。
	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "test",
		Model:      "self",
		TopK:       1,
	}
	_, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("SetModels 后 Rerank 失败: %v", err)
	}
	if newCE.called != 1 {
		t.Errorf("新模型调用次数 = %d, 期望 1", newCE.called)
	}
	if oldCE.called != 0 {
		t.Errorf("旧模型不应被调用, called = %d", oldCE.called)
	}
}

// TestServer_HealthCheck 验证健康检查。
func TestServer_HealthCheck(t *testing.T) {
	ctx := context.Background()

	// 无任何模型：应失败。
	s1 := NewServer(nil, nil, nil, nil)
	if err := s1.HealthCheck(ctx); err == nil {
		t.Error("无模型时 HealthCheck 应返回错误")
	}

	// 有 self 模型：应通过。
	s2 := NewServer(&mockCrossEncoder{}, nil, nil, nil)
	if err := s2.HealthCheck(ctx); err != nil {
		t.Errorf("有 self 模型时 HealthCheck 不应报错: %v", err)
	}

	// 有 external 模型：应通过。
	s3 := NewServer(nil, nil, nil, &mockExternalReranker{})
	if err := s3.HealthCheck(ctx); err != nil {
		t.Errorf("有 external 模型时 HealthCheck 不应报错: %v", err)
	}
}

// TestServer_HealthCheckDegradedWithoutExternal 验证降级但无 external 时健康检查失败。
func TestServer_HealthCheckDegradedWithoutExternal(t *testing.T) {
	ce := &mockCrossEncoder{err: errors.New("unavailable")}
	s := NewServer(ce, nil, nil, nil) // 无 external
	s.SetDegradeThreshold(0.3)
	s.SetDegradeCooldown(1 * time.Hour)

	ctx := context.Background()
	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "测试",
		Model:      "self",
		TopK:       1,
	}

	// 触发降级。
	for i := 0; i < 12; i++ {
		_, _ = s.Rerank(ctx, req)
	}

	if !s.IsDegraded() {
		t.Fatal("应处于降级状态")
	}

	// 降级且无 external：健康检查应失败。
	if err := s.HealthCheck(ctx); err == nil {
		t.Error("降级且无 external 时 HealthCheck 应返回错误")
	}
}

// TestServer_RerankDefaultModel 验证空 Model 默认走 self。
func TestServer_RerankDefaultModel(t *testing.T) {
	ce := &mockCrossEncoder{}
	s := NewServer(ce, nil, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Query:      "测试",
		Model:      "", // 空值，默认 self
		TopK:       1,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank 失败: %v", err)
	}
	if result.ModelUsed != "self" {
		t.Errorf("空 Model 默认 ModelUsed = %q, 期望 self", result.ModelUsed)
	}
}

// TestServer_RerankSelfNoModels 验证无 self 模型且无 external 兜底时报错。
func TestServer_RerankSelfNoModels(t *testing.T) {
	s := NewServer(nil, nil, nil, nil) // 无 self 模型，也无 external 兜底

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      "self",
		TopK:       1,
	}

	_, err := s.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("无 self 模型且无 external 时应返回错误")
	}
}

// TestServer_RerankExternalNotConfigured 验证无 external 时 external 路径报错。
func TestServer_RerankExternalNotConfigured(t *testing.T) {
	s := NewServer(&mockCrossEncoder{}, nil, nil, nil) // 无 external

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      "external",
		TopK:       1,
	}

	_, err := s.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("无 external 时 external 路径应返回错误")
	}
}

// TestServer_RerankTopKTruncation 验证 TopK 截断。
func TestServer_RerankTopKTruncation(t *testing.T) {
	ce := &mockCrossEncoder{}
	s := NewServer(ce, nil, nil, nil)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
		{ArticleID: "a4"},
		{ArticleID: "a5"},
	}

	req := domain.RerankRequest{
		Candidates: cands,
		Query:      "测试",
		Model:      "self",
		TopK:       3,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank 失败: %v", err)
	}
	if len(result.Candidates) != 3 {
		t.Errorf("返回候选数 = %d, 期望 3 (TopK 截断)", len(result.Candidates))
	}
}

// TestServer_RerankSelfTwoTowerOnly 验证仅有 TwoTower 时也能工作。
func TestServer_RerankSelfTwoTowerOnly(t *testing.T) {
	tt := &mockTwoTower{}
	s := NewServer(nil, tt, nil, nil)

	cands := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: cands,
		Model:      "self",
		TopK:       2,
	}

	result, err := s.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("仅有 TwoTower 时 Rerank 失败: %v", err)
	}
	if result.ModelUsed != "self" {
		t.Errorf("ModelUsed = %q, 期望 self", result.ModelUsed)
	}
	if tt.called != 1 {
		t.Errorf("TwoTower 调用次数 = %d, 期望 1", tt.called)
	}
}
