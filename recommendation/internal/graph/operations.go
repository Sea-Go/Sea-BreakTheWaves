package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ----------------------------------------------------------------------------
// Input 类型：与 domain 类型字段对齐，但用本包 input struct 避免 domain 类型膨胀。
// 字段语义参考 domain.ArticleQuality/domain.Channel/domain.BehaviorEvent 等。
// ----------------------------------------------------------------------------

// ArticleInput 文章输入。
type ArticleInput struct {
	ID         string    // 文章业务 ID
	Title      string    // 标题
	Content    string    // 正文（可选，图谱通常不存全文）
	UserID     string    // 创建者用户 ID（用于 Article.user_id 索引）
	Channel    string    // 所属频道
	AuthorID   string    // 作者 ID
	AuthorName string    // 作者名（冗余便于展示）
	IPName     string    // 所属 IP 名
	TypeTag    string    // 主类型标签
	CreateTime time.Time // 创建时间
}

// UserInput 用户输入。
type UserInput struct {
	ID         string    // 用户业务 ID
	Channel    string    // 当前频道
	Name       string    // 昵称
	RegisterAt time.Time // 注册时间
}

// BehaviorInput 行为输入。
type BehaviorInput struct {
	ID        string    // 行为唯一 ID
	UserID    string    // 用户 ID
	ArticleID string    // 文章 ID
	Type      string    // 行为类型（click/like/dislike/finish/...）
	Channel   string    // 频道
	CreatedAt time.Time // 行为时间
	Weight    float64   // 行为权重（如 finish=1.0, click=0.5）
}

// IPInput IP 节点输入。
type IPInput struct {
	Name        string // IP 名（唯一键）
	Description string // IP 描述
}

// ChannelInput 频道节点输入。
type ChannelInput struct {
	Name      string // 频道名（唯一键）
	TypeTag   string // 主类型标签
	FilterKey string // 画像 filterKey（频道隔离）
	PoolSize  int    // 独立召回池大小
}

// ImageInput 图片节点输入（含视觉向量）。
type ImageInput struct {
	ID        string    // 图片 ID
	ArticleID string    // 所属文章 ID
	URL       string    // 图片 URL
	Embedding []float32 // 视觉向量
	Width     int       // 宽
	Height    int       // 高
}

// QualityReportInput 质量报告输入。
type QualityReportInput struct {
	ID           string  // 报告 ID
	ArticleID    string  // 关联文章 ID
	Authority    float64 // 权威性
	Depth        float64 // 深度
	Freshness    float64 // 新鲜度
	Completeness float64 // 完整性
	Readability  float64 // 可读性
	Citation     float64 // 引用质量
	Overall      float64 // 综合评分
	Grade        string  // 等级（A-T）
}

// ----------------------------------------------------------------------------
// Cypher 模板（参数化防注入；提取为常量便于单测校验）
// ----------------------------------------------------------------------------

