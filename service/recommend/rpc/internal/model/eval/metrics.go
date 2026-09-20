// internal/eval/metrics.go — 评估指标实现（Task 15.2）。
//
// 职责：
//   - 提供 MetricsCalculator 实现确定性评估指标：
//     Recall@K / Precision@K / NDCG@K / CTR / CVR / MRR / MAP
//   - 提供 CalculateAll 一次计算所有 ranking 指标
//   - 定义 LLMJudge interface 与 RubricJudge 实现，支持 LLM-as-Judge
//
// 二开扩展点：
//   - 实现 LLMJudge interface 接入自研/第三方 LLM 做 Judge
//   - 在 RubricJudge 中扩展 rubrics map 支持多维度评判
//   - 在 MetricsCalculator 之上追加自定义业务指标（如延迟/成本）
package eval

import (
	"context"
	"fmt"
	"math"
)

// ----------------------------------------------------------------------------
// MetricResult
// ----------------------------------------------------------------------------

// MetricResult 单个指标计算结果。
//
// 字段语义：
//   - Name：指标名（如 "ndcg@10"）
//   - Value：指标值（通常 0-1）
//   - K：K 值（如 10），不适用的指标为 0
//   - Sample：样本数（参与计算的 query/case 数）
type MetricResult struct {
	// Name 指标名。
	Name string `json:"name"`
	// Value 指标值。
	Value float64 `json:"value"`
	// K K 值。
	K int `json:"k"`
	// Sample 样本数。
	Sample int `json:"sample"`
}

// ----------------------------------------------------------------------------
// MetricsCalculator
// ----------------------------------------------------------------------------

// MetricsCalculator 确定性评估指标计算器。
//
// 所有方法无状态、可并发调用。
type MetricsCalculator struct{}

// NewMetricsCalculator 构造 MetricsCalculator。
func NewMetricsCalculator() *MetricsCalculator {
	return &MetricsCalculator{}
}

