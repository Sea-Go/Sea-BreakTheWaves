package recall

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
	"sea/internal/graph"
)

// ============================================================================
// 该文件使用 mock GraphQuerier 测试 GraphRecaller 的召回流程，覆盖：
//   - 默认路径（无 Intent）：调用 RecallByGraph，返回 []Candidate
//   - 实体路径（Intent 含实体）：调用 QueryByCypher + TemplateEntityToArticle，
//     返回 []GraphNode 后转换为 []Candidate
//   - topK 截断
//   - 错误传播
//   - Name() 返回 "graph"
//   - 编译期断言 *GraphRecaller 实现 domain.Recaller
// ============================================================================

// 编译期断言：*GraphRecaller 实现 domain.Recaller interface。
var _ domain.Recaller = (*GraphRecaller)(nil)

// mockGraphQuerier 模拟 domain.GraphQuerier，记录调用参数并返回固定结果。
type mockGraphQuerier struct {
	recallByGraphFn func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error)
	queryByCypherFn func(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error)
	entityLinkFn   func(ctx context.Context, query string) ([]domain.Entity, error)
	imageSearchFn  func(ctx context.Context, imageURL string) ([]domain.Candidate, error)

	// 调用记录
	lastCypher  string
	lastParams  map[string]any
	lastPattern string
	cypherCalls  int
	recallCalls  int
}

func (m *mockGraphQuerier) QueryByCypher(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
	m.cypherCalls++
	m.lastCypher = cypher
	m.lastParams = params
	if m.queryByCypherFn != nil {
		return m.queryByCypherFn(ctx, cypher, params)
	}
	return nil, nil
}

func (m *mockGraphQuerier) RecallByGraph(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
	m.recallCalls++
	m.lastPattern = req.Pattern
	if m.recallByGraphFn != nil {
		return m.recallByGraphFn(ctx, req)
	}
	return nil, nil
}

func (m *mockGraphQuerier) EntityLink(ctx context.Context, query string) ([]domain.Entity, error) {
	if m.entityLinkFn != nil {
		return m.entityLinkFn(ctx, query)
	}
	return nil, nil
}

func (m *mockGraphQuerier) ImageSearch(ctx context.Context, imageURL string) ([]domain.Candidate, error) {
	if m.imageSearchFn != nil {
		return m.imageSearchFn(ctx, imageURL)
	}
	return nil, nil
}

// TestGraphRecaller_Name 验证召回器名称。
func TestGraphRecaller_Name(t *testing.T) {
	r := NewGraphRecaller(&mockGraphQuerier{}, 10)
	if got := r.Name(); got != "graph" {
		t.Errorf("Name() = %q, 期望 graph", got)
	}
}

// TestGraphRecaller_Recall_DefaultPath 验证无 Intent 时走默认图谱召回路径。
func TestGraphRecaller_Recall_DefaultPath(t *testing.T) {
	mock := &mockGraphQuerier{
		recallByGraphFn: func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
			if req.UserKey.UserID != "u1" {
				t.Errorf("UserID = %q, 期望 u1", req.UserKey.UserID)
			}
			if req.Pattern != "user_like_similar" {
				t.Errorf("Pattern = %q, 期望 user_like_similar", req.Pattern)
			}
			if req.TopK != 5 {
				t.Errorf("TopK = %d, 期望 5", req.TopK)
			}
			return []domain.Candidate{
				{ArticleID: "a1", Score: 0.9, Source: "graph"},
				{ArticleID: "a2", Score: 0.8, Source: "graph"},
			}, nil
		},
	}
	r := NewGraphRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    5,
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "graph" {
		t.Errorf("Source = %q, 期望 graph", res.Source)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(res.Candidates))
	}
	if res.Candidates[0].ArticleID != "a1" {
		t.Errorf("首个候选 ArticleID = %q, 期望 a1", res.Candidates[0].ArticleID)
	}
	if mock.recallCalls != 1 {
		t.Errorf("RecallByGraph 调用次数 = %d, 期望 1", mock.recallCalls)
	}
	if mock.cypherCalls != 0 {
		t.Errorf("默认路径不应调用 QueryByCypher, 实际调用 %d 次", mock.cypherCalls)
	}
}