const (
	// cypherUpsertArticle MERGE 文章节点，更新基础属性。
	cypherUpsertArticle = `
MERGE (a:Article {id: $id})
SET a.title = $title,
    a.content = $content,
    a.user_id = $userId,
    a.channel = $channel,
    a.author_id = $authorId,
    a.author_name = $authorName,
    a.ip_name = $ipName,
    a.type_tag = $typeTag,
    a.create_time = $createTime`

	// cypherUpsertUser MERGE 用户节点。
	cypherUpsertUser = `
MERGE (u:User {id: $id})
SET u.channel = $channel,
    u.name = $name,
    u.register_at = $registerAt`

	// cypherRecordBehavior MERGE 行为节点。
	cypherRecordBehavior = `
MERGE (b:Behavior {id: $id})
SET b.user_id = $userId,
    b.article_id = $articleId,
    b.type = $type,
    b.channel = $channel,
    b.created_at = $createdAt,
    b.weight = $weight`

	// cypherUpsertIP MERGE IP 节点。
	cypherUpsertIP = `
MERGE (i:IP {name: $name})
SET i.description = $description`

	// cypherUpsertChannel MERGE 频道节点。
	cypherUpsertChannel = `
MERGE (c:Channel {name: $name})
SET c.type_tag = $typeTag,
    c.filter_key = $filterKey,
    c.pool_size = $poolSize`

	// cypherUpsertImage MERGE 图片节点（含向量属性 embedding）。
	cypherUpsertImage = `
MERGE (m:Image {id: $id})
SET m.article_id = $articleId,
    m.url = $url,
    m.embedding = $embedding,
    m.width = $width,
    m.height = $height`

	// cypherLinkArticleTag MERGE (Article)-[:HAS_TAG]->(Tag)。
	cypherLinkArticleTag = `
MERGE (a:Article {id: $articleId})
MERGE (t:Tag {name: $tag})
MERGE (a)-[:HAS_TAG]->(t)`

	// cypherLinkArticleAuthor MERGE (Article)-[:WRITTEN_BY]->(Author)。
	cypherLinkArticleAuthor = `
MERGE (a:Article {id: $articleId})
MERGE (au:Author {id: $authorId})
MERGE (a)-[:WRITTEN_BY]->(au)`

	// cypherLinkArticleIP MERGE (Article)-[:BELONGS_TO_IP]->(IP)。
	cypherLinkArticleIP = `
MERGE (a:Article {id: $articleId})
MERGE (i:IP {name: $ipName})
MERGE (a)-[:BELONGS_TO_IP]->(i)`

	// cypherLinkArticleImage MERGE (Article)-[:HAS_IMAGE]->(Image)。
	cypherLinkArticleImage = `
MERGE (a:Article {id: $articleId})
MERGE (m:Image {id: $imageId})
MERGE (a)-[:HAS_IMAGE]->(m)`

	// cypherLinkUserBehavior MERGE (User)-[:PERFORMED]->(Behavior)。
	cypherLinkUserBehavior = `
MERGE (u:User {id: $userId})
MERGE (b:Behavior {id: $behaviorId})
MERGE (u)-[:PERFORMED]->(b)`

	// cypherLinkCoOccurrence MERGE (Article)-[:CO_OCCURRED_WITH {weight}]->(Article)。
	cypherLinkCoOccurrence = `
MERGE (a1:Article {id: $articleA})
MERGE (a2:Article {id: $articleB})
MERGE (a1)-[r:CO_OCCURRED_WITH]->(a2)
SET r.weight = $weight`

	// cypherLinkVisuallySimilar MERGE (Image)-[:VISUALLY_SIMILAR {score}]->(Image)。
	// stub：实际向量相似度计算在 image.go（Task 2.7），本方法仅落库已有 score。
	cypherLinkVisuallySimilar = `
MERGE (m1:Image {id: $imageA})
MERGE (m2:Image {id: $imageB})
MERGE (m1)-[r:VISUALLY_SIMILAR]->(m2)
SET r.score = $score`

	// cypherUpsertQualityReport MERGE QualityReport 节点。
	cypherUpsertQualityReport = `
MERGE (q:QualityReport {id: $id})
SET q.article_id = $articleId,
    q.authority = $authority,
    q.depth = $depth,
    q.freshness = $freshness,
    q.completeness = $completeness,
    q.readability = $readability,
    q.citation = $citation,
    q.overall = $overall,
    q.grade = $grade`

	// cypherLinkArticleQualityReport MERGE (Article)-[:HAS_QUALITY_REPORT]->(QualityReport)。
	cypherLinkArticleQualityReport = `
MERGE (a:Article {id: $articleId})
MERGE (q:QualityReport {id: $reportId})
MERGE (a)-[:HAS_QUALITY_REPORT]->(q)`
)

// ----------------------------------------------------------------------------
// Operations（全部基于 ExecuteWrite，参数化防注入）
// ----------------------------------------------------------------------------

// UpsertArticle MERGE 文章节点，更新基础属性（标题/作者/频道/IP/类型/时间）。
func (c *Client) UpsertArticle(ctx context.Context, article ArticleInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertArticle, map[string]any{
			"id":         article.ID,
			"title":      article.Title,
			"content":    article.Content,
			"userId":     article.UserID,
			"channel":    article.Channel,
			"authorId":   article.AuthorID,
			"authorName": article.AuthorName,
			"ipName":     article.IPName,
			"typeTag":    article.TypeTag,
			"createTime": article.CreateTime,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: upsert article: %w", err)
	}
	return nil
}

// UpsertUser MERGE 用户节点，更新频道/昵称/注册时间。
func (c *Client) UpsertUser(ctx context.Context, user UserInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertUser, map[string]any{
			"id":         user.ID,
			"channel":    user.Channel,
			"name":       user.Name,
			"registerAt": user.RegisterAt,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: upsert user: %w", err)
	}
	return nil
}

// RecordBehavior MERGE 行为节点，记录用户对文章的一次行为。
func (c *Client) RecordBehavior(ctx context.Context, behavior BehaviorInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherRecordBehavior, map[string]any{
			"id":        behavior.ID,
			"userId":    behavior.UserID,
			"articleId": behavior.ArticleID,
			"type":      behavior.Type,
			"channel":   behavior.Channel,
			"createdAt": behavior.CreatedAt,
			"weight":    behavior.Weight,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: record behavior: %w", err)
	}
	return nil
}

// UpsertIP MERGE IP 节点，更新描述。
func (c *Client) UpsertIP(ctx context.Context, ip IPInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertIP, map[string]any{
			"name":        ip.Name,
			"description": ip.Description,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: upsert ip: %w", err)
	}
	return nil
}

