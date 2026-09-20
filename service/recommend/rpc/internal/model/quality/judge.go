package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现裁判质量评判 Agent（JudgeAgent）。
//
// JudgeAgent 是 spec.md 双路径架构中 QualityJudger 的裁判 Agent：
//   - 输入：ArticleInput（文章）+ domain.ArticleQuality（候选 Agent 评分初稿）
//   - 调用：LLMClient.Complete，开启 Logprobs=true / TopLogprobs=20，获取
//     A-T 质量标签 token 的 logprobs
//   - 输出：JudgeResult（含修正后 Quality + A-T 标签概率分布 + 置信度）
//
// 与候选 Agent（CandidateAgent）的协作：
//   1. CandidateAgent 通过结构化输出给出 6 维评分初稿
//   2. JudgeAgent 基于文章 + 候选评分 + rubric 重新裁判，开启 logprobs 获取
//      A-T 标签概率分布（而非单点评分）
//   3. 概率分布用于 Best-of-N 选择（llm_verifier_pairwise）与不确定性采样
//
// LLMClient 抽象说明：同 candidate.go，用 interface 解耦 trpc-agent-go，
// 确保离线可编译；二开方在生产环境注入 trpc-agent-go 适配器。
//
// 二开扩展点：
//   - 替换 LLMClient：接入自研/第三方 LLM
//   - 替换裁判 prompt：覆盖 buildJudgePrompt
//   - 替换 logprobs 解析：覆盖 ExtractLabelProbs（适配不同 LLM 的 logprobs 格式）
// ============================================================================

// JudgeResult 裁判结果，包含修正后的质量评分、A-T 标签概率分布与置信度。
//
// 字段语义：
//   - Quality：修正后的质量评分（基于候选评分 + logprobs 最高标签修正 Grade；
//     6 维分数与 Overall 保留候选值，由调用方决定是否覆盖）
//   - LabelProbs：A-T 标签概率分布（已归一化，和为 1.0）
//   - Confidence：置信度（最高标签概率，0-1），用于 Best-of-N 选择与
//     不确定性采样
type JudgeResult struct {
	// Quality 修正后的质量评分。
	Quality domain.ArticleQuality
	// LabelProbs A-T 标签概率分布（标签 → 概率，已归一化）。
	LabelProbs map[string]float64
	// Confidence 置信度（最高标签概率）。
	Confidence float64
}

// JudgeAgent 裁判质量评判 Agent。
//
// 职责：对候选 Agent 的评分初稿进行二次裁判，通过 logprobs 获取 A-T 标签
// 概率分布，修正候选 Grade 并给出置信度。实现 spec.md 中 QualityJudger 的
// 裁判阶段（配合 Best-of-N + llm_verifier_pairwise）。
type JudgeAgent struct {
	// llm LLM 客户端（开启 logprobs）。
	llm LLMClient
	// rubrics 评分标准集合（注入裁判 prompt 约束 LLM 评判语义）。
	rubrics *RubricSet
}

// NewJudgeAgent 构造裁判质量评判 Agent。
// llm LLM 客户端；rubrics 评分标准集合（可为 nil，使用 DefaultRubrics）。
func NewJudgeAgent(llm LLMClient, rubrics *RubricSet) *JudgeAgent {
	if rubrics == nil {
		rubrics = DefaultRubrics()
	}
	return &JudgeAgent{llm: llm, rubrics: rubrics}
}

// Judge 裁判候选 Agent 的评分结果。
// ctx 上下文；article 待评判文章；candidate 候选 Agent 输出的评分初稿。
// 返回 JudgeResult（含修正后质量 + A-T 概率分布 + 置信度）与 error。
//
// 流程：
//  1. 构造裁判 prompt（含文章 + 候选评分 + rubric）
//  2. 调用 LLM，开启 Logprobs=true / TopLogprobs=20
//  3. 解析 logprobs 获取 A-T 标签概率分布（归一化）
//  4. 取概率最高标签作为修正后的 Grade，最高概率作为置信度
//
// 二开扩展点：可覆盖 buildJudgePrompt 自定义裁判 prompt；
// 或覆盖 ExtractLabelProbs 适配不同 LLM 的 logprobs 响应格式。
func (a *JudgeAgent) Judge(ctx context.Context, article ArticleInput, candidate domain.ArticleQuality) (JudgeResult, error) {
	if a == nil || a.llm == nil {
		return JudgeResult{}, fmt.Errorf("JudgeAgent 或 LLMClient 未初始化")
	}
	prompt := a.buildJudgePrompt(article, candidate)
	opts := LLMOptions{
		Logprobs:    true,
		TopLogprobs: 20,
		Temperature: 0.0,
	}
	resp, err := a.llm.Complete(ctx, prompt, opts)
	if err != nil {
		return JudgeResult{}, fmt.Errorf("裁判 Agent LLM 调用失败: %w", err)
	}
	labelProbs := a.ExtractLabelProbs(resp)
	if len(labelProbs) == 0 {
		return JudgeResult{}, fmt.Errorf("裁判 Agent logprobs 中未提取到 A-T 标签")
	}
	// 取概率最高标签作为修正后的 Grade。
	bestLabel, bestProb := pickMaxLabel(labelProbs)
	// 修正 Grade；6 维分数与 Overall 保留候选值，调用方可基于标签分布自行覆盖。
	candidate.ArticleID = ifEmpty(candidate.ArticleID, article.ID)
	candidate.Grade = bestLabel
	return JudgeResult{
		Quality:    candidate,
		LabelProbs: labelProbs,
		Confidence: bestProb,
	}, nil
}

