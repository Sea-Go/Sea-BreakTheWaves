// ============================================================================
// 该文件实现查询理解 IntentParser，输出 SearchIntent。
// 双路径：LLM 结构化输出（增强）+ 基于规则的兜底；LLM 失败回退规则。
//
// 职责：
//   - 意图分类：informational / navigational / transactional / comparative
//   - 实体识别：Author / IP / Tag / Title / Topic
//   - 时效意图：latest / within_7d / within_30d / ever
//
// 二开扩展点：
//   - 实现 LLMClient interface 接入自研 LLM（禁止直接 import trpc-agent-go）
//   - WithRuleFallbackEnabled(false) 关闭规则兜底（LLM 失败即返回错误）
//   - 替换 ruleUnderstand 内的关键词规则适配业务语义
//
// 注：LLMClient interface 在本文件定义，rewrite.go 复用同一 interface（同包）。
// ============================================================================

package search

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/domain"
)

// LLMClient LLM 客户端契约，调用 LLM 并按 JSON Schema 输出结构化结果。
// 二开：实现该 interface 接入 trpc-agent-go / OpenAI / DashScope 等，禁止在本包直接 import。
type LLMClient interface {
	// CompleteWithStructuredOutput 调用 LLM，prompt 为提示词，schema 为输出 JSON Schema。
	// 返回符合 schema 的 JSON 原文。
	CompleteWithStructuredOutput(ctx context.Context, prompt string, schema map[string]any) (json.RawMessage, error)
}

// QueryUnderstander 查询理解 interface。
type QueryUnderstander interface {
	// Understand 理解查询，返回搜索意图。
	Understand(ctx context.Context, query string) (domain.SearchIntent, error)
}

// IntentParser 查询理解实现，LLM 增强为主、规则为辅。
type IntentParser struct {
	llm          LLMClient
	ruleFallback bool
}

// IntentOption IntentParser 构造选项。
type IntentOption func(*IntentParser)

// WithRuleFallbackEnabled 设置是否启用规则兜底（默认启用）。
func WithRuleFallbackEnabled(enabled bool) IntentOption {
	return func(p *IntentParser) {
		p.ruleFallback = enabled
	}
}

// NewIntentParser 创建查询理解器。
// llm 可为 nil（此时仅走规则路径）；opts 为构造选项。
func NewIntentParser(llm LLMClient, opts ...IntentOption) *IntentParser {
	p := &IntentParser{
		llm:          llm,
		ruleFallback: true,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Understand 理解查询，实现 QueryUnderstander.Understand。
//
// 流程：
//  1. 若注入了 LLMClient，优先调用 LLM 结构化输出；
//  2. LLM 失败时，若启用规则兜底则回退 ruleUnderstand，否则返回错误；
//  3. 未注入 LLMClient 时直接走规则路径。
func (p *IntentParser) Understand(ctx context.Context, query string) (domain.SearchIntent, error) {
	if query == "" {
		return domain.SearchIntent{Label: "informational", TimeIntent: "ever"}, nil
	}
	if p.llm != nil {
		intent, err := p.understandWithLLM(ctx, query)
		if err == nil {
			return intent, nil
		}
		if !p.ruleFallback {
			return domain.SearchIntent{}, err
		}
	}
	return p.ruleUnderstand(query), nil
}

// understandWithLLM 调用 LLM 结构化输出意图。
func (p *IntentParser) understandWithLLM(ctx context.Context, query string) (domain.SearchIntent, error) {
	prompt := "分析以下搜索查询的意图，返回 JSON：意图标签(label)、时效意图(time_intent)、实体列表(entities)。\n查询：" + query
	raw, err := p.llm.CompleteWithStructuredOutput(ctx, prompt, intentSchema())
	if err != nil {
		return domain.SearchIntent{}, err
	}
	if len(raw) == 0 {
		return domain.SearchIntent{}, errors.New("llm 返回空结果")
	}
	var out struct {
		Label      string `json:"label"`
		TimeIntent string `json:"time_intent"`
		Entities   []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return domain.SearchIntent{}, err
	}
	intent := domain.SearchIntent{
		Label:      normalizeLabel(out.Label),
		TimeIntent: normalizeTimeIntent(out.TimeIntent),
	}
	for _, e := range out.Entities {
		if e.Name == "" {
			continue
		}
		intent.Entities = append(intent.Entities, domain.Entity{Name: e.Name, Type: e.Type})
	}
	return intent, nil
}

// intentSchema 返回意图结构化输出的 JSON Schema。
func intentSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{
				"type": "string",
				"enum": []string{"informational", "navigational", "transactional", "comparative"},
			},
			"time_intent": map[string]any{
				"type": "string",
				"enum": []string{"latest", "within_7d", "within_30d", "ever"},
			},
			"entities": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"type": map[string]any{"type": "string"},
					},
					"required": []string{"name", "type"},
				},
			},
		},
		"required": []string{"label", "time_intent", "entities"},
	}
}

