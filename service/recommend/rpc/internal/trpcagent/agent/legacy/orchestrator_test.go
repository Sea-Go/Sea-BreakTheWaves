// Package agent orchestrator_test.go — OrchestratorAgent 单元测试（Task 12.1）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 OrchestratorAgent 的：
//   - fast 路径（0 token，无 LLM 调用）
//   - slow 路径（6 Agent 串行调用）
//   - hybrid 路径（fast Top-50 → slow Rerank Top-20 → Top-10）
//   - 路由决策（按 intent.complexity 路由：simple→fast, medium→hybrid, complex→slow）
//   - fallback（主路径失败 + FallbackEnabled 降级到 fast）
//   - 候选融合去重（mergeCandidates 按 ArticleID 去重，分数相加）
//   - Agent.Run（通过 State["recommend_request"] 触发 Recommend）
package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubRecaller 测试用 Recaller stub（实现 domain.Recaller）。
type stubRecaller struct {
	candidates []domain.Candidate
	err        error
	calls      int
}

func (s *stubRecaller) Name() string { return "stub-recaller" }

func (s *stubRecaller) Recall(_ context.Context, _ domain.RecallRequest) (domain.RecallResult, error) {
	s.calls++
	if s.err != nil {
		return domain.RecallResult{}, s.err
	}
	return domain.RecallResult{Candidates: s.candidates, Source: "stub"}, nil
}

// stubRanker 测试用 Ranker stub（实现 domain.Ranker）。
type stubRanker struct {
	err   error
	calls int
}

func (s *stubRanker) Name() string { return "stub-ranker" }

func (s *stubRanker) Rank(_ context.Context, rankCtx domain.RankContext) (domain.RankResult, error) {
	s.calls++
	if s.err != nil {
		return domain.RankResult{}, s.err
	}
	// 默认原样返回候选（模拟排序通过）。
	return domain.RankResult{Candidates: rankCtx.Candidates}, nil
}

// stubAgent 测试用 Agent stub（实现 domain.Agent）。
// 通过 mutate 函数定制 State 变更，通过 callOrder 共享切片记录调用顺序。
type stubAgent struct {
	name      string
	mutate    func(state map[string]any) map[string]any
	callOrder *[]string
	calls     int
	err       error
}

func (s *stubAgent) Name() string { return s.name }

func (s *stubAgent) Run(_ context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	s.calls++
	if s.callOrder != nil {
		*s.callOrder = append(*s.callOrder, s.name)
	}
	if s.err != nil {
		return domain.AgentOutput{}, s.err
	}
	out := copyState(input.State)
	if s.mutate != nil {
		out = s.mutate(out)
	}
	return domain.AgentOutput{State: out, Trace: []string{s.name + ".run"}}, nil
}

// makeOrchCandidates 构造 n 个测试候选（ArticleID 为 a1..aN）。
func makeOrchCandidates(n int) []domain.Candidate {
	cs := make([]domain.Candidate, n)
	for i := 0; i < n; i++ {
		cs[i] = domain.Candidate{
			ArticleID: fmt.Sprintf("a%d", i+1),
			Score:     float64(n-i) / float64(n),
			Source:    "stub",
			Scores:    map[string]float64{"cf_score": 0.5},
		}
	}
	return cs
}

