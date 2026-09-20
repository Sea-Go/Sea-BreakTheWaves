// ============================================================================
// 该文件测试 internal/quality/judge.go 的 JudgeAgent。
// 用 mock LLMClient 返回固定 logprobs JSON，验证：
//   - Judge：裁判 prompt 构造、logprobs 解析、Grade 修正、置信度计算
//   - ExtractLabelProbs：A-T 标签概率分布解析与归一化
// ============================================================================

package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// TestJudgeAgent_Judge 验证裁判流程：解析 logprobs，修正 Grade，计算置信度。
func TestJudgeAgent_Judge(t *testing.T) {
	// LLM 返回 logprobs JSON：A 概率最高（logprob=-0.1），其他较低。
	logprobsResp := `{
		"logprobs": [
			{"token": "A", "logprob": -0.1},
			{"token": "B", "logprob": -2.3},
			{"token": "C", "logprob": -3.5},
			{"token": "X", "logprob": -0.2}
		]
	}`
	mock := &mockScriptedLLM{responses: []string{logprobsResp}}
	agent := NewJudgeAgent(mock, nil)

	article := ArticleInput{
		ID:      "art-1",
		Title:   "深度长文",
		Content: "正文…",
	}
	candidate := domain.ArticleQuality{
		ArticleID: "art-1",
		Overall:   0.5,
		Grade:     "K",
	}
	result, err := agent.Judge(context.Background(), article, candidate)
	if err != nil {
		t.Fatalf("Judge 错误: %v", err)
	}
	// Grade 应被修正为概率最高标签 A。
	if result.Quality.Grade != "A" {
		t.Fatalf("修正后 Grade = %q, want A", result.Quality.Grade)
	}
	// 置信度 = A 的归一化概率。
	if result.Confidence <= 0 || result.Confidence > 1 {
		t.Fatalf("Confidence 越界: %v", result.Confidence)
	}
	// A 的原始概率 exp(-0.1)≈0.905，归一化后应接近 0.9（X 被忽略）。
	if result.Confidence < 0.85 || result.Confidence > 0.95 {
		t.Fatalf("Confidence = %v, want 约 0.9", result.Confidence)
	}
	// LabelProbs 应含 A/B/C，不含 X（非 A-T 标签）。
	if _, ok := result.LabelProbs["A"]; !ok {
		t.Fatal("LabelProbs 缺少 A")
	}
	if _, ok := result.LabelProbs["X"]; ok {
		t.Fatal("LabelProbs 不应含 X（非 A-T 标签）")
	}
	// 概率和应为 1.0（归一化）。
	var sum float64
	for _, p := range result.LabelProbs {
		sum += p
	}
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("LabelProbs 概率和 = %v, want 1.0", sum)
	}

	// 验证 LLM 调用参数：开启 logprobs，TopLogprobs=20。
	if len(mock.calls) != 1 {
		t.Fatalf("LLM 调用次数 = %d, want 1", len(mock.calls))
	}
	call := mock.calls[0]
	if !call.opts.Logprobs {
		t.Fatal("Logprobs 未开启")
	}
	if call.opts.TopLogprobs != 20 {
		t.Fatalf("TopLogprobs = %d, want 20", call.opts.TopLogprobs)
	}
	// prompt 含候选评分与 rubric 标准。
	if !strings.Contains(call.prompt, "候选 Agent 评分初稿") {
		t.Fatal("prompt 未包含候选评分段落")
	}
	if !strings.Contains(call.prompt, "authority") {
		t.Fatal("prompt 未包含 rubric 维度")
	}
}

