// Package agent rerank.go — RerankAgent 大模型 + 自研重排（Task 11.5）。
//
// 该文件实现 RerankAgent：作为多 Agent 架构中的"大模型 + 自研工具"重排分支，
// 负责对召回候选执行多模型融合重排（自研 Cross-encoder + Two-tower + LambdaMART
// 通过 rerank.self_rerank 工具调用，LLM rerank 通过 rerank.llm 工具调用，A/B
// 分桶通过 rerank.ab_test 工具调用）。tools 为 nil 时降级为简单加权排序。
// 不直接 import trpc-agent-go / milvus，所有外部依赖通过 LLMClient / ToolExecutor
// interface 注入。
//
// 二开扩展点：
//   - 替换 ToolExecutor：实现该 interface 接入自研 rerank 服务（Cross-encoder/
//     Two-tower/LambdaMART）与外部 LLM rerank
//   - 扩展降级加权：在 weightedFallback 中追加业务特征权重
//   - 调整 A/B 分桶：通过 opts 配置分桶策略与流量占比
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"sea/internal/domain"
)

// rerankAgentName Agent 名称。
const rerankAgentName = "rerank"

// RerankAgent 大模型 + 自研重排 Agent。
//
// 职责：
//   - 从 input.State["candidates"] 读取 []domain.Candidate
//   - 调用 rerank.self_rerank 工具执行自研 rerank（Cross-encoder + Two-tower + LambdaMART）
//   - 可选调用 rerank.llm 工具执行 LLM rerank（融合自研 + LLM）
//   - 可选调用 rerank.ab_test 工具获取 A/B 分桶状态
//   - tools 为 nil 时降级为简单加权（按 candidate.Scores 加权求和排序）
//
// 二开扩展点：替换 ToolExecutor / 扩展 weightedFallback / 调整 A/B 分桶。
type RerankAgent struct {
	llm   LLMClient
	tools ToolExecutor
	opts  domain.AgentOptions
}

// NewRerankAgent 构造 RerankAgent，返回 domain.Agent 接口。
// llm LLM 客户端；tools 工具执行器（可为 nil，nil 时降级为加权排序）；opts Agent 选项。
func NewRerankAgent(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &RerankAgent{llm: llm, tools: tools, opts: opts}
}

// Name 返回 Agent 名称 "rerank"。
func (a *RerankAgent) Name() string { return rerankAgentName }

// Run 执行重排主流程。
//
// 流程：
//  1. 从 input.State["candidates"] 读取 []domain.Candidate
//  2. 调用 rerank.self_rerank 工具执行自研 rerank
//  3. 可选调用 rerank.llm 工具执行 LLM rerank（融合自研 + LLM）
//  4. 可选调用 rerank.ab_test 工具获取 A/B 分桶状态
//  5. tools 为 nil 时降级为简单加权排序（按 candidate.Scores 加权求和）
//  6. State 写入 rerank_items（[]domain.Candidate 排序后）；Result 写入排序后列表；
//     Trace 追加 "rerank.self"/"rerank.llm"/"rerank.ab_test"
func (a *RerankAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	if a == nil {
		return domain.AgentOutput{}, fmt.Errorf("rerank agent: nil agent")
	}
	candidates := extractCandidates(input)
	trace := make([]string, 0, 3)

	// tools 为 nil 时降级为加权排序。
	if a.tools == nil {
		sorted := weightedFallback(candidates)
		trace = append(trace, "rerank.self")
		return domain.AgentOutput{
			State: map[string]any{
				"rerank_items": sorted,
			},
			Result: sorted,
			Trace:  trace,
		}, nil
	}

	// 1. 调用 rerank.self_rerank 工具执行自研 rerank。
	reranked, selfErr := a.invokeRerank(ctx, "rerank.self_rerank", candidates)
	trace = append(trace, "rerank.self")
	if selfErr != nil {
		// 自研失败 → 降级加权排序，仍可继续 LLM rerank。
		reranked = weightedFallback(candidates)
	}

	// 2. 可选调用 rerank.llm 工具执行 LLM rerank（融合自研 + LLM）。
	if a.llm != nil {
		llmReranked, llmErr := a.invokeRerank(ctx, "rerank.llm", reranked)
		if llmErr == nil && llmReranked != nil {
			reranked = llmReranked
		}
		trace = append(trace, "rerank.llm")
	}

	// 3. 可选调用 rerank.ab_test 工具获取 A/B 分桶状态（不修改候选，仅记录 trace）。
	if _, abErr := a.invokeABTest(ctx, reranked); abErr == nil {
		trace = append(trace, "rerank.ab_test")
	}

	return domain.AgentOutput{
		State: map[string]any{
			"rerank_items": reranked,
		},
		Result: reranked,
		Trace:  trace,
	}, nil
}