// findCandidate 在候选列表中按 ArticleID 查找。
func findCandidate(cs []domain.Candidate, id string) (domain.Candidate, bool) {
	for _, c := range cs {
		if c.ArticleID == id {
			return c, true
		}
	}
	return domain.Candidate{}, false
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestOrchestratorAgent_Name 验证 Name 返回 "orchestrator"。
func TestOrchestratorAgent_Name(t *testing.T) {
	a := NewOrchestratorAgent(nil, nil, nil, domain.AgentOptions{})
	if a.Name() != "orchestrator" {
		t.Errorf("Name = %q, 期望 orchestrator", a.Name())
	}
}

// TestOrchestratorAgent_FastPath 验证 fast 路径：0 token、无 LLM 调用、PathTaken="fast"。
func TestOrchestratorAgent_FastPath(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(5)}
	ranker := &stubRanker{}
	a := NewOrchestratorAgent(recaller, ranker, nil, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "fast",
		TopK:     3,
	}
	resp, err := a.Recommend(context.Background(), req)
	if err != nil {
		t.Fatalf("Recommend 错误: %v", err)
	}
	if resp.PathTaken != "fast" {
		t.Errorf("PathTaken = %q, 期望 fast", resp.PathTaken)
	}
	if resp.Cost.TokensIn != 0 {
		t.Errorf("fast 路径 TokensIn = %d, 期望 0", resp.Cost.TokensIn)
	}
	if resp.Cost.LLMCalls != 0 {
		t.Errorf("fast 路径 LLMCalls = %d, 期望 0", resp.Cost.LLMCalls)
	}
	if len(resp.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望 3", len(resp.Candidates))
	}
	if recaller.calls != 1 {
		t.Errorf("recaller 调用次数 = %d, 期望 1", recaller.calls)
	}
	if ranker.calls != 1 {
		t.Errorf("ranker 调用次数 = %d, 期望 1", ranker.calls)
	}
}

// TestOrchestratorAgent_SlowPath 验证 slow 路径：6 个 Agent 串行调用、PathTaken="slow"。
func TestOrchestratorAgent_SlowPath(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(5)}
	var order []string
	bundle := &AgentBundle{
		Intent: &stubAgent{name: "intent", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			s["intent"] = Intent{Label: "informational", Complexity: 0.8}
			return s
		}},
		RecallPlanner: &stubAgent{name: "recall_planner", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			s["recall_plan"] = "default"
			return s
		}},
		Graph: &stubAgent{name: "graph", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			s["graph_knowledge"] = domain.GraphKnowledge{Cypher: "MATCH (n) RETURN n LIMIT 10", Articles: []string{"a1"}}
			return s
		}},
		Rerank: &stubAgent{name: "rerank", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			cs := candidatesFromState(s, "candidates")
			s["rerank_items"] = cs
			return s
		}},
		Quality: &stubAgent{name: "quality", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			cs := candidatesFromState(s, "rerank_items")
			s["quality_filtered"] = cs
			s["quality_scores"] = map[string]float64{"a1": 0.9, "a2": 0.8}
			return s
		}},
		Explain: &stubAgent{name: "explain", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			s["explanation"] = "基于您的兴趣推荐"
			return s
		}},
	}
	a := NewOrchestratorAgent(recaller, nil, bundle, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "slow",
		TopK:     10,
		Explain:  true,
	}
	resp, err := a.Recommend(context.Background(), req)
	if err != nil {
		t.Fatalf("Recommend 错误: %v", err)
	}
	if resp.PathTaken != "slow" {
		t.Errorf("PathTaken = %q, 期望 slow", resp.PathTaken)
	}
	// 验证 6 个 Agent 串行调用顺序。
	wantOrder := []string{"intent", "recall_planner", "graph", "rerank", "quality", "explain"}
	if len(order) != len(wantOrder) {
		t.Fatalf("调用次数 = %d, 期望 %d", len(order), len(wantOrder))
	}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Errorf("调用顺序[%d] = %q, 期望 %q", i, order[i], name)
		}
	}
	// 验证成本：6 个 Agent，每个 mock 100 in / 50 out。
	if resp.Cost.LLMCalls != 6 {
		t.Errorf("LLMCalls = %d, 期望 6", resp.Cost.LLMCalls)
	}
	if resp.Cost.TokensIn != 600 {
		t.Errorf("TokensIn = %d, 期望 600", resp.Cost.TokensIn)
	}
	if resp.Cost.TokensOut != 300 {
		t.Errorf("TokensOut = %d, 期望 300", resp.Cost.TokensOut)
	}
	// 验证解释文本。
	if resp.Explain != "基于您的兴趣推荐" {
		t.Errorf("Explain = %q, 期望 '基于您的兴趣推荐'", resp.Explain)
	}
	// 验证质量评分。
	if len(resp.QualityScores) != 2 {
		t.Errorf("QualityScores 数量 = %d, 期望 2", len(resp.QualityScores))
	}
	// 验证图谱 trace。
	if resp.GraphTrace == nil {
		t.Errorf("GraphTrace 不应为 nil（slow 路径有图谱）")
	}
	if resp.GraphTrace.Cypher == "" {
		t.Errorf("GraphTrace.Cypher 不应为空")
	}
}

