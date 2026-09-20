package graph

import (
	"context"
	"errors"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ============================================================================
// 该文件定义 7 类预定义 Cypher 模板与执行方法，实现 domain.GraphQuerier 的
// QueryByCypher / RecallByGraph 能力。所有模板均采用参数化查询（$param）防注入，
// 用户输入绝不拼接到 Cypher 字符串中，仅通过 params map 传递给 Neo4j driver。
//
// 模板清单（每类一个常量 + 一个包装方法）：
//  1. TemplateGraphRecall          用户→喜欢→文章→相似→文章
//  2. TemplateSimilarUsers         用户→相似用户→喜欢→文章
//  3. TemplateCoOccurrence         文章→共现→文章
//  4. TemplateEntityToArticle      实体→文章
//  5. TemplateAuthorToArticle      作者→文章
//  6. TemplateIPToArticle          IP→文章
//  7. TemplateImageVisualSimilar   图片→视觉相似→图片→文章
// ============================================================================

// TemplateGraphRecall 图谱召回模板 1：用户→喜欢→文章→相似→文章。
// 查询语义：以用户为起点，沿 PERFORMED→Behavior(click)→ON_ARTICLE→Article 链路
// 找到用户点击过的文章，再沿 SIMILAR_TO / CO_OCCURRED_WITH 扩展到相似/共现文章
// 作为召回候选。适用于无明确意图的个性化推荐（默认召回模板）。
// 参数：$user_id 用户 ID；$topk 召回数量上限。
const TemplateGraphRecall = `MATCH (u:User {id: $user_id})-[:PERFORMED]->(b:Behavior {type:'click'})-[:ON_ARTICLE]->(a:Article)-[:SIMILAR_TO|CO_OCCURRED_WITH]-(rec:Article)
WHERE rec.id <> a.id
RETURN DISTINCT rec
LIMIT $topk`

// TemplateSimilarUsers 图谱召回模板 2：用户→相似用户→喜欢→文章。
// 查询语义：以用户为起点，沿 SIMILAR_TO 找到相似用户，再沿其点击行为扩展到文章，
// 实现基于用户相似度的协同式召回。适用于用户兴趣扩散。
// 参数：$user_id 用户 ID；$topk 召回数量上限。
const TemplateSimilarUsers = `MATCH (u:User {id: $user_id})-[:SIMILAR_TO]->(sim:User)-[:PERFORMED]->(b:Behavior {type:'click'})-[:ON_ARTICLE]->(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`

// TemplateCoOccurrence 图谱召回模板 3：文章→共现→文章。
// 查询语义：以种子文章为起点，沿 CO_OCCURRED_WITH 扩展到共现文章，适用于
// "看了这篇的人也看了"场景。参数：$article_id 种子文章 ID；$topk 召回数量上限。
const TemplateCoOccurrence = `MATCH (a:Article {id: $article_id})-[:CO_OCCURRED_WITH]-(rec:Article)
WHERE rec.id <> a.id
RETURN DISTINCT rec
LIMIT $topk`

// TemplateEntityToArticle 图谱召回模板 4：实体→文章。
// 查询语义：以实体（话题/关键词）为起点，沿 MENTIONED_IN 扩展到提及该实体的文章，
// 适用于搜索/意图驱动的召回。参数：$entity_name 实体名；$topk 召回数量上限。
const TemplateEntityToArticle = `MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`

// TemplateAuthorToArticle 图谱召回模板 5：作者→文章。
// 查询语义：以作者为起点，沿 WROTE 扩展到该作者撰写的文章，适用于作者维度召回。
// 参数：$author_name 作者名；$topk 召回数量上限。
const TemplateAuthorToArticle = `MATCH (au:Author {name: $author_name})-[:WROTE]->(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`

// TemplateIPToArticle 图谱召回模板 6：IP→文章。
// 查询语义：以 IP 为起点，沿 BELONGS_IP 反向扩展到归属于该 IP 的文章，适用于
// IP 频道维度召回。参数：$ip_name IP 名；$topk 召回数量上限。
const TemplateIPToArticle = `MATCH (ip:IP {name: $ip_name})<-[:BELONGS_IP]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`

// TemplateImageVisualSimilar 图谱召回模板 7：图片→视觉相似→图片→文章。
// 查询语义：以图片 URL 为起点，沿 VISUALLY_SIMILAR 找到视觉相似图片，再沿
// ON_ARTICLE 扩展到关联文章，适用于以图搜文场景。
// 参数：$image_url 图片 URL；$topk 召回数量上限。
const TemplateImageVisualSimilar = `MATCH (img:Image {url: $image_url})-[:VISUALLY_SIMILAR]->(sim:Image)-[:ON_ARTICLE]->(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`

// QueryByCypher 执行任意 Cypher 查询，解析结果为 GraphNode 切片。
// 实现 domain.GraphQuerier.QueryByCypher。
// 所有用户输入必须通过 params 参数化传递，严禁拼接到 cypher 字符串中。
// 结果记录中第一个 neo4j.Node 类型的值将被解析为 GraphNode；非节点返回值会被跳过。
func (c *Client) QueryByCypher(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
	if c == nil || c.driver == nil {
		return nil, errors.New("neo4j driver is nil")
	}
	if cypher == "" {
		return nil, errors.New("empty cypher")
	}
	out, err := c.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		var nodes []domain.GraphNode
		for res.Next(ctx) {
			if node, ok := recordToGraphNode(res.Record()); ok {
				nodes = append(nodes, node)
			}
		}
		return nodes, res.Err()
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return out.([]domain.GraphNode), nil
}

// RecallByGraph 图谱多跳召回，实现 domain.GraphQuerier.RecallByGraph。
// 根据 req.Pattern 选择对应 Cypher 模板执行，并将 GraphNode 转换为 Candidate。
//
// 支持的 Pattern（与 domain.GraphRecallRequest.Pattern 对齐）：
//   - "" / "user_like_similar"：用户→喜欢→文章→相似→文章（默认，TemplateGraphRecall）
//   - "user_similar_like"：用户→相似用户→喜欢→文章（TemplateSimilarUsers）
//   - "co_occurrence"：文章→共现→文章（TemplateCoOccurrence，以 UserID 作为种子键）
//   - "entity"：实体→文章（TemplateEntityToArticle，以 UserID 作为实体名）
//   - "article_author"：作者→文章（TemplateAuthorToArticle，以 UserID 作为作者名）
//   - "ip"：IP→文章（TemplateIPToArticle，以 UserID 作为 IP 名）
//   - "image"：图片→视觉相似→图片→文章（TemplateImageVisualSimilar，以 UserID 作为图片 URL）
//
// 注意：非用户维度的 Pattern（co_occurrence/entity/article_author/ip/image）以
// UserKey.UserID 作为种子键调用，适合用户维度场景；若需传入文章 ID / 实体名 / 作者名
// 等专用种子键，请直接调用对应的 RecallXxx 方法或 QueryByCypher。
func (c *Client) RecallByGraph(ctx context.Context, req domain.GraphRecallRequest) ([]domain.Candidate, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = 20
	}
	seed := req.UserKey.UserID
	var nodes []domain.GraphNode
	var err error
	switch req.Pattern {
	case "user_similar_like":
		nodes, err = c.RecallSimilarUsersLike(ctx, seed, topK)
	case "co_occurrence":
		nodes, err = c.RecallCoOccurrence(ctx, seed, topK)
	case "entity":
		nodes, err = c.RecallEntityArticles(ctx, seed, topK)
	case "article_author":
		nodes, err = c.RecallAuthorArticles(ctx, seed, topK)
	case "ip":
		nodes, err = c.RecallIPArticles(ctx, seed, topK)
	case "image":
		nodes, err = c.RecallImageVisualSimilar(ctx, seed, topK)
	default: // "" 或 "user_like_similar"
		nodes, err = c.RecallUserLikeSimilar(ctx, seed, topK)
	}
	if err != nil {
		return nil, err
	}
	return NodesToCandidates(nodes), nil
}

