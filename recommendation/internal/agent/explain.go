// Package agent explain.go — ExplainAgent 大模型解释生成（Task 11.7）。
//
// 该文件实现 ExplainAgent：作为多 Agent 架构中的"解释生成"分支，负责将推荐/搜索
// 结果转换为自然语言解释（如"基于您对'旅游'的兴趣，为您推荐以下 10 篇文章..."）。
// 聚合 rerank_items/quality_scores/graph_knowledge/intent/recall_plan 五类上下文
// 构造 prompt，调用 LLM 生成解释；LLM 失败时用规则模板兜底。支持两种模式：
// recommend_explain（推荐解释）/ search_explain（搜索解释），由 State["explain_mode"] 决定。
// 不直接 import trpc-agent-go，所有外部依赖通过 LLMClient interface 注入。
//
// 二开扩展点：
//   - 替换 LLMClient：实现该 interface 接入自研/第三方 LLM 提升解释质量
//   - 自定义 prompt 模板：在 buildExplainPrompt 中按业务话术改写
//   - 扩展规则兜底：在 fallbackExplanation 中追加业务模板
//   - 接入解释缓存：同 user×article 短时不重复生成
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"sea/internal/domain"
)

// explainAgentName Agent 名称。
const explainAgentName = "explain"

// ExplainAgent 大模型解释生成 Agent。
//
// 职责：
//   - 从 input.State 读取 rerank_items/quality_scores/graph_knowledge/intent/recall_plan
//   - 构造 prompt（包含候选文章 + 用户意图 + 图谱知识 + 召回源 + 质量分）
//   - 调用 llm.Complete 生成自然语言解释
//   - LLM 失败时用规则兜底（拼模板："为您推荐 N 篇文章，主要来源：{sources}，平均质量分：{score}"）
//   - 支持两种模式：recommend_explain / search_explain，由 input.State["explain_mode"] 决定
//
// 二开扩展点：替换 LLMClient / 自定义 prompt 模板 / 扩展规则兜底 / 接入解释缓存。
type ExplainAgent struct {
	llm  LLMClient
	opts domain.AgentOptions
}

// NewExplainAgent 构造 ExplainAgent，返回 domain.Agent 接口。
// llm LLM 客户端；_ 工具执行器（解释 Agent 不使用工具）；opts Agent 选项。
func NewExplainAgent(llm LLMClient, _ ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &ExplainAgent{llm: llm, opts: opts}
}

// Name 返回 Agent 名称 "explain"。
func (a *ExplainAgent) Name() string { return explainAgentName }

// Run 执行解释生成主流程。
//
// 流程：
//  1. 从 input.State 读取 rerank_items/quality_scores/graph_knowledge/intent/recall_plan
//  2. 构造 prompt（包含候选文章 + 用户意图 + 图谱知识 + 召回源 + 质量分）
//  3. 调用 llm.Complete 生成自然语言解释
//  4. LLM 失败时用规则兜底
//  5. State 写入 explanation（string）；Result 写入解释文本；Trace 追加 "explain.generate"
func (a *ExplainAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	if a == nil {
		return domain.AgentOutput{}, fmt.Errorf("explain agent: nil agent")
	}
	mode := extractExplainMode(input)
	items := extractRerankItems(input)
	scores := extractQualityScores(input)
	gk := extractGraphKnowledge(input)
	intent := extractIntentStr(input)
	sources := extractRecallSources(input)
	avgScore := avgScore(scores)

	// 1. 构造 prompt。
	prompt := buildExplainPrompt(mode, items, scores, gk, intent, sources, avgScore)

	// 2. 调用 LLM 生成解释；失败走规则兜底。
	explanation, err := a.generateExplanation(ctx, prompt, mode, items, sources, avgScore)
	trace := []string{"explain.generate"}

	return domain.AgentOutput{
		State: map[string]any{
			"explanation": explanation,
		},
		Result: explanation,
		Trace:  trace,
	}, err
}

// generateExplanation 调用 LLM 生成解释，失败走规则兜底。
// 返回解释文本与 error（规则兜底始终成功，error 为 nil）。
func (a *ExplainAgent) generateExplanation(ctx context.Context, prompt, mode string, items []domain.Candidate, sources []string, avgScore float64) (string, error) {
	if a.llm == nil {
		return fallbackExplanation(mode, items, sources, avgScore), nil
	}
	opts := LLMOptions{
		Temperature: 0.7,
		MaxTokens:   512,
		EnableCache: a.opts.PromptCacheEnabled,
	}
	if a.opts.Model != "" {
		opts.Model = a.opts.Model
	}
	out, err := a.llm.Complete(ctx, prompt, opts)
	if err != nil || strings.TrimSpace(out) == "" {
		// LLM 失败 → 规则兜底。
		return fallbackExplanation(mode, items, sources, avgScore), nil
	}
	return strings.TrimSpace(out), nil
}

