package rerank

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 SelfReranker（Task 8.8），覆盖：
//   - Name 方法
//   - Rerank self 路径（调 server.Rerank）
//   - Rerank 默认模型（空 Model → self）
//   - Rerank external 路径（调 abTest.ExternalRerank）
//   - Rerank ab_test 路径（调 abTest.Rerank）
//   - 特征提取：正常/错误/空特征/nil fe
//   - 错误路径：nil server / nil abTest / 未知模型
//
// mock 复用说明：
//   - mockSelfReranker（定义于 abtest_test.go）满足 domain.Reranker interface。
//   - mockExternalReranker（定义于 server_test.go）满足 ExternalReranker interface。
//   - 本文件定义 mockRerankServer（满足 RerankServer interface）与
//     mockFeatureProvider（满足 FeatureProvider interface）。
// ============================================================================

// ---- mock 实现 ----

// mockRerankServer 模拟 RerankServer interface。
type mockRerankServer struct {
	mu      sync.Mutex
	err     error
	called  int
	lastReq domain.RerankRequest
	result  domain.RerankResult
}

func (m *mockRerankServer) Rerank(_ context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
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
	return domain.RerankResult{Candidates: req.Candidates, ModelUsed: ModelSelf}, nil
}

// mockFeatureProvider 模拟 FeatureProvider interface。
type mockFeatureProvider struct {
	mu     sync.Mutex
	err    error
	called int
	feats  map[string]float64
}

func (m *mockFeatureProvider) Extract(_ context.Context, _ domain.Candidate) (map[string]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++
	if m.err != nil {
		return nil, m.err
	}
	if m.feats != nil {
		// 返回副本，避免调用方修改内部状态。
		out := make(map[string]float64, len(m.feats))
		for k, v := range m.feats {
			out[k] = v
		}
		return out, nil
	}
	return map[string]float64{"mock_feature": 1.0}, nil
}

// ---- 测试用例 ----

// TestSelfReranker_Name 验证重排器名称。
func TestSelfReranker_Name(t *testing.T) {
	r := NewSelfReranker(&mockRerankServer{}, nil, nil)
	if got := r.Name(); got != "self_rerank" {
		t.Errorf("Name() = %q, 期望 self_rerank", got)
	}
}

// TestSelfReranker_RerankSelf 验证 self 路径调用 server.Rerank。
func TestSelfReranker_RerankSelf(t *testing.T) {
	server := &mockRerankServer{}
	r := NewSelfReranker(server, nil, nil)

	cands := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: "u1"},
		Candidates: cands,
		Model:      ModelSelf,
		TopK:       2,
	}

	result, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank self 失败: %v", err)
	}
	if server.called != 1 {
		t.Errorf("server 调用次数 = %d, 期望 1", server.called)
	}
	if result.ModelUsed != ModelSelf {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, ModelSelf)
	}
}

// TestSelfReranker_RerankDefaultModel 验证空 Model 默认走 self 路径。
func TestSelfReranker_RerankDefaultModel(t *testing.T) {
	server := &mockRerankServer{}
	r := NewSelfReranker(server, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      "", // 空值，默认 self
		TopK:       1,
	}

	result, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank 默认路径失败: %v", err)
	}
	if server.called != 1 {
		t.Errorf("server 调用次数 = %d, 期望 1", server.called)
	}
	if result.ModelUsed != ModelSelf {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, ModelSelf)
	}
}

// TestSelfReranker_RerankExternal 验证 external 路径调用 abTest.ExternalRerank。
func TestSelfReranker_RerankExternal(t *testing.T) {
	server := &mockRerankServer{}
	external := &mockExternalReranker{}
	ab := NewABTest(&mockSelfReranker{}, external, 0.5)
	r := NewSelfReranker(server, nil, ab)

	cands := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	req := domain.RerankRequest{
		Candidates: cands,
		Query:      "测试",
		Model:      ModelExternal,
		TopK:       2,
	}

	result, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank external 失败: %v", err)
	}
	if external.called != 1 {
		t.Errorf("external 调用次数 = %d, 期望 1", external.called)
	}
	if server.called != 0 {
		t.Errorf("server 不应被调用, called = %d", server.called)
	}
	if result.ModelUsed != ModelExternal {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, ModelExternal)
	}
}

