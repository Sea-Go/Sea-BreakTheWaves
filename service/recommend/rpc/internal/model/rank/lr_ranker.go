package rank

import (
	"context"
	"math"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 LRRanker，逻辑回归排序器。
// 实现 domain.Ranker interface，对候选按 LR 分数排序（分数降序）。
// 排序零 LLM token，属于 fast / hybrid 路径的精排。
//
// 模型：score = sigmoid(sum(w_i * x_i) + bias)
//   - w_i 为特征权重，x_i 为特征值（来自 Candidate.Scores）
//   - bias 为偏置项
//   - sigmoid 将线性得分压缩到 (0, 1) 区间
//
// 二开扩展点：
//   - 模型权重：通过 NewLRRanker(weights, bias) 注入离线训练的 LR 模型
//   - 自定义特征：在 Candidate.Scores 中注入业务特征
//   - 替换实现：实现 domain.Ranker 并通过 Registry.Register 注入
// ============================================================================

// 编译期断言：*LRRanker 实现 domain.Ranker interface。
var _ domain.Ranker = (*LRRanker)(nil)

// LRRanker 逻辑回归排序器。
//
// 对每个 Candidate 计算 LR 分数：score = sigmoid(sum(w_i * x_i) + bias)，
// 其中 x_i 来自 Candidate.Scores[featureName]，按分数降序排列。
//
// 二开扩展点：weights 与 bias 通常来自离线训练的 LR 模型，
// 业务方可按频道/场景注入不同模型版本。
type LRRanker struct {
	weights map[string]float64
	bias    float64
}

// NewLRRanker 创建逻辑回归排序器。
//
// weights 为特征权重映射；bias 为偏置项。
// 二开：业务方可通过 A/B 实验切换不同模型版本。
func NewLRRanker(weights map[string]float64, bias float64) *LRRanker {
	return &LRRanker{weights: weights, bias: bias}
}

// Rank 执行逻辑回归排序，实现 domain.Ranker.Rank。
//
// 流程：
//  1. 复制候选列表（不修改入参）
//  2. 对每个候选计算 sigmoid(sum(w*x) + bias) 并写入 Score 字段
//  3. 按 Score 降序稳定排序
//
// 返回 RankResult。空候选列表返回空结果，不返回 error。
func (r *LRRanker) Rank(ctx context.Context, rankCtx domain.RankContext) (domain.RankResult, error) {
	cands := make([]domain.Candidate, len(rankCtx.Candidates))
	copy(cands, rankCtx.Candidates)

	for i := range cands {
		cands[i].Score = r.lrScore(cands[i])
	}

	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].Score > cands[j].Score
	})

	return domain.RankResult{Candidates: cands}, nil
}

// lrScore 计算单个候选的 LR 分数。
// score = sigmoid(sum(w_i * x_i) + bias)。
// Candidate.Scores 为 nil 时所有特征为 0，分数为 sigmoid(bias)。
func (r *LRRanker) lrScore(c domain.Candidate) float64 {
	var z float64 = r.bias
	for name, w := range r.weights {
		z += w * c.Scores[name]
	}
	return sigmoid(z)
}

// Name 返回排序器名称，实现 domain.Ranker.Name。
func (r *LRRanker) Name() string {
	return "lr"
}

// sigmoid 逻辑斯谛函数，将线性得分压缩到 (0, 1) 区间。
// sigmoid(z) = 1 / (1 + exp(-z))。
// 当 z 极大时返回 1.0，极小时返回 0.0。
func sigmoid(z float64) float64 {
	return 1.0 / (1.0 + math.Exp(-z))
}
