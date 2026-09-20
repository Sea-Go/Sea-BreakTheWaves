package agent

import (
	"context"
	"errors"
	"testing"

	"sea/service/search/rpc/internal/domain"
	"sea/service/search/rpc/internal/trpcagent/knowledge/engine"
)

// ============================================================================
// 该文件测试 internal/agent/search_agent.go 的 SearchAgent 与 DefaultExecutor。
// 覆盖：正常/搜索失败仍返回部分结果/图谱失败仍返回部分结果/自定义 Executor/
// nil GraphSearcher/DefaultExecutor 直接调用。
// ============================================================================

// stubSearcher 用于测试的 domain.Searcher stub。
type stubSearcher struct {
	result domain.SearchResult
	err    error
	calls  int
}

func (s *stubSearcher) Search(_ context.Context, _ domain.SearchQuery) (domain.SearchResult, error) {
	s.calls++
	if s.err != nil {
		return domain.SearchResult{}, s.err
	}
	return s.result, nil
}

// stubExecutor 用于测试的 AgentExecutor stub。
type stubExecutor struct {
	output AgentOutput
	err    error
	calls  int
}

func (e *stubExecutor) Run(_ context.Context, _ AgentInput) (AgentOutput, error) {
	e.calls++
	if e.err != nil {
		return AgentOutput{}, e.err
	}
	return e.output, nil
}

// stubGraphClientForAgent 测试用 search.GraphClient stub（agent 包内）。
type stubGraphClientForAgent struct {
	entities []domain.Entity
	nodes    []domain.GraphNode
	err      error
}

func (s *stubGraphClientForAgent) EntityLink(_ context.Context, _ string) ([]domain.Entity, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.entities, nil
}

func (s *stubGraphClientForAgent) QueryByCypher(_ context.Context, _ string, _ map[string]any) ([]domain.GraphNode, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.nodes, nil
}

// newTestGraphSearcher 构造测试用 *search.GraphSearcher（带 stub GraphClient）。
func newTestGraphSearcher(entities []domain.Entity, nodes []domain.GraphNode, err error) *search.GraphSearcher {
	return search.NewGraphSearcher(&stubGraphClientForAgent{entities: entities, nodes: nodes, err: err})
}

// newTestHistoryService 构造测试用 *search.HistoryService（内存 store）。
func newTestHistoryService() *search.HistoryService {
	return search.NewHistoryService(search.NewMemoryHistoryStore())
}

