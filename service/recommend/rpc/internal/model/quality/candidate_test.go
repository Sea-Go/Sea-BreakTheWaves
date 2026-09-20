// ============================================================================
// 该文件测试 internal/quality/candidate.go 的 CandidateAgent。
// 用 mock LLMClient 返回固定 JSON，验证：
//   - Evaluate：prompt 构造、结构化输出解析、ArticleID/Grade 填充
//   - BatchEvaluate：批量评判顺序与错误传播
//   - LLMClient 接口契约
// ============================================================================

package quality

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// mockScriptedLLM mock LLM 客户端，按预设响应返回。
type mockScriptedLLM struct {
	// responses 按调用顺序返回的预设响应（每次调用消耗一个）。
	responses []string
	// err 预设错误（非 nil 时直接返回，跳过 responses）。
	err error
	// calls 记录每次调用的 prompt 与 opts，便于断言。
	calls []scriptedCall
}

type scriptedCall struct {
	prompt string
	opts   LLMOptions
}

func (m *mockScriptedLLM) Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error) {
	m.calls = append(m.calls, scriptedCall{prompt: prompt, opts: opts})
	if m.err != nil {
		return "", m.err
	}
	if len(m.responses) == 0 {
		return "", errors.New("mock LLMClient 响应已耗尽")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

// TestCandidateAgent_Evaluate 验证单文章评判：解析 LLM 结构化输出并填充字段。
func TestCandidateAgent_Evaluate(t *testing.T) {
	// LLM 返回 ArticleQuality JSON（含 6 维 + overall）。
	llmJSON := `{
		"authority": 0.9,
		"depth": 0.8,
		"freshness": 0.7,
		"completeness": 0.6,
		"readability": 0.5,
		"citation": 0.4,
		"overall": 0.85
	}`
	mock := &mockScriptedLLM{responses: []string{llmJSON}}
	agent := NewCandidateAgent(mock, nil)

	article := ArticleInput{
		ID:          "art-1",
		Title:       "深度长文",
		Content:     "正文内容…",
		AuthorID:    "author-1",
		Tags:        []string{"科技", "AI"},
		PublishedAt: time.Now(),
	}
	q, err := agent.Evaluate(context.Background(), article)
	if err != nil {
		t.Fatalf("Evaluate 错误: %v", err)
	}
	// 验证 ArticleID 与 Grade 填充。
	if q.ArticleID != "art-1" {
		t.Fatalf("ArticleID = %q, want art-1", q.ArticleID)
	}
	if q.Grade == "" {
		t.Fatal("Grade 为空，应基于 Overall 重算")
	}
	// Overall=0.85 → idx=int((1-0.85)*20)=3 → D
	if q.Grade != "D" {
		t.Fatalf("Grade = %q, want D (Overall=0.85)", q.Grade)
	}
	// 验证 6 维分数解析正确。
	if q.Authority != 0.9 || q.Depth != 0.8 || q.Freshness != 0.7 {
		t.Fatalf("6 维分数解析错误: %+v", q)
	}
	if q.Completeness != 0.6 || q.Readability != 0.5 || q.Citation != 0.4 {
		t.Fatalf("6 维分数解析错误: %+v", q)
	}
	if q.Overall != 0.85 {
		t.Fatalf("Overall = %v, want 0.85", q.Overall)
	}

	// 验证 LLM 调用参数：结构化输出 schema 已注入，temperature=0。
	if len(mock.calls) != 1 {
		t.Fatalf("LLM 调用次数 = %d, want 1", len(mock.calls))
	}
	call := mock.calls[0]
	if call.opts.StructuredOutputJSONSchema == "" {
		t.Fatal("StructuredOutputJSONSchema 未注入")
	}
	if call.opts.Temperature != 0.0 {
		t.Fatalf("Temperature = %v, want 0.0", call.opts.Temperature)
	}
	// prompt 含文章 ID 与 rubric 评分标准。
	if !strings.Contains(call.prompt, "art-1") {
		t.Fatal("prompt 未包含文章 ID")
	}
	if !strings.Contains(call.prompt, "authority") {
		t.Fatal("prompt 未包含 rubric 维度 authority")
	}
}

// TestCandidateAgent_Evaluate_CodeFence 验证 LLM 输出包裹 markdown 代码块时仍可解析。
func TestCandidateAgent_Evaluate_CodeFence(t *testing.T) {
	llmJSON := "```json\n" +
		`{"authority": 0.5, "depth": 0.5, "freshness": 0.5, "completeness": 0.5, "readability": 0.5, "citation": 0.5, "overall": 0.5}` +
		"\n```"
	mock := &mockScriptedLLM{responses: []string{llmJSON}}
	agent := NewCandidateAgent(mock, DefaultRubrics())

	q, err := agent.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err != nil {
		t.Fatalf("Evaluate 代码块包裹输出错误: %v", err)
	}
	if q.Overall != 0.5 {
		t.Fatalf("Overall = %v, want 0.5", q.Overall)
	}
	// Overall=0.5 → K
	if q.Grade != "K" {
		t.Fatalf("Grade = %q, want K", q.Grade)
	}
}

// TestCandidateAgent_Evaluate_LLMError 验证 LLM 调用失败时返回错误。
func TestCandidateAgent_Evaluate_LLMError(t *testing.T) {
	mock := &mockScriptedLLM{err: errors.New("LLM 限流")}
	agent := NewCandidateAgent(mock, nil)
	_, err := agent.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err == nil {
		t.Fatal("LLM 错误时 Evaluate 应返回错误")
	}
	if !strings.Contains(err.Error(), "候选 Agent LLM 调用失败") {
		t.Fatalf("错误信息不含前缀: %v", err)
	}
}

// TestCandidateAgent_Evaluate_InvalidJSON 验证 LLM 返回非法 JSON 时返回解析错误。
func TestCandidateAgent_Evaluate_InvalidJSON(t *testing.T) {
	mock := &mockScriptedLLM{responses: []string{"not a json"}}
	agent := NewCandidateAgent(mock, nil)
	_, err := agent.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	if !strings.Contains(err.Error(), "解析候选 Agent 结构化输出失败") {
		t.Fatalf("错误信息不含前缀: %v", err)
	}
}

// TestCandidateAgent_BatchEvaluate 验证批量评判：顺序一致，结果数正确。
func TestCandidateAgent_BatchEvaluate(t *testing.T) {
	r1 := `{"authority": 0.9, "depth": 0.9, "freshness": 0.9, "completeness": 0.9, "readability": 0.9, "citation": 0.9, "overall": 0.9}`
	r2 := `{"authority": 0.1, "depth": 0.1, "freshness": 0.1, "completeness": 0.1, "readability": 0.1, "citation": 0.1, "overall": 0.1}`
	mock := &mockScriptedLLM{responses: []string{r1, r2}}
	agent := NewCandidateAgent(mock, nil)

	articles := []ArticleInput{
		{ID: "a1", Title: "高质量"},
		{ID: "a2", Title: "低质量"},
	}
	results, err := agent.BatchEvaluate(context.Background(), articles)
	if err != nil {
		t.Fatalf("BatchEvaluate 错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("结果数 = %d, want 2", len(results))
	}
	// 顺序与输入一致。
	if results[0].ArticleID != "a1" || results[1].ArticleID != "a2" {
		t.Fatalf("结果顺序错乱: %s, %s", results[0].ArticleID, results[1].ArticleID)
	}
	// 分数与 mock 响应一致。
	if results[0].Overall != 0.9 || results[1].Overall != 0.1 {
		t.Fatalf("Overall 分数异常: %v, %v", results[0].Overall, results[1].Overall)
	}
	// a1 (Overall=0.9) → idx=int((1-0.9)*20)=2 → C；a2 (Overall=0.1) → idx=int(0.9*20)=18 → S
	if results[0].Grade != "C" {
		t.Fatalf("a1 Grade = %q, want C", results[0].Grade)
	}
	if results[1].Grade != "S" {
		t.Fatalf("a2 Grade = %q, want S", results[1].Grade)
	}
}

// TestCandidateAgent_BatchEvaluate_PartialFailure 验证批量评判中任一失败立即返回错误。
func TestCandidateAgent_BatchEvaluate_PartialFailure(t *testing.T) {
	r1 := `{"authority": 0.9, "depth": 0.9, "freshness": 0.9, "completeness": 0.9, "readability": 0.9, "citation": 0.9, "overall": 0.9}`
	// 第二个响应为非法 JSON，触发解析失败。
	mock := &mockScriptedLLM{responses: []string{r1, "invalid"}}
	agent := NewCandidateAgent(mock, nil)

	articles := []ArticleInput{{ID: "a1"}, {ID: "a2"}}
	_, err := agent.BatchEvaluate(context.Background(), articles)
	if err == nil {
		t.Fatal("批量评判含失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "批量评判文章") {
		t.Fatalf("错误信息不含前缀: %v", err)
	}
}

// TestNewCandidateAgent_NilRubrics 验证 rubrics 为 nil 时使用默认 rubric 集。
func TestNewCandidateAgent_NilRubrics(t *testing.T) {
	mock := &mockScriptedLLM{responses: []string{
		`{"authority": 0.5, "depth": 0.5, "freshness": 0.5, "completeness": 0.5, "readability": 0.5, "citation": 0.5, "overall": 0.5}`,
	}}
	agent := NewCandidateAgent(mock, nil)
	if agent.rubrics == nil {
		t.Fatal("nil 入参应回退到 DefaultRubrics")
	}
	if len(agent.rubrics.Rubrics) != 7 {
		t.Fatalf("默认 rubric 数量 = %d, want 7", len(agent.rubrics.Rubrics))
	}
}

// TestCandidateAgent_NilAgent 验证 nil Agent 与 nil LLM 的安全性。
func TestCandidateAgent_NilAgent(t *testing.T) {
	var agent *CandidateAgent
	_, err := agent.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err == nil {
		t.Fatal("nil CandidateAgent.Evaluate 应返回错误")
	}
	// LLM 为 nil。
	agent = &CandidateAgent{}
	_, err = agent.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err == nil {
		t.Fatal("LLM 为 nil 时 Evaluate 应返回错误")
	}
}

// 编译期断言：CandidateAgent 实现 LLMClient 调用契约（不直接实现 domain.QualityJudger，
// 入参为 ArticleInput 而非 QualityRequest）。
var _ LLMClient = (*mockScriptedLLM)(nil)