// ruleUnderstand 基于规则的意图理解兜底。
//
// 关键词匹配：
//   - 时效：'最新/今天/本周/近期/刚刚' → latest；'最近一周/7天/近一周' → within_7d；
//     '最近一个月/30天/近一个月/本月' → within_30d；其余 → ever
//   - 意图：'vs/对比/区别/比较/哪个好' → comparative；
//     '怎么买/价格/购买/买/订购/下单' → transactional；
//     其余 → informational
//   - 实体：识别 "作者:xxx" → Author，"#tag#" → Tag
func (p *IntentParser) ruleUnderstand(query string) domain.SearchIntent {
	q := strings.ToLower(query)
	intent := domain.SearchIntent{Label: "informational", TimeIntent: "ever"}

	// 时效意图
	switch {
	case containsAny(q, "最新", "今天", "本周", "近期", "刚刚", "latest"):
		intent.TimeIntent = "latest"
	case containsAny(q, "最近一周", "7天", "七天内", "近一周", "一周内"):
		intent.TimeIntent = "within_7d"
	case containsAny(q, "最近一个月", "30天", "近一个月", "本月", "一个月内"):
		intent.TimeIntent = "within_30d"
	}

	// 意图标签
	switch {
	case containsAny(q, "vs", "对比", "区别", "比较", "哪个好", "哪一个好"):
		intent.Label = "comparative"
	case containsAny(q, "怎么买", "价格", "购买", "买", "订购", "下单", "哪里买"):
		intent.Label = "transactional"
	default:
		intent.Label = "informational"
	}

	intent.Entities = extractEntities(query)
	return intent
}

// containsAny 判断 s 是否包含任一子串。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// authorRe 识别 "作者:xxx" / "author:xxx" 形式的作者实体。
var authorRe = regexp.MustCompile(`(?:作者|author|Author)[:：]\s*([^\s,，#]+)`)

// tagRe 识别 "#tag#" / "#tag" 形式的标签实体。
var tagRe = regexp.MustCompile(`#([^#\s]+)#?`)

// extractEntities 基于规则的实体识别（作者 / 标签）。
func extractEntities(query string) []domain.Entity {
	var ents []domain.Entity
	if m := authorRe.FindStringSubmatch(query); m != nil {
		ents = append(ents, domain.Entity{Name: m[1], Type: "Author"})
	}
	for _, m := range tagRe.FindAllStringSubmatch(query, -1) {
		ents = append(ents, domain.Entity{Name: m[1], Type: "Tag"})
	}
	return ents
}

// normalizeLabel 归一化意图标签，非法值降级为 informational。
func normalizeLabel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "informational", "navigational", "transactional", "comparative":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "informational"
	}
}

// normalizeTimeIntent 归一化时效意图，非法值降级为 ever。
func normalizeTimeIntent(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "latest", "within_7d", "within_30d", "ever":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "ever"
	}
}