// invokeRerank 调用 rerank 工具（self_rerank 或 llm），解析返回的候选列表。
func (a *RerankAgent) invokeRerank(ctx context.Context, tool string, candidates []domain.Candidate) ([]domain.Candidate, error) {
	input := map[string]any{
		"candidates": candidates,
		"topk":       len(candidates),
	}
	raw, err := a.tools.Invoke(ctx, tool, mustMarshal(input))
	if err != nil {
		return nil, fmt.Errorf("%s invoke: %w", tool, err)
	}
	return parseCandidates(raw)
}

// invokeABTest 调用 rerank.ab_test 工具获取 A/B 分桶状态。
func (a *RerankAgent) invokeABTest(ctx context.Context, candidates []domain.Candidate) (map[string]any, error) {
	input := map[string]any{
		"candidates": candidates,
	}
	raw, err := a.tools.Invoke(ctx, "rerank.ab_test", mustMarshal(input))
	if err != nil {
		return nil, fmt.Errorf("rerank.ab_test invoke: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse ab_test result: %w", err)
	}
	return out, nil
}

// parseCandidates 解析 rerank 工具返回的 JSON 为 Candidate 列表。
// 支持两种格式：{"candidates":[...]} 或直接 [...]。
func parseCandidates(raw json.RawMessage) ([]domain.Candidate, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// 优先尝试 {"candidates":[...]} 包装格式。
	var wrapped struct {
		Candidates []domain.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Candidates != nil {
		return wrapped.Candidates, nil
	}
	// 退化尝试直接 [...]。
	var cands []domain.Candidate
	if err := json.Unmarshal(raw, &cands); err != nil {
		return nil, fmt.Errorf("parse candidates: %w", err)
	}
	return cands, nil
}

// extractCandidates 从 AgentInput.State["candidates"] 读取 []domain.Candidate。
// 支持直接 []domain.Candidate 与 []map[string]any（JSON 反序列化场景）两种形态。
func extractCandidates(input domain.AgentInput) []domain.Candidate {
	if input.State == nil {
		return nil
	}
	v, ok := input.State["candidates"]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case []domain.Candidate:
		return vv
	case []any:
		// 通过 JSON 往返转换。
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

// weightedFallback 降级加权排序：按 candidate.Scores 加权求和降序排序。
// 用于 tools 为 nil 或自研 rerank 失败时的兜底。
func weightedFallback(candidates []domain.Candidate) []domain.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]domain.Candidate, len(candidates))
	copy(out, candidates)
	sort.SliceStable(out, func(i, j int) bool {
		return weightedScore(out[i]) > weightedScore(out[j])
	})
	return out
}

// weightedScore 计算候选的加权总分（对 Scores map 求和）。
// 缺省回退到 candidate.Score 字段。
func weightedScore(c domain.Candidate) float64 {
	if len(c.Scores) == 0 {
		return c.Score
	}
	sum := 0.0
	for _, v := range c.Scores {
		sum += v
	}
	return sum
}

// 编译期断言：RerankAgent 实现 domain.Agent。
var _ domain.Agent = (*RerankAgent)(nil)
