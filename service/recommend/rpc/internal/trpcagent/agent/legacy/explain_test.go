// Package agent explain_test.go — ExplainAgent 单元测试（Task 11.7）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 ExplainAgent 的：
//   - 推荐模式（recommend_explain）：LLM 生成解释
//   - 搜索模式（search_explain）：LLM 生成解释
//   - LLM 失败回退规则模板
//   - LLM 未注入时走规则兜底
//   - 空候选时规则兜底
//   - 多种 State 形态（domain.GraphKnowledge / map / agent.Intent / RecallPlan）
//   - Name 返回 "explain"
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubLLMForExplain 测试用 LLMClient stub（ExplainAgent 专用）。
type stubLLMForExplain struct {
	completeOut string
	completeErr error
	calls       int
	lastPrompt  string
	lastOpts    LLMOptions
}

func (s *stubLLMForExplain) Complete(_ context.Context, prompt string, opts LLMOptions) (string, error) {
	s.calls++
	s.lastPrompt = prompt
	s.lastOpts = opts
	if s.completeErr != nil {
		return "", s.completeErr
	}
	return s.completeOut, nil
}

func (s *stubLLMForExplain) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return nil, errors.New("not used")
}

func (s *stubLLMForExplain) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, errors.New("not used")
}

// makeExplainState 构造测试用 State（含 rerank_items/quality_scores/graph_knowledge/intent/recall_plan）。
func makeExplainState(mode string) map[string]any {
	return map[string]any{
		"explain_mode": mode,
		"rerank_items": []domain.Candidate{
			{ArticleID: "a1", Score: 0.9},
			{ArticleID: "a2", Score: 0.8},
			{ArticleID: "a3", Score: 0.7},
		},
		"quality_scores": map[string]float64{
			"a1": 0.9, "a2": 0.8, "a3": 0.7,
		},
		"graph_knowledge": domain.GraphKnowledge{
			Entities: []domain.Entity{{Name: "旅游", Type: "Topic"}},
			Articles: []string{"a1", "a2"},
			Authors:  []string{"张三"},
			IPs:      []string{"旅游IP"},
			Cypher:   "MATCH (e:Entity {name: $entity_name}) RETURN rec",
		},
		"intent": Intent{
			Label:      "informational",
			Complexity: 0.5,
			TimeIntent: "today",
		},
		"recall_plan": RecallPlan{
			Sources: []RecallSource{SourceContent, SourceGraph, SourceCF},
			Weights: map[string]float64{"content": 0.4, "graph": 0.3, "cf": 0.3},
		},
	}
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestExplainAgent_Name 验证 Name 返回 "explain"。
func TestExplainAgent_Name(t *testing.T) {
	a := NewExplainAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "explain" {
		t.Errorf("Name = %q, 期望 explain", a.Name())
	}
}

// TestExplainAgent_Run_RecommendMode 验证推荐模式：LLM 生成推荐解释。
func TestExplainAgent_Run_RecommendMode(t *testing.T) {
	llm := &stubLLMForExplain{
		completeOut: "基于您对旅游的兴趣，为您推荐 3 篇文章。",
	}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{Model: "gpt-4o", PromptCacheEnabled: true})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: makeExplainState("recommend_explain"),
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("期望 LLM 调用 1 次, 实际 %d", llm.calls)
	}
	// prompt 应包含"推荐"语义。
	if !strings.Contains(llm.lastPrompt, "推荐") {
		t.Errorf("推荐模式 prompt 应包含'推荐', 实际: %s", llm.lastPrompt)
	}
	// prompt 应包含候选数。
	if !strings.Contains(llm.lastPrompt, "3") {
		t.Errorf("prompt 应包含候选数 3, 实际: %s", llm.lastPrompt)
	}
	// prompt 应包含召回源。
	if !strings.Contains(llm.lastPrompt, "content") || !strings.Contains(llm.lastPrompt, "graph") {
		t.Errorf("prompt 应包含召回源, 实际: %s", llm.lastPrompt)
	}
	// opts 应透传 Model 与 EnableCache。
	if llm.lastOpts.Model != "gpt-4o" {
		t.Errorf("opts.Model = %q, 期望 gpt-4o", llm.lastOpts.Model)
	}
	if !llm.lastOpts.EnableCache {
		t.Errorf("opts.EnableCache = false, 期望 true")
	}
	// Result 应为 LLM 输出。
	if got.Result != llm.completeOut {
		t.Errorf("Result = %q, 期望 %q", got.Result, llm.completeOut)
	}
	// State 应包含 explanation。
	if _, ok := got.State["explanation"]; !ok {
		t.Errorf("State 缺少 explanation")
	}
	// Trace 应包含 explain.generate。
	if len(got.Trace) != 1 || got.Trace[0] != "explain.generate" {
		t.Errorf("Trace = %v, 期望 [explain.generate]", got.Trace)
	}
}