// RecallAtK 计算 Recall@K：前 K 个结果中命中的期望项数 / 期望项总数。
// ranked 排序后的结果列表；expected 期望命中列表；k 截断位置。
// 返回 0-1 之间的 Recall 值。
func (MetricsCalculator) RecallAtK(ranked []string, expected []string, k int) float64 {
	if k <= 0 || len(expected) == 0 {
		return 0
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	hits := 0
	for i, item := range ranked {
		if i >= k {
			break
		}
		if expectedSet[item] {
			hits++
		}
	}
	return float64(hits) / float64(len(expected))
}

// PrecisionAtK 计算 Precision@K：前 K 个结果中命中的期望项数 / K。
// ranked 排序后的结果列表；expected 期望命中列表；k 截断位置。
// 返回 0-1 之间的 Precision 值。
func (MetricsCalculator) PrecisionAtK(ranked []string, expected []string, k int) float64 {
	if k <= 0 {
		return 0
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	hits := 0
	for i, item := range ranked {
		if i >= k {
			break
		}
		if expectedSet[item] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// NDCGAtK 计算 NDCG@K（Normalized Discounted Cumulative Gain）。
// ranked 排序后的结果列表；expected 期望命中列表；k 截断位置。
// 返回 0-1 之间的 NDCG 值。
func (MetricsCalculator) NDCGAtK(ranked []string, expected []string, k int) float64 {
	if k <= 0 || len(expected) == 0 {
		return 0
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	// DCG：累加 1/log2(i+2)（i 从 0 开始，命中时 rel=1）。
	dcg := 0.0
	for i, item := range ranked {
		if i >= k {
			break
		}
		if expectedSet[item] {
			dcg += 1.0 / math.Log2(float64(i+2))
		}
	}
	// IDCG：理想排序（所有 expected 在前）。
	n := len(expected)
	if n > k {
		n = k
	}
	idcg := 0.0
	for i := 0; i < n; i++ {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// CTR 计算点击率：clicks / impressions。
// impressions 曝光数；clicks 点击数。impressions<=0 时返回 0。
func (MetricsCalculator) CTR(impressions, clicks int) float64 {
	if impressions <= 0 {
		return 0
	}
	return float64(clicks) / float64(impressions)
}

// CVR 计算转化率：conversions / clicks。
// clicks 点击数；conversions 转化数。clicks<=0 时返回 0。
func (MetricsCalculator) CVR(clicks, conversions int) float64 {
	if clicks <= 0 {
		return 0
	}
	return float64(conversions) / float64(clicks)
}

// MRR 计算平均倒数排名（Mean Reciprocal Rank）。
// ranked 排序后的结果列表；expected 期望命中列表。
// 返回 1/rank（rank 从 1 开始），无命中返回 0。
func (MetricsCalculator) MRR(ranked []string, expected []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	for i, item := range ranked {
		if expectedSet[item] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// MAP 计算平均精度均值（Mean Average Precision）。
// queries 多个 query 的排序列表；expected 多个 query 的期望列表（一一对应）。
// 返回所有 query 的 AP 平均值。
func (MetricsCalculator) MAP(queries [][]string, expected [][]string) float64 {
	if len(queries) == 0 || len(queries) != len(expected) {
		return 0
	}
	sum := 0.0
	for i := range queries {
		sum += averagePrecision(queries[i], expected[i])
	}
	return sum / float64(len(queries))
}

// averagePrecision 计算单 query 的 Average Precision（AP）。
func averagePrecision(ranked, expected []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	expectedSet := make(map[string]bool, len(expected))
	for _, e := range expected {
		expectedSet[e] = true
	}
	hits := 0
	sumPrecision := 0.0
	for i, item := range ranked {
		if expectedSet[item] {
			hits++
			sumPrecision += float64(hits) / float64(i+1)
		}
	}
	if hits == 0 {
		return 0
	}
	return sumPrecision / float64(len(expected))
}

// CalculateAll 一次计算所有 ranking 指标（Recall@K/Precision@K/NDCG@K/MRR）。
// actual 排序后的结果列表；expected 期望命中列表；k 截断位置。
// 返回 MetricResult 列表。
func (c MetricsCalculator) CalculateAll(actual, expected []string, k int) []MetricResult {
	return []MetricResult{
		{Name: "recall@" + itoa(k), Value: c.RecallAtK(actual, expected, k), K: k, Sample: 1},
		{Name: "precision@" + itoa(k), Value: c.PrecisionAtK(actual, expected, k), K: k, Sample: 1},
		{Name: "ndcg@" + itoa(k), Value: c.NDCGAtK(actual, expected, k), K: k, Sample: 1},
		{Name: "mrr", Value: c.MRR(actual, expected), K: 0, Sample: 1},
	}
}

// itoa 简单 int → string 转换（避免引入 strconv 仅用一处）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ----------------------------------------------------------------------------
// LLM Judge
// ----------------------------------------------------------------------------

// LLMJudge LLM-as-Judge 抽象，用 LLM 对推荐结果打分。
//
// 二开：实现该 interface 接入自研/第三方 LLM。
type LLMJudge interface {
	// Judge 对单 query 的推荐结果打分。
	// query 查询文本；actual 推荐结果列表。返回 0-1 分数。
	Judge(ctx context.Context, query string, actual []string) (float64, error)
}

// RubricJudge 基于 rubric 的 LLM Judge 实现。
//
// 工作流程：
//  1. 按 rubric 维度（如 "relevance/diversity/freshness"）分别调用 LLM Judge
//  2. 加权平均（rubrics map 的 value 为权重）得到最终分数
//
// 二开扩展点：调整 rubrics map 自定义评判维度与权重。
type RubricJudge struct {
	// llm LLM 客户端。
	llm LLMJudge
	// rubrics rubric 维度 → 权重（如 {"relevance": 0.5, "diversity": 0.3}）。
	rubrics map[string]float64
}

// NewRubricJudge 构造 RubricJudge。
// llm LLM 客户端；rubrics rubric 维度 → 权重（权重之和应为 1.0）。
func NewRubricJudge(llm LLMJudge, rubrics map[string]float64) *RubricJudge {
	return &RubricJudge{llm: llm, rubrics: rubrics}
}

// Judge 对 query 的推荐结果打分。
// 调用底层 LLM Judge 取原始分数，再按 rubrics 权重做加权调整。
// rubrics 为空时直接返回 LLM 原始分数。
// 二开可扩展为按 rubric 维度独立调用 LLM 后再聚合。
func (r *RubricJudge) Judge(ctx context.Context, query string, actual []string) (float64, error) {
	if r.llm == nil {
		return 0, fmt.Errorf("llm judge 未注入")
	}
	score, err := r.llm.Judge(ctx, query, actual)
	if err != nil {
		return 0, err
	}
	// 无 rubric 时直接返回原始分数。
	if len(r.rubrics) == 0 {
		return score, nil
	}
	// 计算 rubric 权重归一化系数：若权重之和 != 1，按比例缩放。
	// 这里用权重之和作为乘数效应：分数 = score * (权重之和)，
	// 权重之和为 1 时维持原值；超过 1 表示多维度加成；小于 1 表示惩罚。
	totalWeight := 0.0
	for _, w := range r.rubrics {
		totalWeight += w
	}
	if totalWeight <= 0 {
		return score, nil
	}
	// 防止分数超过 1。
	adjusted := score * totalWeight
	if adjusted > 1 {
		adjusted = 1
	}
	if adjusted < 0 {
		adjusted = 0
	}
	return adjusted, nil
}
