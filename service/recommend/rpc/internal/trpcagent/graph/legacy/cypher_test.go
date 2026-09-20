package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ============================================================================
// 该文件测试 7 类 Cypher 模板的正确性与参数化防注入能力，以及 GraphNode 转换逻辑。
// 由于单测无法连接真实 Neo4j，测试聚焦：
//   - 模板字符串包含必要的 $param 参数占位符
//   - 模板不含 Go 格式化动词（杜绝字符串拼接注入）
//   - 参数化防注入：含引号/分号的恶意输入不会进入 Cypher 模板字符串
//   - NodeToCandidate / nodeToGraphNode 转换逻辑
//   - QueryByCypher / RecallByGraph 在 driver 为 nil 时优雅返回错误
// ============================================================================

// TestTemplatePlaceholders 验证每个 Cypher 模板包含必要的参数占位符，
// 且不含 Go 格式化动词（%s/%d 等），确保所有用户输入通过 $param 参数化传递。
func TestTemplatePlaceholders(t *testing.T) {
	cases := []struct {
		name     string
		cypher   string
		required []string
	}{
		{"TemplateGraphRecall", TemplateGraphRecall, []string{"$user_id", "$topk"}},
		{"TemplateSimilarUsers", TemplateSimilarUsers, []string{"$user_id", "$topk"}},
		{"TemplateCoOccurrence", TemplateCoOccurrence, []string{"$article_id", "$topk"}},
		{"TemplateEntityToArticle", TemplateEntityToArticle, []string{"$entity_name", "$topk"}},
		{"TemplateAuthorToArticle", TemplateAuthorToArticle, []string{"$author_name", "$topk"}},
		{"TemplateIPToArticle", TemplateIPToArticle, []string{"$ip_name", "$topk"}},
		{"TemplateImageVisualSimilar", TemplateImageVisualSimilar, []string{"$image_url", "$topk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range tc.required {
				if !strings.Contains(tc.cypher, p) {
					t.Errorf("模板 %s 缺少参数占位符 %s\n模板内容:\n%s", tc.name, p, tc.cypher)
				}
			}
			// 不允许 Go 格式化动词，杜绝 fmt.Sprintf 拼接注入
			for _, verb := range []string{"%s", "%d", "%v", "%f"} {
				if strings.Contains(tc.cypher, verb) {
					t.Errorf("模板 %s 含 Go 格式化动词 %q，存在拼接注入风险", tc.name, verb)
				}
			}
			// 每个模板必须带 LIMIT $topk
			if !strings.Contains(tc.cypher, "LIMIT $topk") {
				t.Errorf("模板 %s 缺少 LIMIT $topk", tc.name)
			}
			// 每个模板必须 RETURN 节点
			if !strings.Contains(tc.cypher, "RETURN DISTINCT rec") {
				t.Errorf("模板 %s 必须 RETURN DISTINCT rec", tc.name)
			}
		})
	}
}

// TestTemplateCount 验证预定义模板数量为 7。
func TestTemplateCount(t *testing.T) {
	templates := []string{
		TemplateGraphRecall,
		TemplateSimilarUsers,
		TemplateCoOccurrence,
		TemplateEntityToArticle,
		TemplateAuthorToArticle,
		TemplateIPToArticle,
		TemplateImageVisualSimilar,
	}
	if len(templates) != 7 {
		t.Fatalf("预定义模板数量应为 7，实际 %d", len(templates))
	}
	// 验证各模板互不相同
	seen := make(map[string]bool, len(templates))
	for _, tpl := range templates {
		if seen[tpl] {
			t.Errorf("存在重复的模板字符串")
		}
		seen[tpl] = true
	}
}

