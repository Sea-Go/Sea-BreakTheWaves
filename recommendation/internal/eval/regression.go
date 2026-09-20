// internal/eval/regression.go — 回归测试（Task 15.3）。
//
// 职责：
//   - 定义 RegressionRunner 执行回归测试流程：
//     加载评估集 → 调用 Recommender → 计算 metrics → 对比 baseline → 生成报告
//   - 定义 Recommender interface 抽象 Orchestrator，二开可注入实际编排器
//   - 定义 RegressionBaseline/RegressionReport/DriftItem 数据模型
//   - 漂移判定阈值：默认 10%（current 相对 baseline 变化超过 10% 视为 drift）
//
// 二开扩展点：
//   - 实现 Recommender interface 接入实际 Orchestrator
//   - 调整 driftThreshold 改变漂移灵敏度
//   - 在 Run 中追加自定义 metric 计算与对比逻辑
package eval

import (
	"context"
	"math"
	"time"
)

// ----------------------------------------------------------------------------
// 类型定义
// ----------------------------------------------------------------------------

// RegressionBaseline 回归测试基线。
//
// 字段语义：
//   - Version：基线版本标识（如 "v1.0.0"）
//   - Metrics：基线 metrics（指标名 → 值）
//   - CreatedAt：基线创建时间（Unix 秒）
type RegressionBaseline struct {
	// Version 基线版本。
	Version string `json:"version"`
	// Metrics 基线指标。
	Metrics map[string]float64 `json:"metrics"`
	// CreatedAt 创建时间。
	CreatedAt int64 `json:"created_at"`
}

// DriftItem 单个指标的漂移信息。
//
// 字段语义：
//   - Metric：指标名
//   - Baseline：基线值
//   - Current：当前值
//   - DriftPercent：变化百分比（current - baseline） / baseline * 100
//   - Detected：是否判定为漂移（超过阈值）
type DriftItem struct {
	// Metric 指标名。
	Metric string `json:"metric"`
	// Baseline 基线值。
	Baseline float64 `json:"baseline"`
	// Current 当前值。
	Current float64 `json:"current"`
	// DriftPercent 变化百分比。
	DriftPercent float64 `json:"drift_percent"`
	// Detected 是否漂移。
	Detected bool `json:"detected"`
}

// RegressionReport 回归测试报告。
//
// 字段语义：
//   - Version：当前版本
//   - BaselineVersion：基线版本
//   - Cases：评估用例数
//   - Passed/Failed：通过/失败用例数
//   - Metrics：聚合 metrics
//   - Drift：漂移项列表
//   - Duration：执行耗时（纳秒）
type RegressionReport struct {
	// Version 当前版本。
	Version string `json:"version"`
	// BaselineVersion 基线版本。
	BaselineVersion string `json:"baseline_version"`
	// Cases 用例总数。
	Cases int `json:"cases"`
	// Passed 通过数。
	Passed int `json:"passed"`
	// Failed 失败数。
	Failed int `json:"failed"`
	// Metrics 聚合指标。
	Metrics map[string]float64 `json:"metrics"`
	// Drift 漂移项。
	Drift []DriftItem `json:"drift"`
	// Duration 执行耗时（纳秒）。
	Duration int64 `json:"duration"`
}

// ----------------------------------------------------------------------------
// Recommender interface
// ----------------------------------------------------------------------------

// Recommender 推荐抽象，用于回归测试时调用 Orchestrator 获取实际推荐结果。
//
// 二开：实现该 interface 接入实际 Orchestrator（如 domain.Orchestrator 适配器）。
type Recommender interface {
	// Recommend 按 query 返回推荐结果（文章 ID 列表）。
	Recommend(ctx context.Context, query string) ([]string, error)
}

// ----------------------------------------------------------------------------
// RegressionRunner
// ----------------------------------------------------------------------------

// defaultDriftThreshold 默认漂移阈值（10%）。
const defaultRegressionDriftThreshold = 0.1

// RegressionRunner 回归测试执行器。
//
// 工作流程：
//  1. 从 CaseStore 加载评估集（按 filter 过滤）
//  2. 对每个 case 调用 Recommender.Recommend(query) 获取实际结果
//  3. 用 MetricsCalculator 计算 Recall@K/Precision@K/NDCG@K/MRR
//  4. 聚合 metrics（取平均）
//  5. 对比 baseline，超过阈值的指标标记 drift
//  6. 返回 RegressionReport
//
// 二开扩展点：替换 Recommender 实现；调整阈值；扩展 metric 计算。
type RegressionRunner struct {
	// cases 评估集存储。
	cases CaseStore
	// calculator 指标计算器。
	calculator *MetricsCalculator
	// baseline 基线。
	baseline RegressionBaseline
	// recommender 推荐器（实际调 Orchestrator）。
	recommender Recommender
	// k K 值（默认 10）。
	k int
	// threshold 漂移阈值（默认 0.1）。
	threshold float64
}