// buildExplainPrompt 构造 LLM 解释生成的 prompt。
// mode 解释模式（recommend_explain/search_explain）；items 候选列表；
// scores 质量分；gk 图谱知识；intent 意图；sources 召回源；avgScore 平均质量分。
func buildExplainPrompt(mode string, items []domain.Candidate, scores map[string]float64, gk domain.GraphKnowledge, intent string, sources []string, avgScore float64) string {
	var sb strings.Builder
	if mode == "search_explain" {
		sb.WriteString("你是搜索结果解释专家。基于以下搜索上下文生成自然语言解释，说明搜索结果的相关性与排序依据。\n\n")
	} else {
		sb.WriteString("你是推荐解释专家。基于以下推荐上下文生成自然语言解释，说明推荐理由与候选文章来源。\n\n")
	}
	// 候选文章。
	sb.WriteString(fmt.Sprintf("候选文章数：%d\n", len(items)))
	if len(items) > 0 {
		sb.WriteString("候选文章 ID 列表：")
		ids := make([]string, 0, len(items))
		for _, c := range items {
			ids = append(ids, c.ArticleID)
		}
		sb.WriteString(strings.Join(ids, ", "))
		sb.WriteString("\n")
	}
	// 用户意图。
	if intent != "" {
		sb.WriteString(fmt.Sprintf("用户意图：%s\n", intent))
	}
	// 图谱知识。
	if gk.Cypher != "" || len(gk.Entities) > 0 || len(gk.Articles) > 0 {
		sb.WriteString(fmt.Sprintf("图谱知识：实体 %d 个，关联文章 %d 篇，作者 %d 位，IP %d 个\n",
			len(gk.Entities), len(gk.Articles), len(gk.Authors), len(gk.IPs)))
	}
	// 召回源。
	if len(sources) > 0 {
		sb.WriteString(fmt.Sprintf("召回源：%s\n", strings.Join(sources, ", ")))
	}
	// 质量分。
	if len(scores) > 0 {
		sb.WriteString(fmt.Sprintf("平均质量分：%.2f\n", avgScore))
	}
	// 输出要求。
	if mode == "search_explain" {
		sb.WriteString("\n请生成一段自然语言搜索结果解释（不超过 200 字），说明结果与查询的匹配度与排序依据。")
	} else {
		sb.WriteString("\n请生成一段自然语言推荐解释（不超过 200 字），说明推荐理由与候选文章的主要来源。")
	}
	return sb.String()
}

// fallbackExplanation 规则兜底解释生成。
// 模板：推荐模式 → "为您推荐 N 篇文章，主要来源：{sources}，平均质量分：{score}"
//
//	搜索模式 → "为您找到 N 篇相关文章，主要来源：{sources}，平均质量分：{score}"
func fallbackExplanation(mode string, items []domain.Candidate, sources []string, avgScore float64) string {
	n := len(items)
	srcStr := "默认"
	if len(sources) > 0 {
		srcStr = strings.Join(sources, ", ")
	}
	if mode == "search_explain" {
		if n == 0 {
			return "未找到相关文章，请尝试更换搜索词。"
		}
		return fmt.Sprintf("为您找到 %d 篇相关文章，主要来源：%s，平均质量分：%.2f", n, srcStr, avgScore)
	}
	if n == 0 {
		return "暂无推荐文章。"
	}
	return fmt.Sprintf("为您推荐 %d 篇文章，主要来源：%s，平均质量分：%.2f", n, srcStr, avgScore)
}

// extractExplainMode 从 State["explain_mode"] 读取解释模式。
// 缺省返回 "recommend_explain"。
func extractExplainMode(input domain.AgentInput) string {
	if input.State == nil {
		return "recommend_explain"
	}
	if m, ok := input.State["explain_mode"].(string); ok && m != "" {
		return m
	}
	return "recommend_explain"
}

// extractQualityScores 从 State["quality_scores"] 读取 map[string]float64。
// 支持 map[string]float64 与 map[string]any（JSON 反序列化场景）两种形态。
func extractQualityScores(input domain.AgentInput) map[string]float64 {
	if input.State == nil {
		return nil
	}
	v, ok := input.State["quality_scores"]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case map[string]float64:
		return vv
	case map[string]any:
		out := make(map[string]float64, len(vv))
		for k, val := range vv {
			if f, ok := val.(float64); ok {
				out[k] = f
			}
		}
		return out
	}
	return nil
}

