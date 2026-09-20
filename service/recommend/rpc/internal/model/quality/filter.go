package quality

import (
	"context"
	"fmt"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// 质量阈值过滤 + validate 节点（grounding + 质量检查）。
// 对应 spec Task 9.5。judger 复用 domain.QualityJudger interface。
// ----------------------------------------------------------------------------

// GroundingChecker grounding 校验抽象，检查文章内容是否与候选标签/摘要一致。
//
// 二开点：实现该接口接入自研 grounding 模型（如 NLI / 事实校验 / 引用核验），
// 返回 0-1 一致性分数。
type GroundingChecker interface {
	// Check 返回候选的 grounding 一致性分数（0-1）。
	Check(ctx context.Context, candidate domain.Candidate) (float64, error)
}

// grounding 通过阈值（低于该值视为内容与标签不一致）。
const groundingThreshold = 0.7

// Filter 质量阈值过滤器，过滤低于阈值的候选，并提供 validate 节点
// （真实 grounding + 质量检查）。
//
// 二开点：通过 WithGrounding 注入自研 GroundingChecker；通过实现
// domain.QualityJudger 替换评判逻辑。
type Filter struct {
	threshold float64              // 默认质量阈值
	judger    domain.QualityJudger // 质量评判器（复用 domain.QualityJudger）
	grounding GroundingChecker     // grounding 校验器（可空，空时跳过 grounding 检查）
}

// NewFilter 创建质量过滤器。
func NewFilter(threshold float64, judger domain.QualityJudger) *Filter {
	return &Filter{
		threshold: threshold,
		judger:    judger,
	}
}

// WithGrounding 注入 grounding 校验器（可选，Builder 风格）。
func (f *Filter) WithGrounding(checker GroundingChecker) *Filter {
	f.grounding = checker
	return f
}

// Filter 过滤低于阈值的候选，返回通过阈值的候选列表。
// threshold <= 0 时使用 Filter struct 的默认阈值。
func (f *Filter) Filter(ctx context.Context, candidates []domain.Candidate, threshold float64) ([]domain.Candidate, error) {
	if threshold <= 0 {
		threshold = f.threshold
	}
	result := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		q, err := f.judger.Judge(ctx, domain.QualityRequest{
			ArticleID: c.ArticleID,
			Content:   extractContent(c),
		})
		if err != nil {
			// 评判失败保守丢弃该候选（不影响其他候选）。
			continue
		}
		if q.Overall >= threshold {
			result = append(result, c)
		}
	}
	return result, nil
}

// ValidationResult validate 节点结果。
type ValidationResult struct {
	Pass           bool                  // 是否通过（grounding + 质量均达标）
	Quality        domain.ArticleQuality // 质量评分
	GroundingScore float64               // grounding 一致性分数（0-1，无 checker 时默认 1）
	Reasons        []string              // 未通过原因列表（Pass=true 时为空）
}

// Validate validate 节点：真实 grounding + 质量检查。
//   - grounding：检查文章内容是否与候选标签/摘要一致（调 GroundingChecker）
//   - 质量检查：调 judger.Judge
//
// 返回 ValidationResult（pass/fail + 原因）。
func (f *Filter) Validate(ctx context.Context, candidate domain.Candidate) (ValidationResult, error) {
	result := ValidationResult{
		GroundingScore: 1.0,
		Reasons:        []string{},
	}

	// grounding 检查（若注入了 checker）。
	if f.grounding != nil {
		score, err := f.grounding.Check(ctx, candidate)
		if err != nil {
			return result, fmt.Errorf("filter: grounding check: %w", err)
		}
		result.GroundingScore = score
		if score < groundingThreshold {
			result.Reasons = append(result.Reasons,
				fmt.Sprintf("grounding 分数 %.2f 低于阈值 %.2f", score, groundingThreshold))
		}
	}

	// 质量检查。
	q, err := f.judger.Judge(ctx, domain.QualityRequest{
		ArticleID: candidate.ArticleID,
		Content:   extractContent(candidate),
	})
	if err != nil {
		return result, fmt.Errorf("filter: judge: %w", err)
	}
	result.Quality = q
	if q.Overall < f.threshold {
		result.Reasons = append(result.Reasons,
			fmt.Sprintf("质量评分 %.2f 低于阈值 %.2f", q.Overall, f.threshold))
	}

	// 通过条件：无任何未通过原因。
	result.Pass = len(result.Reasons) == 0
	return result, nil
}

// extractContent 从候选 Extra 提取文章内容（若有）。
// 评判器可据此对正文做真实 grounding，无内容时返回空串。
func extractContent(c domain.Candidate) string {
	if c.Extra == nil {
		return ""
	}
	if v, ok := c.Extra["content"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