// NewRegressionRunner 构造 RegressionRunner。
// cases 评估集存储；calc 指标计算器；baseline 基线。
func NewRegressionRunner(cases CaseStore, calc *MetricsCalculator, baseline RegressionBaseline) *RegressionRunner {
	return &RegressionRunner{
		cases:       cases,
		calculator:  calc,
		baseline:    baseline,
		k:           10,
		threshold:   defaultRegressionDriftThreshold,
		recommender: nil,
	}
}

// WithRecommender 注入 Recommender（默认为 nil，nil 时返回空结果）。
func (r *RegressionRunner) WithRecommender(rec Recommender) *RegressionRunner {
	r.recommender = rec
	return r
}

// WithK 设置 K 值（默认 10）。
func (r *RegressionRunner) WithK(k int) *RegressionRunner {
	if k > 0 {
		r.k = k
	}
	return r
}

// WithThreshold 设置漂移阈值（默认 0.1）。
func (r *RegressionRunner) WithThreshold(threshold float64) *RegressionRunner {
	if threshold > 0 {
		r.threshold = threshold
	}
	return r
}

// Run 执行回归测试。
//
// 流程：
//  1. 从 CaseStore 加载评估集
//  2. 对每个 case 调用 Recommender.Recommend 获取结果（recommender 为 nil 时返回空结果）
//  3. 用 MetricsCalculator 计算 Recall@K/Precision@K/NDCG@K/MRR
//  4. 聚合 metrics（取平均）
//  5. 对比 baseline，超过阈值标记 drift
//  6. 返回 RegressionReport
func (r *RegressionRunner) Run(ctx context.Context, filter CaseFilter) (RegressionReport, error) {
	start := time.Now()
	report := RegressionReport{
		BaselineVersion: r.baseline.Version,
		Metrics:         make(map[string]float64),
	}

	// 1. 加载评估集。
	cases, err := r.cases.List(ctx, filter)
	if err != nil {
		return report, err
	}
	report.Cases = len(cases)

	if len(cases) == 0 {
		report.Duration = time.Since(start).Nanoseconds()
		return report, nil
	}

	// 2-3. 对每个 case 执行评估并计算 metrics。
	var recallSum, precisionSum, ndcgSum, mrrSum float64
	passed := 0
	for _, c := range cases {
		var actual []string
		if r.recommender != nil {
			recs, err := r.recommender.Recommend(ctx, c.Query)
			if err == nil {
				actual = recs
			}
		}
		ndcg := r.calculator.NDCGAtK(actual, c.Expected, r.k)
		recallSum += r.calculator.RecallAtK(actual, c.Expected, r.k)
		precisionSum += r.calculator.PrecisionAtK(actual, c.Expected, r.k)
		ndcgSum += ndcg
		mrrSum += r.calculator.MRR(actual, c.Expected)
		// NDCG > 0 视为通过。
		if ndcg > 0 {
			passed++
		}
	}
	report.Passed = passed
	report.Failed = len(cases) - passed

	// 4. 聚合 metrics（平均）。
	n := float64(len(cases))
	report.Metrics["recall@"+itoa(r.k)] = recallSum / n
	report.Metrics["precision@"+itoa(r.k)] = precisionSum / n
	report.Metrics["ndcg@"+itoa(r.k)] = ndcgSum / n
	report.Metrics["mrr"] = mrrSum / n

	// 5. 对比 baseline 检测漂移。
	report.Drift = r.detectDrift(report.Metrics)

	report.Duration = time.Since(start).Nanoseconds()
	return report, nil
}

// detectDrift 对比当前 metrics 与 baseline，生成 DriftItem 列表。
// 超过阈值的指标标记 Detected=true。
func (r *RegressionRunner) detectDrift(current map[string]float64) []DriftItem {
	if len(r.baseline.Metrics) == 0 {
		return nil
	}
	items := make([]DriftItem, 0, len(r.baseline.Metrics))
	for metric, baseVal := range r.baseline.Metrics {
		curVal, ok := current[metric]
		if !ok {
			continue
		}
		item := DriftItem{
			Metric:   metric,
			Baseline: baseVal,
			Current:  curVal,
		}
		if baseVal != 0 {
			item.DriftPercent = (curVal - baseVal) / math.Abs(baseVal) * 100
			// 相对变化绝对值超过阈值 → drift。
			relChange := math.Abs(curVal-baseVal) / math.Abs(baseVal)
			if relChange > r.threshold {
				item.Detected = true
			}
		} else {
			// baseline 为 0 时，current 非零视为 drift。
			if curVal != 0 {
				item.DriftPercent = 100
				item.Detected = true
			}
		}
		items = append(items, item)
	}
	return items
}
