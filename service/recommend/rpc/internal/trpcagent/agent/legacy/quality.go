// Package agent quality.go — QualityAgent Best-of-N + LLM Verifier（Task 11.6）。
//
// 该文件实现 QualityAgent：作为多 Agent 架构中的"质量评判"分支，负责对重排候选
// 执行批量质量评分（quality.score 工具）、LLM 裁判（quality.judge 工具）、
// Best-of-N pairwise 选择（基于 LLM logprobs）。tools 为 nil 时降级为 LLMClient
// 直接调 CompleteWithLogprobs 评分。最终按 RecommendConfig.QualityThreshold 过滤。
// 不直接 import trpc-agent-go / milvus，所有外部依赖通过 LLMClient / ToolExecutor
// interface 注入。
//
// 二开扩展点：
//   - 替换 ToolExecutor：实现该 interface 接入自研质量评分服务（JudgeAgent/CandidateAgent）
//   - 替换 LLMClient：实现该 interface 接入自研 LLM 做 Best-of-N 评判
//   - 调整阈值：通过 input.State["config"] 传入 RecommendConfig.QualityThreshold
//   - 扩展 Best-of-N：在 bestOfN 中追加 pairwise 比较策略
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"sea/service/recommend/rpc/internal/domain"
)

// qualityAgentName Agent 名称。
const qualityAgentName = "quality"

// defaultQualityThreshold 默认质量阈值（当 State 无 config 时使用）。
const defaultQualityThreshold = 0.6

// defaultBestOfN 默认 Best-of-N 数量（当 opts.MaxToolCalls ≤ 0 时使用）。
const defaultBestOfN = 3

// QualityAgent 质量评判 Agent。
//
// 职责：
//   - 从 input.State["rerank_items"] 读取 []domain.Candidate
//   - 调用 quality.score 工具批量评分
//   - 可选调用 quality.judge 工具 LLM 裁判
//   - 应用 RecommendConfig.QualityThreshold 过滤
//   - Best-of-N：对 top-N 候选用 LLM 生成 N 个评判，pairwise 选择最高质量
//   - tools 为 nil 时用 LLMClient.CompleteWithLogprobs 直接评分（降级）
//
// 二开扩展点：替换 ToolExecutor / LLMClient / 调整阈值与 Best-of-N 策略。
type QualityAgent struct {
	llm   LLMClient
	tools ToolExecutor
	opts  domain.AgentOptions
}

// NewQualityAgent 构造 QualityAgent，返回 domain.Agent 接口。
// llm LLM 客户端；tools 工具执行器（可为 nil，nil 时降级为 LLM 直接评分）；opts Agent 选项。
func NewQualityAgent(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &QualityAgent{llm: llm, tools: tools, opts: opts}
}

// Name 返回 Agent 名称 "quality"。
func (a *QualityAgent) Name() string { return qualityAgentName }

// Run 执行质量评判主流程。
//
// 流程：
//  1. 从 input.State["rerank_items"] 读取 []domain.Candidate
//  2. 调用 quality.score 工具批量评分
//  3. 可选调用 quality.judge 工具 LLM 裁判
//  4. 应用 RecommendConfig.QualityThreshold 过滤（从 input.State["config"] 读取，默认 0.6）
//  5. Best-of-N：对 top-N 候选用 LLM 生成 N 个评判，pairwise 选择最高质量
//  6. tools 为 nil 时用 LLMClient 直接调 CompleteWithLogprobs 评分（降级）
//  7. State 写入 quality_scores（map[string]float64）/quality_filtered（[]domain.Candidate）；
//     Result 写入过滤后列表；Trace 追加 "quality.score"/"quality.judge"/"quality.best_of_n"
func (a *QualityAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	if a == nil {
		return domain.AgentOutput{}, fmt.Errorf("quality agent: nil agent")
	}
	candidates := extractRerankItems(input)
	threshold := extractQualityThreshold(input)
	trace := make([]string, 0, 3)

	// 1. 批量评分。
	scores, scoreErr := a.scoreCandidates(ctx, candidates)
	trace = append(trace, "quality.score")
	if scoreErr != nil {
		// 评分失败 → 用候选原有 Score 兜底。
		scores = fallbackScores(candidates)
	}

	// 2. 可选 LLM 裁判（仅在 tools 与 llm 均可用时）。
	if a.tools != nil && a.llm != nil {
		judged, judgeErr := a.judgeWithLLM(ctx, candidates, scores)
		if judgeErr == nil && len(judged) > 0 {
			// 融合 judge 分数到 scores（取最大值）。
			for id, s := range judged {
				if cur, ok := scores[id]; !ok || s > cur {
					scores[id] = s
				}
			}
		}
		trace = append(trace, "quality.judge")
	}

	// 3. Best-of-N：对 top-N 候选用 LLM 生成 N 个评判，pairwise 选择最高质量。
	if a.llm != nil && len(candidates) > 0 {
		bestID := a.bestOfN(ctx, candidates, scores)
		if bestID != "" {
			// 提升 Best-of-N 选中的候选分数（确保它通过阈值）。
			if cur, ok := scores[bestID]; !ok || cur < threshold {
				scores[bestID] = threshold
			}
		}
		trace = append(trace, "quality.best_of_n")
	}

	// 4. 按阈值过滤。
	filtered := filterByThreshold(candidates, scores, threshold)

	return domain.AgentOutput{
		State: map[string]any{
			"quality_scores":   scores,
			"quality_filtered": filtered,
		},
		Result: filtered,
		Trace:  trace,
	}, nil
}