// TestJudgeAgent_Judge_ArticleIDFill 验证候选 ArticleID 为空时用文章 ID 填充。
func TestJudgeAgent_Judge_ArticleIDFill(t *testing.T) {
	logprobsResp := `{"logprobs": [{"token": "B", "logprob": -0.5}]}`
	mock := &mockScriptedLLM{responses: []string{logprobsResp}}
	agent := NewJudgeAgent(mock, nil)

	candidate := domain.ArticleQuality{Overall: 0.9, Grade: "B"}
	// 候选 ArticleID 为空。
	result, err := agent.Judge(context.Background(), ArticleInput{ID: "art-x"}, candidate)
	if err != nil {
		t.Fatalf("Judge 错误: %v", err)
	}
	if result.Quality.ArticleID != "art-x" {
		t.Fatalf("ArticleID = %q, want art-x（应从文章填充）", result.Quality.ArticleID)
	}
}

// TestJudgeAgent_Judge_LLMError 验证 LLM 调用失败时返回错误。
func TestJudgeAgent_Judge_LLMError(t *testing.T) {
	mock := &mockScriptedLLM{err: errors.New("LLM 超时")}
	agent := NewJudgeAgent(mock, nil)
	_, err := agent.Judge(context.Background(), ArticleInput{ID: "a1"}, domain.ArticleQuality{})
	if err == nil {
		t.Fatal("LLM 错误时 Judge 应返回错误")
	}
	if !strings.Contains(err.Error(), "裁判 Agent LLM 调用失败") {
		t.Fatalf("错误信息不含前缀: %v", err)
	}
}

// TestJudgeAgent_Judge_NoLabels 验证 logprobs 中无 A-T 标签时返回错误。
func TestJudgeAgent_Judge_NoLabels(t *testing.T) {
	// 所有 token 都不是 A-T 标签。
	logprobsResp := `{"logprobs": [{"token": "X", "logprob": -0.1}, {"token": "Y", "logprob": -0.2}]}`
	mock := &mockScriptedLLM{responses: []string{logprobsResp}}
	agent := NewJudgeAgent(mock, nil)
	_, err := agent.Judge(context.Background(), ArticleInput{ID: "a1"}, domain.ArticleQuality{})
	if err == nil {
		t.Fatal("无 A-T 标签时 Judge 应返回错误")
	}
	if !strings.Contains(err.Error(), "未提取到 A-T 标签") {
		t.Fatalf("错误信息不含前缀: %v", err)
	}
}

// TestExtractLabelProbs 验证 logprobs 解析与归一化（含非法 token 过滤）。
func TestExtractLabelProbs(t *testing.T) {
	agent := NewJudgeAgent(&mockScriptedLLM{}, nil)

	cases := []struct {
		name     string
		input    string
		wantKeys []string
	}{
		{
			name: "标准 A-T 标签 + 非法 token 过滤",
			input: `{
				"logprobs": [
					{"token": "A", "logprob": 0.0},
					{"token": "B", "logprob": -1.0},
					{"token": "Z", "logprob": -0.1},
					{"token": "AB", "logprob": -0.1}
				]
			}`,
			wantKeys: []string{"A", "B"},
		},
		{
			name: "全 20 个 A-T 标签",
			input: func() string {
				sb := strings.Builder{}
				sb.WriteString(`{"logprobs": [`)
				for i := 0; i < 20; i++ {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(`{"token": "`)
					sb.WriteByte(byte('A' + i))
					sb.WriteString(`", "logprob": -0.1}`)
				}
				sb.WriteString(`]}`)
				return sb.String()
			}(),
			wantKeys: []string{
				"A", "B", "C", "D", "E", "F", "G", "H", "I", "J",
				"K", "L", "M", "N", "O", "P", "Q", "R", "S", "T",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probs := agent.ExtractLabelProbs(c.input)
			if len(probs) != len(c.wantKeys) {
				t.Fatalf("标签数 = %d, want %d", len(probs), len(c.wantKeys))
			}
			for _, k := range c.wantKeys {
				if _, ok := probs[k]; !ok {
					t.Fatalf("缺少标签 %q", k)
				}
			}
			// 归一化：概率和在 [0.999, 1.001]。
			var sum float64
			for _, p := range probs {
				sum += p
			}
			if len(probs) > 0 && (sum < 0.999 || sum > 1.001) {
				t.Fatalf("概率和 = %v, want 1.0", sum)
			}
		})
	}
}

