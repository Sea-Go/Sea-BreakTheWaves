package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sea/internal/domain"
)

// ============================================================================
// 该文件实现候选质量评判 Agent（CandidateAgent）。
//
// CandidateAgent 是 spec.md 双路径架构中 QualityJudger 的候选 Agent：
//   - 输入：ArticleInput（文章 ID/标题/正文/作者/标签/发布时间）
//   - 调用：LLMClient.Complete，WithStructuredOutputJSONSchema 约束输出为
//     ArticleQuality JSON
//   - 输出：domain.ArticleQuality（6 维分数 + Overall + Grade）
//
// 与裁判 Agent（JudgeAgent）的协作：CandidateAgent 给出 6 维评分初稿，
// JudgeAgent 基于文章 + 候选评分 + rubric，开启 logprobs 获取 A-T 标签概率
// 分布，修正候选结果并给出置信度。
//
// LLMClient 抽象说明：
//   - 用 interface 而非直接 import trpc-agent-go，确保 internal/quality 包
//     离线可编译（stdlib only + domain 包）
//   - 二开方在生产环境注入 trpc-agent-go 适配器（将 LLMOptions 映射到
//     WithStructuredOutputJSONSchema/WithLogprobs/WithTopLogprobs/
//     WithTemperature 等 trpc-agent-go Option）
//
// 二开扩展点：
//   - 替换 LLMClient：实现该 interface 接入自研/第三方 LLM
//   - 替换 prompt 模板：覆盖 buildPrompt 方法或注入 PromptTemplate
//   - 替换 JSON Schema：覆盖 articleQualitySchema 函数
// ============================================================================

// LLMClient LLM 调用抽象，用于解耦 internal/quality 与 trpc-agent-go。
//
// 二开方在生产环境实现该 interface，将 LLMOptions 映射到 trpc-agent-go 的
// LLMAgent Option：
//   - StructuredOutputJSONSchema → WithStructuredOutputJSONSchema(schema)
//   - Logprobs → WithLogprobs(true)
//   - TopLogprobs → WithTopLogprobs(n)
//   - Temperature → WithTemperature(t)
//
// 离线/测试环境可用 mock 实现返回固定 JSON，验证 CandidateAgent/JudgeAgent
// 的 prompt 构造与输出解析逻辑。
type LLMClient interface {
	// Complete 执行一次 LLM 调用。
	// ctx 上下文；prompt 输入提示词；opts LLM 选项（结构化输出/logprobs 等）。
	// 返回 LLM 输出文本（结构化输出场景为 JSON 字符串；logprobs 场景为
	// logprobs JSON 字符串，由调用方解析）与 error。
	Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error)
}

// LLMOptions LLM 调用选项，映射 trpc-agent-go 的 LLMAgent Option。
//
// 字段语义对齐 trpc-agent-go：
//   - StructuredOutputJSONSchema：约束 LLM 输出符合该 JSON Schema 的结构
//     （对应 WithStructuredOutputJSONSchema），用于 CandidateAgent 强制输出
//     ArticleQuality JSON
//   - Logprobs：是否返回 token logprobs（对应 WithLogprobs），用于 JudgeAgent
//     获取 A-T 标签概率分布
//   - TopLogprobs：每个位置返回的 top-N token logprobs（对应 WithTopLogprobs）
//   - Temperature：采样温度（对应 WithTemperature），评分场景建议 0.0 保证稳定性
type LLMOptions struct {
	// StructuredOutputJSONSchema JSON Schema 字符串，约束输出结构。
	StructuredOutputJSONSchema string
	// Logprobs 是否返回 logprobs。
	Logprobs bool
	// TopLogprobs 每位置返回 top-N logprobs（如 20）。
	TopLogprobs int
	// Temperature 采样温度（0-2，评分场景建议 0.0）。
	Temperature float64
}

// ArticleInput 候选评判的输入文章，由调用方从 Candidate/数据库构造。
//
// 字段对齐 domain.Candidate（ArticleID/Score/Source/...）与图谱 Article 节点
// 属性，但不直接依赖以保持 internal/quality 包独立可编译。
type ArticleInput struct {
	// ID 文章 ID（对应 domain.Candidate.ArticleID）。
	ID string
	// Title 文章标题。
	Title string
	// Content 文章正文（用于 LLM 评判；调用方负责截断到模型上下文上限）。
	Content string
	// AuthorID 作者 ID（用于权威性维度评分）。
	AuthorID string
	// Tags 文章标签列表。
	Tags []string
	// PublishedAt 发布时间（用于新鲜度维度评分）。
	PublishedAt time.Time
}

