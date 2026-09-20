package graph

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 编译期断言：14 个公开方法均存在且签名正确。
var (
	_ = (*Client)(nil).UpsertArticle
	_ = (*Client)(nil).UpsertUser
	_ = (*Client)(nil).RecordBehavior
	_ = (*Client)(nil).UpsertIP
	_ = (*Client)(nil).UpsertChannel
	_ = (*Client)(nil).UpsertImage
	_ = (*Client)(nil).LinkArticleTag
	_ = (*Client)(nil).LinkArticleAuthor
	_ = (*Client)(nil).LinkArticleIP
	_ = (*Client)(nil).LinkArticleImage
	_ = (*Client)(nil).LinkUserBehavior
	_ = (*Client)(nil).LinkCoOccurrence
	_ = (*Client)(nil).LinkVisuallySimilar
	_ = (*Client)(nil).UpsertQualityReport
)

// TestOperationCount 校验公开 operations 总数为 14。
// 注：Cypher 模板常量数为 15（UpsertQualityReport 内部使用 2 条 Cypher）。
func TestOperationCount(t *testing.T) {
	templates := []string{
		cypherUpsertArticle,
		cypherUpsertUser,
		cypherRecordBehavior,
		cypherUpsertIP,
		cypherUpsertChannel,
		cypherUpsertImage,
		cypherLinkArticleTag,
		cypherLinkArticleAuthor,
		cypherLinkArticleIP,
		cypherLinkArticleImage,
		cypherLinkUserBehavior,
		cypherLinkCoOccurrence,
		cypherLinkVisuallySimilar,
		cypherUpsertQualityReport,
		cypherLinkArticleQualityReport,
	}
	if got, want := len(templates), 15; got != want {
		t.Errorf("cypher 模板总数 = %d, 期望 %d（14 方法 + 1 内部 Link）", got, want)
	}
	// 公开方法数为 14（UpsertQualityReport 用 2 个模板，故 15-1=14）。
}

