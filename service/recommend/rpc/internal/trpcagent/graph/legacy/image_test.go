// image 单测：验证以图搜文 Cypher 模板正确性与参数化（防注入）。
// 不依赖运行中的 Neo4j，仅校验模板字符串与参数构造。
//
// 注：UpsertImage / LinkVisuallySimilar 的 Cypher 模板测试由 operations_test.go
// （Task 2.3）覆盖；本文件仅测试 ImageSearchArticles 相关模板与参数。
package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestImageSearchCypherTemplate(t *testing.T) {
	// 按 URL 定位 Image 节点
	assert.Contains(t, imageSearchCypher, "MATCH (i:Image {url: $url})")
	// 遍历 VISUALLY_SIMILAR 关系（带变量 r 以引用 r.score）
	assert.Contains(t, imageSearchCypher, "[r:VISUALLY_SIMILAR]")
	// 通过 article_id 属性关联 Article（不依赖关系类型命名）
	assert.Contains(t, imageSearchCypher, "MATCH (a:Article {id: similar.article_id})")
	// 按相似度降序
	assert.Contains(t, imageSearchCypher, "ORDER BY r.score DESC")
	// 参数化上限
	assert.Contains(t, imageSearchCypher, "LIMIT $limit")
	// 去重
	assert.Contains(t, imageSearchCypher, "DISTINCT")
	// 防注入：不拼接字面量
	assert.NotContains(t, imageSearchCypher, "{url: '")
}

func TestImageSearchParams(t *testing.T) {
	t.Run("default limit when zero", func(t *testing.T) {
		p := imageSearchParams("https://img/a.png", 0)
		assert.Equal(t, "https://img/a.png", p["url"])
		assert.Equal(t, imageSearchDefaultLimit, p["limit"])
	})
	t.Run("negative limit falls back to default", func(t *testing.T) {
		p := imageSearchParams("https://img/b.png", -3)
		assert.Equal(t, imageSearchDefaultLimit, p["limit"])
	})
	t.Run("explicit limit respected", func(t *testing.T) {
		p := imageSearchParams("https://img/c.png", 5)
		assert.Equal(t, 5, p["limit"])
		assert.Equal(t, "https://img/c.png", p["url"])
	})
	t.Run("url carried verbatim", func(t *testing.T) {
		// 确保查询以参数形式传递，不做拼接
		p := imageSearchParams("'; MATCH (n) DETACH DELETE n; --", 10)
		assert.Equal(t, "'; MATCH (n) DETACH DELETE n; --", p["url"])
	})
}