// CandidateAgent 候选质量评判 Agent。
//
// 职责：对单篇/批量文章调用 LLM（结构化输出）生成 6 维质量评分初稿，
// 供 JudgeAgent 进一步裁判修正。实现 spec.md 中 QualityJudger 的候选阶段。
//
// 不直接实现 domain.QualityJudger interface（该 interface 入参为
// QualityRequest，本 Agent 入参为 ArticleInput，便于批量与离线场景使用）；
// 调用方可在 adapter 层将 ArticleInput 与 QualityRequest 互转。
type CandidateAgent struct {
	// llm LLM 客户端（结构化输出）。
	llm LLMClient
	// rubrics 评分标准集合（注入 prompt 约束 LLM 评分语义）。
	rubrics *RubricSet
}

// NewCandidateAgent 构造候选质量评判 Agent。
// llm LLM 客户端；rubrics 评分标准集合（可为 nil，使用 DefaultRubrics）。
func NewCandidateAgent(llm LLMClient, rubrics *RubricSet) *CandidateAgent {
	if rubrics == nil {
		rubrics = DefaultRubrics()
	}
	return &CandidateAgent{llm: llm, rubrics: rubrics}
}

// Evaluate 评判单篇文章质量。
// ctx 上下文；article 待评判文章。
// 返回 domain.ArticleQuality（含 6 维分数 + Overall + Grade）与 error。
//
// 流程：
//  1. 构造 prompt（含文章内容 + rubric 评分标准）
//  2. 调用 LLM（WithStructuredOutputJSONSchema 约束输出 ArticleQuality JSON）
//  3. 解析结构化输出为 ArticleQuality，填充 ArticleID 与 Grade
//
// 二开扩展点：可覆盖 buildPrompt / articleQualitySchema 自定义 prompt 与 schema。
func (a *CandidateAgent) Evaluate(ctx context.Context, article ArticleInput) (domain.ArticleQuality, error) {
	if a == nil || a.llm == nil {
		return domain.ArticleQuality{}, fmt.Errorf("CandidateAgent 或 LLMClient 未初始化")
	}
	prompt := a.buildPrompt(article)
	opts := LLMOptions{
		StructuredOutputJSONSchema: articleQualitySchema(),
		Temperature:                0.0,
	}
	resp, err := a.llm.Complete(ctx, prompt, opts)
	if err != nil {
		return domain.ArticleQuality{}, fmt.Errorf("候选 Agent LLM 调用失败: %w", err)
	}
	q, err := parseArticleQuality(resp)
	if err != nil {
		return domain.ArticleQuality{}, fmt.Errorf("解析候选 Agent 结构化输出失败: %w", err)
	}
	q.ArticleID = article.ID
	// 始终基于 Overall 重算 Grade，避免 LLM 输出漂移。
	q.Grade = q.ToGrade()
	return q, nil
}

// BatchEvaluate 批量评判多篇文章质量。
// ctx 上下文；articles 待评判文章列表。
// 返回 ArticleQuality 列表（与 articles 顺序一致）与 error。
//
// 实现说明：当前为顺序调用 LLM（避免并发触发限流）；如需并发可在此处启用
// goroutine 池（注意 LLM 限流与上下文取消传播）。
// 任一文章评判失败立即返回 error（已评判结果丢弃），调用方可基于 error
// 决定重试或降级。
func (a *CandidateAgent) BatchEvaluate(ctx context.Context, articles []ArticleInput) ([]domain.ArticleQuality, error) {
	if a == nil || a.llm == nil {
		return nil, fmt.Errorf("CandidateAgent 或 LLMClient 未初始化")
	}
	results := make([]domain.ArticleQuality, 0, len(articles))
	for _, art := range articles {
		q, err := a.Evaluate(ctx, art)
		if err != nil {
			return nil, fmt.Errorf("批量评判文章 %q 失败: %w", art.ID, err)
		}
		results = append(results, q)
	}
	return results, nil
}

