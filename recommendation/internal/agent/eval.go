// Package agent eval.go — EvalAgent 离线评估（Task 11.11）。
//
// 职责：执行离线评估用例，收集 metrics（NDCG@K/Precision@K/Recall@K/CTR/Latency/Cost），
// 可选 LLM Judge（rubric-based），行为漂移检测（对比基线 metrics 超阈值标记 drift）。
//
// 二开扩展点：
//   - 替换 LLMClient：接入自研/第三方 LLM 做 Judge
//   - 替换 ToolExecutor：接入自研工具框架执行 eval.run
//   - 扩展 metrics：在 computeCaseMetrics 中追加自定义指标
//   - 自定义漂移检测：覆盖 detectDrift 方法
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"sea/internal/domain"
)

// ----------------------------------------------------------------------------
// 评估类型定义
// ----------------------------------------------------------------------------

// EvalCase 评估用例。
//
// 字段语义：
//   - ID：用例唯一 ID
//   - Query：查询文本（推荐/搜索场景）
//   - Expected：期望命中的文章 ID 列表（golden set）
//   - Surface：曝光面（对应频道 Channel）
type EvalCase struct {
	// ID 用例 ID。
	ID string `json:"id"`
	// Query 查询文本。
	Query string `json:"query"`
	// Expected 期望命中的文章 ID 列表。
	Expected []string `json:"expected"`
	// Surface 曝光面（频道）。
	Surface string `json:"surface"`
}

// EvalConfig 评估配置。
type EvalConfig struct {
	// Metrics 评估指标列表（如 ndcg@10/precision@10/recall@10/ctr/latency/cost）。
	Metrics []string `json:"metrics"`
	// Rubric LLM Judge 评判标准（为空时跳过 LLM Judge）。
	Rubric string `json:"rubric"`
	// SampleSize 采样数量（0 表示全量评估）。
	SampleSize int `json:"sample_size"`
	// DriftThreshold 漂移阈值（默认 0.1，即 10%）。
	DriftThreshold float64 `json:"drift_threshold"`
}

// EvalReport 评估报告。
type EvalReport struct {
	// Cases 评估用例总数。
	Cases int `json:"cases"`
	// Passed 通过用例数（NDCG@K > 0 视为通过）。
	Passed int `json:"passed"`
	// Metrics 聚合指标（指标名 → 值）。
	Metrics map[string]float64 `json:"metrics"`
	// DriftDetected 是否检测到行为漂移。
	DriftDetected bool `json:"drift_detected"`
	// DriftMetrics 发生漂移的指标名列表。
	DriftMetrics []string `json:"drift_metrics"`
	// LLMJudgeScore LLM Judge 评分（0-1，未启用时为 0）。
	LLMJudgeScore float64 `json:"llm_judge_score"`
}

// evalRunResult eval.run 工具返回的评估结果。
type evalRunResult struct {
	// Ranked 排序后的文章 ID 列表。
	Ranked []string `json:"ranked"`
	// LatencyMS 延迟（毫秒）。
	LatencyMS int `json:"latency_ms"`
	// Cost 单次评估成本。
	Cost float64 `json:"cost"`
	// CTR 点击率。
	CTR float64 `json:"ctr"`
}

// ----------------------------------------------------------------------------
// EvalAgent 实现
// ----------------------------------------------------------------------------

// defaultEvalK 默认 K 值（NDCG@K/Precision@K/Recall@K 的 K）。
const defaultEvalK = 10

// defaultDriftThreshold 默认漂移阈值（10%）。
const defaultDriftThreshold = 0.1

// goodMetrics 高值为优的指标（漂移检测时 current < baseline 即 drift）。
var goodMetrics = map[string]bool{
	"ndcg@10":      true,
	"precision@10": true,
	"recall@10":    true,
	"ctr":          true,
}

// badMetrics 低值为优的指标（漂移检测时 current > baseline 即 drift）。
var badMetrics = map[string]bool{
	"latency": true,
	"cost":    true,
}