// extractGraphKnowledge 从 State["graph_knowledge"] 读取 domain.GraphKnowledge。
// 支持 domain.GraphKnowledge 与 map[string]any（JSON 反序列化场景）两种形态。
func extractGraphKnowledge(input domain.AgentInput) domain.GraphKnowledge {
	if input.State == nil {
		return domain.GraphKnowledge{}
	}
	v, ok := input.State["graph_knowledge"]
	if !ok || v == nil {
		return domain.GraphKnowledge{}
	}
	switch vv := v.(type) {
	case domain.GraphKnowledge:
		return vv
	case *domain.GraphKnowledge:
		if vv != nil {
			return *vv
		}
		return domain.GraphKnowledge{}
	case map[string]any:
		// 通过 JSON 往返转换。
		b, err := json.Marshal(vv)
		if err != nil {
			return domain.GraphKnowledge{}
		}
		var gk domain.GraphKnowledge
		if err := json.Unmarshal(b, &gk); err != nil {
			return domain.GraphKnowledge{}
		}
		return gk
	}
	return domain.GraphKnowledge{}
}

// extractIntentStr 从 State["intent"] 读取意图描述字符串。
// 支持 agent.Intent（包内类型）、domain.Intent、map[string]any 与 string 多种形态。
func extractIntentStr(input domain.AgentInput) string {
	if input.State == nil {
		return ""
	}
	v, ok := input.State["intent"]
	if !ok || v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case Intent:
		return formatIntent(vv)
	case *Intent:
		if vv != nil {
			return formatIntent(*vv)
		}
	case domain.Intent:
		return formatDomainIntent(vv)
	case *domain.Intent:
		if vv != nil {
			return formatDomainIntent(*vv)
		}
	case map[string]any:
		if label, ok := vv["Label"].(string); ok && label != "" {
			return label
		}
		if label, ok := vv["label"].(string); ok && label != "" {
			return label
		}
	}
	return ""
}

// extractRecallSources 从 State["recall_plan"] 读取召回源列表。
// 支持 agent.RecallPlan（包内类型）与 map[string]any 多种形态。
func extractRecallSources(input domain.AgentInput) []string {
	if input.State == nil {
		return nil
	}
	v, ok := input.State["recall_plan"]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case RecallPlan:
		return formatRecallSources(vv)
	case *RecallPlan:
		if vv != nil {
			return formatRecallSources(*vv)
		}
	case map[string]any:
		// 尝试从 sources 字段提取。
		if srcs, ok := vv["Sources"].([]any); ok {
			return anySliceToStrings(srcs)
		}
		if srcs, ok := vv["sources"].([]any); ok {
			return anySliceToStrings(srcs)
		}
	}
	return nil
}

// formatIntent 格式化 agent.Intent 为可读字符串。
func formatIntent(i Intent) string {
	parts := make([]string, 0, 3)
	if i.Label != "" {
		parts = append(parts, i.Label)
	}
	if i.TimeIntent != "" {
		parts = append(parts, "时效:"+i.TimeIntent)
	}
	if i.Complexity > 0 {
		parts = append(parts, fmt.Sprintf("复杂度:%.2f", i.Complexity))
	}
	return strings.Join(parts, " ")
}

// formatDomainIntent 格式化 domain.Intent 为可读字符串。
func formatDomainIntent(i domain.Intent) string {
	parts := make([]string, 0, 3)
	if i.Label != "" {
		parts = append(parts, i.Label)
	}
	if i.TimeIntent != "" {
		parts = append(parts, "时效:"+i.TimeIntent)
	}
	if i.Complexity != "" {
		parts = append(parts, "复杂度:"+i.Complexity)
	}
	return strings.Join(parts, " ")
}

// formatRecallSources 格式化 RecallPlan 的召回源为字符串列表。
func formatRecallSources(p RecallPlan) []string {
	out := make([]string, 0, len(p.Sources))
	for _, s := range p.Sources {
		out = append(out, string(s))
	}
	return out
}

// anySliceToStrings 将 []any 转为 []string（仅保留 string 元素）。
func anySliceToStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// avgScore 计算质量分 map 的平均值。
func avgScore(scores map[string]float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	return sum / float64(len(scores))
}

// 编译期断言：ExplainAgent 实现 domain.Agent。
var _ domain.Agent = (*ExplainAgent)(nil)
