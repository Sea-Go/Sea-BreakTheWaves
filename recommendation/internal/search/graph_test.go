package search

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// ============================================================================
// 该文件测试 internal/search/graph.go 的 GraphSearcher。
// 覆盖：四类实体识别（Author/IP/Tag/Title）+ Cypher 生成正确性 + 空结果 +
// 实体链接错误 + Cypher 错误不阻断 + 多实体聚合去重 + topK 默认值。
// ============================================================================

// stubGraphClient 用于测试的 GraphClient stub。
type stubGraphClient struct {
	entities    []domain.Entity
	entityErr   error
	cypherNodes map[string][]domain.GraphNode // cypher → nodes
	cypherErr   error
	lastCypher  string
	lastParams  map[string]any
	cypherCalls int
}

func (s *stubGraphClient) EntityLink(_ context.Context, _ string) ([]domain.Entity, error) {
	if s.entityErr != nil {
		return nil, s.entityErr
	}
	return s.entities, nil
}

func (s *stubGraphClient) QueryByCypher(_ context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
	s.cypherCalls++
	s.lastCypher = cypher
	s.lastParams = params
	if s.cypherErr != nil {
		return nil, s.cypherErr
	}
	if s.cypherNodes == nil {
		return nil, nil
	}
	return s.cypherNodes[cypher], nil
}

// TestGraphSearcher_Search_AuthorEntity 验证作者实体识别与 Cypher 生成。
func TestGraphSearcher_Search_AuthorEntity(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "张三", Type: "Author"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByAuthor: {
				{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}},
				{ID: "a2", Type: "Article", Props: map[string]any{"id": "a2"}},
			},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "张三的文章", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Errorf("期望 1 个实体, 实际 %d", len(got.Entities))
	}
	if len(got.Articles) != 2 {
		t.Errorf("期望 2 篇文章, 实际 %d", len(got.Articles))
	}
	if len(got.Authors) != 1 || got.Authors[0] != "张三" {
		t.Errorf("Authors = %v, 期望 [张三]", got.Authors)
	}
	if client.lastCypher != cypherByAuthor {
		t.Errorf("Cypher 不匹配, 期望作者模板")
	}
	if client.lastParams["author_name"] != "张三" {
		t.Errorf("参数 author_name = %v, 期望 张三", client.lastParams["author_name"])
	}
	if client.lastParams["topk"] != 10 {
		t.Errorf("参数 topk = %v, 期望 10", client.lastParams["topk"])
	}
	if got.Cypher == "" {
		t.Errorf("Cypher 调试串不应为空")
	}
}

// TestGraphSearcher_Search_IPEntity 验证 IP 实体识别与 Cypher 生成。
func TestGraphSearcher_Search_IPEntity(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "极客时间", Type: "IP"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByIP: {{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "极客时间", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.IPs) != 1 || got.IPs[0] != "极客时间" {
		t.Errorf("IPs = %v, 期望 [极客时间]", got.IPs)
	}
	if client.lastCypher != cypherByIP {
		t.Errorf("期望 IP 模板")
	}
	if client.lastParams["ip_name"] != "极客时间" {
		t.Errorf("参数 ip_name = %v, 期望 极客时间", client.lastParams["ip_name"])
	}
}

// TestGraphSearcher_Search_TagEntity 验证 Tag 实体识别与 Cypher 生成。
func TestGraphSearcher_Search_TagEntity(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "AI", Type: "Tag"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByTag: {{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "AI", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Articles) != 1 {
		t.Errorf("期望 1 篇文章, 实际 %d", len(got.Articles))
	}
	if client.lastCypher != cypherByTag {
		t.Errorf("期望 Tag 模板")
	}
	if client.lastParams["tag_name"] != "AI" {
		t.Errorf("参数 tag_name = %v, 期望 AI", client.lastParams["tag_name"])
	}
}

// TestGraphSearcher_Search_TitleEntity 验证 Title 实体识别与 Cypher 生成。
func TestGraphSearcher_Search_TitleEntity(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "深度学习", Type: "Title"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByTitle: {{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "深度学习", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Articles) != 1 {
		t.Errorf("期望 1 篇文章, 实际 %d", len(got.Articles))
	}
	if client.lastCypher != cypherByTitle {
		t.Errorf("期望 Title 模板")
	}
	if client.lastParams["title"] != "深度学习" {
		t.Errorf("参数 title = %v, 期望 深度学习", client.lastParams["title"])
	}
}

// TestGraphSearcher_Search_TopicEntity 验证 Topic 实体识别与 Cypher 生成。
func TestGraphSearcher_Search_TopicEntity(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "机器学习", Type: "Topic"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByEntity: {{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "机器学习", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Articles) != 1 {
		t.Errorf("期望 1 篇文章, 实际 %d", len(got.Articles))
	}
	if client.lastCypher != cypherByEntity {
		t.Errorf("期望 Entity/Topic 模板")
	}
	if client.lastParams["entity_name"] != "机器学习" {
		t.Errorf("参数 entity_name = %v, 期望 机器学习", client.lastParams["entity_name"])
	}
}

// TestGraphSearcher_Search_MultipleEntities 验证多实体聚合与文章去重。
func TestGraphSearcher_Search_MultipleEntities(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{
			{Name: "张三", Type: "Author"},
			{Name: "AI", Type: "Tag"},
		},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByAuthor: {
				{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}},
			},
			cypherByTag: {
				{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}}, // 重复，应去重
				{ID: "a2", Type: "Article", Props: map[string]any{"id": "a2"}},
			},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "张三 AI", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Articles) != 2 {
		t.Errorf("期望 2 篇文章（去重后）, 实际 %d", len(got.Articles))
	}
	if len(got.Entities) != 2 {
		t.Errorf("期望 2 个实体, 实际 %d", len(got.Entities))
	}
	if client.cypherCalls != 2 {
		t.Errorf("期望 2 次 Cypher 调用, 实际 %d", client.cypherCalls)
	}
}

// TestGraphSearcher_Search_EmptyEntities 验证空实体返回空结果。
func TestGraphSearcher_Search_EmptyEntities(t *testing.T) {
	client := &stubGraphClient{entities: nil}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "无实体查询", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Entities) != 0 {
		t.Errorf("空实体应返回空, 实际 %d", len(got.Entities))
	}
	if len(got.Articles) != 0 {
		t.Errorf("空实体应无文章, 实际 %d", len(got.Articles))
	}
	if client.cypherCalls != 0 {
		t.Errorf("空实体不应调用 Cypher, 实际 %d 次", client.cypherCalls)
	}
}

