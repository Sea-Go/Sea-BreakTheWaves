// Package search graph.go — 图谱知识搜索。
//
// 该文件实现 GraphSearcher：基于图谱实体链接 + Cypher 模板，
// 从查询文本识别实体（作者/IP/Tag/Title/Topic）并聚合关联文章/作者/IP，
// 返回 GraphKnowledge 供搜索结果增强。
//
// 二开扩展点：
//   - 实现 GraphClient interface 替换图谱后端（Neo4j/JanusGraph 等）
//   - 扩展 Cypher 模板（新增 entity type → article 查询路径）
package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
)

// GraphClient 图谱客户端抽象，提供实体链接与 Cypher 查询能力。
//
// 二开扩展点：实现该 interface 对接 Neo4j/JanusGraph/自研图谱后端。
type GraphClient interface {
	// EntityLink 实体链接，从查询文本识别实体（Author/IP/Tag/Title/Topic）。
	EntityLink(ctx context.Context, query string) ([]domain.Entity, error)
	// QueryByCypher 执行参数化 Cypher 查询，返回图谱节点。
	// params 通过 $param 占位符注入，防止 Cypher 注入。
	QueryByCypher(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error)
}

// cypherByAuthor 作者→文章 Cypher 模板（参数化防注入）。
// 参数：$author_name 作者名；$topk 返回上限。
const cypherByAuthor = `MATCH (au:Author {name: $author_name})-[:WROTE]->(a:Article)
RETURN DISTINCT a
LIMIT $topk`

// cypherByIP IP→文章 Cypher 模板（参数化防注入）。
// 参数：$ip_name IP 名；$topk 返回上限。
const cypherByIP = `MATCH (ip:IP {name: $ip_name})<-[:BELONGS_IP]-(a:Article)
RETURN DISTINCT a
LIMIT $topk`

// cypherByTag Tag→文章 Cypher 模板（参数化防注入）。
// 参数：$tag_name 标签名；$topk 返回上限。
const cypherByTag = `MATCH (t:Tag {name: $tag_name})<-[:HAS_TAG]-(a:Article)
RETURN DISTINCT a
LIMIT $topk`

// cypherByTitle Title→文章 Cypher 模板（参数化防注入）。
// 参数：$title 标题；$topk 返回上限。
const cypherByTitle = `MATCH (a:Article)
WHERE toLower(a.title) CONTAINS toLower($title)
RETURN DISTINCT a
LIMIT $topk`

// cypherByEntity 通用实体→文章 Cypher 模板（Topic/Entity 节点）。
// 参数：$entity_name 实体名；$topk 返回上限。
const cypherByEntity = `MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(a:Article)
RETURN DISTINCT a
LIMIT $topk`

// GraphSearcher 图谱知识搜索器。
//
// 职责：从查询文本识别实体，按实体类型生成 Cypher，聚合关联文章/作者/IP，
// 返回 GraphKnowledge 供搜索结果增强（SearchResult.GraphKnowledge）。
type GraphSearcher struct {
	client GraphClient
}

// NewGraphSearcher 构造 GraphSearcher。
// client 图谱客户端（实现 GraphClient interface）。
func NewGraphSearcher(client GraphClient) *GraphSearcher {
	return &GraphSearcher{client: client}
}

// Search 执行图谱知识搜索。
//
// 流程：
//  1. 调 EntityLink 识别实体
//  2. 对每个实体按类型生成 Cypher（作者/IP/Tag/Title/Topic）
//  3. 执行 Cypher 聚合关联文章/作者/IP
//  4. 返回 GraphKnowledge（含 Cypher 调试串）
//
// 单个实体 Cypher 查询失败不阻断整体，跳过该实体继续下一个。
func (s *GraphSearcher) Search(ctx context.Context, query string, topK int) (domain.GraphKnowledge, error) {
	if s == nil || s.client == nil {
		return domain.GraphKnowledge{}, fmt.Errorf("graph searcher: client 未注入")
	}
	if topK <= 0 {
		topK = 20
	}

	out := domain.GraphKnowledge{
		Entities: []domain.Entity{},
		Articles: []string{},
		Authors:  []string{},
		IPs:      []string{},
	}
	var cypherBuf strings.Builder

	// 1. 实体链接
	entities, err := s.client.EntityLink(ctx, query)
	if err != nil {
		return domain.GraphKnowledge{}, fmt.Errorf("graph searcher: entity link 失败: %w", err)
	}
	if len(entities) == 0 {
		return out, nil
	}
	out.Entities = entities

	// 2. 对每个实体生成并执行 Cypher
	seenArticle := make(map[string]struct{})
	seenAuthor := make(map[string]struct{})
	seenIP := make(map[string]struct{})
	for _, e := range entities {
		cypher, params, authorName, ipName, ok := buildCypherForEntity(e, topK)
		if !ok {
			continue
		}
		if cypherBuf.Len() > 0 {
			cypherBuf.WriteString("\n;\n")
		}
		cypherBuf.WriteString(cypher)
		// 记录关联作者/IP（基于实体本身，不依赖 Cypher 结果）。
		if authorName != "" {
			if _, ok := seenAuthor[authorName]; !ok {
				seenAuthor[authorName] = struct{}{}
				out.Authors = append(out.Authors, authorName)
			}
		}
		if ipName != "" {
			if _, ok := seenIP[ipName]; !ok {
				seenIP[ipName] = struct{}{}
				out.IPs = append(out.IPs, ipName)
			}
		}
		// 3. 执行 Cypher 聚合关联文章
		nodes, qErr := s.client.QueryByCypher(ctx, cypher, params)
		if qErr != nil {
			// 单个实体查询失败不阻断整体，继续下一个。
			continue
		}
		for _, n := range nodes {
			aid := articleIDFromNode(n)
			if aid == "" {
				continue
			}
			if _, ok := seenArticle[aid]; !ok {
				seenArticle[aid] = struct{}{}
				out.Articles = append(out.Articles, aid)
			}
		}
	}

	out.Cypher = cypherBuf.String()
	return out, nil
}

// buildCypherForEntity 根据实体类型构造对应 Cypher 与参数。
// 返回：cypher 语句、参数 map、authorName（作者实体时填充）、ipName（IP 实体时填充）、ok 是否匹配。
func buildCypherForEntity(e domain.Entity, topK int) (string, map[string]any, string, string, bool) {
	params := map[string]any{"topk": topK}
	switch e.Type {
	case "Author":
		params["author_name"] = e.Name
		return cypherByAuthor, params, e.Name, "", true
	case "IP":
		params["ip_name"] = e.Name
		return cypherByIP, params, "", e.Name, true
	case "Tag":
		params["tag_name"] = e.Name
		return cypherByTag, params, "", "", true
	case "Title":
		params["title"] = e.Name
		return cypherByTitle, params, "", "", true
	case "Topic", "Entity":
		params["entity_name"] = e.Name
		return cypherByEntity, params, "", "", true
	default:
		return "", nil, "", "", false
	}
}

// articleIDFromNode 从 GraphNode 提取文章 ID。
// 优先取 Props.id / Props.article_id，回退到节点 ID。
func articleIDFromNode(n domain.GraphNode) string {
	if id, ok := n.Props["id"]; ok {
		if s, ok := id.(string); ok && s != "" {
			return s
		}
	}
	if id, ok := n.Props["article_id"]; ok {
		if s, ok := id.(string); ok && s != "" {
			return s
		}
	}
	return n.ID
}