// TestOrchestratorAgent_HybridPath 验证 hybrid 路径：fast Top-50 → slow Rerank Top-20 → Top-10。
func TestOrchestratorAgent_HybridPath(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(60)}
	var order []string

	rerankInputCount := 0
	qualityInputCount := 0

	bundle := &AgentBundle{
		Rerank: &stubAgent{name: "rerank", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			cs := candidatesFromState(s, "candidates")
			rerankInputCount = len(cs)
			s["rerank_items"] = truncateCandidates(cs, hybridRerankTopN)
			return s
		}},
		Quality: &stubAgent{name: "quality", callOrder: &order, mutate: func(s map[string]any) map[string]any {
			cs := candidatesFromState(s, "rerank_items")
			qualityInputCount = len(cs)
			s["quality_filtered"] = truncateCandidates(cs, 10)
			return s
		}},
	}
	a := NewOrchestratorAgent(recaller, nil, bundle, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "hybrid",
		TopK:     10,
	}
	resp, err := a.Recommend(context.Background(), req)
	if err != nil {
		t.Fatalf("Recommend 错误: %v", err)
	}
	if resp.PathTaken != "hybrid" {
		t.Errorf("PathTaken = %q, 期望 hybrid", resp.PathTaken)
	}
	// 验证调用顺序：仅 rerank + quality。
	wantOrder := []string{"rerank", "quality"}
	if len(order) != len(wantOrder) {
		t.Fatalf("调用次数 = %d, 期望 %d", len(order), len(wantOrder))
	}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Errorf("调用顺序[%d] = %q, 期望 %q", i, order[i], name)
		}
	}
	// 验证 fast 阶段产出 Top-50。
	if rerankInputCount != hybridFastTopN {
		t.Errorf("rerank 输入数 = %d, 期望 %d（hybridFastTopN）", rerankInputCount, hybridFastTopN)
	}
	// 验证 rerank 阶段截断到 Top-20。
	if qualityInputCount != hybridRerankTopN {
		t.Errorf("quality 输入数 = %d, 期望 %d（hybridRerankTopN）", qualityInputCount, hybridRerankTopN)
	}
	// 验证最终 Top-10。
	if len(resp.Candidates) > 10 {
		t.Errorf("候选数 = %d, 期望 ≤ 10", len(resp.Candidates))
	}
	// 验证成本：2 个 Agent（rerank + quality）。
	if resp.Cost.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, 期望 2", resp.Cost.LLMCalls)
	}
}