// ExtractLabelProbs 从 LLM logprobs 响应中解析 A-T 标签概率分布。
// logprobsResponse LLM 返回的 logprobs JSON 字符串。
//
// 期望响应格式（兼容 OpenAI 风格 logprobs）：
//
//	{
//	  "logprobs": [
//	    {"token": "A", "logprob": -0.1},
//	    {"token": "B", "logprob": -2.3},
//	    ...
//	  ]
//	}
//
// 解析逻辑：
//  1. 遍历 logprobs 数组，仅保留 token 为 A-T 单字符的条目
//  2. 用 math.Exp 将 logprob 转为概率
//  3. 对 A-T 概率做归一化（和为 1.0）
//
// 二开扩展点：不同 LLM 的 logprobs 响应格式可能不同（如字段名为
// "top_logprobs" 或嵌套结构），可覆盖此方法适配。
func (a *JudgeAgent) ExtractLabelProbs(logprobsResponse string) map[string]float64 {
	cleaned := stripCodeFence(logprobsResponse)
	var resp struct {
		Logprobs []struct {
			Token   string  `json:"token"`
			Logprob float64 `json:"logprob"`
		} `json:"logprobs"`
	}
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil
	}
	raw := make(map[string]float64)
	var sum float64
	for _, lp := range resp.Logprobs {
		if !isGradeLabel(lp.Token) {
			continue
		}
		// 同一标签可能多次出现（不同位置），取首次（首个生成 token 通常是
		// 裁判标签的位置）；若已存在则保留首次。
		if _, exists := raw[lp.Token]; exists {
			continue
		}
		p := math.Exp(lp.Logprob)
		raw[lp.Token] = p
		sum += p
	}
	if sum <= 0 {
		return raw
	}
	// 归一化使概率和为 1.0。
	for k := range raw {
		raw[k] /= sum
	}
	return raw
}

// buildJudgePrompt 构造裁判 prompt，包含文章、候选评分与 rubric 评分标准。
// 二开扩展点：业务方可覆盖此方法注入自定义裁判 prompt（如 Pairwise 对比场景）。
func (a *JudgeAgent) buildJudgePrompt(article ArticleInput, candidate domain.ArticleQuality) string {
	var sb strings.Builder
	sb.WriteString("你是文章质量裁判 Agent。基于候选 Agent 的评分初稿与 rubric 评分标准，\n")
	sb.WriteString("对文章整体质量给出 A-T 等级标签（A 最佳，T 最差，共 20 档）。\n")
	sb.WriteString("请仅输出一个 A-T 字符作为最终裁判标签，不要输出任何额外文本。\n\n")

	sb.WriteString("【Rubric 评分标准】\n")
	for _, r := range a.rubrics.Rubrics {
		fmt.Fprintf(&sb, "- %s（权重 %.2f）：%s\n", r.Dimension, r.Weight, r.Description)
		for _, c := range r.Criteria {
			fmt.Fprintf(&sb, "    %.1f = %s\n", c.Score, c.Description)
		}
	}

	sb.WriteString("\n【候选 Agent 评分初稿】\n")
	fmt.Fprintf(&sb, "Authority=%.3f, Depth=%.3f, Freshness=%.3f, Completeness=%.3f, Readability=%.3f, Citation=%.3f\n",
		candidate.Authority, candidate.Depth, candidate.Freshness, candidate.Completeness, candidate.Readability, candidate.Citation)
	fmt.Fprintf(&sb, "Overall=%.3f, 候选 Grade=%s\n", candidate.Overall, candidate.Grade)

	sb.WriteString("\n【待评判文章】\n")
	fmt.Fprintf(&sb, "文章 ID：%s\n", article.ID)
	fmt.Fprintf(&sb, "标题：%s\n", article.Title)
	fmt.Fprintf(&sb, "作者 ID：%s\n", article.AuthorID)
	if len(article.Tags) > 0 {
		fmt.Fprintf(&sb, "标签：%s\n", strings.Join(article.Tags, ", "))
	}
	sb.WriteString("正文：\n")
	sb.WriteString(article.Content)
	sb.WriteString("\n")
	return sb.String()
}

// isGradeLabel 判断 token 是否为 A-T 等级标签（单字符 A-T）。
func isGradeLabel(token string) bool {
	if len(token) != 1 {
		return false
	}
	c := token[0]
	return c >= 'A' && c <= 'T'
}

// pickMaxLabel 从标签概率分布中选取概率最高的标签与对应概率。
func pickMaxLabel(probs map[string]float64) (string, float64) {
	bestLabel := ""
	bestProb := -1.0
	for label, p := range probs {
		if p > bestProb {
			bestLabel = label
			bestProb = p
		}
	}
	if bestLabel == "" {
		return domain.GradeBest, 0
	}
	return bestLabel, bestProb
}

// ifEmpty 若 s 为空返回 fallback，否则返回 s。
func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