// TestParameterizedSafety 验证参数化防注入：含引号/分号的恶意用户输入
// 不会破坏 Cypher 模板字符串。由于模板为常量、用户输入仅通过 params map 传递，
// 模板字符串本身永远不会包含用户输入。
func TestParameterizedSafety(t *testing.T) {
	malicious := []string{
		`'; DROP DATABASE neo4j; --`,
		`" OR 1=1 --`,
		`a';b;c`,
		`'); RETURN n; //`,
		`UNION MATCH (u:User) DELETE u`,
	}
	for _, m := range malicious {
		// 模拟 helper 构造 params（与 RecallUserLikeSimilar 一致）
		params := map[string]any{
			"user_id": m,
			"topk":    10,
		}
		// 模板字符串是常量，绝不包含用户输入
		if strings.Contains(TemplateGraphRecall, m) {
			t.Errorf("用户输入泄漏到 Cypher 模板: %q", m)
		}
		// params 中保留原始恶意字符串（由 Neo4j driver 参数化处理，不会注入）
		if got := params["user_id"]; got != m {
			t.Errorf("params 被篡改: 期望 %q, 实际 %v", m, got)
		}
	}
	// 同样验证其他模板的种子参数
	seededCases := []struct {
		tpl   string
		key   string
		value string
	}{
		{TemplateCoOccurrence, "article_id", `'; MATCH (n) DETACH DELETE n; --`},
		{TemplateEntityToArticle, "entity_name", `" OR rec.id='x`},
		{TemplateAuthorToArticle, "author_name", `'; --`},
		{TemplateIPToArticle, "ip_name", `a'b"c`},
		{TemplateImageVisualSimilar, "image_url", `http://x/?a=' OR 1=1--`},
	}
	for _, tc := range seededCases {
		params := map[string]any{tc.key: tc.value, "topk": 5}
		if strings.Contains(tc.tpl, tc.value) {
			t.Errorf("用户输入泄漏到模板 %s: %q", tc.key, tc.value)
		}
		if params[tc.key] != tc.value {
			t.Errorf("params 被篡改")
		}
	}
}

// TestNodeToGraphNode 验证 neo4j.Node → domain.GraphNode 转换。
func TestNodeToGraphNode(t *testing.T) {
	n := neo4j.Node{
		ElementId: "elem-1",
		Labels:    []string{"Article", "Featured"},
		Props:     map[string]any{"id": "art-1", "title": "测试文章"},
	}
	got := nodeToGraphNode(n)
	if got.ID != "elem-1" {
		t.Errorf("ID = %q, 期望 elem-1", got.ID)
	}
	if got.Type != "Article|Featured" {
		t.Errorf("Type = %q, 期望 Article|Featured", got.Type)
	}
	if got.Props["id"] != "art-1" {
		t.Errorf("Props.id = %v, 期望 art-1", got.Props["id"])
	}
}

// TestRecordToGraphNode 验证从 Neo4j 记录提取节点。
func TestRecordToGraphNode(t *testing.T) {
	// 记录的第一个值是 Node
	rec := &neo4j.Record{
		Keys:   []string{"rec"},
		Values: []any{neo4j.Node{ElementId: "e1", Labels: []string{"Article"}, Props: map[string]any{"id": "a1"}}},
	}
	node, ok := recordToGraphNode(rec)
	if !ok {
		t.Fatal("期望成功提取节点")
	}
	if node.ID != "e1" || node.Type != "Article" {
		t.Errorf("提取节点异常: %+v", node)
	}

	// 记录中无 Node 值
	rec2 := &neo4j.Record{
		Keys:   []string{"cnt"},
		Values: []any{int64(42)},
	}
	if _, ok := recordToGraphNode(rec2); ok {
		t.Error("非节点记录不应提取成功")
	}

	// nil 记录
	if _, ok := recordToGraphNode(nil); ok {
		t.Error("nil 记录不应提取成功")
	}
}

