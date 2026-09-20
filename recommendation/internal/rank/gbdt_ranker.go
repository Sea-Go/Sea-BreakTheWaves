package rank

import (
	"context"
	"errors"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现 GBDTRanker，梯度提升决策树排序器（stub）。
// 实现 domain.Ranker interface，当前为桩实现，实际 XGBoost 推理待集成。
//
// TODO（二开点）：
//   - 集成 XGBoost C 库或 golang 绑定进行 GBDT 推理
//   - 加载 modelPath 指向的模型文件
//   - 将 Candidate.Scores 转为特征向量送入模型推理
//   - 支持模型热更新与 A/B 分桶
//
// 当前 Rank 直接返回 not implemented 错误，确保编译通过但运行时
// 明确告知调用方该排序器尚未实现，避免静默返回错误结果。
// ============================================================================

// 编译期断言：*GBDTRanker 实现 domain.Ranker interface。
var _ domain.Ranker = (*GBDTRanker)(nil)

// GBDTRanker 梯度提升决策树排序器（stub）。
//
// modelPath 指向 XGBoost 模型文件路径，实际推理待集成。
// 二开扩展点：集成 XGBoost 后替换 Rank 方法实现。
type GBDTRanker struct {
	modelPath string
}

// NewGBDTRanker 创建 GBDT 排序器。
//
// modelPath 为 XGBoost 模型文件路径（如 "/data/models/rank_gbdt.json"）。
// 当前仅存储路径，不做加载；集成 XGBoost 后在此加载模型。
func NewGBDTRanker(modelPath string) *GBDTRanker {
	return &GBDTRanker{modelPath: modelPath}
}

// Rank 执行 GBDT 排序，实现 domain.Ranker.Rank。
//
// TODO: 集成 XGBoost 推理。当前返回 not implemented 错误。
func (r *GBDTRanker) Rank(ctx context.Context, rankCtx domain.RankContext) (domain.RankResult, error) {
	// TODO: 集成 XGBoost 推理
	// 1. 将 rankCtx.Candidates 的 Scores 转为特征矩阵
	// 2. 调用 XGBoost 预测
	// 3. 按预测分数降序排序
	return domain.RankResult{}, errors.New("gbdt ranker: not implemented, XGBoost integration pending")
}

// Name 返回排序器名称，实现 domain.Ranker.Name。
func (r *GBDTRanker) Name() string {
	return "gbdt"
}
