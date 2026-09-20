package quality

import (
	"context"
	"fmt"
	"time"

	"sea/internal/domain"
)

// ----------------------------------------------------------------------------
// 反馈闭环 + 图谱 QualityReport 节点。
// 对应 spec Task 9.6。实现 domain.Hook，监听 quality_judge 事件，
// 写入 FeedbackRepo + 图谱 QualityReport 节点；离线评估校准 rubrics。
// RubricSet 复用 rubrics.go（Task 9.1）定义，不重复定义。
// ----------------------------------------------------------------------------

// FeedbackRepo 反馈持久化抽象。
//
// 二开点：实现该接口接入 MySQL / ES / 对象存储等持久化后端。
type FeedbackRepo interface {
	// SaveFeedback 保存单条质量反馈。
	SaveFeedback(ctx context.Context, feedback QualityFeedback) error
	// ListFeedback 列出指定文章的历史质量反馈（用于离线校准）。
	ListFeedback(ctx context.Context, articleID string) ([]QualityFeedback, error)
}

// GraphQuerier 图谱 QualityReport 抽象（复用 graph.Client.UpsertQualityReport 语义）。
//
// 二开点：实现该接口接入自研图谱后端；默认适配 graph.Client（adapter 转换
// QualityReportInput → graph.QualityReportInput 的 6 维字段）。
type GraphQuerier interface {
	// UpsertQualityReport MERGE QualityReport 节点 + (Article)-[:HAS_QUALITY_REPORT]->(QualityReport)。
	UpsertQualityReport(ctx context.Context, report QualityReportInput) error
}

// QualityReportInput 质量报告输入（同步图谱 QualityReport 节点）。
// 与 graph.QualityReportInput 区别：本类型聚合 domain.ArticleQuality + 评估时间，
// 由 adapter 层展开为 graph.QualityReportInput 的 6 维字段。
type QualityReportInput struct {
	ArticleID   string                // 关联文章 ID
	Quality     domain.ArticleQuality // 质量评分（6 维 + Overall + Grade）
	EvaluatedAt time.Time             // 评估时间
}

// QualityFeedback 质量反馈条目（quality 包内类型，区别于 domain.QualityFeedback）。
// domain.QualityFeedback 用于 rubric 校准（按维度）；本类型用于反馈持久化与图谱同步。
type QualityFeedback struct {
	ID        string                // 反馈唯一 ID（通常用 EventID）
	ArticleID string                // 文章 ID
	UserID    string                // 反馈用户 ID
	Quality   domain.ArticleQuality // 质量评分
	Feedback  string                // 反馈文本（用户备注）
	Action    string                // 反馈动作（quality_judge / like / dislike / report）
	CreatedAt time.Time             // 创建时间
}

// FeedbackHook 质量反馈 Hook，实现 domain.Hook。
// 监听 quality_judge 事件，写入 FeedbackRepo + 图谱 QualityReport 节点；
// 支持离线评估校准 rubrics。
//
// 二开点：
//   - 扩展监听事件类型（如 click/like 触发质量再评估）
//   - 替换 FeedbackRepo / GraphQuerier 后端
//   - CalibrateRubrics 对接 Phase 15 离线评估体系
type FeedbackHook struct {
	repo  FeedbackRepo
	graph GraphQuerier
}

// 编译期断言：FeedbackHook 实现 domain.Hook。
var _ domain.Hook = (*FeedbackHook)(nil)

// NewFeedbackHook 创建质量反馈 Hook。
func NewFeedbackHook(repo FeedbackRepo, graph GraphQuerier) *FeedbackHook {
	return &FeedbackHook{repo: repo, graph: graph}
}

// Name 返回 Hook 名称。
func (h *FeedbackHook) Name() string {
	return "quality_feedback"
}

// OnEvent 处理行为事件。
// quality_judge 事件：写入 FeedbackRepo + 图谱 QualityReport 节点。
// 其他事件类型忽略（返回 nil）。
func (h *FeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if e.EventType != "quality_judge" {
		return nil
	}
	quality := extractQualityFromPayload(e.Payload)
	feedback := QualityFeedback{
		ID:        e.EventID,
		ArticleID: e.ArticleID,
		UserID:    e.UserID,
		Quality:   quality,
		Action:    e.EventType,
		Feedback:  extractStringFromPayload(e.Payload, "feedback"),
		CreatedAt: e.Timestamp,
	}
	if err := h.SaveFeedback(ctx, feedback); err != nil {
		return fmt.Errorf("quality feedback: onEvent: %w", err)
	}
	return nil
}

// SaveFeedback 保存反馈 + 同步图谱 QualityReport 节点。
func (h *FeedbackHook) SaveFeedback(ctx context.Context, feedback QualityFeedback) error {
	if err := h.repo.SaveFeedback(ctx, feedback); err != nil {
		return fmt.Errorf("save feedback: %w", err)
	}
	if h.graph != nil {
		report := QualityReportInput{
			ArticleID:   feedback.ArticleID,
			Quality:     feedback.Quality,
			EvaluatedAt: feedback.CreatedAt,
		}
		if err := h.graph.UpsertQualityReport(ctx, report); err != nil {
			return fmt.Errorf("upsert quality report: %w", err)
		}
	}
	return nil
}

// CalibrateRubrics 离线评估校准 rubrics：拉取反馈数据校准 rubric 权重。
//
// TODO: 对接 Phase 15 离线评估体系，实现完整校准逻辑：
//   - 拉取近期 QualityFeedback（按文章/维度聚合）
//   - 转换为 domain.QualityFeedback 调用 rubrics.Calibrate
//   - 落库校准后的 rubrics
//
// 二开点：基于用户反馈信号（点赞/不感兴趣/举报）反向校准评判标准。
// 当前为占位实现，仅校验 rubrics 非空。
func (h *FeedbackHook) CalibrateRubrics(ctx context.Context, rubrics *RubricSet) error {
	if rubrics == nil {
		return fmt.Errorf("calibrate: rubrics is nil")
	}
	// TODO: 拉取反馈数据，转换为 domain.QualityFeedback，调用 rubrics.Calibrate。
	return nil
}

// extractQualityFromPayload 从事件 Payload 提取质量评分。
func extractQualityFromPayload(payload map[string]any) domain.ArticleQuality {
	q := domain.ArticleQuality{}
	if payload == nil {
		return q
	}
	q.ArticleID, _ = payload["article_id"].(string)
	q.Authority = extractFloat(payload, "authority")
	q.Depth = extractFloat(payload, "depth")
	q.Freshness = extractFloat(payload, "freshness")
	q.Completeness = extractFloat(payload, "completeness")
	q.Readability = extractFloat(payload, "readability")
	q.Citation = extractFloat(payload, "citation")
	q.Overall = extractFloat(payload, "overall")
	q.Grade, _ = payload["grade"].(string)
	return q
}

// extractStringFromPayload 从事件 Payload 提取字符串。
func extractStringFromPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, _ := payload[key].(string)
	return v
}

// extractFloat 从 map 提取 float64（兼容 float64/float32/int/int64）。
func extractFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
