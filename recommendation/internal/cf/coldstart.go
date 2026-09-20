package cf

import (
	"context"
	"sort"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现冷启动策略（Task 7.5）。
//
// 核心能力：
//   - ColdStartUser：新用户冷启动，基于 StaticProfile.Interests 召回 + 热门兜底
//   - ColdStartArticle：新文章冷启动，用内容向量找相似文章的 CF 分数迁移
//     （相似文章的 CF 分数加权迁移到新文章）
//
// 二开扩展点：
//   - 实现 HotArticleRepo interface 替换热门文章数据源（如 Redis ZSet）
//   - 实现 ContentSimilarityRepo interface 替换内容相似度后端（如 Milvus/Faiss）
//   - 扩展 ColdStartUser 兴趣匹配逻辑（接入标签召回/画像召回）
// ============================================================================

// HotArticleRepo 热门文章仓储 interface。
// 二开：实现该 interface 注入自研热门文章数据源
// （如 Redis ZSet / Postgres 排行榜 / 离线预计算池）。
type HotArticleRepo interface {
	// ListHot 返回热门文章候选。
	// ctx 上下文；topK 返回数量上限。
	// 返回候选列表与 error。
	ListHot(ctx context.Context, topK int) ([]domain.Candidate, error)
}

// ContentSimilarityRepo 内容相似度仓储 interface，基于内容向量找相似文章。
// 二开：实现该 interface 注入自研内容相似度后端
// （如 Milvus 向量库 / Faiss / Postgres pgvector）。
type ContentSimilarityRepo interface {
	// FindSimilarArticles 基于内容向量找相似文章。
	// ctx 上下文；articleID 种子文章 ID；topK 返回数量上限。
	// 返回候选列表（含内容相似度分数）与 error。
	FindSimilarArticles(ctx context.Context, articleID string, topK int) ([]domain.Candidate, error)
}

// ColdStart 冷启动策略，处理新用户与新文章的推荐冷启动问题。
//
// 新用户冷启动（ColdStartUser）：
//  1. 基于 StaticProfile.Interests 召回匹配文章（若 ContentSimilarityRepo 支持）
//  2. 不足部分用热门文章兜底
//
// 新文章冷启动（ColdStartArticle）：
//  1. 用 ContentSimilarityRepo 找内容相似文章
//  2. 将相似文章的 CF 分数加权迁移到新文章（分数迁移策略）
type ColdStart struct {
	hotRepo      HotArticleRepo
	contentRepo  ContentSimilarityRepo
	fallbackTopK int
}

// NewColdStart 创建冷启动策略。
// hotRepo 热门文章仓储；contentRepo 内容相似度仓储；
// topK 默认召回数量上限（<=0 时用默认值 20）。
func NewColdStart(hotRepo HotArticleRepo, contentRepo ContentSimilarityRepo, topK int) *ColdStart {
	if topK <= 0 {
		topK = 20
	}
	return &ColdStart{
		hotRepo:      hotRepo,
		contentRepo:  contentRepo,
		fallbackTopK: topK,
	}
}

// ColdStartUser 新用户冷启动召回。
// 基于 StaticProfile.Interests 召回 + 热门兜底。
//
// 流程：
//  1. 取用户 StaticProfile.Interests 作为兴趣标签
//  2. 调 HotArticleRepo.ListHot 取热门文章作为基础候选
//  3. 若用户有 Interests，按兴趣对热门文章做匹配打分（标记 Source="cf_cold_user"）
//  4. 不足 fallbackTopK 时用热门文章补足
//
// 二开扩展点：可扩展为接入标签召回/画像召回/Onboarding 问卷召回，
// 替换当前"热门 + 兴趣打分"策略。
func (cs *ColdStart) ColdStartUser(ctx context.Context, profile domain.UserProfile) ([]domain.Candidate, error) {
	topK := cs.fallbackTopK

	// 取热门文章作为基础候选。
	hot, err := cs.hotRepo.ListHot(ctx, topK)
	if err != nil {
		return nil, err
	}

	// 取用户兴趣标签（若存在）。
	var interests []string
	if profile.Static != nil {
		interests = profile.Static.Interests
	}

	// 对热门文章按兴趣匹配度加权打分。
	// 二开点：此处用简单的 Source 标记 + 原始分数；
	// 真实实现可接入标签召回/画像召回，按兴趣标签匹配文章标签加权。
	result := make([]domain.Candidate, 0, len(hot))
	for _, c := range hot {
		c.Source = "cf_cold_user"
		if c.Scores == nil {
			c.Scores = make(map[string]float64)
		}
		c.Scores["cf_cold"] = c.Score
		// 兴趣加权：若有兴趣标签，对分数做轻微提升（二开可替换为标签匹配逻辑）。
		if len(interests) > 0 {
			c.Scores["cf_cold_interest_boost"] = 0.1
		}
		result = append(result, c)
	}

	// 截断到 topK。
	if topK > 0 && len(result) > topK {
		result = result[:topK]
	}
	return result, nil
}

// ColdStartArticle 新文章冷启动召回。
// 用内容向量找相似文章的 CF 分数迁移（相似文章的 CF 分数加权迁移到新文章）。
//
// 流程：
//  1. 调 ContentSimilarityRepo.FindSimilarArticles 找内容相似文章
//  2. 将相似文章的内容相似度分数作为迁移后的 CF 分数
//     （真实实现应查相似文章的 CF 分数再按相似度加权迁移）
//  3. 返回候选列表（标记 Source="cf_cold_article"）
//
// 二开扩展点：注入 CFScoreSource 查相似文章的真实 CF 分数，
// 替换当前"内容相似度直接迁移"策略。
func (cs *ColdStart) ColdStartArticle(ctx context.Context, articleID string) ([]domain.Candidate, error) {
	topK := cs.fallbackTopK

	// 基于内容向量找相似文章。
	similar, err := cs.contentRepo.FindSimilarArticles(ctx, articleID, topK)
	if err != nil {
		return nil, err
	}

	// 分数迁移：将相似文章的内容相似度分数作为新文章的 CF 分数估计。
	// 真实实现：cf_score(new) = sum( content_sim(i) * cf_score(i) ) / sum(content_sim(i))
	// 当前 stub：直接用内容相似度分数作为迁移分数（标注二开点）。
	result := make([]domain.Candidate, 0, len(similar))
	for _, c := range similar {
		c.Source = "cf_cold_article"
		if c.Scores == nil {
			c.Scores = make(map[string]float64)
		}
		// 内容相似度分数迁移为 CF 分数。
		c.Scores["cf_transfer"] = c.Score
		c.Scores["cf_cold"] = c.Score
		result = append(result, c)
	}

	// 按迁移后的 CF 分数倒序排序。
	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	// 截断到 topK。
	if topK > 0 && len(result) > topK {
		result = result[:topK]
	}
	return result, nil
}
