// 该文件实现 GraphQuerier.EntityLink：基于图谱节点名 + 别名做精确/前缀/包含
// 匹配，UNION 多类节点（Article/Tag/Author/IP/Entity），返回识别实体列表。
//
// 依赖前置（Task 2.1）：Client 结构体定义于 client.go，含字段
// `driver neo4j.DriverWithContext`，并提供 ExecuteRead 事务包装。本文件仅追加
// 方法，不重复定义 Client。
package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// entityLinkCypher 实体链接 Cypher 模板（参数化防注入）。
//
// 策略：对 Article.title / Tag.name / Author.name / IP.name / Entity.name
// 五类节点做 toLower 后的包含匹配，并用 CASE 区分精确/前缀/包含三档置信度：
//   - 精确匹配：1.0（Article）/ 0.95（其余）
//   - 前缀匹配：0.85（Article）/ 0.80（其余）
//   - 包含匹配：0.70（Article）/ 0.65（其余）
//
// 使用 UNION 合并多类节点结果，LIMIT $limit 限定返回条数（作用于 UNION 合并后
// 的整体结果）。参数：$q（查询文本）、$limit（上限）。
//
// TODO: embedding 相似度匹配需 Milvus 向量库支持，本 task 先做名称匹配。
//
//	后续可在 internal/embedding 计算 query 向量，到 Milvus 做语义近邻
//	召回，与图谱名称匹配结果融合（按置信度加权）。
const entityLinkCypher = `
MATCH (n:Article) WHERE toLower(n.title) CONTAINS toLower($q)
RETURN n.id AS id, 'Article' AS type, n.title AS name,
  CASE WHEN toLower(n.title) = toLower($q) THEN 1.0
       WHEN toLower(n.title) STARTS WITH toLower($q) THEN 0.85
       ELSE 0.70 END AS score
UNION
MATCH (n:Tag) WHERE toLower(n.name) CONTAINS toLower($q)
RETURN n.id AS id, 'Tag' AS type, n.name AS name,
  CASE WHEN toLower(n.name) = toLower($q) THEN 0.95
       WHEN toLower(n.name) STARTS WITH toLower($q) THEN 0.80
       ELSE 0.65 END AS score
UNION
MATCH (n:Author) WHERE toLower(n.name) CONTAINS toLower($q)
RETURN n.id AS id, 'Author' AS type, n.name AS name,
  CASE WHEN toLower(n.name) = toLower($q) THEN 0.95
       WHEN toLower(n.name) STARTS WITH toLower($q) THEN 0.80
       ELSE 0.65 END AS score
UNION
MATCH (n:IP) WHERE toLower(n.name) CONTAINS toLower($q)
RETURN n.id AS id, 'IP' AS type, n.name AS name,
  CASE WHEN toLower(n.name) = toLower($q) THEN 0.95
       WHEN toLower(n.name) STARTS WITH toLower($q) THEN 0.80
       ELSE 0.65 END AS score
UNION
MATCH (n:Entity) WHERE toLower(n.name) CONTAINS toLower($q)
RETURN n.id AS id, 'Entity' AS type, n.name AS name,
  CASE WHEN toLower(n.name) = toLower($q) THEN 0.95
       WHEN toLower(n.name) STARTS WITH toLower($q) THEN 0.80
       ELSE 0.65 END AS score
LIMIT $limit
`

// entityLinkDefaultLimit 实体链接默认返回上限。
const entityLinkDefaultLimit = 20

// entityLinkParams 构造实体链接 Cypher 参数（参数化防注入）。
// 抽出为独立函数便于单测验证参数形状，避免直接拼接字面量查询。
func entityLinkParams(query string, limit int) map[string]any {
	if limit <= 0 {
		limit = entityLinkDefaultLimit
	}
	return map[string]any{
		"q":     query,
		"limit": limit,
	}
}

// entityHit 内部实体命中记录，携带置信度用于排序与去重。
//
// 注：domain.Entity 当前仅含 Name/Type 两个字段，ID 与置信度仅用于内部
// 去重与排序，不外暴露。如需外暴露，可在 domain.Entity 扩展 ID/Confidence
// 字段（属 Task 2.1 domain 调整范围）。
type entityHit struct {
	name  string
	typ   string
	score float64
}

// EntityLink 实体链接，实现 GraphQuerier.EntityLink。
//
// 策略：
//  1. 基于图谱节点名 + 别名做精确/前缀/包含匹配（entityLinkCypher）。
//  2. 对返回结果按 (type,name) 去重，保留最高置信度。
//  3. 按置信度降序排序后返回 domain.Entity 列表（仅 Name/Type）。
//
// 通过 c.ExecuteRead 事务包装执行（与 cypher.go 的 QueryByCypher 一致）。
func (c *Client) EntityLink(ctx context.Context, query string) ([]domain.Entity, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if c == nil || c.driver == nil {
		return nil, fmt.Errorf("neo4j driver is nil")
	}

	out, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, entityLinkCypher, entityLinkParams(query, entityLinkDefaultLimit))
		if err != nil {
			return nil, err
		}
		// 去重：相同 (type,name) 保留最高置信度。
		hitByKey := make(map[string]entityHit)
		for res.Next(ctx) {
			rec := res.Record()
			nameVal, _ := rec.Get("name")
			typeVal, _ := rec.Get("type")
			scoreVal, _ := rec.Get("score")
			name, _ := nameVal.(string)
			typ, _ := typeVal.(string)
			score := toFloat64(scoreVal)
			if strings.TrimSpace(name) == "" {
				continue
			}
			key := typ + "\x00" + name
			if existing, ok := hitByKey[key]; !ok || score > existing.score {
				hitByKey[key] = entityHit{name: name, typ: typ, score: score}
			}
		}
		return hitByKey, res.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("entity link query failed: %w", err)
	}

	hitByKey, _ := out.(map[string]entityHit)
	hits := make([]entityHit, 0, len(hitByKey))
	for _, h := range hitByKey {
		hits = append(hits, h)
	}
	// 按置信度降序，同分按名字升序，保证结果稳定。
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})

	entities := make([]domain.Entity, 0, len(hits))
	for _, h := range hits {
		entities = append(entities, domain.Entity{Name: h.name, Type: h.typ})
	}
	return entities, nil
}

// toFloat64 将 neo4j 返回的数值（float64/int64/int）统一转为 float64。
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}