// UpsertChannel MERGE 频道节点，更新主类型标签/filterKey/池大小。
func (c *Client) UpsertChannel(ctx context.Context, channel ChannelInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertChannel, map[string]any{
			"name":      channel.Name,
			"typeTag":   channel.TypeTag,
			"filterKey": channel.FilterKey,
			"poolSize":  channel.PoolSize,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: upsert channel: %w", err)
	}
	return nil
}

// UpsertImage MERGE 图片节点，含视觉向量属性（用于 VISUALLY_SIMILAR 计算）。
func (c *Client) UpsertImage(ctx context.Context, image ImageInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherUpsertImage, map[string]any{
			"id":        image.ID,
			"articleId": image.ArticleID,
			"url":       image.URL,
			"embedding": image.Embedding,
			"width":     image.Width,
			"height":    image.Height,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: upsert image: %w", err)
	}
	return nil
}

// LinkArticleTag MERGE (Article)-[:HAS_TAG]->(Tag) 关系，标签不存在则自动创建。
func (c *Client) LinkArticleTag(ctx context.Context, articleID, tag string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkArticleTag, map[string]any{
			"articleId": articleID,
			"tag":       tag,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link article tag: %w", err)
	}
	return nil
}

// LinkArticleAuthor MERGE (Article)-[:WRITTEN_BY]->(Author) 关系。
func (c *Client) LinkArticleAuthor(ctx context.Context, articleID, authorID string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkArticleAuthor, map[string]any{
			"articleId": articleID,
			"authorId":  authorID,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link article author: %w", err)
	}
	return nil
}

// LinkArticleIP MERGE (Article)-[:BELONGS_TO_IP]->(IP) 关系。
func (c *Client) LinkArticleIP(ctx context.Context, articleID, ipName string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkArticleIP, map[string]any{
			"articleId": articleID,
			"ipName":    ipName,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link article ip: %w", err)
	}
	return nil
}

// LinkArticleImage MERGE (Article)-[:HAS_IMAGE]->(Image) 关系。
func (c *Client) LinkArticleImage(ctx context.Context, articleID, imageID string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkArticleImage, map[string]any{
			"articleId": articleID,
			"imageId":   imageID,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link article image: %w", err)
	}
	return nil
}

// LinkUserBehavior MERGE (User)-[:PERFORMED]->(Behavior) 关系。
func (c *Client) LinkUserBehavior(ctx context.Context, userID, behaviorID string) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkUserBehavior, map[string]any{
			"userId":     userID,
			"behaviorId": behaviorID,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link user behavior: %w", err)
	}
	return nil
}

// LinkCoOccurrence MERGE (Article)-[:CO_OCCURRED_WITH {weight}]->(Article) 关系。
// weight 为共现权重（如同一用户在 N 分钟内浏览两文章的次数）。
func (c *Client) LinkCoOccurrence(ctx context.Context, articleA, articleB string, weight float64) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkCoOccurrence, map[string]any{
			"articleA": articleA,
			"articleB": articleB,
			"weight":   weight,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link co-occurrence: %w", err)
	}
	return nil
}

// LinkVisuallySimilar MERGE (Image)-[:VISUALLY_SIMILAR {score}]->(Image) 关系。
// stub：实际向量相似度计算在 image.go（Task 2.7），本方法仅落库已有 score。
func (c *Client) LinkVisuallySimilar(ctx context.Context, imageA, imageB string, score float64) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		_, err := tx.Run(ctx, cypherLinkVisuallySimilar, map[string]any{
			"imageA": imageA,
			"imageB": imageB,
			"score":  score,
		})
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("graph: link visually similar: %w", err)
	}
	return nil
}

// UpsertQualityReport MERGE QualityReport 节点 + (Article)-[:HAS_QUALITY_REPORT]->(QualityReport) 关系。
// 一次性写入报告属性与文章关联，保证原子性（同一写事务内两条 Cypher）。
func (c *Client) UpsertQualityReport(ctx context.Context, report QualityReportInput) error {
	_, err := c.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		if _, err := tx.Run(ctx, cypherUpsertQualityReport, map[string]any{
			"id":           report.ID,
			"articleId":    report.ArticleID,
			"authority":    report.Authority,
			"depth":        report.Depth,
			"freshness":    report.Freshness,
			"completeness": report.Completeness,
			"readability":  report.Readability,
			"citation":     report.Citation,
			"overall":      report.Overall,
			"grade":        report.Grade,
		}); err != nil {
			return nil, err
		}
		if _, err := tx.Run(ctx, cypherLinkArticleQualityReport, map[string]any{
			"articleId": report.ArticleID,
			"reportId":  report.ID,
		}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("graph: upsert quality report: %w", err)
	}
	return nil
}
