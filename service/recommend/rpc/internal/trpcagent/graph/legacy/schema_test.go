package graph

import (
	"context"
	"strings"
	"testing"
)

// 编译期断言：*Client 拥有 EnsureSchema 方法，签名匹配 (context.Context) error。
var _ = (*Client)(nil).EnsureSchema

// TestSchemaConstraintDefinitions 校验 12 类节点唯一性约束的定义（接口断言，不依赖 Neo4j）。
func TestSchemaConstraintDefinitions(t *testing.T) {
	if got, want := len(schemaConstraints), 12; got != want {
		t.Errorf("schemaConstraints 数量 = %d, 期望 %d", got, want)
	}

	// 期望的 12 个约束名（覆盖 12 类节点：Article/Tag/TypeTag/Author/IP/Channel/
	// Topic/Entity/Image/User/Behavior/QualityReport）。
	expected := map[string]bool{
		"article_id_unique":        false,
		"tag_name_unique":          false,
		"type_tag_name_unique":     false,
		"author_id_unique":         false,
		"ip_name_unique":           false,
		"channel_name_unique":      false,
		"topic_name_unique":        false,
		"entity_id_unique":         false,
		"image_id_unique":          false,
		"user_id_unique":           false,
		"behavior_id_unique":       false,
		"quality_report_id_unique": false,
	}
	for _, c := range schemaConstraints {
		if c.name == "" {
			t.Errorf("约束 name 为空: %+v", c)
			continue
		}
		if _, ok := expected[c.name]; !ok {
			t.Errorf("意外约束名: %s", c.name)
			continue
		}
		expected[c.name] = true
		// 双重幂等：IF NOT EXISTS + IS UNIQUE。
		if !strings.Contains(c.cypher, "IF NOT EXISTS") {
			t.Errorf("约束 %s 缺少 IF NOT EXISTS: %s", c.name, c.cypher)
		}
		if !strings.Contains(c.cypher, "IS UNIQUE") {
			t.Errorf("约束 %s 缺少 IS UNIQUE: %s", c.name, c.cypher)
		}
		if !strings.Contains(c.cypher, "REQUIRE") {
			t.Errorf("约束 %s 缺少 REQUIRE: %s", c.name, c.cypher)
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("缺失约束: %s", name)
		}
	}
}

// TestSchemaIndexDefinitions 校验 8 类索引的定义（接口断言，不依赖 Neo4j）。
func TestSchemaIndexDefinitions(t *testing.T) {
	if got, want := len(schemaIndexes), 8; got != want {
		t.Errorf("schemaIndexes 数量 = %d, 期望 %d", got, want)
	}

	// 期望的 8 个索引名。
	expected := map[string]bool{
		"article_user_id_index":     false,
		"article_channel_index":     false,
		"article_author_id_index":   false,
		"article_create_time_index": false,
		"user_channel_index":        false,
		"behavior_user_id_index":    false,
		"behavior_created_at_index": false,
		"image_article_id_index":    false,
	}
	for _, ix := range schemaIndexes {
		if ix.name == "" {
			t.Errorf("索引 name 为空: %+v", ix)
			continue
		}
		if _, ok := expected[ix.name]; !ok {
			t.Errorf("意外索引名: %s", ix.name)
			continue
		}
		expected[ix.name] = true
		// 双重幂等：IF NOT EXISTS + ON (...)。
		if !strings.Contains(ix.cypher, "IF NOT EXISTS") {
			t.Errorf("索引 %s 缺少 IF NOT EXISTS: %s", ix.name, ix.cypher)
		}
		if !strings.Contains(ix.cypher, " ON (") {
			t.Errorf("索引 %s 缺少 ON (...): %s", ix.name, ix.cypher)
		}
		if !strings.Contains(ix.cypher, "CREATE INDEX") {
			t.Errorf("索引 %s 缺少 CREATE INDEX: %s", ix.name, ix.cypher)
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("缺失索引: %s", name)
		}
	}
}

// TestSchemaCoversAllNodeTypes 校验约束覆盖 12 类节点 label。
func TestSchemaCoversAllNodeTypes(t *testing.T) {
	wantLabels := []string{
		"Article", "Tag", "TypeTag", "Author", "IP", "Channel",
		"Topic", "Entity", "Image", "User", "Behavior", "QualityReport",
	}
	covered := make(map[string]bool, len(wantLabels))
	for _, c := range schemaConstraints {
		// 约束 cypher 形如 `... FOR (n:Label) REQUIRE ...`，提取 label。
		for _, label := range wantLabels {
			if strings.Contains(c.cypher, "(n:"+label+")") {
				covered[label] = true
			}
		}
	}
	for _, label := range wantLabels {
		if !covered[label] {
			t.Errorf("约束未覆盖节点 label: %s", label)
		}
	}
}

// TestEnsureSchema_Idempotent 测试 EnsureSchema 幂等性。
// 若 Neo4j 不可用则跳过（构造失败或首次 EnsureSchema 失败均视为不可用）。
func TestEnsureSchema_Idempotent(t *testing.T) {
	c, err := NewClient("bolt://localhost:7687", "neo4j", "password")
	if err != nil {
		t.Skipf("neo4j not available (new client failed): %v", err)
	}
	defer c.Close(context.Background())

	ctx := context.Background()
	// 首次调用：若失败说明 Neo4j 不可用，跳过。
	if err := c.EnsureSchema(ctx); err != nil {
		t.Skipf("neo4j not available (first EnsureSchema failed): %v", err)
	}
	// 二次调用：必须幂等无错误。
	if err := c.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema 二次调用失败（幂等被破坏）: %v", err)
	}
}