// TestSearchAgent_Execute_Normal 验证正常流程：搜索 + 图谱 + 历史记录。
func TestSearchAgent_Execute_Normal(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits: []domain.SearchHit{
			{ArticleID: "a1", Score: 0.9, Source: "vector"},
			{ArticleID: "a2", Score: 0.8, Source: "bm25"},
		},
	}}
	gs := newTestGraphSearcher(
		[]domain.Entity{{Name: "AI", Type: "Tag"}},
		[]domain.GraphNode{{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		nil,
	)
	hs := newTestHistoryService()
	sa := NewSearchAgent(searcher, gs, hs, nil)

	got, err := sa.Execute(context.Background(), AgentInput{
		Query:   "AI",
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    10,
	})
	if err != nil {
		t.Fatalf("Execute 错误: %v", err)
	}
	if len(got.Hits) != 2 {
		t.Errorf("期望 2 个 hits, 实际 %d", len(got.Hits))
	}
	if got.Graph == nil {
		t.Errorf("Graph 不应为 nil")
	} else if len(got.Graph.Entities) != 1 {
		t.Errorf("期望 1 个实体, 实际 %d", len(got.Graph.Entities))
	}
	if len(got.Trace) != 3 {
		t.Errorf("期望 3 步 trace, 实际 %d", len(got.Trace))
	}
	// 验证历史已记录。
	hist, _ := hs.List(context.Background(), "u1", 10)
	if len(hist) != 1 {
		t.Errorf("期望历史记录 1 条, 实际 %d", len(hist))
	}
}

// TestSearchAgent_Execute_SearchFail 验证搜索失败仍返回图谱知识与历史。
func TestSearchAgent_Execute_SearchFail(t *testing.T) {
	searcher := &stubSearcher{err: errors.New("search down")}
	gs := newTestGraphSearcher(
		[]domain.Entity{{Name: "AI", Type: "Tag"}},
		[]domain.GraphNode{{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		nil,
	)
	hs := newTestHistoryService()
	sa := NewSearchAgent(searcher, gs, hs, nil)

	got, err := sa.Execute(context.Background(), AgentInput{
		Query:   "AI",
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    10,
	})
	if err != nil {
		t.Fatalf("搜索失败不应返回错误, 实际 %v", err)
	}
	if len(got.Hits) != 0 {
		t.Errorf("搜索失败应无 hits, 实际 %d", len(got.Hits))
	}
	// 图谱知识仍应存在。
	if got.Graph == nil {
		t.Errorf("搜索失败仍应返回图谱知识")
	}
	// 历史仍应记录。
	hist, _ := hs.List(context.Background(), "u1", 10)
	if len(hist) != 1 {
		t.Errorf("搜索失败仍应记录历史, 期望 1 条, 实际 %d", len(hist))
	}
}

// TestSearchAgent_Execute_GraphFail 验证图谱失败仍返回 hits。
func TestSearchAgent_Execute_GraphFail(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits: []domain.SearchHit{{ArticleID: "a1", Score: 0.9}},
	}}
	gs := newTestGraphSearcher(nil, nil, errors.New("graph down"))
	hs := newTestHistoryService()
	sa := NewSearchAgent(searcher, gs, hs, nil)

	got, err := sa.Execute(context.Background(), AgentInput{
		Query:   "AI",
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    10,
	})
	if err != nil {
		t.Fatalf("图谱失败不应返回错误, 实际 %v", err)
	}
	// hits 仍应存在。
	if len(got.Hits) != 1 {
		t.Errorf("图谱失败仍应返回 hits, 期望 1, 实际 %d", len(got.Hits))
	}
	// 图谱为 nil。
	if got.Graph != nil {
		t.Errorf("图谱失败时 Graph 应为 nil")
	}
}

// TestSearchAgent_Execute_CustomExecutor 验证注入自定义 Executor 时走 Executor 而非 Searcher。
func TestSearchAgent_Execute_CustomExecutor(t *testing.T) {
	searcher := &stubSearcher{} // 不应被调用
	exec := &stubExecutor{output: AgentOutput{
		Hits:   []domain.SearchHit{{ArticleID: "x1", Score: 1.0}},
		Answer: "custom",
	}}
	gs := newTestGraphSearcher(nil, nil, nil) // 空 entity → 空 graph
	hs := newTestHistoryService()
	sa := NewSearchAgent(searcher, gs, hs, exec)

	got, err := sa.Execute(context.Background(), AgentInput{
		Query: "q",
		TopK:  10,
	})
	if err != nil {
		t.Fatalf("Execute 错误: %v", err)
	}
	if exec.calls != 1 {
		t.Errorf("期望 Executor 调用 1 次, 实际 %d", exec.calls)
	}
	if searcher.calls != 0 {
		t.Errorf("自定义 Executor 时不应调 Searcher, 实际 %d 次", searcher.calls)
	}
	if got.Answer != "custom" {
		t.Errorf("Answer = %q, 期望 custom", got.Answer)
	}
	if len(got.Hits) != 1 {
		t.Errorf("期望 1 个 hit, 实际 %d", len(got.Hits))
	}
}

// TestSearchAgent_Execute_NilGraphSearcher 验证 GraphSearcher 为 nil 时跳过图谱步骤。
func TestSearchAgent_Execute_NilGraphSearcher(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits: []domain.SearchHit{{ArticleID: "a1"}},
	}}
	sa := NewSearchAgent(searcher, nil, nil, nil)
	got, err := sa.Execute(context.Background(), AgentInput{Query: "q", TopK: 10})
	if err != nil {
		t.Fatalf("Execute 错误: %v", err)
	}
	if got.Graph != nil {
		t.Errorf("GraphSearcher nil 时 Graph 应为 nil")
	}
	if len(got.Hits) != 1 {
		t.Errorf("期望 1 个 hit, 实际 %d", len(got.Hits))
	}
}