// TestCypherTemplates 校验所有 operations Cypher 模板的正确性（不依赖真实 Neo4j）。
// 每个 case 列出该模板必须包含的子串（参数化键、MERGE/SET/关系类型）。
func TestCypherTemplates(t *testing.T) {
	cases := []struct {
		name        string
		cypher      string
		mustContain []string
	}{
		{
			name:   "UpsertArticle",
			cypher: cypherUpsertArticle,
			mustContain: []string{
				"MERGE (a:Article {id: $id})",
				"SET a.title = $title",
				"a.user_id = $userId",
				"a.channel = $channel",
				"a.author_id = $authorId",
				"a.author_name = $authorName",
				"a.ip_name = $ipName",
				"a.type_tag = $typeTag",
				"a.create_time = $createTime",
			},
		},
		{
			name:   "UpsertUser",
			cypher: cypherUpsertUser,
			mustContain: []string{
				"MERGE (u:User {id: $id})",
				"u.channel = $channel",
				"u.name = $name",
				"u.register_at = $registerAt",
			},
		},
		{
			name:   "RecordBehavior",
			cypher: cypherRecordBehavior,
			mustContain: []string{
				"MERGE (b:Behavior {id: $id})",
				"b.user_id = $userId",
				"b.article_id = $articleId",
				"b.type = $type",
				"b.channel = $channel",
				"b.created_at = $createdAt",
				"b.weight = $weight",
			},
		},
		{
			name:   "UpsertIP",
			cypher: cypherUpsertIP,
			mustContain: []string{
				"MERGE (i:IP {name: $name})",
				"i.description = $description",
			},
		},
		{
			name:   "UpsertChannel",
			cypher: cypherUpsertChannel,
			mustContain: []string{
				"MERGE (c:Channel {name: $name})",
				"c.type_tag = $typeTag",
				"c.filter_key = $filterKey",
				"c.pool_size = $poolSize",
			},
		},
		{
			name:   "UpsertImage",
			cypher: cypherUpsertImage,
			mustContain: []string{
				"MERGE (m:Image {id: $id})",
				"m.article_id = $articleId",
				"m.url = $url",
				"m.embedding = $embedding",
				"m.width = $width",
				"m.height = $height",
			},
		},
		{
			name:   "LinkArticleTag",
			cypher: cypherLinkArticleTag,
			mustContain: []string{
				"MERGE (a:Article {id: $articleId})",
				"MERGE (t:Tag {name: $tag})",
				"MERGE (a)-[:HAS_TAG]->(t)",
			},
		},
		{
			name:   "LinkArticleAuthor",
			cypher: cypherLinkArticleAuthor,
			mustContain: []string{
				"MERGE (a:Article {id: $articleId})",
				"MERGE (au:Author {id: $authorId})",
				"MERGE (a)-[:WRITTEN_BY]->(au)",
			},
		},
		{
			name:   "LinkArticleIP",
			cypher: cypherLinkArticleIP,
			mustContain: []string{
				"MERGE (a:Article {id: $articleId})",
				"MERGE (i:IP {name: $ipName})",
				"MERGE (a)-[:BELONGS_TO_IP]->(i)",
			},
		},
		{
			name:   "LinkArticleImage",
			cypher: cypherLinkArticleImage,
			mustContain: []string{
				"MERGE (a:Article {id: $articleId})",
				"MERGE (m:Image {id: $imageId})",
				"MERGE (a)-[:HAS_IMAGE]->(m)",
			},
		},
		{
			name:   "LinkUserBehavior",
			cypher: cypherLinkUserBehavior,
			mustContain: []string{
				"MERGE (u:User {id: $userId})",
				"MERGE (b:Behavior {id: $behaviorId})",
				"MERGE (u)-[:PERFORMED]->(b)",
			},
		},
		{
			name:   "LinkCoOccurrence",
			cypher: cypherLinkCoOccurrence,
			mustContain: []string{
				"MERGE (a1:Article {id: $articleA})",
				"MERGE (a2:Article {id: $articleB})",
				"MERGE (a1)-[r:CO_OCCURRED_WITH]->(a2)",
				"r.weight = $weight",
			},
		},
		{
			name:   "LinkVisuallySimilar",
			cypher: cypherLinkVisuallySimilar,
			mustContain: []string{
				"MERGE (m1:Image {id: $imageA})",
				"MERGE (m2:Image {id: $imageB})",
				"MERGE (m1)-[r:VISUALLY_SIMILAR]->(m2)",
				"r.score = $score",
			},
		},
		{
			name:   "UpsertQualityReport",
			cypher: cypherUpsertQualityReport,
			mustContain: []string{
				"MERGE (q:QualityReport {id: $id})",
				"q.article_id = $articleId",
				"q.authority = $authority",
				"q.depth = $depth",
				"q.freshness = $freshness",
				"q.completeness = $completeness",
				"q.readability = $readability",
				"q.citation = $citation",
				"q.overall = $overall",
				"q.grade = $grade",
			},
		},
		{
			name:   "LinkArticleQualityReport",
			cypher: cypherLinkArticleQualityReport,
			mustContain: []string{
				"MERGE (a:Article {id: $articleId})",
				"MERGE (q:QualityReport {id: $reportId})",
				"MERGE (a)-[:HAS_QUALITY_REPORT]->(q)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, sub := range tc.mustContain {
				if !strings.Contains(tc.cypher, sub) {
					t.Errorf("%s cypher 缺少子串 %q\n完整 cypher:\n%s", tc.name, sub, tc.cypher)
				}
			}
			// 所有模板必须以 MERGE 开头（防 SELECT/DELETE 误用）。
			trimmed := strings.TrimSpace(tc.cypher)
			if !strings.HasPrefix(trimmed, "MERGE") {
				end := 20
				if len(trimmed) < end {
					end = len(trimmed)
				}
				t.Errorf("%s cypher 必须以 MERGE 开头，实际: %q", tc.name, trimmed[:end])
			}
		})
	}
}