// TestGraphSearcher_Search_EntityLinkError 验证实体链接错误返回 error。
func TestGraphSearcher_Search_EntityLinkError(t *testing.T) {
	client := &stubGraphClient{entityErr: errors.New("entity link down")}
	gs := NewGraphSearcher(client)
	_, err := gs.Search(context.Background(), "q", 10)
	if err == nil {
		t.Fatalf("期望返回错误, 实际 nil")
	}
}

// TestGraphSearcher_Search_CypherErrorContinues 验证 Cypher 失败不阻断，仍返回实体与作者/IP。
func TestGraphSearcher_Search_CypherErrorContinues(t *testing.T) {
	client := &stubGraphClient{
		entities:  []domain.Entity{{Name: "张三", Type: "Author"}},
		cypherErr: errors.New("cypher exec fail"),
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("Cypher 失败不应返回错误, 实际 %v", err)
	}
	if len(got.Entities) != 1 {
		t.Errorf("期望 1 个实体, 实际 %d", len(got.Entities))
	}
	if len(got.Authors) != 1 {
		t.Errorf("期望 1 个作者, 实际 %d", len(got.Authors))
	}
	if len(got.Articles) != 0 {
		t.Errorf("Cypher 失败应无文章, 实际 %d", len(got.Articles))
	}
}

// TestBuildCypherForEntity_DefaultNoMatch 验证未知实体类型不匹配。
func TestBuildCypherForEntity_DefaultNoMatch(t *testing.T) {
	cypher, _, _, _, ok := buildCypherForEntity(domain.Entity{Name: "x", Type: "Unknown"}, 10)
	if ok {
		t.Errorf("未知类型应返回 ok=false")
	}
	if cypher != "" {
		t.Errorf("未知类型 cypher 应为空")
	}
}

// TestGraphSearcher_Search_TopKDefault 验证 topK<=0 时默认 20。
func TestGraphSearcher_Search_TopKDefault(t *testing.T) {
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "AI", Type: "Tag"}},
	}
	gs := NewGraphSearcher(client)
	_, _ = gs.Search(context.Background(), "AI", 0)
	if client.lastParams["topk"] != 20 {
		t.Errorf("topK=0 应默认 20, 实际 %v", client.lastParams["topk"])
	}
}

// TestGraphSearcher_Search_ArticleIDFallback 验证从节点 ID 回退提取文章 ID。
func TestGraphSearcher_Search_ArticleIDFallback(t *testing.T) {
	// 节点 Props 无 id/article_id，应回退到 GraphNode.ID。
	client := &stubGraphClient{
		entities: []domain.Entity{{Name: "AI", Type: "Tag"}},
		cypherNodes: map[string][]domain.GraphNode{
			cypherByTag: {{ID: "fallback-id", Type: "Article", Props: map[string]any{}}},
		},
	}
	gs := NewGraphSearcher(client)
	got, err := gs.Search(context.Background(), "AI", 10)
	if err != nil {
		t.Fatalf("Search 错误: %v", err)
	}
	if len(got.Articles) != 1 {
		t.Fatalf("期望 1 篇文章, 实际 %d", len(got.Articles))
	}
	if got.Articles[0] != "fallback-id" {
		t.Errorf("文章 ID = %q, 期望 fallback-id", got.Articles[0])
	}
}