// TestOrchestratorAgent_RouteDecision 验证路由决策：PathMode 空时按 intent.complexity 路由。
func TestOrchestratorAgent_RouteDecision(t *testing.T) {
	cases := []struct {
		name       string
		complexity float64
		wantPath   string
	}{
		{"simple", 0.3, "fast"},
		{"medium", 0.5, "hybrid"},
		{"complex", 0.8, "slow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recaller := &stubRecaller{candidates: makeOrchCandidates(60)}
			bundle := &AgentBundle{
				Intent: &stubAgent{name: "intent", mutate: func(s map[string]any) map[string]any {
					s["intent"] = Intent{Label: c.name, Complexity: c.complexity}
					return s
				}},
				RecallPlanner: &stubAgent{name: "recall_planner"},
				Graph:         &stubAgent{name: "graph"},
				Rerank: &stubAgent{name: "rerank", mutate: func(s map[string]any) map[string]any {
					cs := candidatesFromState(s, "candidates")
					s["rerank_items"] = truncateCandidates(cs, hybridRerankTopN)
					return s
				}},
				Quality: &stubAgent{name: "quality", mutate: func(s map[string]any) map[string]any {
					cs := candidatesFromState(s, "rerank_items")
					s["quality_filtered"] = cs
					return s
				}},
				Explain: &stubAgent{name: "explain", mutate: func(s map[string]any) map[string]any {
					s["explanation"] = "test"
					return s
				}},
			}
			a := NewOrchestratorAgent(recaller, nil, bundle, domain.AgentOptions{})

			req := domain.RecommendRequest{
				UserKey:  domain.UserKey{UserID: "u1"},
				PathMode: "", // auto，按 complexity 路由
				TopK:     5,
			}
			resp, err := a.Recommend(context.Background(), req)
			if err != nil {
				t.Fatalf("Recommend 错误: %v", err)
			}
			if resp.PathTaken != c.wantPath {
				t.Errorf("complexity=%.1f → PathTaken = %q, 期望 %q", c.complexity, resp.PathTaken, c.wantPath)
			}
		})
	}
}

// TestOrchestratorAgent_Fallback 验证主路径失败 + FallbackEnabled 时降级到 fast。
func TestOrchestratorAgent_Fallback(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(5)}
	// intent Agent 返回错误，导致 slow 路径失败。
	bundle := &AgentBundle{
		Intent: &stubAgent{name: "intent", err: errors.New("intent agent down")},
	}
	a := NewOrchestratorAgent(recaller, nil, bundle, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "slow",
		Config:   domain.RecommendConfig{FallbackEnabled: true},
		TopK:     3,
	}
	resp, err := a.Recommend(context.Background(), req)
	if err != nil {
		t.Fatalf("fallback 应成功, 实际错误: %v", err)
	}
	if resp.PathTaken != "fast" {
		t.Errorf("PathTaken = %q, 期望 fast（fallback）", resp.PathTaken)
	}
	if len(resp.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望 3", len(resp.Candidates))
	}
}

// TestOrchestratorAgent_FallbackDisabled 验证主路径失败 + FallbackEnabled=false 时返回错误。
func TestOrchestratorAgent_FallbackDisabled(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(5)}
	bundle := &AgentBundle{
		Intent: &stubAgent{name: "intent", err: errors.New("intent agent down")},
	}
	a := NewOrchestratorAgent(recaller, nil, bundle, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "slow",
		Config:   domain.RecommendConfig{FallbackEnabled: false},
		TopK:     3,
	}
	_, err := a.Recommend(context.Background(), req)
	if err == nil {
		t.Fatalf("FallbackEnabled=false 时应返回错误")
	}
}