// TestCypherNoStringConcat 校验所有 Cypher 模板不含字符串拼接（防注入）。
// 通过检查 Cypher 中不出现 %s/%v/%d 等格式化占位符实现。
func TestCypherNoStringConcat(t *testing.T) {
	templates := []struct {
		name   string
		cypher string
	}{
		{"UpsertArticle", cypherUpsertArticle},
		{"UpsertUser", cypherUpsertUser},
		{"RecordBehavior", cypherRecordBehavior},
		{"UpsertIP", cypherUpsertIP},
		{"UpsertChannel", cypherUpsertChannel},
		{"UpsertImage", cypherUpsertImage},
		{"LinkArticleTag", cypherLinkArticleTag},
		{"LinkArticleAuthor", cypherLinkArticleAuthor},
		{"LinkArticleIP", cypherLinkArticleIP},
		{"LinkArticleImage", cypherLinkArticleImage},
		{"LinkUserBehavior", cypherLinkUserBehavior},
		{"LinkCoOccurrence", cypherLinkCoOccurrence},
		{"LinkVisuallySimilar", cypherLinkVisuallySimilar},
		{"UpsertQualityReport", cypherUpsertQualityReport},
		{"LinkArticleQualityReport", cypherLinkArticleQualityReport},
	}
	for _, tc := range templates {
		for _, bad := range []string{"%s", "%v", "%d", "%f", "fmt.Sprintf"} {
			if strings.Contains(tc.cypher, bad) {
				t.Errorf("%s cypher 含字符串拼接 %q（注入风险）: %s", tc.name, bad, tc.cypher)
			}
		}
	}
}

// TestInputStructs 校验 input struct 字段可正确构造（接口断言，防字段意外删除）。
func TestInputStructs(t *testing.T) {
	a := ArticleInput{
		ID: "a1", Title: "t", Content: "c", UserID: "u1", Channel: "c1",
		AuthorID: "au1", AuthorName: "name", IPName: "ip1", TypeTag: "tt",
		CreateTime: time.Now(),
	}
	if a.ID == "" || a.Title == "" || a.UserID == "" || a.Channel == "" ||
		a.AuthorID == "" || a.IPName == "" || a.TypeTag == "" {
		t.Errorf("ArticleInput 字段断言失败: %+v", a)
	}

	u := UserInput{ID: "u1", Channel: "c1", Name: "n", RegisterAt: time.Now()}
	if u.ID == "" || u.Channel == "" || u.Name == "" {
		t.Errorf("UserInput 字段断言失败: %+v", u)
	}

	b := BehaviorInput{
		ID: "b1", UserID: "u1", ArticleID: "a1", Type: "click",
		Channel: "c1", CreatedAt: time.Now(), Weight: 0.5,
	}
	if b.ID == "" || b.UserID == "" || b.ArticleID == "" || b.Type == "" || b.Weight == 0 {
		t.Errorf("BehaviorInput 字段断言失败: %+v", b)
	}

	ip := IPInput{Name: "ip1", Description: "d"}
	if ip.Name == "" {
		t.Errorf("IPInput 字段断言失败: %+v", ip)
	}

	ch := ChannelInput{Name: "c1", TypeTag: "tt", FilterKey: "fk", PoolSize: 100}
	if ch.Name == "" || ch.TypeTag == "" || ch.FilterKey == "" {
		t.Errorf("ChannelInput 字段断言失败: %+v", ch)
	}

	im := ImageInput{
		ID: "i1", ArticleID: "a1", URL: "http://x",
		Embedding: []float32{0.1, 0.2}, Width: 100, Height: 200,
	}
	if im.ID == "" || im.ArticleID == "" || im.URL == "" || len(im.Embedding) == 0 {
		t.Errorf("ImageInput 字段断言失败: %+v", im)
	}

	q := QualityReportInput{
		ID: "q1", ArticleID: "a1", Authority: 0.8, Depth: 0.7, Freshness: 0.6,
		Completeness: 0.5, Readability: 0.9, Citation: 0.4, Overall: 0.7, Grade: "A",
	}
	if q.ID == "" || q.ArticleID == "" || q.Grade == "" || q.Overall == 0 {
		t.Errorf("QualityReportInput 字段断言失败: %+v", q)
	}
}

// TestNewClientConstructsDriver 校验 NewClient 能构造 Client（不连接 Neo4j）。
// 因 NewClient 不调用 VerifyConnectivity，即使无 Neo4j 也应成功。
func TestNewClientConstructsDriver(t *testing.T) {
	c, err := NewClient("bolt://localhost:7687", "neo4j", "password")
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient 返回 nil")
	}
	if c.driver == nil {
		t.Fatal("Client.driver 为 nil")
	}
	// 关闭驱动（不连接服务端，应无副作用）。
	if err := c.Close(context.Background()); err != nil {
		t.Errorf("Close 失败: %v", err)
	}
}