// scoreCandidates 调用 quality.score 工具批量评分。
// tools 为 nil 时降级为 LLMClient.CompleteWithLogprobs 评分。
func (a *QualityAgent) scoreCandidates(ctx context.Context, candidates []domain.Candidate) (map[string]float64, error) {
	if len(candidates) == 0 {
		return map[string]float64{}, nil
	}
	// tools 为 nil → 降级用 LLM logprobs 评分。
	if a.tools == nil {
		return a.scoreWithLLM(ctx, candidates)
	}
	input := map[string]any{
		"candidates": candidates,
	}
	raw, err := a.tools.Invoke(ctx, "quality.score", mustMarshal(input))
	if err != nil {
		return nil, fmt.Errorf("quality.score invoke: %w", err)
	}
	return parseQualityScores(raw)
}

// judgeWithLLM 调用 quality.judge 工具做 LLM 裁判，返回修正后的分数。
func (a *QualityAgent) judgeWithLLM(ctx context.Context, candidates []domain.Candidate, scores map[string]float64) (map[string]float64, error) {
	input := map[string]any{
		"candidates": candidates,
		"scores":     scores,
	}
	raw, err := a.tools.Invoke(ctx, "quality.judge", mustMarshal(input))
	if err != nil {
		return nil, fmt.Errorf("quality.judge invoke: %w", err)
	}
	return parseQualityScores(raw)
}

// bestOfN 对 top-N 候选用 LLM 生成 N 个评判，pairwise 选择最高质量。
// 返回选中的 ArticleID（空表示未选中）。
func (a *QualityAgent) bestOfN(ctx context.Context, candidates []domain.Candidate, scores map[string]float64) string {
	n := a.opts.MaxToolCalls
	if n <= 0 {
		n = defaultBestOfN
	}
	if n > len(candidates) {
		n = len(candidates)
	}
	if n <= 0 {
		return ""
	}
	// 取 top-N 候选（按已有分数降序）。
	topN := topNByScore(candidates, scores, n)
	if len(topN) == 0 {
		return ""
	}
	// 对 top-N 候选逐一调 LLM logprobs 评分，取分数最高者。
	bestID := ""
	bestScore := -1.0
	for _, c := range topN {
		s := a.llmScoreOne(ctx, c)
		if s > bestScore {
			bestScore = s
			bestID = c.ArticleID
		}
	}
	return bestID
}

// llmScoreOne 用 LLM logprobs 评分单个候选，返回 [0,1] 分数。
// 调 CompleteWithLogprobs，解析响应中 "yes"/"pass" 等 token 的概率作为通过概率。
func (a *QualityAgent) llmScoreOne(ctx context.Context, c domain.Candidate) float64 {
	if a.llm == nil {
		return 0
	}
	prompt := fmt.Sprintf("评估文章 %s 的质量是否通过（输出 yes/no）：", c.ArticleID)
	_, logprobs, err := a.llm.CompleteWithLogprobs(ctx, prompt, 5)
	if err != nil || len(logprobs) == 0 {
		return 0
	}
	// 取首位 top logprobs 中 "yes"/"pass"/"good" 等正向 token 的概率之和。
	for _, entries := range logprobs {
		for _, e := range entries {
			switch normalizeToken(e.Token) {
			case "yes", "pass", "good", "high", "ok":
				// logprob → probability。
				prob := e.Logprob
				if prob > 0 {
					prob = 0
				}
				return expApprox(prob)
			}
		}
	}
	return 0
}