// RecallUserLikeSimilar 用户→喜欢→文章→相似→文章召回（TemplateGraphRecall）。
func (c *Client) RecallUserLikeSimilar(ctx context.Context, userID string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateGraphRecall, map[string]any{
		"user_id": userID,
		"topk":    topK,
	})
}

// RecallSimilarUsersLike 用户→相似用户→喜欢→文章召回（TemplateSimilarUsers）。
func (c *Client) RecallSimilarUsersLike(ctx context.Context, userID string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateSimilarUsers, map[string]any{
		"user_id": userID,
		"topk":    topK,
	})
}

// RecallCoOccurrence 文章→共现→文章召回（TemplateCoOccurrence）。
func (c *Client) RecallCoOccurrence(ctx context.Context, articleID string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateCoOccurrence, map[string]any{
		"article_id": articleID,
		"topk":       topK,
	})
}

// RecallEntityArticles 实体→文章召回（TemplateEntityToArticle）。
func (c *Client) RecallEntityArticles(ctx context.Context, entityName string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateEntityToArticle, map[string]any{
		"entity_name": entityName,
		"topk":        topK,
	})
}

// RecallAuthorArticles 作者→文章召回（TemplateAuthorToArticle）。
func (c *Client) RecallAuthorArticles(ctx context.Context, authorName string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateAuthorToArticle, map[string]any{
		"author_name": authorName,
		"topk":        topK,
	})
}