// TestExplainAgent_Run_SearchMode 验证搜索模式：LLM 生成搜索解释。
func TestExplainAgent_Run_SearchMode(t *testing.T) {
	llm := &stubLLMForExplain{
		completeOut: "为您找到 3 篇相关文章，按相关性排序。",
	}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: makeExplainState("search_explain"),
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// prompt 应包含"搜索"语义。
	if !strings.Contains(llm.lastPrompt, "搜索") {
		t.Errorf("搜索模式 prompt 应包含'搜索', 实际: %s", llm.lastPrompt)
	}
	if got.Result != llm.completeOut {
		t.Errorf("Result = %q, 期望 %q", got.Result, llm.completeOut)
	}
}

// TestExplainAgent_Run_LLMFailFallback 验证 LLM 失败回退规则模板。
func TestExplainAgent_Run_LLMFailFallback(t *testing.T) {
	llm := &stubLLMForExplain{completeErr: errors.New("llm down")}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: makeExplainState("recommend_explain"),
	})
	if err != nil {
		t.Fatalf("LLM 失败不应返回错误, 实际 %v", err)
	}
	explanation, ok := got.Result.(string)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	// 规则兜底应包含"为您推荐"。
	if !strings.Contains(explanation, "为您推荐") {
		t.Errorf("规则兜底应包含'为您推荐', 实际: %s", explanation)
	}
	// 应包含候选数 3。
	if !strings.Contains(explanation, "3") {
		t.Errorf("规则兜底应包含候选数 3, 实际: %s", explanation)
	}
	// 应包含召回源。
	if !strings.Contains(explanation, "content") || !strings.Contains(explanation, "graph") {
		t.Errorf("规则兜底应包含召回源, 实际: %s", explanation)
	}
}

// TestExplainAgent_Run_LLMEmptyOutputFallback 验证 LLM 返回空字符串走规则兜底。
func TestExplainAgent_Run_LLMEmptyOutputFallback(t *testing.T) {
	llm := &stubLLMForExplain{completeOut: "   "}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: makeExplainState("recommend_explain"),
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	explanation := got.Result.(string)
	if !strings.Contains(explanation, "为您推荐") {
		t.Errorf("LLM 空输出应走规则兜底, 实际: %s", explanation)
	}
}

