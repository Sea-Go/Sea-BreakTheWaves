// 该文件实现以图搜文（ImageSearchArticles）：
//
//	Image(url) -[:VISUALLY_SIMILAR]-> similar Image -> 通过 article_id 属性关联 Article
//
// 图片节点写入与视觉相似关系已由 operations.go（Task 2.3）提供，本文件不重复
// 定义以避免方法冲突（遵循 Task 2.7 冲突约束）：
//   - UpsertImage(ctx, image ImageInput) error            // MERGE Image 节点（含 embedding）
//   - LinkVisuallySimilar(ctx, imageA, imageB string, score float64) error
//
// ImageInput 字段（定义于 operations.go）：ID / ArticleID / URL /
// Embedding []float32 / Width / Height。其中 article_id 由 UpsertImage 写入
// Image 节点属性，本文件的以图搜文即通过该属性关联 Article。
//
// 依赖前置（Task 2.1）：Client 结构体定义于 client.go，含字段
// `driver neo4j.DriverWithContext`，并提供 ExecuteRead 事务包装。
package graph

import (
	"context"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// imageSearchCypher 以图搜文：Image(url) -[:VISUALLY_SIMILAR]-> similar Image
// -> 通过 Image.article_id 属性关联 Article。
//
// 通过 article_id 属性（operations.go 的 UpsertImage 写入 m.article_id）关联
// 文章，避免依赖 Article-Image 关系类型命名（HAS_IMAGE / ON_ARTICLE 等），
// 保证读路径与写路径解耦。参数：$url（图片 URL）、$limit（上限）。
const imageSearchCypher = `
MATCH (i:Image {url: $url})-[r:VISUALLY_SIMILAR]->(similar:Image)
MATCH (a:Article {id: similar.article_id})
RETURN DISTINCT a.id AS id, a.title AS title, r.score AS score
ORDER BY r.score DESC
LIMIT $limit
`

// imageSearchDefaultLimit 以图搜文默认返回上限。
const imageSearchDefaultLimit = 20

// imageSearchParams 构造以图搜文 Cypher 参数（参数化防注入）。
// 抽出为独立函数便于单测验证参数形状，避免直接拼接字面量。
func imageSearchParams(imageURL string, limit int) map[string]any {
	if limit <= 0 {
		limit = imageSearchDefaultLimit
	}
	return map[string]any{
		"url":   imageURL,
		"limit": limit,
	}
}

// ImageSearchArticles 以图搜文，实现 GraphQuerier.ImageSearch 的图谱关系查询部分。
//
// 流程：根据图片 URL 找 Image 节点 -> VISUALLY_SIMILAR -> 关联 Article。
// 返回 GraphNode 列表（ID / Type=Article / Props=title,score）。
//
// 注：GraphQuerier.ImageSearch 接口返回 []Candidate；本方法返回 []GraphNode
// 供上层（如搜索召回）做进一步映射。如需直接满足 interface，可基于
// cypher.go 的 NodesToCandidates 做转换，或在 cypher.go 增加适配方法。
//
// TODO: 实际图片向量计算（CLIP 等）与向量近邻检索应在 internal/embedding +
//
//	Milvus 完成；本 task 先做图谱关系查询。若 Image 节点已带 embedding，
//	后续可扩展为向量近邻 + 图谱关系融合召回。
func (c *Client) ImageSearchArticles(ctx context.Context, imageURL string) ([]domain.GraphNode, error) {
	if c == nil || c.driver == nil {
		return nil, fmt.Errorf("neo4j driver is nil")
	}

	out, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, imageSearchCypher, imageSearchParams(imageURL, imageSearchDefaultLimit))
		if err != nil {
			return nil, err
		}
		nodes := make([]domain.GraphNode, 0)
		for res.Next(ctx) {
			rec := res.Record()
			idVal, _ := rec.Get("id")
			titleVal, _ := rec.Get("title")
			scoreVal, _ := rec.Get("score")
			idStr, _ := idVal.(string)
			titleStr, _ := titleVal.(string)
			nodes = append(nodes, domain.GraphNode{
				ID:   idStr,
				Type: "Article",
				Props: map[string]any{
					"title": titleStr,
					"score": toFloat64(scoreVal),
				},
			})
		}
		return nodes, res.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("image search articles failed: %w", err)
	}

	nodes, _ := out.([]domain.GraphNode)
	return nodes, nil
}