// TestSelfReranker_RerankABTest 验证 ab_test 路径调用 abTest.Rerank。
func TestSelfReranker_RerankABTest(t *testing.T) {
	server := &mockRerankServer{}
	self := &mockSelfReranker{}
	external := &mockExternalReranker{}
	ab := NewABTest(self, external, 0.5)
	r := NewSelfReranker(server, nil, ab)

	uid := findUserForBucket(ab, bucketSelf)
	if uid == "" {
		t.Fatal("未找到 self 桶 userID")
	}

	req := domain.RerankRequest{
		UserKey:    domain.UserKey{UserID: uid},
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelABTest,
		TopK:       1,
	}

	result, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank ab_test 失败: %v", err)
	}
	// ab_test 路径应调用 self/external reranker（取决于分桶），不调 server。
	if server.called != 0 {
		t.Errorf("ab_test 路径不应调用 server, called = %d", server.called)
	}
	if self.called != 1 {
		t.Errorf("selfReranker 调用次数 = %d, 期望 1", self.called)
	}
	if result.ModelUsed != bucketSelf {
		t.Errorf("ModelUsed = %q, 期望 %q", result.ModelUsed, bucketSelf)
	}
}

// TestSelfReranker_RerankFeatureExtraction 验证特征提取并合并到候选 Scores。
func TestSelfReranker_RerankFeatureExtraction(t *testing.T) {
	server := &mockRerankServer{}
	fe := &mockFeatureProvider{feats: map[string]float64{"f1": 0.5, "f2": 0.8}}
	r := NewSelfReranker(server, fe, nil)

	cands := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	req := domain.RerankRequest{
		Candidates: cands,
		Model:      ModelSelf,
		TopK:       2,
	}

	_, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank 失败: %v", err)
	}
	if fe.called != 2 {
		t.Errorf("FeatureProvider 调用次数 = %d, 期望 2（每候选一次）", fe.called)
	}
	// 验证特征合并到候选 Scores。
	lastReq := server.lastReq
	for i := range lastReq.Candidates {
		if got := lastReq.Candidates[i].Scores["f1"]; got != 0.5 {
			t.Errorf("候选 %d Scores[f1] = %.2f, 期望 0.5", i, got)
		}
		if got := lastReq.Candidates[i].Scores["f2"]; got != 0.8 {
			t.Errorf("候选 %d Scores[f2] = %.2f, 期望 0.8", i, got)
		}
	}
}

// TestSelfReranker_RerankFeatureExtractionError 验证特征提取错误传播。
func TestSelfReranker_RerankFeatureExtractionError(t *testing.T) {
	server := &mockRerankServer{}
	fe := &mockFeatureProvider{err: errors.New("extract failed")}
	r := NewSelfReranker(server, fe, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("特征提取失败应返回错误")
	}
	if server.called != 0 {
		t.Errorf("特征提取失败时 server 不应被调用, called = %d", server.called)
	}
}

// TestSelfReranker_RerankFeatureExtractionEmpty 验证空特征跳过合并。
func TestSelfReranker_RerankFeatureExtractionEmpty(t *testing.T) {
	server := &mockRerankServer{}
	fe := &mockFeatureProvider{feats: map[string]float64{}} // 空特征
	r := NewSelfReranker(server, fe, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("空特征不应报错: %v", err)
	}
	if server.called != 1 {
		t.Errorf("空特征时 server 应被调用, called = %d", server.called)
	}
	// 空特征时不应创建 Scores map。
	if server.lastReq.Candidates[0].Scores != nil {
		t.Error("空特征时不应创建 Scores map")
	}
}

// TestSelfReranker_RerankNilFeatureProvider 验证 nil fe 跳过特征提取。
func TestSelfReranker_RerankNilFeatureProvider(t *testing.T) {
	server := &mockRerankServer{}
	r := NewSelfReranker(server, nil, nil) // fe = nil

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	result, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("nil fe 时 Rerank 不应失败: %v", err)
	}
	if server.called != 1 {
		t.Errorf("server 调用次数 = %d, 期望 1", server.called)
	}
	if len(result.Candidates) != 1 {
		t.Errorf("返回候选数 = %d, 期望 1", len(result.Candidates))
	}
}