// TestMergeCandidates_Dedup 验证候选融合去重：按 ArticleID 去重，分数相加。
func TestMergeCandidates_Dedup(t *testing.T) {
	fast := []domain.Candidate{
		{ArticleID: "a1", Score: 0.5, Scores: map[string]float64{"cf": 0.3}},
		{ArticleID: "a2", Score: 0.6, Scores: map[string]float64{"cf": 0.4}},
	}
	slow := []domain.Candidate{
		{ArticleID: "a2", Score: 0.4, Scores: map[string]float64{"graph": 0.2}},
		{ArticleID: "a3", Score: 0.7, Scores: map[string]float64{"graph": 0.5}},
	}
	merged := mergeCandidates(fast, slow)
	if len(merged) != 3 {
		t.Fatalf("融合后数量 = %d, 期望 3（a1/a2/a3）", len(merged))
	}
	// a2 分数应相加：0.6 + 0.4 = 1.0。
	a2, ok := findCandidate(merged, "a2")
	if !ok {
		t.Fatalf("未找到 a2")
	}
	if a2.Score != 1.0 {
		t.Errorf("a2 Score = %f, 期望 1.0（0.6+0.4）", a2.Score)
	}
	// a2 的 Scores 应合并 cf + graph。
	if a2.Scores["cf"] != 0.4 {
		t.Errorf("a2 cf = %f, 期望 0.4", a2.Scores["cf"])
	}
	if a2.Scores["graph"] != 0.2 {
		t.Errorf("a2 graph = %f, 期望 0.2", a2.Scores["graph"])
	}
	// a1/a3 分数不变。
	a1, _ := findCandidate(merged, "a1")
	if a1.Score != 0.5 {
		t.Errorf("a1 Score = %f, 期望 0.5", a1.Score)
	}
	a3, _ := findCandidate(merged, "a3")
	if a3.Score != 0.7 {
		t.Errorf("a3 Score = %f, 期望 0.7", a3.Score)
	}
}

// TestDedupCandidates 验证按 ArticleID 去重保留首次出现。
func TestDedupCandidates(t *testing.T) {
	cs := []domain.Candidate{
		{ArticleID: "a1", Score: 0.9},
		{ArticleID: "a2", Score: 0.5},
		{ArticleID: "a1", Score: 0.3},
		{ArticleID: "a3", Score: 0.7},
	}
	out := dedupCandidates(cs)
	if len(out) != 3 {
		t.Fatalf("去重后数量 = %d, 期望 3", len(out))
	}
	if out[0].ArticleID != "a1" || out[0].Score != 0.9 {
		t.Errorf("首次 a1 应保留 Score=0.9, 实际 %f", out[0].Score)
	}
	if out[1].ArticleID != "a2" {
		t.Errorf("第二位应为 a2, 实际 %s", out[1].ArticleID)
	}
	if out[2].ArticleID != "a3" {
		t.Errorf("第三位应为 a3, 实际 %s", out[2].ArticleID)
	}
}

// TestOrchestratorAgent_AgentRun 验证通过 AgentInput.State["recommend_request"] 触发 Recommend。
func TestOrchestratorAgent_AgentRun(t *testing.T) {
	recaller := &stubRecaller{candidates: makeOrchCandidates(3)}
	a := NewOrchestratorAgent(recaller, nil, nil, domain.AgentOptions{})

	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "fast",
		TopK:     3,
	}
	out, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"recommend_request": req},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// 验证 Trace。
	wantTrace := []string{"orchestrator.route", "orchestrator.fast", "orchestrator.finish"}
	if len(out.Trace) != len(wantTrace) {
		t.Fatalf("Trace 长度 = %d, 期望 %d", len(out.Trace), len(wantTrace))
	}
	for i, tr := range wantTrace {
		if out.Trace[i] != tr {
			t.Errorf("Trace[%d] = %q, 期望 %q", i, out.Trace[i], tr)
		}
	}
	// 验证 State["recommend_response"]。
	resp, ok := out.State["recommend_response"].(domain.RecommendResponse)
	if !ok {
		t.Fatalf("State[recommend_response] 类型断言失败: %T", out.State["recommend_response"])
	}
	if resp.PathTaken != "fast" {
		t.Errorf("resp.PathTaken = %q, 期望 fast", resp.PathTaken)
	}
	// 验证 Result。
	if _, ok := out.Result.(domain.RecommendResponse); !ok {
		t.Errorf("Result 类型断言失败: %T", out.Result)
	}
	// 验证 State["path_decision"]。
	if pd, _ := out.State["path_decision"].(string); pd != "fast" {
		t.Errorf("State[path_decision] = %q, 期望 fast", pd)
	}
}

