// Package agent recall_planner.go — RecallPlannerAgent 召回规划 Agent。
//
// 该文件实现 RecallPlannerAgent：基于小模型决定召回源组合与权重。
// 从 State["intent"] 读取 Intent，调用 LLM 结构化输出 RecallPlan；
// LLM 失败时用规则兜底（默认 5 路等权重 0.2，complexity≥0.7 时强化 graph+cf）。
//
// 二开扩展点：
//   - 替换 LLMClient 接入自研 LLM
//   - 修改 ruleBasedPlan 调整规则兜底权重
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"sea/service/recommend/rpc/internal/domain"
)

// RecallSource 召回源类型。
type RecallSource string

const (
	// SourceContent 内容召回源。
	SourceContent RecallSource = "content"
	// SourceCF 协同过滤召回源。
	SourceCF RecallSource = "cf"
	// SourceGraph 图谱召回源。
	SourceGraph RecallSource = "graph"
	// SourceChannel 频道召回源。
	SourceChannel RecallSource = "channel"
	// SourceRule 规则召回源。
	SourceRule RecallSource = "rule"
)

// RecallPlan 召回规划结果。
type RecallPlan struct {
	// Sources 召回源列表。
	Sources []RecallSource
	// Weights 各召回源权重（key 为 RecallSource 字符串值）。
	Weights map[string]float64
}

// RecallPlannerAgent 召回规划 Agent，使用小模型决定召回源组合。
type RecallPlannerAgent struct {
	// llm LLM 客户端（小模型）。
	llm LLMClient
	// opts Agent 构造选项。
	opts domain.AgentOptions
}

// NewRecallPlannerAgent 创建 RecallPlannerAgent。
// llm LLM 客户端；tools 工具执行器（暂不使用）；opts 构造选项。
// 返回 domain.Agent 接口。
func NewRecallPlannerAgent(llm LLMClient, _ ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &RecallPlannerAgent{llm: llm, opts: opts}
}

// Name 返回 Agent 名称 "recall_planner"。
func (a *RecallPlannerAgent) Name() string {
	return "recall_planner"
}

// Run 执行召回规划。
// 从 input.State["intent"] 读取 Intent（若存在），调用 LLM 结构化输出 RecallPlan；
// LLM 失败时用规则兜底。State 写入 recall_plan，Result 写入 RecallPlan，Trace 追加 "recall.plan"。
func (a *RecallPlannerAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	intent, _ := readIntent(input.State)

	plan, llmOK := a.planWithLLM(ctx, input.Message, intent)
	if !llmOK {
		plan = ruleBasedPlan(intent)
	}

	// 复制 State 并写入 recall_plan，不修改原 State。
	state := make(map[string]any, len(input.State)+1)
	for k, v := range input.State {
		state[k] = v
	}
	state["recall_plan"] = plan

	return domain.AgentOutput{
		State:  state,
		Result: plan,
		Trace:  []string{"recall.plan"},
	}, nil
}

// planWithLLM 调用 LLM 结构化输出召回规划。
// 成功返回 RecallPlan 与 true，失败返回零值与 false。
func (a *RecallPlannerAgent) planWithLLM(ctx context.Context, query string, intent Intent) (RecallPlan, bool) {
	if a.llm == nil {
		return RecallPlan{}, false
	}
	schema := recallPlanSchema()
	prompt := fmt.Sprintf("根据查询与意图规划召回源组合与权重（content/cf/graph/channel/rule）。查询：%s，意图复杂度：%.2f", query, intent.Complexity)
	raw, err := a.llm.CompleteWithStructuredOutput(ctx, prompt, schema)
	if err != nil {
		return RecallPlan{}, false
	}
	plan, err := parseRecallPlanJSON(raw)
	if err != nil {
		return RecallPlan{}, false
	}
	return plan, true
}

// recallPlanSchema 返回 RecallPlan 的 JSON Schema。
func recallPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sources": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"weights": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "number"},
			},
		},
	}
}

// parseRecallPlanJSON 将 LLM 返回的 JSON 解析为 RecallPlan。
func parseRecallPlanJSON(raw json.RawMessage) (RecallPlan, error) {
	var dto struct {
		Sources []string           `json:"sources"`
		Weights map[string]float64 `json:"weights"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		return RecallPlan{}, err
	}
	sources := make([]RecallSource, 0, len(dto.Sources))
	for _, s := range dto.Sources {
		sources = append(sources, RecallSource(s))
	}
	if dto.Weights == nil {
		dto.Weights = map[string]float64{}
	}
	return RecallPlan{Sources: sources, Weights: dto.Weights}, nil
}

// ruleBasedPlan 规则兜底召回规划。
// 默认 5 路（content/cf/graph/channel/rule）等权重 0.2；
// intent.complexity≥0.7 时强化 graph+cf（graph 0.3, cf 0.25, content 0.2, channel 0.15, rule 0.1）。
func ruleBasedPlan(intent Intent) RecallPlan {
	sources := []RecallSource{SourceContent, SourceCF, SourceGraph, SourceChannel, SourceRule}
	if intent.Complexity >= 0.7 {
		return RecallPlan{
			Sources: sources,
			Weights: map[string]float64{
				string(SourceGraph):   0.3,
				string(SourceCF):      0.25,
				string(SourceContent): 0.2,
				string(SourceChannel): 0.15,
				string(SourceRule):    0.1,
			},
		}
	}
	return RecallPlan{
		Sources: sources,
		Weights: map[string]float64{
			string(SourceContent): 0.2,
			string(SourceCF):      0.2,
			string(SourceGraph):   0.2,
			string(SourceChannel): 0.2,
			string(SourceRule):    0.2,
		},
	}
}

// readIntent 从 State 读取 Intent。
// 返回 Intent 与是否找到。
func readIntent(state map[string]any) (Intent, bool) {
	if state == nil {
		return Intent{}, false
	}
	v, ok := state["intent"]
	if !ok {
		return Intent{}, false
	}
	intent, ok := v.(Intent)
	return intent, ok
}