// EvalAgent 离线评估 Agent。
//
// 职责：
//   - 对每个 EvalCase 调用 tools.Invoke("eval.run") 执行评估
//   - 收集 metrics：NDCG@K / Precision@K / Recall@K / CTR / Latency / Cost
//   - 可选 LLM Judge：调用 llm.CompleteWithLogprobs 对结果打分（rubric-based）
//   - 行为漂移检测：对比基线 metrics，超过阈值标记 drift
//
// 二开扩展点：替换 LLMClient/ToolExecutor；扩展 metrics；覆盖 detectDrift。
type EvalAgent struct {
	// llm LLM 客户端（用于 LLM Judge，可为 nil）。
	llm LLMClient
	// tools 工具执行器（用于 eval.run，可为 nil）。
	tools ToolExecutor
	// opts Agent 选项。
	opts domain.AgentOptions
}

// 编译期断言：EvalAgent 实现 domain.Agent。
var _ domain.Agent = (*EvalAgent)(nil)

// NewEvalAgent 构造 EvalAgent。
// llm LLM 客户端；tools 工具执行器；opts Agent 选项。返回 domain.Agent 接口。
func NewEvalAgent(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &EvalAgent{llm: llm, tools: tools, opts: opts}
}

// Name 返回 Agent 名称。
func (a *EvalAgent) Name() string { return "eval" }

// Run 实现 domain.Agent.Run。
//
// 流程：
//  1. 从 input.State["eval_cases"] 读取 []EvalCase
//  2. 从 input.State["eval_config"] 读取 EvalConfig
//  3. 对每个 case 调用 tools.Invoke("eval.run", caseInput) 执行评估
//  4. 收集 metrics：NDCG@K / Precision@K / Recall@K / CTR / Latency / Cost
//  5. 可选 LLM Judge：调用 llm.CompleteWithLogprobs 对结果打分（rubric-based）
//  6. 行为漂移检测：对比基线 metrics（input.State["eval_baseline"]），超过阈值标记 drift
//  7. State 写入 eval_report（EvalReport）；Result 写入 EvalReport
func (a *EvalAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	trace := make([]string, 0, 3)

	cases := extractEvalCases(input.State["eval_cases"])
	cfg := extractEvalConfig(input.State["eval_config"])
	if cfg.DriftThreshold <= 0 {
		cfg.DriftThreshold = defaultDriftThreshold
	}

	// 应用 SampleSize 采样。
	if cfg.SampleSize > 0 && cfg.SampleSize < len(cases) {
		cases = cases[:cfg.SampleSize]
	}

	report := EvalReport{
		Cases:   len(cases),
		Metrics: make(map[string]float64),
	}

	// 3-4. 对每个 case 调用 tools.Invoke("eval.run") 并收集 metrics。
	results := make([]evalRunResult, 0, len(cases))
	passed := 0
	for _, c := range cases {
		result, err := a.runCase(ctx, c)
		if err != nil {
			results = append(results, evalRunResult{})
			continue
		}
		results = append(results, result)
		// NDCG@K > 0 视为通过。
		if NDCGAtK(result.Ranked, c.Expected, defaultEvalK) > 0 {
			passed++
		}
	}
	report.Passed = passed
	trace = append(trace, "eval.run")

	// 聚合 metrics。
	aggregateMetrics(&report, cases, results)

	// 5. 可选 LLM Judge。
	if a.llm != nil && cfg.Rubric != "" {
		score := a.llmJudge(ctx, cfg.Rubric, cases, results)
		report.LLMJudgeScore = score
		trace = append(trace, "eval.judge")
	}

	// 6. 行为漂移检测。
	baseline := extractBaseline(input.State["eval_baseline"])
	drifted, driftMetrics := detectDrift(report.Metrics, baseline, cfg.DriftThreshold)
	report.DriftDetected = drifted
	report.DriftMetrics = driftMetrics
	trace = append(trace, "eval.drift")

	state := copyState(input.State)
	state["eval_report"] = report
	return domain.AgentOutput{
		State:  state,
		Result: report,
		Trace:  trace,
	}, nil
}

// runCase 调用 tools.Invoke("eval.run") 执行单用例评估。
// ctx 上下文；c 评估用例。返回 evalRunResult 与 error。
// tools 为 nil 时返回空结果与 nil error。
func (a *EvalAgent) runCase(ctx context.Context, c EvalCase) (evalRunResult, error) {
	if a.tools == nil {
		return evalRunResult{}, nil
	}
	input, _ := json.Marshal(c)
	raw, err := a.tools.Invoke(ctx, "eval.run", input)
	if err != nil {
		return evalRunResult{}, err
	}
	var result evalRunResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return evalRunResult{}, err
	}
	return result, nil
}

