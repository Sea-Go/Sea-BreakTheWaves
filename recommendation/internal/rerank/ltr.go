package rerank

import (
	"context"
	"math"
	"sort"

	"sea/internal/domain"
)

// ============================================================================
// ltr.go 实现基于 XGBoost LambdaMART 的终排重排 + NDCG 评估指标。
// Model 用 interface 抽象（不直接 import XGBoost），确保离线编译。
// 训练流水线（每日全量 + 每小时增量）位于 internal/rerank/training/。
// ============================================================================

// LambdaMART 基于 XGBoost LambdaMART 的终排重排器。
// 输入为 6 类特征展平向量，输出相关性标量。
// 二开扩展点：替换 Model 实现接入真实 XGBoost 运行时（TODO）；
// 训练流水线（每日全量 + 每小时增量）位于 internal/rerank/training/。
type LambdaMART struct {
	model    Model             // XGBoost 模型抽象，nil 时使用加权特征 stub
	features *FeatureExtractor // 特征提取器
}

// NewLambdaMART 创建 LambdaMART 重排器。
// model XGBoost 模型抽象（可为 nil，走 stub）；fe 特征提取器（为 nil 时自动创建）。
func NewLambdaMART(model Model, fe *FeatureExtractor) *LambdaMART {
	if fe == nil {
		fe = NewFeatureExtractor()
	}
	return &LambdaMART{model: model, features: fe}
}

// Rerank 提取特征 + 模型预测 + 重排。
// ctx 上下文；candidates 候选列表；profile 用户画像；temporal 时间画像；topK 返回数量（<=0 表示不截断）。
// 若 model 为 nil，使用加权特征 stub 评分（TODO 集成 XGBoost）。
func (lm *LambdaMART) Rerank(ctx context.Context, candidates []domain.Candidate, profile domain.UserProfile, temporal domain.TemporalProfile, topK int) ([]domain.Candidate, error) {
	keys := featureKeys()
	scored := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		feats := lm.features.Extract(ctx, c, profile, temporal)
		flat := flattenFeatures(feats)
		var score float64
		if lm.model == nil {
			// TODO: 集成 XGBoost 运行时加载真实 LambdaMART 模型
			score = lm.stubScore(flat)
		} else {
			input := featureVectorToSlice(flat, keys)
			s, err := lm.model.Predict(input)
			if err != nil {
				return nil, err
			}
			score = s
		}
		c.Score = score
		scored = append(scored, c)
	}
	sortByScoreDesc(scored)
	if topK > 0 && topK < len(scored) {
		scored = scored[:topK]
	}
	return scored, nil
}

// stubScore 基于加权特征的 stub 评分（model 未加载时使用）。
// 加权融合 6 类特征的关键指标，输出归一化到 [0,1]。
func (lm *LambdaMART) stubScore(fv FeatureVector) float64 {
	get := func(k string) float64 { return fv[k] }
	score := get("behavior.behavior_overall")*0.30 +
		get("quality.quality_overall")*0.25 +
		get("profile.interest_match")*0.15 +
		get("profile.liked_tag_match")*0.10 +
		get("time.freshness")*0.10 +
		get("text.tag_density")*0.05 +
		get("diversity.tag_diversity")*0.05 -
		get("profile.disliked_tag_penalty")*0.10 -
		get("diversity.diversity_penalty")*0.05
	return clamp01(score)
}

// ---- NDCG 评估指标 ----

// DCG 折损累计增益：DCG = sum(rel_i / log2(i+2))。
// relevances 按排序位置的 relevance 增益值序列。
func DCG(relevances []float64) float64 {
	dcg := 0.0
	for i, rel := range relevances {
		dcg += rel / math.Log2(float64(i+2))
	}
	return dcg
}

// IDCG 理想 DCG（假设前 n 位均为完全相关，rel=1，二值场景）。
// n 排序长度。
func IDCG(n int) float64 {
	if n <= 0 {
		return 0.0
	}
	ideal := make([]float64, n)
	for i := 0; i < n; i++ {
		ideal[i] = 1.0
	}
	return DCG(ideal)
}

// NDCGAtK 计算 NDCG@K（支持分级 relevance）。
// ranked 排序后的候选列表；relevanceLabels articleID -> relevance（增益值，可非二值）；k 截断位置。
// 返回 [0,1]，完美排序为 1.0。
func NDCGAtK(ranked []domain.Candidate, relevanceLabels map[string]float64, k int) float64 {
	if k <= 0 || len(ranked) == 0 || len(relevanceLabels) == 0 {
		return 0.0
	}
	if k > len(ranked) {
		k = len(ranked)
	}
	// 实际 DCG：取前 k 位的 relevance
	actualRels := make([]float64, k)
	for i := 0; i < k; i++ {
		actualRels[i] = relevanceLabels[ranked[i].ArticleID]
	}
	dcg := DCG(actualRels)
	// 理想 DCG：所有标签降序排列后取前 k
	allRels := make([]float64, 0, len(relevanceLabels))
	for _, rel := range relevanceLabels {
		allRels = append(allRels, rel)
	}
	sort.SliceStable(allRels, func(i, j int) bool {
		return allRels[i] > allRels[j]
	})
	limit := k
	if limit > len(allRels) {
		limit = len(allRels)
	}
	idcg := DCG(allRels[:limit])
	if idcg == 0 {
		return 0.0
	}
	return dcg / idcg
}