// buildPrompt 构造候选评判 prompt，包含文章元信息、正文与 rubric 评分标准。
// 二开扩展点：业务方可覆盖此方法注入自定义 prompt 模板（如频道特定 prompt）。
func (a *CandidateAgent) buildPrompt(article ArticleInput) string {
	var sb strings.Builder
	sb.WriteString("你是文章质量候选评判 Agent。请依据下方 rubric 评分标准，对文章进行 6 维质量评分。\n")
	sb.WriteString("输出必须严格符合 JSON Schema（仅含字段 authority/depth/freshness/completeness/readability/citation/overall），\n")
	sb.WriteString("各维度分数 0-1，overall 为 6 维加权综合分（权重见 rubric）。不要输出任何额外文本。\n\n")

	sb.WriteString("【Rubric 评分标准】\n")
	for _, r := range a.rubrics.Rubrics {
		if r.Dimension == DimensionAccuracy {
			// accuracy 为裁判元维度，候选阶段不输出。
			continue
		}
		fmt.Fprintf(&sb, "- %s（权重 %.2f）：%s\n", r.Dimension, r.Weight, r.Description)
		for _, c := range r.Criteria {
			fmt.Fprintf(&sb, "    %.1f = %s\n", c.Score, c.Description)
		}
	}

	sb.WriteString("\n【待评判文章】\n")
	fmt.Fprintf(&sb, "文章 ID：%s\n", article.ID)
	fmt.Fprintf(&sb, "标题：%s\n", article.Title)
	fmt.Fprintf(&sb, "作者 ID：%s\n", article.AuthorID)
	if len(article.Tags) > 0 {
		fmt.Fprintf(&sb, "标签：%s\n", strings.Join(article.Tags, ", "))
	}
	if !article.PublishedAt.IsZero() {
		fmt.Fprintf(&sb, "发布时间：%s\n", article.PublishedAt.Format(time.RFC3339))
	}
	sb.WriteString("正文：\n")
	sb.WriteString(article.Content)
	sb.WriteString("\n")
	return sb.String()
}

// articleQualitySchema 返回 ArticleQuality 的 JSON Schema，约束 LLM 输出结构。
// 对齐 trpc-agent-go WithStructuredOutputJSONSchema 入参格式。
//
// 二开扩展点：业务方可扩展 schema（如增加 rationale 字段输出评分理由），
// 但需同步更新 parseArticleQuality 解析逻辑。
func articleQualitySchema() string {
	return `{
  "type": "object",
  "properties": {
    "authority":     {"type": "number", "minimum": 0, "maximum": 1, "description": "权威性 0-1"},
    "depth":         {"type": "number", "minimum": 0, "maximum": 1, "description": "深度 0-1"},
    "freshness":     {"type": "number", "minimum": 0, "maximum": 1, "description": "新鲜度 0-1"},
    "completeness":  {"type": "number", "minimum": 0, "maximum": 1, "description": "完整性 0-1"},
    "readability":   {"type": "number", "minimum": 0, "maximum": 1, "description": "可读性 0-1"},
    "citation":      {"type": "number", "minimum": 0, "maximum": 1, "description": "引用质量 0-1"},
    "overall":       {"type": "number", "minimum": 0, "maximum": 1, "description": "综合评分 0-1"}
  },
  "required": ["authority", "depth", "freshness", "completeness", "readability", "citation", "overall"],
  "additionalProperties": false
}`
}

// parseArticleQuality 解析 LLM 结构化输出为 ArticleQuality。
// 接受标准 JSON 字符串（结构化输出场景）。容忍 LLM 输出包裹的 markdown
// 代码块（```json ... ```），便于兼容不同 LLM 实现的差异。
func parseArticleQuality(raw string) (domain.ArticleQuality, error) {
	cleaned := stripCodeFence(raw)
	var q domain.ArticleQuality
	if err := json.Unmarshal([]byte(cleaned), &q); err != nil {
		return domain.ArticleQuality{}, fmt.Errorf("JSON 解析失败: %w", err)
	}
	return q, nil
}

// stripCodeFence 去除 LLM 输出可能包裹的 ```json / ``` 代码块围栏。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 去掉首行 ```json 或 ```。
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	}
	// 去掉末尾 ```。
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