// TestGraphRecaller_Recall_EntityPath 验证 Intent 含实体时走实体→文章路径。
func TestGraphRecaller_Recall_EntityPath(t *testing.T) {
	mock := &mockGraphQuerier{
		queryByCypherFn: func(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
			if cypher != graph.TemplateEntityToArticle {
				t.Errorf("Cypher 不是 TemplateEntityToArticle")
			}
			if name, _ := params["entity_name"].(string); name != "AI" {
				t.Errorf("entity_name = %q, 期望 AI", name)
			}
			if topk, _ := params["topk"].(int); topk != 5 {
				t.Errorf("topk = %d, 期望 5", topk)
			}
			return []domain.GraphNode{
				{ID: "e1", Type: "Article", Props: map[string]any{"id": "art-1", "score": 0.85}},
				{ID: "e2", Type: "Article", Props: map[string]any{"id": "art-2", "score": 0.75}},
			}, nil
		},
	}
	r := NewGraphRecaller(mock, 10)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    5,
		Intent: &domain.Intent{
			Label: "informational",
			Entities: []domain.Entity{
				{Name: "AI", Type: "Topic"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if res.Source != "graph" {
		t.Errorf("Source = %q, 期望 graph", res.Source)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("候选数 = %d, 期望 2", len(res.Candidates))
	}
	// 验证 GraphNode → Candidate 转换
	if res.Candidates[0].ArticleID != "art-1" {
		t.Errorf("首个候选 ArticleID = %q, 期望 art-1", res.Candidates[0].ArticleID)
	}
	if res.Candidates[0].Score != 0.85 {
		t.Errorf("首个候选 Score = %v, 期望 0.85", res.Candidates[0].Score)
	}
	if res.Candidates[0].Source != "graph" {
		t.Errorf("首个候选 Source = %q, 期望 graph", res.Candidates[0].Source)
	}
	if mock.cypherCalls != 1 {
		t.Errorf("QueryByCypher 调用次数 = %d, 期望 1", mock.cypherCalls)
	}
	if mock.recallCalls != 0 {
		t.Errorf("实体路径不应调用 RecallByGraph, 实际调用 %d 次", mock.recallCalls)
	}
}

// TestGraphRecaller_Recall_EntityPathMalicious 验证实体名含恶意字符时参数化安全。
func TestGraphRecaller_Recall_EntityPathMalicious(t *testing.T) {
	var capturedParams map[string]any
	var capturedCypher string
	mock := &mockGraphQuerier{
		queryByCypherFn: func(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
			capturedCypher = cypher
			capturedParams = params
			return nil, nil
		},
	}
	r := NewGraphRecaller(mock, 10)
	malicious := `'; DROP DATABASE; --`
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Intent: &domain.Intent{
			Entities: []domain.Entity{{Name: malicious, Type: "Topic"}},
		},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	// Cypher 模板字符串不包含恶意输入
	if capturedCypher != graph.TemplateEntityToArticle {
		t.Errorf("Cypher 模板被篡改")
	}
	// 恶意输入通过 params 原样传递
	if got, _ := capturedParams["entity_name"].(string); got != malicious {
		t.Errorf("entity_name 被篡改: 期望 %q, 实际 %q", malicious, got)
	}
}

// TestGraphRecaller_Recall_TopKCap 验证召回结果超过 topK 时被截断。
func TestGraphRecaller_Recall_TopKCap(t *testing.T) {
	mock := &mockGraphQuerier{
		recallByGraphFn: func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
			cands := make([]domain.Candidate, 0, 8)
			for i := 0; i < 8; i++ {
				cands = append(cands, domain.Candidate{ArticleID: "a", Source: "graph"})
			}
			return cands, nil
		},
	}
	r := NewGraphRecaller(mock, 3)
	res, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Errorf("候选数 = %d, 期望被截断到 3", len(res.Candidates))
	}
}

// TestGraphRecaller_Recall_DefaultTopKFromRecaller 验证 req.TopK<=0 时使用 recaller 的 topK。
func TestGraphRecaller_Recall_DefaultTopKFromRecaller(t *testing.T) {
	var capturedTopK int
	mock := &mockGraphQuerier{
		recallByGraphFn: func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
			capturedTopK = req.TopK
			return nil, nil
		},
	}
	r := NewGraphRecaller(mock, 7)
	_, _ = r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		// TopK 为 0，应使用 recaller 默认 7
	})
	if capturedTopK != 7 {
		t.Errorf("TopK = %d, 期望回退到 recaller 默认 7", capturedTopK)
	}
}

// TestGraphRecaller_Recall_ErrorPropagation 验证 GraphQuerier 错误向上传播。
func TestGraphRecaller_Recall_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("graph backend down")
	mock := &mockGraphQuerier{
		recallByGraphFn: func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
			return nil, sentinel
		},
	}
	r := NewGraphRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}

// TestGraphRecaller_Recall_EntityPathError 验证实体路径错误传播。
func TestGraphRecaller_Recall_EntityPathError(t *testing.T) {
	sentinel := errors.New("cypher exec failed")
	mock := &mockGraphQuerier{
		queryByCypherFn: func(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
			return nil, sentinel
		},
	}
	r := NewGraphRecaller(mock, 10)
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Intent: &domain.Intent{
			Entities: []domain.Entity{{Name: "AI"}},
		},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("期望错误 %v 传播, 实际 %v", sentinel, err)
	}
}

// TestGraphRecaller_Recall_EmptyIntentEntities 验证 Intent 无实体时走默认路径。
func TestGraphRecaller_Recall_EmptyIntentEntities(t *testing.T) {
	mock := &mockGraphQuerier{
		recallByGraphFn: func(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
			return []domain.Candidate{{ArticleID: "a1", Source: "graph"}}, nil
		},
	}
	r := NewGraphRecaller(mock, 10)
	// Intent 非 nil 但 Entities 为空
	_, err := r.Recall(context.Background(), domain.RecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		Intent:  &domain.Intent{Label: "informational"},
	})
	if err != nil {
		t.Fatalf("Recall 返回错误: %v", err)
	}
	if mock.recallCalls != 1 {
		t.Errorf("应走默认路径 RecallByGraph, 调用 %d 次", mock.recallCalls)
	}
	if mock.cypherCalls != 0 {
		t.Errorf("不应调用 QueryByCypher")
	}
}