// aggregateMetrics 聚合 metrics 到 report。
// 计算 NDCG@K/Precision@K/Recall@K 的平均值，CTR/Latency/Cost 的聚合值。
func aggregateMetrics(report *EvalReport, cases []EvalCase, results []evalRunResult) {
	if len(cases) == 0 || len(results) == 0 {
		return
	}
	var ndcgSum, precisionSum, recallSum, ctrSum float64
	var latencySum, costSum float64
	count := 0
	for i, c := range cases {
		if i >= len(results) {
			break
		}
		r := results[i]
		ndcgSum += NDCGAtK(r.Ranked, c.Expected, defaultEvalK)
		precisionSum += PrecisionAtK(r.Ranked, c.Expected, defaultEvalK)
		recallSum += RecallAtK(r.Ranked, c.Expected, defaultEvalK)
		ctrSum += r.CTR
		latencySum += float64(r.LatencyMS)
		costSum += r.Cost
		count++
	}
	if count == 0 {
		return
	}
	report.Metrics["ndcg@10"] = ndcgSum / float64(count)
	report.Metrics["precision@10"] = precisionSum / float64(count)
	report.Metrics["recall@10"] = recallSum / float64(count)
	report.Metrics["ctr"] = ctrSum / float64(count)
	report.Metrics["latency"] = latencySum / float64(count) // 平均延迟（ms）
	report.Metrics["cost"] = costSum                        // 总成本
}

// llmJudge 调用 LLM CompleteWithLogprobs 对评估结果打分（rubric-based）。
// ctx 上下文；rubric 评判标准；cases 评估用例；results 评估结果。返回 0-1 分数。
func (a *EvalAgent) llmJudge(ctx context.Context, rubric string, cases []EvalCase, results []evalRunResult) float64 {
	if a.llm == nil || rubric == "" {
		return 0
	}
	prompt := fmt.Sprintf("根据 rubric 评估推荐系统质量。\nRubric: %s\n用例数: %d\n请返回 0-1 之间的分数（1 表示完美）。",
		rubric, len(cases))
	resp, logprobs, err := a.llm.CompleteWithLogprobs(ctx, prompt, 5)
	if err != nil {
		return 0
	}
	// 优先从响应文本解析分数。
	if score := parseScoreFromText(resp); score >= 0 {
		return score
	}
	// 解析失败时用 logprobs 平均置信度作为分数。
	if len(logprobs) > 0 {
		return avgLogprobConfidence(logprobs)
	}
	return 0
}

// detectDrift 行为漂移检测。
// 对比当前 metrics 与基线，超过阈值标记 drift。
// goodMetrics（高值为优）：current < baseline * (1 - threshold) → drift
// badMetrics（低值为优）：current > baseline * (1 + threshold) → drift
// current, baseline 指标 map；threshold 阈值（如 0.1 表示 10%）。
// 返回是否漂移与漂移指标名列表。
func detectDrift(current, baseline map[string]float64, threshold float64) (bool, []string) {
	if len(baseline) == 0 || threshold <= 0 {
		return false, nil
	}
	drifted := make([]string, 0)
	for metric, baseVal := range baseline {
		if baseVal == 0 {
			continue
		}
		curVal, ok := current[metric]
		if !ok {
			continue
		}
		relChange := (curVal - baseVal) / baseVal
		if goodMetrics[metric] {
			// 高值为优：下降超过阈值 → drift。
			if relChange < -threshold {
				drifted = append(drifted, metric)
			}
		} else if badMetrics[metric] {
			// 低值为优：上升超过阈值 → drift。
			if relChange > threshold {
				drifted = append(drifted, metric)
			}
		} else {
			// 未知方向：绝对变化超过阈值 → drift。
			if math.Abs(relChange) > threshold {
				drifted = append(drifted, metric)
			}
		}
	}
	return len(drifted) > 0, drifted
}

// ----------------------------------------------------------------------------
// 内置 metrics 实现
// ----------------------------------------------------------------------------

