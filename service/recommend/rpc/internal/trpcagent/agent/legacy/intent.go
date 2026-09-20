// Package agent intent.go — IntentAgent 意图理解 Agent。
//
// 该文件实现 IntentAgent：基于小模型结构化输出解析用户查询意图，
// 输出 Intent（Label/Confidence/Signals/Complexity/Entities/TimeIntent）。
// Complexity 字段（float64）驱动 OrchestratorAgent 路由决策
// （<0.4→fast, 0.4≤medium<0.7→hybrid, ≥0.7→slow）。
// LLM 失败时用规则兜底，确保离线可用。
//
// 二开扩展点：
//   - 替换 LLMClient 接入自研 LLM
//   - 修改 ruleBasedIntent 调整规则兜底逻辑
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// Intent 意图理解结果（agent 包内部表示）。
// 与 domain.Intent 不同：Complexity 为 float64（驱动路径决策），Signals 为 []string。
type Intent struct {
	// Label 意图标签（informational/comparative/transactional/latest）。
	Label string
	// Confidence 置信度（0-1）。
	Confidence float64
	// Signals 意图信号列表。
	Signals []string
	// Complexity 复杂度（0-1），驱动 OrchestratorAgent 路由
	// （<0.4→fast, 0.4≤medium<0.7→hybrid, ≥0.7→slow）。
	Complexity float64
	// Entities 识别实体列表。
	Entities []domain.Entity
	// TimeIntent 时效意图（如 "today"/"latest"/""）。
	TimeIntent string
}

// IntentAgent 意图理解 Agent，使用小模型结构化输出。
type IntentAgent struct {
	// llm LLM 客户端（小模型）。
	llm LLMClient
	// opts Agent 构造选项。
	opts domain.AgentOptions
}

// NewIntentAgent 创建 IntentAgent。
// llm LLM 客户端；tools 工具执行器（暂不使用）；opts 构造选项。
// 返回 domain.Agent 接口。
func NewIntentAgent(llm LLMClient, _ ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &IntentAgent{llm: llm, opts: opts}
}

// Name 返回 Agent 名称 "intent"。
func (a *IntentAgent) Name() string {
	return "intent"
}

// Run 执行意图理解。
// 从 input.Message 取查询文本，调用 LLM 结构化输出 Intent；
// LLM 失败时用规则兜底。State 写入 intent 字段，Result 写入 Intent，Trace 追加 "intent.parse"。
func (a *IntentAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	query := input.Message

	intent, llmOK := a.parseWithLLM(ctx, query)
	if !llmOK {
		intent = ruleBasedIntent(query)
	}

	// 复制 State 并写入 intent，不修改原 State。
	state := make(map[string]any, len(input.State)+1)
	for k, v := range input.State {
		state[k] = v
	}
	state["intent"] = intent

	return domain.AgentOutput{
		State:  state,
		Result: intent,
		Trace:  []string{"intent.parse"},
	}, nil
}

// parseWithLLM 调用 LLM 结构化输出解析意图。
// 成功返回 Intent 与 true，失败返回零值与 false。
func (a *IntentAgent) parseWithLLM(ctx context.Context, query string) (Intent, bool) {
	if a.llm == nil {
		return Intent{}, false
	}
	schema := intentSchema()
	prompt := fmt.Sprintf("解析以下用户查询的意图，输出结构化 JSON（label/confidence/signals/complexity/entities/time_intent）。查询：%s", query)
	raw, err := a.llm.CompleteWithStructuredOutput(ctx, prompt, schema)
	if err != nil {
		return Intent{}, false
	}
	intent, err := parseIntentJSON(raw)
	if err != nil {
		return Intent{}, false
	}
	return intent, true
}

// intentSchema 返回 Intent 的 JSON Schema，约束 LLM 结构化输出。
func intentSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label":       map[string]any{"type": "string"},
			"confidence":  map[string]any{"type": "number"},
			"signals":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"complexity":  map[string]any{"type": "number"},
			"time_intent": map[string]any{"type": "string"},
			"entities": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"type": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// parseIntentJSON 将 LLM 返回的 JSON 解析为 Intent。
func parseIntentJSON(raw json.RawMessage) (Intent, error) {
	var dto struct {
		Label      string          `json:"label"`
		Confidence float64         `json:"confidence"`
		Signals    []string        `json:"signals"`
		Complexity float64         `json:"complexity"`
		TimeIntent string          `json:"time_intent"`
		Entities   []domain.Entity `json:"entities"`
	}
	if err := json.Unmarshal(raw, &dto); err != nil {
		return Intent{}, err
	}
	return Intent{
		Label:      dto.Label,
		Confidence: dto.Confidence,
		Signals:    dto.Signals,
		Complexity: dto.Complexity,
		Entities:   dto.Entities,
		TimeIntent: dto.TimeIntent,
	}, nil
}

// ruleBasedIntent 规则兜底意图解析。
// 关键词匹配：
//   - '最新'/'今天' → latest，complexity 0.5
//   - 'vs'/'对比'/'区别' → comparative（label），complexity 0.8
//   - '怎么买'/'购买' → transactional，complexity 0.3
//   - 其他 → informational，complexity 0.3
//
// 注意：complexity 匹配 'vs'/'对比'/'区别'，label 匹配 'vs'/'对比'。
func ruleBasedIntent(query string) Intent {
	q := strings.ToLower(query)
	var label, timeIntent string
	signals := make([]string, 0, 2)

	switch {
	case strings.Contains(q, "最新") || strings.Contains(q, "今天"):
		label = "latest"
		timeIntent = "today"
		signals = append(signals, "freshness")
	case strings.Contains(q, "vs") || strings.Contains(q, "对比"):
		label = "comparative"
		signals = append(signals, "comparison")
	case strings.Contains(q, "怎么买") || strings.Contains(q, "购买"):
		label = "transactional"
		signals = append(signals, "transaction")
	default:
		label = "informational"
	}

	complexity := 0.3
	switch {
	case strings.Contains(q, "vs") || strings.Contains(q, "对比") || strings.Contains(q, "区别"):
		complexity = 0.8
	case strings.Contains(q, "最新") || strings.Contains(q, "今天"):
		complexity = 0.5
	}

	return Intent{
		Label:      label,
		Confidence: 0.6,
		Signals:    signals,
		Complexity: complexity,
		Entities:   nil,
		TimeIntent: timeIntent,
	}
}
