package cf

import (
	"math"
	"sort"

	"sea/internal/domain"
)

// ============================================================================
// User-CF：基于用户的协同过滤（Task 7.1）。
// 流程：用户行为向量化 -> 余弦相似度找 Top-N 相似用户 -> 聚合相似用户未看
// 文章加权打分 -> 输出候选。
//
// 二开点：
//   - 替换相似度计算：覆盖 CosineSimilarity 实现皮尔逊/杰卡德等自定义度量
//   - 替换推荐策略：覆盖 Recommend 中的分数聚合方式（加入时间衰减/多样性惩罚）
// ============================================================================

// UserCF User-based 协同过滤。
type UserCF struct {
	// numNeighbors 相似用户邻居数（FindSimilarUsers 的默认 topN）。
	numNeighbors int
	// minSimilarity 最小相似度阈值，低于该值的用户不参与召回。
	minSimilarity float64
}

// NewUserCF 创建 User-CF。
func NewUserCF(numNeighbors int, minSimilarity float64) *UserCF {
	return &UserCF{
		numNeighbors:  numNeighbors,
		minSimilarity: minSimilarity,
	}
}

// UserBehavior 用户行为序列。
type UserBehavior struct {
	// UserID 用户 ID。
	UserID string
	// ArticleIDs 用户交互过的文章 ID 列表（可重复，重复表示多次点击）。
	ArticleIDs []string
	// Weights 显式权重（如评分/打分），键为文章 ID。非空时覆盖点击计数。
	Weights map[string]float64
}

// SimilarUser 相似用户。
type SimilarUser struct {
	// UserID 用户 ID。
	UserID string
	// Similarity 与目标用户的相似度。
	Similarity float64
}

// Vectorize 将用户行为向量化为 article_id -> weight 的稀疏向量。
// weight 取值：若 Weights 中显式给出则用评分；否则按 ArticleIDs 出现次数计数（点击次数）。
func (ucf *UserCF) Vectorize(behaviors UserBehavior) map[string]float64 {
	vec := make(map[string]float64)
	// 先按点击次数累计基础权重
	for _, aid := range behaviors.ArticleIDs {
		vec[aid]++
	}
	// 显式权重（评分）覆盖点击计数
	for aid, w := range behaviors.Weights {
		vec[aid] = w
	}
	return vec
}

// CosineSimilarity 计算两个稀疏向量的余弦相似度。
// 二开点：可替换为皮尔逊相关系数或其他相似度度量。
func (ucf *UserCF) CosineSimilarity(a, b map[string]float64) float64 {
	var dot, normA, normB float64
	for k, va := range a {
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
		normA += va * va
	}
	for _, vb := range b {
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// FindSimilarUsers 从候选用户中找出与目标用户最相似的 Top-N 用户。
// 过滤掉相似度低于 minSimilarity 的用户；topN <= 0 时使用 numNeighbors。
func (ucf *UserCF) FindSimilarUsers(target UserBehavior, candidates []UserBehavior, topN int) []SimilarUser {
	if topN <= 0 {
		topN = ucf.numNeighbors
	}
	targetVec := ucf.Vectorize(target)
	sims := make([]SimilarUser, 0, len(candidates))
	for _, c := range candidates {
		if c.UserID == target.UserID {
			continue
		}
		sim := ucf.CosineSimilarity(targetVec, ucf.Vectorize(c))
		if sim >= ucf.minSimilarity {
			sims = append(sims, SimilarUser{UserID: c.UserID, Similarity: sim})
		}
	}
	sort.Slice(sims, func(i, j int) bool {
		return sims[i].Similarity > sims[j].Similarity
	})
	if topN > 0 && len(sims) > topN {
		sims = sims[:topN]
	}
	return sims
}

// Recommend 基于相似用户推荐目标用户未看过的文章。
// 分数聚合：sum(相似度 * 相似用户对该文章的权重)。
// 二开点：可替换为加入时间衰减/多样性惩罚的打分策略。
func (ucf *UserCF) Recommend(target UserBehavior, similarUsers []UserBehavior, topK int) []domain.Candidate {
	// 标记目标用户已看文章
	seen := make(map[string]bool)
	for _, aid := range target.ArticleIDs {
		seen[aid] = true
	}
	for aid := range target.Weights {
		seen[aid] = true
	}

	targetVec := ucf.Vectorize(target)
	scores := make(map[string]float64)
	for _, su := range similarUsers {
		sim := ucf.CosineSimilarity(targetVec, ucf.Vectorize(su))
		if sim < ucf.minSimilarity {
			continue
		}
		for _, aid := range su.ArticleIDs {
			if seen[aid] {
				continue
			}
			w := 1.0
			if su.Weights != nil {
				if ww, ok := su.Weights[aid]; ok {
					w = ww
				}
			}
			scores[aid] += sim * w
		}
	}

	cands := make([]domain.Candidate, 0, len(scores))
	for aid, s := range scores {
		cands = append(cands, domain.Candidate{
			ArticleID: aid,
			Score:     s,
			Source:    "cf",
			Scores:    map[string]float64{"cf_score": s},
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].Score > cands[j].Score
	})
	if topK > 0 && len(cands) > topK {
		cands = cands[:topK]
	}
	return cands
}