// TestOrchestratorAgent_AgentRun_MissingRequest 验证 State 缺少 recommend_request 时返回错误。
func TestOrchestratorAgent_AgentRun_MissingRequest(t *testing.T) {
	a := NewOrchestratorAgent(nil, nil, nil, domain.AgentOptions{})
	_, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{},
	})
	if err == nil {
		t.Fatalf("缺少 recommend_request 应返回错误")
	}
}

// TestOrchestratorAgent_NilReceiver 验证 nil receiver 安全处理。
func TestOrchestratorAgent_NilReceiver(t *testing.T) {
	var a *OrchestratorAgent
	_, err := a.Recommend(context.Background(), domain.RecommendRequest{})
	if err == nil {
		t.Errorf("nil receiver Recommend 应返回错误")
	}
	_, err = a.Run(context.Background(), domain.AgentInput{})
	if err == nil {
		t.Errorf("nil receiver Run 应返回错误")
	}
}

// TestOrchestratorAgent_FastPath_NoRecaller 验证 fast 路径 recaller 为 nil 时返回错误。
func TestOrchestratorAgent_FastPath_NoRecaller(t *testing.T) {
	a := NewOrchestratorAgent(nil, nil, nil, domain.AgentOptions{})
	req := domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		PathMode: "fast",
		TopK:     3,
	}
	_, err := a.Recommend(context.Background(), req)
	if err == nil {
		t.Fatalf("recaller 为 nil 时应返回错误")
	}
}

// TestExtractFinalCandidates 验证从 state 提取最终候选的优先级。
func TestExtractFinalCandidates(t *testing.T) {
	// 优先级：quality_filtered → rerank_items → candidates。
	// 1. 三者都有 → 取 quality_filtered。
	s1 := map[string]any{
		"candidates":       []domain.Candidate{{ArticleID: "a1"}},
		"rerank_items":     []domain.Candidate{{ArticleID: "a2"}},
		"quality_filtered": []domain.Candidate{{ArticleID: "a3"}},
	}
	got1 := extractFinalCandidates(s1)
	if len(got1) != 1 || got1[0].ArticleID != "a3" {
		t.Errorf("优先取 quality_filtered 失败: %v", got1)
	}
	// 2. 无 quality_filtered → 取 rerank_items。
	s2 := map[string]any{
		"candidates":   []domain.Candidate{{ArticleID: "a1"}},
		"rerank_items": []domain.Candidate{{ArticleID: "a2"}},
	}
	got2 := extractFinalCandidates(s2)
	if len(got2) != 1 || got2[0].ArticleID != "a2" {
		t.Errorf("次选取 rerank_items 失败: %v", got2)
	}
	// 3. 仅 candidates → 取 candidates。
	s3 := map[string]any{
		"candidates": []domain.Candidate{{ArticleID: "a1"}},
	}
	got3 := extractFinalCandidates(s3)
	if len(got3) != 1 || got3[0].ArticleID != "a1" {
		t.Errorf("兜底取 candidates 失败: %v", got3)
	}
	// 4. 空状态 → nil。
	got4 := extractFinalCandidates(map[string]any{})
	if got4 != nil {
		t.Errorf("空状态应返回 nil, 实际 %v", got4)
	}
}

// TestIntentToDomain 验证 agent.Intent → domain.Intent 转换。
func TestIntentToDomain(t *testing.T) {
	cases := []struct {
		complexity float64
		want       string
	}{
		{0.3, "simple"},
		{0.5, "medium"},
		{0.8, "complex"},
		{0.4, "medium"},
		{0.7, "complex"},
	}
	for _, c := range cases {
		got := intentToDomain(Intent{Complexity: c.complexity, Label: "test"})
		if got.Complexity != c.want {
			t.Errorf("complexity=%.1f → %q, 期望 %q", c.complexity, got.Complexity, c.want)
		}
		if got.Label != "test" {
			t.Errorf("Label = %q, 期望 test", got.Label)
		}
	}
}