// TestSearchAgent_Execute_EmptyQuerySkipsGraph 验证空 query 时跳过图谱步骤。
func TestSearchAgent_Execute_EmptyQuerySkipsGraph(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits: []domain.SearchHit{{ArticleID: "a1"}},
	}}
	gs := newTestGraphSearcher(
		[]domain.Entity{{Name: "AI", Type: "Tag"}},
		nil,
		nil,
	)
	sa := NewSearchAgent(searcher, gs, nil, nil)
	got, err := sa.Execute(context.Background(), AgentInput{Query: "", TopK: 10})
	if err != nil {
		t.Fatalf("Execute 错误: %v", err)
	}
	if got.Graph != nil {
		t.Errorf("空 query 时 Graph 应为 nil（跳过图谱步骤）")
	}
}

// TestSearchAgent_Execute_NilHistoryService 验证 HistoryService 为 nil 时跳过历史步骤。
func TestSearchAgent_Execute_NilHistoryService(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits: []domain.SearchHit{{ArticleID: "a1"}},
	}}
	sa := NewSearchAgent(searcher, nil, nil, nil)
	got, err := sa.Execute(context.Background(), AgentInput{Query: "q", TopK: 10})
	if err != nil {
		t.Fatalf("Execute 错误: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Errorf("期望 1 个 hit, 实际 %d", len(got.Hits))
	}
}

// TestDefaultExecutor_Run 验证 DefaultExecutor 正常调用 Searcher.Search。
func TestDefaultExecutor_Run(t *testing.T) {
	searcher := &stubSearcher{result: domain.SearchResult{
		Hits:           []domain.SearchHit{{ArticleID: "a1", Score: 0.9}},
		RewrittenQuery: "AI rewritten",
	}}
	exec := &DefaultExecutor{Searcher: searcher}
	got, err := exec.Run(context.Background(), AgentInput{Query: "AI", TopK: 10})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Errorf("期望 1 个 hit, 实际 %d", len(got.Hits))
	}
	if len(got.Trace) != 1 {
		t.Errorf("期望 1 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0]["rewritten_query"] != "AI rewritten" {
		t.Errorf("trace rewritten_query = %v, 期望 AI rewritten", got.Trace[0]["rewritten_query"])
	}
}

// TestDefaultExecutor_Run_SearchError 验证 DefaultExecutor 搜索错误透传。
func TestDefaultExecutor_Run_SearchError(t *testing.T) {
	searcher := &stubSearcher{err: errors.New("search fail")}
	exec := &DefaultExecutor{Searcher: searcher}
	_, err := exec.Run(context.Background(), AgentInput{Query: "AI", TopK: 10})
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestDefaultExecutor_Run_NilSearcher 验证 DefaultExecutor 无 Searcher 时返回错误。
func TestDefaultExecutor_Run_NilSearcher(t *testing.T) {
	exec := &DefaultExecutor{}
	_, err := exec.Run(context.Background(), AgentInput{Query: "AI", TopK: 10})
	if err == nil {
		t.Fatalf("Nil searcher 应返回错误")
	}
}

// TestSearchAgent_Execute_ExecutorFailStillReturnsGraph 验证 Executor 失败但图谱成功时返回图谱。
func TestSearchAgent_Execute_ExecutorFailStillReturnsGraph(t *testing.T) {
	exec := &stubExecutor{err: errors.New("executor down")}
	gs := newTestGraphSearcher(
		[]domain.Entity{{Name: "AI", Type: "Tag"}},
		[]domain.GraphNode{{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		nil,
	)
	hs := newTestHistoryService()
	sa := NewSearchAgent(nil, gs, hs, exec)

	got, err := sa.Execute(context.Background(), AgentInput{
		Query:   "AI",
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    10,
	})
	if err != nil {
		t.Fatalf("Executor 失败不应返回错误, 实际 %v", err)
	}
	if len(got.Hits) != 0 {
		t.Errorf("Executor 失败应无 hits, 实际 %d", len(got.Hits))
	}
	if got.Graph == nil {
		t.Errorf("Executor 失败仍应返回图谱知识")
	}
}
