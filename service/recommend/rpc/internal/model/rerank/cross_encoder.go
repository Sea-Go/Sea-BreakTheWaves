package rerank

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// cross_encoder.go 实现 Cross-encoder 重排（BERT-base 中文 + linear head）。
// 输入为 [query, candidate] 拼接，输出相关性标量。
// Model 用 interface 抽象（不直接 import ONNX/XGBoost），确保离线编译；
// 真实推理后端（ONNX Runtime / GGUF）通过实现 Model interface 注入。
// ============================================================================

// Model 模型抽象接口，抽象 ONNX/GGUF/XGBoost 等推理后端。
// 二开扩展点：实现该 interface 接入真实推理运行时。
type Model interface {
	// Predict 对输入特征向量打分，返回标量预测值。
	Predict(input []float64) (float64, error)
	// Load 从指定路径加载模型。
	Load(path string) error
}

// CrossEncoder 基于 BERT-base 中文 + linear head 的 Cross-encoder 重排器。
// 输入为 [query, candidate] 拼接，输出相关性标量。
// 二开扩展点：替换 Model 实现接入真实 ONNX/GGUF 运行时；模型热加载与版本化通过 LoadModel 实现。
type CrossEncoder struct {
	modelPath string // 模型文件路径
	model     Model  // 模型抽象，nil 时使用 stub 评分
	dim       int    // 输入向量维度（query+candidate 拼接后维度）
}

// NewCrossEncoder 创建 Cross-encoder，modelPath 为模型文件路径。
// 注：构造时不自动加载模型，需显式调用 LoadModel；未加载时 Score 走 stub。
func NewCrossEncoder(modelPath string) *CrossEncoder {
	return &CrossEncoder{
		modelPath: modelPath,
		dim:       64, // 默认输入维度（二开可调整）
	}
}

// LoadModel 加载模型（依赖注入的 Model 实现）。
// 二开点：业务方实现 Model interface 并通过该方法注入，替换 stub 评分。
func (ce *CrossEncoder) LoadModel(m Model) error {
	if err := m.Load(ce.modelPath); err != nil {
		return err
	}
	ce.model = m
	return nil
}

// Score 对单个候选评分。
// ctx 上下文；query 查询文本；candidate 候选文章。
// 若 model 为 nil（未加载），返回基于特征的 stub 分数（TODO 集成 ONNX 运行时）。
func (ce *CrossEncoder) Score(ctx context.Context, query string, candidate domain.Candidate) (float64, error) {
	_ = ctx
	if ce.model == nil {
		// TODO: 集成 ONNX Runtime 加载真实 BERT Cross-encoder 模型
		return ce.stubScore(query, candidate), nil
	}
	// 模型已加载：构造输入向量并调用 Predict
	input := ce.buildInput(query, candidate)
	score, err := ce.model.Predict(input)
	if err != nil {
		return 0, err
	}
	// sigmoid 归一化到 0-1
	return sigmoid(score), nil
}

// Rerank 批量重排，返回按分数降序的 topK 候选。
// ctx 上下文；query 查询文本；candidates 候选列表；topK 返回数量（<=0 表示不截断）。
func (ce *CrossEncoder) Rerank(ctx context.Context, query string, candidates []domain.Candidate, topK int) ([]domain.Candidate, error) {
	scored := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		s, err := ce.Score(ctx, query, c)
		if err != nil {
			return nil, err
		}
		c.Score = s
		scored = append(scored, c)
	}
	sortByScoreDesc(scored)
	if topK > 0 && topK < len(scored) {
		scored = scored[:topK]
	}
	return scored, nil
}

// stubScore 基于特征的 stub 评分（model 未加载时使用）。
// 综合查询-标题/摘要/标签关键词重合度、候选已有分数、文本长度评分。
func (ce *CrossEncoder) stubScore(query string, c domain.Candidate) float64 {
	title := stringField(c, "title")
	summary := stringField(c, "summary")
	tags := stringSliceField(c, "tags")

	qTokens := tokenize(query)
	base := normalizeScore(c.Score)
	if len(qTokens) == 0 {
		// 无查询时用候选原分数与文本长度归一化
		return clamp01(base*0.6 + textLengthScore(title, summary)*0.4)
	}
	titleHit := tokenHitRate(qTokens, title)
	summaryHit := tokenHitRate(qTokens, summary)
	tagHit := tokenTagHitRate(qTokens, tags)
	// 加权融合：标题命中 0.4 + 摘要命中 0.25 + 标签命中 0.25 + 原分数 0.1
	score := titleHit*0.4 + summaryHit*0.25 + tagHit*0.25 + base*0.1
	return clamp01(score)
}

// buildInput 构造模型输入向量（query 与候选文本的 hash 向量拼接）。
func (ce *CrossEncoder) buildInput(query string, c domain.Candidate) []float64 {
	qVec := hashVector(query, ce.dim/2)
	cVec := hashVector(stringField(c, "title")+"|"+stringField(c, "summary"), ce.dim/2)
	return append(qVec, cVec...)
}

// ---- 辅助函数 ----

// sortByScoreDesc 将候选列表按 Score 降序稳定排序。
func sortByScoreDesc(cs []domain.Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		return cs[i].Score > cs[j].Score
	})
}

// sigmoid Logistic 函数，将得分归一化到 0-1。
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// normalizeScore 将候选分数归一化到 0-1（超出则 clamp）。
func normalizeScore(s float64) float64 {
	return clamp01(s)
}

// clamp01 将分数限制在 [0,1]。
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// textLengthScore 基于标题与摘要长度的评分（鼓励适度长度）。
func textLengthScore(title, summary string) float64 {
	score := 0.0
	if len(title) > 0 {
		score += math.Min(float64(len(title))/30.0, 1.0) * 0.5
	}
	if len(summary) > 0 {
		score += math.Min(float64(len(summary))/200.0, 1.0) * 0.5
	}
	return score
}

// tokenize 简单分词：按空格/标点切分，CJK 文本按字符切分。
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	fields := splitFields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		if isCJK(f) {
			for _, r := range f {
				out = append(out, string(r))
			}
		} else {
			out = append(out, f)
		}
	}
	return out
}

// splitFields 按空格与常见中英文标点切分字符串。
func splitFields(s string) []string {
	out := []string{}
	cur := []rune{}
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' || r == ';' ||
			r == ':' || r == '，' || r == '。' || r == '；' || r == '、' {
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = cur[:0]
			}
		} else {
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// isCJK 判断字符串是否包含 CJK 字符。
func isCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// tokenHitRate 计算 tokens 在文本中的命中率（0-1）。
func tokenHitRate(tokens []string, text string) float64 {
	if len(tokens) == 0 || text == "" {
		return 0.0
	}
	hit := 0
	for _, t := range tokens {
		if strings.Contains(text, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(tokens))
}

// tokenTagHitRate 计算 tokens 在标签列表中的命中率（0-1）。
func tokenTagHitRate(tokens []string, tags []string) float64 {
	if len(tokens) == 0 || len(tags) == 0 {
		return 0.0
	}
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}
	hit := 0
	for _, t := range tokens {
		if tagSet[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(tokens))
}
