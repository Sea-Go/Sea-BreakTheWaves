package domain

import (
	"math"
	"time"
)

// ============================================================================
// 该文件为文章质量评判（Quality）补充领域类型与辅助方法。
//
// ArticleQuality 结构体（含 ArticleID + 6 维分数 + Overall + Grade）已定义于
// types.go，本文件不重复定义该结构，仅补充：
//   - ArticleQualityReport：质量评判报告（含模型版本/logprobs/反馈列表）
//   - QualityFeedback：人工/行为反馈条目，用于离线校准 rubric 权重
//   - ArticleQuality 辅助方法：IsPass / ToGrade
//   - OverallToGrade：0-1 综合分 → A-T 等级映射工具函数
//
// 等级约定：A 表示最佳（Overall≈1），T 表示最差（Overall≈0），共 20 档，
// 供裁判 Agent（JudgeAgent）的 logprobs 标签分布使用。
//
// 二开扩展点：业务方可实现 QualityJudger interface 接入自研评判模型，
// 或通过 QualityFeedback 反馈数据定期调用 RubricSet.Calibrate 校准权重。
// ============================================================================

// 质量等级常量。共 20 档（A-T），与 OverallToGrade 映射保持一致。
const (
	// GradeBest 最佳等级（Overall ∈ [0.95, 1.0]）。
	GradeBest = "A"
	// GradeWorst 最差等级（Overall ∈ [0.0, 0.05)）。
	GradeWorst = "T"
	// GradeLen 等级档位数（A-T 共 20 档）。
	GradeLen = 20
)

// ArticleQualityReport 文章质量评判报告，封装一次完整评判的全部上下文。
//
// 由 CandidateAgent（候选 Agent）+ JudgeAgent（裁判 Agent）协作生成：
//   - Quality：6 维评分 + 总分 + 等级（裁判修正后的最终结果）
//   - Logprobs：裁判 Agent 返回的 A-T 标签概率分布（token → 概率）
//   - Feedback：人工/行为反馈条目，用于离线校准 rubric
//
// 二开扩展点：可扩展 ModelVersion 字段做多模型 A/B；Logprobs 可落库用于
// 置信度监控与不确定性采样。
type ArticleQualityReport struct {
	// ArticleID 文章 ID。
	ArticleID string
	// Quality 质量评分（6 维 + Overall + Grade）。
	Quality ArticleQuality
	// EvaluatedAt 评判时间。
	EvaluatedAt time.Time
	// ModelVersion 评判模型版本（如 "qwen2.5-72b@20260601"），用于多模型 A/B。
	ModelVersion string
	// Logprobs 裁判 Agent 返回的 A-T 标签概率分布（标签 → 概率）。
	Logprobs map[string]float64
	// Feedback 关联的人工/行为反馈条目列表。
	Feedback []QualityFeedback
}

// QualityFeedback 质量反馈条目，由用户行为（点赞/收藏/完成阅读/举报）或
// 人工标注产生，用于离线校准 rubric 权重与评判模型。
//
// 二开扩展点：业务方实现 FeedbackHook 写入 ArticleQualityFeedback 表 + 图谱
// QualityReport 节点，并定期调用 RubricSet.Calibrate 触发权重校准。
type QualityFeedback struct {
	// UserID 反馈用户 ID。
	UserID string
	// ArticleID 反馈对应文章 ID。
	ArticleID string
	// Score 反馈分数（0-1，点赞≈1.0/举报≈0.0/完成阅读≈0.7 等业务定义）。
	Score float64
	// Dimension 反馈针对的维度（authority/depth/freshness/completeness/
	// readability/citation/overall，可空表示综合反馈）。
	Dimension string
	// Comment 反馈文本评论（可为空）。
	Comment string
	// CreatedAt 反馈创建时间。
	CreatedAt time.Time
}

// IsPass 判断文章综合评分是否达到质量阈值。
// threshold 阈值（如 RecommendConfig.QualityThreshold，0-1）。
// 返回 true 表示通过（可参与召回/排序/推荐）。
//
// 二开扩展点：业务方可实现自定义阈值逻辑（如按频道独立阈值、按时段调整），
// 替代默认的全局阈值判定。
func (q ArticleQuality) IsPass(threshold float64) bool {
	return q.Overall >= threshold
}

// ToGrade 根据 Overall 综合分计算 A-T 等级。
// 映射规则：idx = clamp(int((1.0-Overall)*20), 0, 19)，等级 = 'A' + idx。
//   - Overall = 1.0 → A（最佳）
//   - Overall = 0.0 → T（最差）
//   - Overall = 0.5 → K（中位）
//
// 注意：本方法始终基于 Overall 重新计算，不读取已设置的 Grade 字段，
// 便于 CandidateAgent 在拿到 6 维分数后填充 Grade。如需保留外部 Grade，
// 请直接读取 q.Grade 字段。
func (q ArticleQuality) ToGrade() string {
	return OverallToGrade(q.Overall)
}

// OverallToGrade 将 0-1 综合分映射为 A-T 等级（共 20 档）。
// overall 越高等级越优（A 最佳，T 最差）。输入越界自动 clamp 到 [0,1]。
//
// 使用 math.Round 避免 float64 截断导致的边界漂移
// （如 0.9 在 float64 中存储为 0.90000000000000002，1.0-0.9=0.09999...，
// 截断后 idx=1 而非预期的 2）。
func OverallToGrade(overall float64) string {
	if overall < 0 {
		overall = 0
	}
	if overall > 1 {
		overall = 1
	}
	idx := int(math.Round((1.0 - overall) * float64(GradeLen)))
	if idx < 0 {
		idx = 0
	}
	if idx > GradeLen-1 {
		idx = GradeLen - 1
	}
	return string(rune('A' + idx))
}

// AllGrades 返回全部 A-T 等级标签，供裁判 Agent 构造 logprobs 标签空间使用。
func AllGrades() []string {
	grades := make([]string, 0, GradeLen)
	for i := 0; i < GradeLen; i++ {
		grades = append(grades, string(rune('A'+i)))
	}
	return grades
}
