package rank

import (
	"context"
	"sort"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 WeightedRanker，加权排序器。
// 实现 domain.Ranker interface，对候选按加权分数排序（分数降序）。
// 排序零 LLM token，属于 fast / hybrid 路径的精排基础。
//
// 特征工程：Candidate.Scores map[string]float64 作为特征来源，
// 分数 = sum(weight_i * feature_i)。
//
// 二开扩展点：
//   - 自定义权重：通过 NewWeightedRanker(weights) 注入业务权重
//   - 自定义特征：在 Candidate.Scores 中注入业务特征（如活动加成）
//   - 替换实现：实现 domain.Ranker 并通过 Registry.Register 注入
// ============================================================================

// 编译期断言：*WeightedRanker 实现 domain.Ranker interface。
var _ domain.Ranker = (*WeightedRanker)(nil)

// WeightedRanker 加权排序器。
//
// 对每个 Candidate 计算加权分数：score = sum(weight_i * feature_i)，
// 其中 feature_i 来自 Candidate.Scores[featureName]，
// 按分数降序排列。Candidate.Scores 为 nil 时该候选分数为 0。
//
// 二开扩展点：weights 可通过 config.yaml 配置或依赖注入，
// 支持业务方按场景调整各特征权重（如大促期间提升转化率特征权重）。
type WeightedRanker struct {
	weights map[string]float64
}

// NewWeightedRanker 创建加权排序器。
//
// weights 为特征名到权重的映射（如 {"cf_score": 0.3, "graph_score": 0.2,
// "freshness": 0.5}）。weights 为 nil 时所有候选分数为 0，按原序稳定返回。
//
// 二开：业务方可根据 A/B 实验动态调整权重。
func NewWeightedRanker(weights map[string]float64) *WeightedRanker {
	return &WeightedRanker{weights: weights}
}

// Rank 执行加权排序，实现 domain.Ranker.Rank。
//
// 流程：
//  1. 复制候选列表（不修改入参）
//  2. 对每个候选计算加权分数并写入 Score 字段
//  3. 按 Score 降序稳定排序
//
// 返回 RankResult。空候选列表返回空结果，不返回 error。
func (r *WeightedRanker) Rank(ctx context.Context, rankCtx domain.RankContext) (domain.RankResult, error) {
	cands := make([]domain.Candidate, len(rankCtx.Candidates))
	copy(cands, rankCtx.Candidates)

	for i := range cands {
		cands[i].Score = r.weightedScore(cands[i])
	}

	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].Score > cands[j].Score
	})

	return domain.RankResult{Candidates: cands}, nil
}

// weightedScore 计算单个候选的加权分数。
// 分数 = sum(weight_i * feature_i)，feature_i 取自 Candidate.Scores。
// Candidate.Scores 为 nil 时返回 0（nil map 读取返回零值）。
func (r *WeightedRanker) weightedScore(c domain.Candidate) float64 {
	var score float64
	for name, w := range r.weights {
		score += w * c.Scores[name]
	}
	return score
}

// Name 返回排序器名称，实现 domain.Ranker.Name。
func (r *WeightedRanker) Name() string {
	return "weighted"
}