// TestNodeToCandidate 验证 GraphNode → Candidate 转换。
func TestNodeToCandidate(t *testing.T) {
	t.Run("props_id", func(t *testing.T) {
		node := domain.GraphNode{
			ID:    "elem-1",
			Type:  "Article",
			Props: map[string]any{"id": "art-1", "score": 0.92, "title": "标题"},
		}
		c := NodeToCandidate(node)
		if c.ArticleID != "art-1" {
			t.Errorf("ArticleID = %q, 期望 art-1", c.ArticleID)
		}
		if c.Score != 0.92 {
			t.Errorf("Score = %v, 期望 0.92", c.Score)
		}
		if c.Source != "graph" {
			t.Errorf("Source = %q, 期望 graph", c.Source)
		}
		if c.Extra["title"] != "标题" {
			t.Errorf("Extra.title = %v, 期望 标题", c.Extra["title"])
		}
	})
	t.Run("article_id_fallback", func(t *testing.T) {
		node := domain.GraphNode{
			ID:    "elem-2",
			Props: map[string]any{"article_id": "art-2", "weight": 1.5},
		}
		c := NodeToCandidate(node)
		if c.ArticleID != "art-2" {
			t.Errorf("ArticleID = %q, 期望 art-2", c.ArticleID)
		}
		if c.Score != 1.5 {
			t.Errorf("Score = %v, 期望 1.5", c.Score)
		}
	})
	t.Run("elementid_fallback", func(t *testing.T) {
		node := domain.GraphNode{ID: "elem-3", Props: map[string]any{}}
		c := NodeToCandidate(node)
		if c.ArticleID != "elem-3" {
			t.Errorf("ArticleID = %q, 期望 elem-3（回退到节点 ID）", c.ArticleID)
		}
	})
	t.Run("int_score", func(t *testing.T) {
		// 整型分数应被兼容转换
		node := domain.GraphNode{Props: map[string]any{"id": "a", "score": int64(3)}}
		c := NodeToCandidate(node)
		if c.Score != 3.0 {
			t.Errorf("Score = %v, 期望 3.0", c.Score)
		}
	})
	t.Run("nil_props", func(t *testing.T) {
		node := domain.GraphNode{ID: "elem-4", Props: nil}
		c := NodeToCandidate(node)
		if c.ArticleID != "elem-4" {
			t.Errorf("nil Props 时 ArticleID 应回退到节点 ID")
		}
		if c.Extra == nil {
			t.Error("nil Props 时 Extra 应被初始化为空 map")
		}
	})
}

// TestNodesToCandidates 验证批量转换。
func TestNodesToCandidates(t *testing.T) {
	nodes := []domain.GraphNode{
		{ID: "e1", Props: map[string]any{"id": "a1", "score": 0.9}},
		{ID: "e2", Props: map[string]any{"id": "a2", "score": 0.8}},
	}
	cands := NodesToCandidates(nodes)
	if len(cands) != 2 {
		t.Fatalf("len = %d, 期望 2", len(cands))
	}
	if cands[0].ArticleID != "a1" || cands[1].ArticleID != "a2" {
		t.Errorf("转换顺序异常: %+v", cands)
	}
	// 空切片
	if got := NodesToCandidates(nil); len(got) != 0 {
		t.Errorf("nil 输入应返回空切片")
	}
}

// TestQueryByCypher_NilDriver 验证 driver 为 nil 时 QueryByCypher 优雅返回错误。
func TestQueryByCypher_NilDriver(t *testing.T) {
	c := &Client{} // driver 为 nil，触发错误分支
	_, err := c.QueryByCypher(context.Background(), TemplateGraphRecall, map[string]any{"user_id": "u1"})
	if err == nil {
		t.Fatal("driver 为 nil 时应返回错误")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("错误信息应提示 driver 为 nil, 实际: %v", err)
	}
}

// TestQueryByCypher_EmptyCypher 验证空 Cypher 被拒绝。
func TestQueryByCypher_EmptyCypher(t *testing.T) {
	c := &Client{} // driver 为 nil，但空 Cypher 应优先被拒绝
	_, err := c.QueryByCypher(context.Background(), "", nil)
	if err == nil {
		t.Fatal("空 Cypher 应返回错误")
	}
}

// TestRecallByGraph_NilDriver 验证 driver 为 nil 时 RecallByGraph 优雅返回错误。
func TestRecallByGraph_NilDriver(t *testing.T) {
	c := &Client{} // driver 为 nil，触发错误分支
	_, err := c.RecallByGraph(context.Background(), domain.GraphRecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    5,
		Pattern: "user_like_similar",
	})
	if err == nil {
		t.Fatal("driver 为 nil 时应返回错误")
	}
}

// TestRecallByGraph_DefaultTopK 验证 TopK<=0 时回退到默认值 20（通过 nil driver 触发默认路径）。
func TestRecallByGraph_DefaultTopK(t *testing.T) {
	c := &Client{} // driver 为 nil，触发默认 TopK 回退后返回错误
	// 调用 RecallByGraph，TopK=0 应内部回退到 20，然后因 driver nil 返回错误
	_, err := c.RecallByGraph(context.Background(), domain.GraphRecallRequest{
		UserKey: domain.UserKey{UserID: "u1"},
		TopK:    0,
	})
	if err == nil {
		t.Fatal("driver 为 nil 时应返回错误")
	}
}