// RecallIPArticles IP→文章召回（TemplateIPToArticle）。
func (c *Client) RecallIPArticles(ctx context.Context, ipName string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateIPToArticle, map[string]any{
		"ip_name": ipName,
		"topk":    topK,
	})
}

// RecallImageVisualSimilar 图片→视觉相似→图片→文章召回（TemplateImageVisualSimilar）。
func (c *Client) RecallImageVisualSimilar(ctx context.Context, imageURL string, topK int) ([]domain.GraphNode, error) {
	return c.QueryByCypher(ctx, TemplateImageVisualSimilar, map[string]any{
		"image_url": imageURL,
		"topk":      topK,
	})
}

// recordToGraphNode 从 Neo4j 记录中提取第一个 Node 值并转换为 GraphNode。
// 用于解析 `RETURN rec` / `RETURN a` 等返回节点的 Cypher 结果。
func recordToGraphNode(rec *neo4j.Record) (domain.GraphNode, bool) {
	if rec == nil {
		return domain.GraphNode{}, false
	}
	for _, v := range rec.Values {
		if n, ok := v.(neo4j.Node); ok {
			return nodeToGraphNode(n), true
		}
	}
	return domain.GraphNode{}, false
}

// nodeToGraphNode 将 neo4j.Node 转换为 domain.GraphNode。
// 多 label 用 "|" 拼接（如 "Article|Featured"）。
func nodeToGraphNode(n neo4j.Node) domain.GraphNode {
	return domain.GraphNode{
		ID:    n.ElementId,
		Type:  strings.Join(n.Labels, "|"),
		Props: n.Props,
	}
}

// NodeToCandidate 将 GraphNode 转换为召回候选 Candidate（导出，供 recall 包复用）。
// 文章 ID 优先取 Props.id / Props.article_id，回退到节点 ElementId；
// 分数优先取 Props.score / Props.weight；其余属性透传到 Extra。
func NodeToCandidate(node domain.GraphNode) domain.Candidate {
	c := domain.Candidate{
		Source: "graph",
		Extra:  make(map[string]any, len(node.Props)),
	}
	if id, ok := getStringProp(node.Props, "id"); ok {
		c.ArticleID = id
	} else if id, ok := getStringProp(node.Props, "article_id"); ok {
		c.ArticleID = id
	} else {
		c.ArticleID = node.ID
	}
	if s, ok := getFloatProp(node.Props, "score"); ok {
		c.Score = s
	} else if s, ok := getFloatProp(node.Props, "weight"); ok {
		c.Score = s
	}
	for k, v := range node.Props {
		c.Extra[k] = v
	}
	return c
}

// NodesToCandidates 批量转换 GraphNode 为 Candidate。
func NodesToCandidates(nodes []domain.GraphNode) []domain.Candidate {
	out := make([]domain.Candidate, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, NodeToCandidate(n))
	}
	return out
}

// getStringProp 从属性 map 中读取字符串值。
func getStringProp(props map[string]any, key string) (string, bool) {
	if props == nil {
		return "", false
	}
	v, ok := props[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// getFloatProp 从属性 map 中读取浮点值（兼容 float64/float32/int64/int）。
func getFloatProp(props map[string]any, key string) (float64, bool) {
	if props == nil {
		return 0, false
	}
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}
