// Package search image.go — 图片搜索（以图搜文）。
//
// 该文件实现 ImageSearcher：基于图片 URL，通过图谱视觉相似关系
// 找到关联文章 ID，再加载文章元数据返回结构化结果。
//
// 二开扩展点：
//   - 实现 ImageGraphClient interface 替换图谱后端
//   - 实现 ArticleMetaRepo interface 替换元数据存储（Postgres/ES）
package search

import (
	"context"
	"fmt"
)

// ImageGraphClient 图片图谱客户端抽象，提供以图搜文的关系查询。
//
// 二开扩展点：实现该 interface 对接 Neo4j/JanusGraph 等。
type ImageGraphClient interface {
	// ImageSearchArticles 根据图片 URL 找到视觉相似图片关联的文章 ID。
	// imageURL 图片 URL。
	// 返回文章 ID 列表与 error。
	ImageSearchArticles(ctx context.Context, imageURL string) ([]string, error)
}

// ArticleMeta 文章元数据（定义于本文件，不修改 domain）。
// 用于图片搜索结果展示，含标题/封面/作者/创建时间。
type ArticleMeta struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Title 文章标题。
	Title string
	// CoverURL 文章封面图 URL。
	CoverURL string
	// AuthorID 作者 ID。
	AuthorID string
	// CreateTime 文章创建时间（毫秒）。
	CreateTime int64
}

// ArticleMetaRepo 文章元数据批量加载抽象。
//
// 二开扩展点：实现该 interface 对接 Postgres/ES/缓存等。
type ArticleMetaRepo interface {
	// LoadMany 批量加载文章元数据。
	// articleIDs 文章 ID 列表。
	// 返回 articleID → ArticleMeta 映射（缺失的 ID 不在 map 中）与 error。
	LoadMany(ctx context.Context, articleIDs []string) (map[string]ArticleMeta, error)
}

// ImageSearchHit 图片搜索命中条目。
type ImageSearchHit struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Title 文章标题。
	Title string
	// CoverURL 文章封面图 URL。
	CoverURL string
	// Score 命中分数（按 rank 递减：1/rank）。
	Score float64
	// AuthorID 作者 ID。
	AuthorID string
}

// ImageSearcher 图片搜索器（以图搜文）。
//
// 职责：调用 ImageGraphClient 找到关联文章 ID，再通过 ArticleMetaRepo
// 加载元数据，组装为 ImageSearchHit 列表返回。
type ImageSearcher struct {
	graphClient ImageGraphClient
	metaRepo    ArticleMetaRepo
}

// NewImageSearcher 构造 ImageSearcher。
// graphClient 图片图谱客户端；metaRepo 文章元数据仓储。
func NewImageSearcher(graphClient ImageGraphClient, metaRepo ArticleMetaRepo) *ImageSearcher {
	return &ImageSearcher{
		graphClient: graphClient,
		metaRepo:    metaRepo,
	}
}

// Search 执行以图搜文。
//
// 流程：
//  1. 调 ImageSearchArticles 获取关联文章 ID 列表
//  2. 调 LoadMany 加载文章元数据
//  3. 组装 ImageSearchHit 列表，按 rank 递减打分（rank=1 → 1.0，rank=N → 1/N）
//  4. 截断到 topK
//
// 部分文章元数据缺失不影响整体，缺失字段留空。
func (s *ImageSearcher) Search(ctx context.Context, imageURL string, topK int) ([]ImageSearchHit, error) {
	if s == nil || s.graphClient == nil {
		return nil, fmt.Errorf("image searcher: graph client 未注入")
	}
	if imageURL == "" {
		return nil, fmt.Errorf("image searcher: imageURL 为空")
	}
	if topK <= 0 {
		topK = 20
	}

	// 1. 图谱查询关联文章 ID。
	articleIDs, err := s.graphClient.ImageSearchArticles(ctx, imageURL)
	if err != nil {
		return nil, fmt.Errorf("image searcher: 图谱查询失败: %w", err)
	}
	if len(articleIDs) == 0 {
		return []ImageSearchHit{}, nil
	}
	// 截断到 topK（图谱返回的可能超过 topK）。
	if len(articleIDs) > topK {
		articleIDs = articleIDs[:topK]
	}

	// 2. 加载元数据。
	var metaMap map[string]ArticleMeta
	if s.metaRepo != nil {
		metaMap, err = s.metaRepo.LoadMany(ctx, articleIDs)
		if err != nil {
			return nil, fmt.Errorf("image searcher: 加载元数据失败: %w", err)
		}
	}
	if metaMap == nil {
		metaMap = map[string]ArticleMeta{}
	}

	// 3. 组装 ImageSearchHit 列表，按 rank 递减打分。
	hits := make([]ImageSearchHit, 0, len(articleIDs))
	for i, aid := range articleIDs {
		// rank 从 1 开始，分数 = 1/rank（rank 越靠前分数越高）。
		score := 1.0 / float64(i+1)
		hit := ImageSearchHit{
			ArticleID: aid,
			Score:     score,
		}
		if meta, ok := metaMap[aid]; ok {
			hit.Title = meta.Title
			hit.CoverURL = meta.CoverURL
			hit.AuthorID = meta.AuthorID
		}
		hits = append(hits, hit)
	}
	return hits, nil
}