// TestSelfReranker_RerankUnknownModel 验证未知模型返回错误。
func TestSelfReranker_RerankUnknownModel(t *testing.T) {
	server := &mockRerankServer{}
	r := NewSelfReranker(server, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      "unknown_model",
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("未知模型应返回错误")
	}
	if server.called != 0 {
		t.Errorf("未知模型时 server 不应被调用, called = %d", server.called)
	}
}

// TestSelfReranker_RerankNilServer 验证 self 路径 nil server 报错。
func TestSelfReranker_RerankNilServer(t *testing.T) {
	r := NewSelfReranker(nil, nil, nil) // server = nil

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("nil server 时 self 路径应返回错误")
	}
}

// TestSelfReranker_RerankNilABTestExternal 验证 external 路径 nil abTest 报错。
func TestSelfReranker_RerankNilABTestExternal(t *testing.T) {
	r := NewSelfReranker(&mockRerankServer{}, nil, nil) // abTest = nil

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelExternal,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("nil abTest 时 external 路径应返回错误")
	}
}

// TestSelfReranker_RerankNilABTestABTest 验证 ab_test 路径 nil abTest 报错。
func TestSelfReranker_RerankNilABTestABTest(t *testing.T) {
	r := NewSelfReranker(&mockRerankServer{}, nil, nil) // abTest = nil

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelABTest,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("nil abTest 时 ab_test 路径应返回错误")
	}
}

// TestSelfReranker_RerankServerError 验证 server 错误传播。
func TestSelfReranker_RerankServerError(t *testing.T) {
	server := &mockRerankServer{err: errors.New("server error")}
	r := NewSelfReranker(server, nil, nil)

	req := domain.RerankRequest{
		Candidates: []domain.Candidate{{ArticleID: "a1"}},
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err == nil {
		t.Fatal("server 错误应传播")
	}
}

// TestSelfReranker_RerankFeaturePreservesExistingScores 验证特征合并不覆盖已有 Scores。
func TestSelfReranker_RerankFeaturePreservesExistingScores(t *testing.T) {
	server := &mockRerankServer{}
	fe := &mockFeatureProvider{feats: map[string]float64{"new_feat": 0.5}}
	r := NewSelfReranker(server, fe, nil)

	cands := []domain.Candidate{
		{ArticleID: "a1", Scores: map[string]float64{"existing_feat": 0.9}},
	}
	req := domain.RerankRequest{
		Candidates: cands,
		Model:      ModelSelf,
		TopK:       1,
	}

	_, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rerank 失败: %v", err)
	}
	lastReq := server.lastReq
	if got := lastReq.Candidates[0].Scores["existing_feat"]; got != 0.9 {
		t.Errorf("已有特征被覆盖: existing_feat = %.2f, 期望 0.9", got)
	}
	if got := lastReq.Candidates[0].Scores["new_feat"]; got != 0.5 {
		t.Errorf("新特征未合并: new_feat = %.2f, 期望 0.5", got)
	}
}

// TestSelfReranker_RerankEmptyCandidates 验证空候选列表不调特征提取。
func TestSelfReranker_RerankEmptyCandidates(t *testing.T) {
	server := &mockRerankServer{result: domain.RerankResult{Candidates: []domain.Candidate{}, ModelUsed: ModelSelf}}
	fe := &mockFeatureProvider{}
	r := NewSelfReranker(server, fe, nil)

	req := domain.RerankRequest{
		Candidates: nil,
		Model:      ModelSelf,
		TopK:       0,
	}

	_, err := r.Rerank(context.Background(), req)
	if err != nil {
		t.Fatalf("空候选列表 Rerank 失败: %v", err)
	}
	if fe.called != 0 {
		t.Errorf("空候选列表时 FeatureProvider 不应被调用, called = %d", fe.called)
	}
}