// TestExplainAgent_Run_NilLLM 验证 LLM 未注入时走规则兜底。
func TestExplainAgent_Run_NilLLM(t *testing.T) {
	a := NewExplainAgent(nil, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: makeExplainState("recommend_explain"),
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	explanation := got.Result.(string)
	if !strings.Contains(explanation, "为您推荐") {
		t.Errorf("LLM 未注入应走规则兜底, 实际: %s", explanation)
	}
}

// TestExplainAgent_Run_EmptyCandidates 验证空候选时规则兜底返回"暂无推荐文章"。
func TestExplainAgent_Run_EmptyCandidates(t *testing.T) {
	a := NewExplainAgent(nil, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"explain_mode": "recommend_explain",
			"rerank_items": []domain.Candidate{},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	explanation := got.Result.(string)
	if !strings.Contains(explanation, "暂无推荐") {
		t.Errorf("空候选应返回'暂无推荐文章', 实际: %s", explanation)
	}
}

// TestExplainAgent_Run_EmptyCandidatesSearch 验证搜索模式空候选返回"未找到相关文章"。
func TestExplainAgent_Run_EmptyCandidatesSearch(t *testing.T) {
	a := NewExplainAgent(nil, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"explain_mode": "search_explain",
			"rerank_items": []domain.Candidate{},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	explanation := got.Result.(string)
	if !strings.Contains(explanation, "未找到") {
		t.Errorf("搜索空候选应返回'未找到相关文章', 实际: %s", explanation)
	}
}

// TestExplainAgent_Run_DefaultMode 验证无 explain_mode 时默认为 recommend_explain。
func TestExplainAgent_Run_DefaultMode(t *testing.T) {
	llm := &stubLLMForExplain{completeOut: "推荐解释"}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	_, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": []domain.Candidate{{ArticleID: "a1"}},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// prompt 应包含"推荐"（默认模式）。
	if !strings.Contains(llm.lastPrompt, "推荐") {
		t.Errorf("默认模式 prompt 应包含'推荐', 实际: %s", llm.lastPrompt)
	}
}

// TestExplainAgent_Run_GraphKnowledgeMap 验证 graph_knowledge 为 map 形态时正常工作。
func TestExplainAgent_Run_GraphKnowledgeMap(t *testing.T) {
	llm := &stubLLMForExplain{completeOut: "解释"}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	_, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": []domain.Candidate{{ArticleID: "a1"}},
			"graph_knowledge": map[string]any{
				"cypher":   "MATCH (e) RETURN e",
				"articles": []any{"a1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// prompt 应包含图谱知识（1 篇文章）。
	if !strings.Contains(llm.lastPrompt, "图谱") {
		t.Errorf("prompt 应包含图谱知识, 实际: %s", llm.lastPrompt)
	}
}

// TestExplainAgent_Run_IntentDomainType 验证 intent 为 domain.Intent 形态时正常工作。
func TestExplainAgent_Run_IntentDomainType(t *testing.T) {
	llm := &stubLLMForExplain{completeOut: "解释"}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	_, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": []domain.Candidate{{ArticleID: "a1"}},
			"intent": domain.Intent{
				Label:      "comparative",
				Complexity: "complex",
				TimeIntent: "today",
			},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// prompt 应包含意图 label。
	if !strings.Contains(llm.lastPrompt, "comparative") {
		t.Errorf("prompt 应包含意图 label, 实际: %s", llm.lastPrompt)
	}
}

// TestExplainAgent_Run_RecallPlanMap 验证 recall_plan 为 map 形态时正常工作。
func TestExplainAgent_Run_RecallPlanMap(t *testing.T) {
	llm := &stubLLMForExplain{completeOut: "解释"}
	a := NewExplainAgent(llm, nil, domain.AgentOptions{})

	_, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{
			"rerank_items": []domain.Candidate{{ArticleID: "a1"}},
			"recall_plan": map[string]any{
				"sources": []any{"content", "graph"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	// prompt 应包含召回源。
	if !strings.Contains(llm.lastPrompt, "content") {
		t.Errorf("prompt 应包含召回源 content, 实际: %s", llm.lastPrompt)
	}
}

// TestFallbackExplanation 验证规则兜底模板。
func TestFallbackExplanation(t *testing.T) {
	items := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	sources := []string{"content", "graph"}
	scores := map[string]float64{"a1": 0.8, "a2": 0.6}

	// 推荐模式。
	rec := fallbackExplanation("recommend_explain", items, sources, avgScore(scores))
	if !strings.Contains(rec, "为您推荐 2") {
		t.Errorf("推荐兜底应包含'为您推荐 2', 实际: %s", rec)
	}
	if !strings.Contains(rec, "content") {
		t.Errorf("推荐兜底应包含召回源, 实际: %s", rec)
	}
	if !strings.Contains(rec, "0.70") {
		t.Errorf("推荐兜底应包含平均质量分 0.70, 实际: %s", rec)
	}

	// 搜索模式。
	sch := fallbackExplanation("search_explain", items, sources, avgScore(scores))
	if !strings.Contains(sch, "为您找到 2") {
		t.Errorf("搜索兜底应包含'为您找到 2', 实际: %s", sch)
	}

	// 空候选推荐。
	emptyRec := fallbackExplanation("recommend_explain", nil, sources, 0)
	if !strings.Contains(emptyRec, "暂无推荐") {
		t.Errorf("空候选推荐应包含'暂无推荐', 实际: %s", emptyRec)
	}

	// 空候选搜索。
	emptySch := fallbackExplanation("search_explain", nil, sources, 0)
	if !strings.Contains(emptySch, "未找到") {
		t.Errorf("空候选搜索应包含'未找到', 实际: %s", emptySch)
	}
}

// TestExtractExplainMode 验证 explain_mode 提取。
func TestExtractExplainMode(t *testing.T) {
	// 显式 recommend_explain。
	if m := extractExplainMode(domain.AgentInput{State: map[string]any{"explain_mode": "recommend_explain"}}); m != "recommend_explain" {
		t.Errorf("显式 recommend_explain 失败: %s", m)
	}
	// 显式 search_explain。
	if m := extractExplainMode(domain.AgentInput{State: map[string]any{"explain_mode": "search_explain"}}); m != "search_explain" {
		t.Errorf("显式 search_explain 失败: %s", m)
	}
	// 缺省 → recommend_explain。
	if m := extractExplainMode(domain.AgentInput{State: map[string]any{}}); m != "recommend_explain" {
		t.Errorf("缺省应返回 recommend_explain, 实际: %s", m)
	}
	// State 为 nil → recommend_explain。
	if m := extractExplainMode(domain.AgentInput{}); m != "recommend_explain" {
		t.Errorf("nil State 应返回 recommend_explain, 实际: %s", m)
	}
}

// TestExtractQualityScores 验证 quality_scores 提取支持多种形态。
func TestExtractQualityScores(t *testing.T) {
	// 形态 1：map[string]float64
	s1 := extractQualityScores(domain.AgentInput{State: map[string]any{
		"quality_scores": map[string]float64{"a1": 0.9},
	}})
	if s1["a1"] != 0.9 {
		t.Errorf("形态 1 提取失败: %v", s1)
	}

	// 形态 2：map[string]any（JSON 反序列化场景）
	s2 := extractQualityScores(domain.AgentInput{State: map[string]any{
		"quality_scores": map[string]any{"a1": 0.9, "a2": 0.8},
	}})
	if s2["a1"] != 0.9 || s2["a2"] != 0.8 {
		t.Errorf("形态 2 提取失败: %v", s2)
	}

	// 形态 3：无 quality_scores → nil
	s3 := extractQualityScores(domain.AgentInput{State: map[string]any{}})
	if s3 != nil {
		t.Errorf("形态 3 应返回 nil, 实际 %v", s3)
	}
}

// TestAvgScore 验证平均分计算。
func TestAvgScore(t *testing.T) {
	if s := avgScore(map[string]float64{"a1": 0.9, "a2": 0.7}); s < 0.79 || s > 0.81 {
		t.Errorf("avgScore = %v, 期望 0.8", s)
	}
	if s := avgScore(nil); s != 0 {
		t.Errorf("avgScore(nil) = %v, 期望 0", s)
	}
	if s := avgScore(map[string]float64{}); s != 0 {
		t.Errorf("avgScore(空) = %v, 期望 0", s)
	}
}

// TestBuildExplainPrompt 验证 prompt 构造包含所有上下文。
func TestBuildExplainPrompt(t *testing.T) {
	items := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	scores := map[string]float64{"a1": 0.9, "a2": 0.7}
	gk := domain.GraphKnowledge{
		Entities: []domain.Entity{{Name: "AI", Type: "Topic"}},
		Articles: []string{"a1"},
		Cypher:   "MATCH (e) RETURN e",
	}
	sources := []string{"content", "graph"}

	prompt := buildExplainPrompt("recommend_explain", items, scores, gk, "informational", sources, 0.8)

	// 应包含候选数。
	if !strings.Contains(prompt, "2") {
		t.Errorf("prompt 应包含候选数 2, 实际: %s", prompt)
	}
	// 应包含文章 ID。
	if !strings.Contains(prompt, "a1") {
		t.Errorf("prompt 应包含文章 ID a1, 实际: %s", prompt)
	}
	// 应包含意图。
	if !strings.Contains(prompt, "informational") {
		t.Errorf("prompt 应包含意图, 实际: %s", prompt)
	}
	// 应包含图谱知识。
	if !strings.Contains(prompt, "图谱") {
		t.Errorf("prompt 应包含图谱知识, 实际: %s", prompt)
	}
	// 应包含召回源。
	if !strings.Contains(prompt, "content") {
		t.Errorf("prompt 应包含召回源, 实际: %s", prompt)
	}
	// 应包含平均质量分。
	if !strings.Contains(prompt, "0.80") {
		t.Errorf("prompt 应包含平均质量分 0.80, 实际: %s", prompt)
	}
}

// TestExtractIntentStr 验证意图提取支持多种形态。
func TestExtractIntentStr(t *testing.T) {
	// 形态 1：agent.Intent
	s1 := extractIntentStr(domain.AgentInput{State: map[string]any{
		"intent": Intent{Label: "informational", Complexity: 0.5, TimeIntent: "today"},
	}})
	if !strings.Contains(s1, "informational") || !strings.Contains(s1, "today") {
		t.Errorf("形态 1 提取失败: %s", s1)
	}

	// 形态 2：domain.Intent
	s2 := extractIntentStr(domain.AgentInput{State: map[string]any{
		"intent": domain.Intent{Label: "comparative", Complexity: "complex"},
	}})
	if !strings.Contains(s2, "comparative") {
		t.Errorf("形态 2 提取失败: %s", s2)
	}

	// 形态 3：string
	s3 := extractIntentStr(domain.AgentInput{State: map[string]any{
		"intent": "raw query intent",
	}})
	if s3 != "raw query intent" {
		t.Errorf("形态 3 提取失败: %s", s3)
	}

	// 形态 4：map[string]any
	s4 := extractIntentStr(domain.AgentInput{State: map[string]any{
		"intent": map[string]any{"label": "transactional"},
	}})
	if s4 != "transactional" {
		t.Errorf("形态 4 提取失败: %s", s4)
	}

	// 形态 5：无 intent → 空字符串
	s5 := extractIntentStr(domain.AgentInput{State: map[string]any{}})
	if s5 != "" {
		t.Errorf("形态 5 应返回空, 实际: %s", s5)
	}
}

// TestExtractRecallSources 验证召回源提取支持多种形态。
func TestExtractRecallSources(t *testing.T) {
	// 形态 1：agent.RecallPlan
	s1 := extractRecallSources(domain.AgentInput{State: map[string]any{
		"recall_plan": RecallPlan{Sources: []RecallSource{SourceContent, SourceGraph}},
	}})
	if len(s1) != 2 || s1[0] != "content" || s1[1] != "graph" {
		t.Errorf("形态 1 提取失败: %v", s1)
	}

	// 形态 2：map[string]any
	s2 := extractRecallSources(domain.AgentInput{State: map[string]any{
		"recall_plan": map[string]any{
			"sources": []any{"content", "cf"},
		},
	}})
	if len(s2) != 2 || s2[0] != "content" || s2[1] != "cf" {
		t.Errorf("形态 2 提取失败: %v", s2)
	}

	// 形态 3：无 recall_plan → nil
	s3 := extractRecallSources(domain.AgentInput{State: map[string]any{}})
	if s3 != nil {
		t.Errorf("形态 3 应返回 nil, 实际 %v", s3)
	}
}