// TestExtractLabelProbs_InvalidJSON 验证非法 JSON 返回 nil。
func TestExtractLabelProbs_InvalidJSON(t *testing.T) {
	agent := NewJudgeAgent(&mockScriptedLLM{}, nil)
	if probs := agent.ExtractLabelProbs("not a json"); probs != nil {
		t.Fatalf("非法 JSON 应返回 nil, 实际 %v", probs)
	}
}

// TestExtractLabelProbs_DuplicateToken 验证同一标签多次出现时保留首次。
func TestExtractLabelProbs_DuplicateToken(t *testing.T) {
	agent := NewJudgeAgent(&mockScriptedLLM{}, nil)
	input := `{
		"logprobs": [
			{"token": "A", "logprob": -0.1},
			{"token": "A", "logprob": -2.0},
			{"token": "B", "logprob": -0.5}
		]
	}`
	probs := agent.ExtractLabelProbs(input)
	if len(probs) != 2 {
		t.Fatalf("标签数 = %d, want 2", len(probs))
	}
	// A 应保留首次的 exp(-0.1)≈0.905，而非 exp(-2.0)≈0.135。
	// 归一化后（A=0.905, B=0.607, sum=1.512）：A≈0.60, B≈0.40。
	// 若错误地保留了第二次 A（0.135），则 A 归一化后 ≈0.18。
	// 因此断言 A > 0.5 即可证明保留了首次（更高概率）。
	if probs["A"] <= 0.5 {
		t.Fatalf("A 概率 = %v, 应保留首次（>0.5），疑似保留了第二次低概率条目", probs["A"])
	}
	if probs["A"] <= probs["B"] {
		t.Fatalf("A 概率 = %v 应大于 B 概率 = %v（首次 A 原始概率更高）", probs["A"], probs["B"])
	}
}

// TestJudgeAgent_NilAgent 验证 nil Agent 与 nil LLM 的安全性。
func TestJudgeAgent_NilAgent(t *testing.T) {
	var agent *JudgeAgent
	_, err := agent.Judge(context.Background(), ArticleInput{ID: "a1"}, domain.ArticleQuality{})
	if err == nil {
		t.Fatal("nil JudgeAgent.Judge 应返回错误")
	}
	// LLM 为 nil。
	agent = &JudgeAgent{}
	_, err = agent.Judge(context.Background(), ArticleInput{ID: "a1"}, domain.ArticleQuality{})
	if err == nil {
		t.Fatal("LLM 为 nil 时 Judge 应返回错误")
	}
}

// TestNewJudgeAgent_NilRubrics 验证 rubrics 为 nil 时使用默认 rubric 集。
func TestNewJudgeAgent_NilRubrics(t *testing.T) {
	agent := NewJudgeAgent(&mockScriptedLLM{}, nil)
	if agent.rubrics == nil {
		t.Fatal("nil 入参应回退到 DefaultRubrics")
	}
	if len(agent.rubrics.Rubrics) != 7 {
		t.Fatalf("默认 rubric 数量 = %d, want 7", len(agent.rubrics.Rubrics))
	}
}

// TestJudgeResult_TypeCompleteness 验证 JudgeResult 结构体字段完整性。
func TestJudgeResult_TypeCompleteness(t *testing.T) {
	r := JudgeResult{
		Quality:    domain.ArticleQuality{Overall: 0.9, Grade: "C"},
		LabelProbs: map[string]float64{"A": 0.3, "B": 0.7},
		Confidence: 0.7,
	}
	if r.Quality.Grade != "C" {
		t.Fatalf("Quality.Grade = %q, want C", r.Quality.Grade)
	}
	if r.LabelProbs["B"] != 0.7 {
		t.Fatalf("LabelProbs[B] = %v, want 0.7", r.LabelProbs["B"])
	}
	if r.Confidence != 0.7 {
		t.Fatalf("Confidence = %v, want 0.7", r.Confidence)
	}
}