// NDCGAtK 计算 NDCG@K（Normalized Discounted Cumulative Gain）。
// ranked 排序后的文章 ID 列表；expected 期望命中的文章 ID 列表；k 截断位置。
// 返回 0-1 之间的 NDCG 值。
func NDCGAtK(ranked []string, expected []string, k int) float64 {
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

// PrecisionAtK 计算 Precision@K。
// ranked 排序后的文章 ID 列表；expected 期望命中的文章 ID 列表；k 截断位置。
// 返回 0-1 之间的 Precision 值。
func PrecisionAtK(ranked []string, expected []string, k int) float64 {
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

// RecallAtK 计算 Recall@K。
// ranked 排序后的文章 ID 列表；expected 期望命中的文章 ID 列表；k 截断位置。
// 返回 0-1 之间的 Recall 值。
func RecallAtK(ranked []string, expected []string, k int) float64 {
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

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// extractEvalCases 从 input.State["eval_cases"] 提取 []EvalCase。
// 支持 []EvalCase 与 []any（每项为 map[string]any）两种形式。
func extractEvalCases(raw any) []EvalCase {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []EvalCase:
		return v
	case []any:
		out := make([]EvalCase, 0, len(v))
		for _, item := range v {
			out = append(out, extractSingleCase(item))
		}
		return out
	}
	return nil
}

// extractSingleCase 从 any 提取 EvalCase。
func extractSingleCase(raw any) EvalCase {
	switch v := raw.(type) {
	case EvalCase:
		return v
	case map[string]any:
		c := EvalCase{
			ID:      getStringFromAny(v["id"]),
			Query:   getStringFromAny(v["query"]),
			Surface: getStringFromAny(v["surface"]),
		}
		if expected, ok := v["expected"].([]any); ok {
			c.Expected = make([]string, 0, len(expected))
			for _, e := range expected {
				if s, ok := e.(string); ok {
					c.Expected = append(c.Expected, s)
				}
			}
		}
		return c
	}
	return EvalCase{}
}

// extractEvalConfig 从 input.State["eval_config"] 提取 EvalConfig。
func extractEvalConfig(raw any) EvalConfig {
	if raw == nil {
		return EvalConfig{}
	}
	switch v := raw.(type) {
	case EvalConfig:
		return v
	case map[string]any:
		cfg := EvalConfig{
			Rubric:         getStringFromAny(v["rubric"]),
			DriftThreshold: getFloatFromAny(v["drift_threshold"]),
		}
		if metrics, ok := v["metrics"].([]any); ok {
			cfg.Metrics = make([]string, 0, len(metrics))
			for _, m := range metrics {
				if s, ok := m.(string); ok {
					cfg.Metrics = append(cfg.Metrics, s)
				}
			}
		}
		if ss, ok := v["sample_size"].(int); ok {
			cfg.SampleSize = ss
		} else if ssf, ok := v["sample_size"].(float64); ok {
			cfg.SampleSize = int(ssf)
		}
		return cfg
	}
	return EvalConfig{}
}

// extractBaseline 从 input.State["eval_baseline"] 提取基线 metrics。
// 支持 map[string]float64 与 map[string]any 两种形式。
func extractBaseline(raw any) map[string]float64 {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case map[string]float64:
		return v
	case map[string]any:
		out := make(map[string]float64, len(v))
		for k, val := range v {
			out[k] = getFloatFromAny(val)
		}
		return out
	}
	return nil
}

// parseScoreFromText 从文本中解析 0-1 之间的分数。
// 支持纯数字、含 "score: 0.85" 等格式。返回 -1 表示解析失败。
func parseScoreFromText(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return -1
	}
	// 直接解析纯数字。
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return clampScore(f)
	}
	// 尝试从文本中提取数字（如 "0.85" 或 "score: 0.85"）。
	fields := strings.Fields(text)
	for _, f := range fields {
		// 去除标点。
		f = strings.Trim(f, ".,;:!?")
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			return clampScore(v)
		}
	}
	return -1
}

// clampScore 将分数限制在 0-1 之间。
func clampScore(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		// 兼容 0-100 分制。
		if f > 1 && f <= 100 {
			return f / 100
		}
		return 1
	}
	return f
}

// avgLogprobConfidence 从 logprobs 计算平均置信度（exp(logprob) 的平均值）。
// logprobs 二维切片（每位置多个 LogprobEntry）。返回 0-1 之间的置信度。
func avgLogprobConfidence(logprobs [][]LogprobEntry) float64 {
	if len(logprobs) == 0 {
		return 0
	}
	sum := 0.0
	count := 0
	for _, entries := range logprobs {
		if len(entries) == 0 {
			continue
		}
		// 取每位置最高 logprob（概率最大的 token）。
		maxLP := entries[0].Logprob
		for _, e := range entries[1:] {
			if e.Logprob > maxLP {
				maxLP = e.Logprob
			}
		}
		sum += math.Exp(maxLP)
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
