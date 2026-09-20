// entity_link 单测：验证 Cypher 模板正确性与参数化（防注入）。
// 不依赖运行中的 Neo4j，仅校验模板字符串与参数构造。
package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntityLinkCypherTemplate(t *testing.T) {
	// 覆盖五类节点
	assert.Contains(t, entityLinkCypher, "MATCH (n:Article)")
	assert.Contains(t, entityLinkCypher, "MATCH (n:Tag)")
	assert.Contains(t, entityLinkCypher, "MATCH (n:Author)")
	assert.Contains(t, entityLinkCypher, "MATCH (n:IP)")
	assert.Contains(t, entityLinkCypher, "MATCH (n:Entity)")
	// UNION 合并多类节点
	assert.Contains(t, entityLinkCypher, "UNION")
	// 参数化（防注入）：使用 $q / $limit 占位符
	assert.Contains(t, entityLinkCypher, "$q")
	assert.Contains(t, entityLinkCypher, "$limit")
	// 不应拼接字面量查询值
	assert.NotContains(t, entityLinkCypher, "WHERE n.title = '")
	assert.NotContains(t, entityLinkCypher, "WHERE n.name = '")
	// 大小写归一化
	assert.Contains(t, entityLinkCypher, "toLower")
	// 置信度分档：精确 / 前缀 / 包含
	assert.Contains(t, entityLinkCypher, "THEN 1.0")
	assert.Contains(t, entityLinkCypher, "STARTS WITH")
	assert.Contains(t, entityLinkCypher, "CONTAINS")
	// 返回字段统一（UNION 要求列一致）
	assert.Contains(t, entityLinkCypher, "AS id")
	assert.Contains(t, entityLinkCypher, "AS type")
	assert.Contains(t, entityLinkCypher, "AS name")
	assert.Contains(t, entityLinkCypher, "AS score")
}

func TestEntityLinkParams(t *testing.T) {
	t.Run("default limit when zero", func(t *testing.T) {
		p := entityLinkParams("foo", 0)
		assert.Equal(t, "foo", p["q"])
		assert.Equal(t, entityLinkDefaultLimit, p["limit"])
	})
	t.Run("negative limit falls back to default", func(t *testing.T) {
		p := entityLinkParams("bar", -5)
		assert.Equal(t, entityLinkDefaultLimit, p["limit"])
		assert.Equal(t, "bar", p["q"])
	})
	t.Run("explicit limit respected", func(t *testing.T) {
		p := entityLinkParams("baz", 42)
		assert.Equal(t, 42, p["limit"])
		assert.Equal(t, "baz", p["q"])
	})
	t.Run("query carried verbatim", func(t *testing.T) {
		// 确保查询以参数形式传递，不做拼接
		p := entityLinkParams("'; DROP TABLE n; --", 10)
		assert.Equal(t, "'; DROP TABLE n; --", p["q"])
	})
}

func TestToFloat64(t *testing.T) {
	assert.Equal(t, 1.5, toFloat64(float64(1.5)))
	assert.Equal(t, 3.0, toFloat64(int64(3)))
	assert.Equal(t, 7.0, toFloat64(7))
	assert.Equal(t, 0.0, toFloat64("not a number"))
	assert.Equal(t, 0.0, toFloat64(nil))
}