// scoreWithLLM 降级路径：用 LLMClient.CompleteWithLogprobs 直接评分所有候选。
func (a *QualityAgent) scoreWithLLM(ctx context.Context, candidates []domain.Candidate) (map[string]float64, error) {
	scores := make(map[string]float64, len(candidates))
	for _, c := range candidates {
		scores[c.ArticleID] = a.llmScoreOne(ctx, c)
	}
	return scores, nil
}

// parseQualityScores 解析 quality.score / quality.judge 工具返回的 JSON。
// 支持格式：{"scores": {"article_id": 0.8}} 或直接 {"article_id": 0.8}。
func parseQualityScores(raw json.RawMessage) (map[string]float64, error) {
	if len(raw) == 0 {
		return map[string]float64{}, nil
	}
	// 优先尝试 {"scores": {...}} 包装格式。
	var wrapped struct {
		Scores map[string]float64 `json:"scores"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Scores != nil {
		return wrapped.Scores, nil
	}
	// 退化尝试直接 {"article_id": 0.8}。
	var scores map[string]float64
	if err := json.Unmarshal(raw, &scores); err != nil {
		return nil, fmt.Errorf("parse quality scores: %w", err)
	}
	return scores, nil
}

// extractRerankItems 从 AgentInput.State["rerank_items"] 读取 []domain.Candidate。
// 支持 []domain.Candidate 与 []any（JSON 反序列化场景）两种形态。
func extractRerankItems(input domain.AgentInput) []domain.Candidate {
	if input.State == nil {
		return nil
	}
	v, ok := input.State["rerank_items"]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case []domain.Candidate:
		return vv
	case []any:
		b, err := json.Marshal(vv)
		if err != nil {
			return nil
		}
		var cands []domain.Candidate
		if err := json.Unmarshal(b, &cands); err != nil {
			return nil
		}
		return cands
	default:
		return nil
	}
}

// extractQualityThreshold 从 input.State["config"] 读取质量阈值。
// 支持 domain.RecommendConfig 与 map[string]any 两种形态；缺省返回 defaultQualityThreshold。
func extractQualityThreshold(input domain.AgentInput) float64 {
	if input.State == nil {
		return defaultQualityThreshold
	}
	v, ok := input.State["config"]
	if !ok || v == nil {
		return defaultQualityThreshold
	}
	switch vv := v.(type) {
	case domain.RecommendConfig:
		if vv.QualityThreshold > 0 {
			return vv.QualityThreshold
		}
	case map[string]any:
		if t, ok := vv["QualityThreshold"].(float64); ok && t > 0 {
			return t
		}
		if t, ok := vv["quality_threshold"].(float64); ok && t > 0 {
			return t
		}
	}
	return defaultQualityThreshold
}

// fallbackScores 评分失败兜底：用候选原有 Score 字段构造分数 map。
func fallbackScores(candidates []domain.Candidate) map[string]float64 {
	scores := make(map[string]float64, len(candidates))
	for _, c := range candidates {
		scores[c.ArticleID] = c.Score
	}
	return scores
}

// filterByThreshold 按阈值过滤候选：保留 scores[id] >= threshold 的候选。
// 保持原候选顺序。
func filterByThreshold(candidates []domain.Candidate, scores map[string]float64, threshold float64) []domain.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if s, ok := scores[c.ArticleID]; ok && s >= threshold {
			out = append(out, c)
		}
	}
	return out
}

// topNByScore 按分数降序取 top-N 候选。
func topNByScore(candidates []domain.Candidate, scores map[string]float64, n int) []domain.Candidate {
	sorted := make([]domain.Candidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		return scores[sorted[i].ArticleID] > scores[sorted[j].ArticleID]
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}

// normalizeToken 归一化 token：小写 + 去空白，便于匹配 yes/pass 等标签。
func normalizeToken(tok string) string {
	out := make([]rune, 0, len(tok))
	for _, r := range tok {
		if r == ' ' || r == '\t' || r == '\n' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r = r - 'A' + 'a'
		}
		out = append(out, r)
	}
	return string(out)
}

// expApprox 近似 exp(x)，用于 logprob → probability 转换。
// 输入 x ≤ 0（logprob 域），返回 [0,1]。直接使用 math.Exp 保证精度。
func expApprox(x float64) float64 {
	if x >= 0 {
		return 1
	}
	r := math.Exp(x)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// 编译期断言：QualityAgent 实现 domain.Agent。
var _ domain.Agent = (*QualityAgent)(nil)
