package event

import (
	"context"
	"log/slog"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 内置 Hook 实现（Task 5.3）
//
// 该文件实现 8 个内置 Hook，每个实现 domain.Hook interface（Name + OnEvent）。
// 所有外部依赖（prometheus/zlog/ProfileManager/GraphQuerier 等）通过本地
// interface 抽象，确保离线编译与可测试性。
//
// 事件类型常量引用 internal/domain/event.go 提供的 domain.EventXxx 常量
// （由 Task 5.1 并行创建），确保与 Registry.EmitTyped 类型分发一致。
//
// 二开扩展点：
//   - 替换依赖实现：通过 New*Hook 构造函数注入自定义 MetricsRecorder/
//     EvalBuffer/ProfileUpdater 等实现
//   - 新增 Hook：实现 domain.Hook interface 并通过 HookRegistry.Register 注入
//   - 事件过滤：每个 Hook 的 OnEvent 根据 event.EventType 过滤，无关事件直接
//     return nil，可按需扩展过滤逻辑
// ============================================================================

// 事件类型常量（本地定义）。
// 大部分事件类型已由 internal/domain/event.go 提供 domain.EventXxx 常量，
// 此处仅保留 domain 包未定义的事件类型。
// TODO 待 domain 包补充 EventRecall/EventRank 后统一切换。
const (
	eventRecall = "recall" // 召回事件（domain 包暂未定义）
	eventRank   = "rank"   // 排序事件（domain 包暂未定义）
)

// ----------------------------------------------------------------------------
// 依赖 interface 抽象（确保离线编译 + 可测试）
// ----------------------------------------------------------------------------

// MetricsRecorder 指标采集 interface，抽象 prometheus client。
// 二开：提供 prometheus.CounterVec/HistogramVec 的具体实现注入。
type MetricsRecorder interface {
	// IncCounter 递增计数器。
	IncCounter(name string, labels map[string]string)
	// ObserveHistogram 记录直方图观测值。
	ObserveHistogram(name string, value float64, labels map[string]string)
}

// EvalBuffer 评估缓冲 interface，抽象 Phase 15 评估体系。
// 二开：对接离线评估 Agent / 评估存储。
type EvalBuffer interface {
	// Append 追加事件到评估缓冲。
	Append(event domain.BehaviorEvent)
	// Flush 刷新缓冲（定期调用）。
	Flush() error
}

// ProfileUpdater 画像更新 interface，抽象 domain.ProfileManager 的写操作。
// 二开：注入 domain.ProfileManager 实现，触发画像增量更新。
type ProfileUpdater interface {
	// UpdateOnAction 根据用户行为更新画像。
	UpdateOnAction(ctx context.Context, key domain.UserKey, action, articleID string) error
}

// QualityRecorder 质量记录 interface，抽象 QualityReport 表写入。
// 二开：对接图谱 QualityReport 节点 / DB 表。
type QualityRecorder interface {
	// RecordQuality 记录文章质量评分。
	RecordQuality(ctx context.Context, quality domain.ArticleQuality) error
}

// CoOccurrenceUpdater 共现矩阵 + 图谱边写入 interface，抽象 CF 反馈写路径。
// 二开：注入 CF 引擎 + domain.GraphQuerier 实现共现与 CO_OCCURRED_WITH 边写入。
type CoOccurrenceUpdater interface {
	// UpdateCoOccurrence 更新文章共现矩阵（点击事件触发）。
	UpdateCoOccurrence(ctx context.Context, userID string, articleID string) error
	// AddCOOccurredEdge 写入图谱 CO_OCCURRED_WITH 边。
	AddCOOccurredEdge(ctx context.Context, articleA, articleB string) error
}

// RerankFeedbackRecorder 重排反馈记录 interface，抽象 rerank 训练数据写入。
// 二开：对接 rerank 模型训练管线（特征 + label 落盘）。
type RerankFeedbackRecorder interface {
	// RecordRerankFeedback 记录 rerank 分数与实际点击结果。
	RecordRerankFeedback(ctx context.Context, userID, articleID string, rerankScore float64, clicked bool) error
}

// GraphStatsRecorder 图谱统计记录 interface，抽象图谱查询耗时与召回数记录。
// 二开：对接 Prometheus histogram / obs 包 MetricsCollector。
type GraphStatsRecorder interface {
	// RecordGraphStats 记录图谱查询耗时与召回数。
	RecordGraphStats(ctx context.Context, cypher string, duration time.Duration, recalled int) error
}

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// toFloat64 将 any 安全转换为 float64（支持 int/float64/time.Duration 等）。
func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case time.Duration:
		return float64(x.Milliseconds()), true
	}
	return 0, false
}

// payloadFloat 从 BehaviorEvent.Payload 取 float64 字段，缺失返回 0。
func payloadFloat(e domain.BehaviorEvent, key string) float64 {
	if v, ok := e.Payload[key]; ok {
		if f, ok := toFloat64(v); ok {
			return f
		}
	}
	return 0
}

// ============================================================================
// 1. LogHook — 结构化日志 Hook
// ============================================================================

// LogHook 将行为事件记录到结构化日志。
// 二开：通过 NewLogHook 注入自定义 *slog.Logger；TODO 对接 zlog 替换 slog。
type LogHook struct {
	logger *slog.Logger
}

// NewLogHook 创建日志 Hook。
// logger 结构化日志器（可为 nil，默认用 slog.Default()）。
func NewLogHook(logger *slog.Logger) *LogHook {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogHook{logger: logger}
}

// Name 返回 Hook 名称。
func (h *LogHook) Name() string { return "log" }

// OnEvent 记录所有事件到结构化日志（不过滤）。
func (h *LogHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	h.logger.InfoContext(ctx, "behavior event",
		slog.String("event_id", e.EventID),
		slog.String("event_type", e.EventType),
		slog.String("user_id", e.UserID),
		slog.String("article_id", e.ArticleID),
		slog.String("channel", e.Channel),
		slog.String("path", e.PathTaken),
	)
	return nil
}

// ============================================================================
// 2. MetricsHook — Prometheus 指标 Hook
// ============================================================================

// MetricsHook 更新 Prometheus 指标（事件计数 + 耗时直方图）。
// 二开：通过 NewMetricsHook 注入 MetricsRecorder 实现（如 prometheus 适配器）。
type MetricsHook struct {
	recorder MetricsRecorder
}

// NewMetricsHook 创建指标 Hook。
// recorder 指标采集器（若 nil 则 Hook 不采集，直接 return）。
func NewMetricsHook(recorder MetricsRecorder) *MetricsHook {
	return &MetricsHook{recorder: recorder}
}

// Name 返回 Hook 名称。
func (h *MetricsHook) Name() string { return "metrics" }

// OnEvent 对所有事件递增计数器，对含耗时的记录直方图。
func (h *MetricsHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.recorder == nil {
		return nil
	}
	labels := map[string]string{
		"event_type": e.EventType,
		"channel":    e.Channel,
		"path":       e.PathTaken,
	}
	h.recorder.IncCounter("behavior_event_total", labels)
	// 若 Payload 含 duration_ms，记录直方图
	if d, ok := e.Payload["duration_ms"]; ok {
		if ms, ok := toFloat64(d); ok {
			h.recorder.ObserveHistogram("behavior_event_duration_ms", ms, labels)
		}
	}
	return nil
}

// ============================================================================
// 3. EvalHook — 评估缓冲 Hook
// ============================================================================

// EvalHook 将事件追加到评估缓冲，供定期 flush 落盘离线评估。
// 二开：通过 NewEvalHook 注入 EvalBuffer；TODO 对接 Phase 15 评估体系。
type EvalHook struct {
	buf EvalBuffer
}

// NewEvalHook 创建评估 Hook。
// buf 评估缓冲（若 nil 则不缓冲）。
func NewEvalHook(buf EvalBuffer) *EvalHook {
	return &EvalHook{buf: buf}
}

// Name 返回 Hook 名称。
func (h *EvalHook) Name() string { return "eval" }

// OnEvent 过滤评估相关事件（召回/排序/重排/质量/点击/路径命中）追加到缓冲。
func (h *EvalHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.buf == nil {
		return nil
	}
	switch e.EventType {
	case eventRecall, eventRank,
		domain.EventRerank, domain.EventQualityJudge,
		domain.EventClick, domain.EventLike, domain.EventDislike,
		domain.EventFavorite, domain.EventReadComplete,
		domain.EventFastPathHit, domain.EventSlowPathHit, domain.EventHybridMerge:
		h.buf.Append(e)
	}
	return nil
}

// ============================================================================
// 4. OnlineFeedbackHook — 在线画像反馈 Hook
// ============================================================================

// OnlineFeedbackHook 在点击/点赞/收藏/完成事件时触发画像增量更新。
// 二开：通过 NewOnlineFeedbackHook 注入 ProfileUpdater（可适配 domain.ProfileManager）。
type OnlineFeedbackHook struct {
	updater ProfileUpdater
}

// NewOnlineFeedbackHook 创建在线反馈 Hook。
// updater 画像更新器（若 nil 则不更新）。
func NewOnlineFeedbackHook(updater ProfileUpdater) *OnlineFeedbackHook {
	return &OnlineFeedbackHook{updater: updater}
}

// Name 返回 Hook 名称。
func (h *OnlineFeedbackHook) Name() string { return "online_feedback" }

// OnEvent 过滤用户行为事件，触发画像更新。
func (h *OnlineFeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.updater == nil {
		return nil
	}
	switch e.EventType {
	case domain.EventClick, domain.EventLike, domain.EventDislike,
		domain.EventFavorite, domain.EventReadComplete:
	default:
		return nil
	}
	if e.UserID == "" || e.ArticleID == "" {
		return nil
	}
	key := domain.UserKey{UserID: e.UserID, Channel: e.Channel}
	return h.updater.UpdateOnAction(ctx, key, e.EventType, e.ArticleID)
}

// ============================================================================
// 5. QualityFeedbackHook — 质量反馈 Hook
// ============================================================================

// QualityFeedbackHook 在质量评判事件时记录 ArticleQuality 到 QualityReport。
// 二开：通过 NewQualityFeedbackHook 注入 QualityRecorder（可写图谱节点或 DB）。
type QualityFeedbackHook struct {
	recorder QualityRecorder
}

// NewQualityFeedbackHook 创建质量反馈 Hook。
func NewQualityFeedbackHook(recorder QualityRecorder) *QualityFeedbackHook {
	return &QualityFeedbackHook{recorder: recorder}
}

// Name 返回 Hook 名称。
func (h *QualityFeedbackHook) Name() string { return "quality_feedback" }

// OnEvent 过滤 quality_judge 事件，记录质量评分。
func (h *QualityFeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.recorder == nil {
		return nil
	}
	if e.EventType != domain.EventQualityJudge {
		return nil
	}
	q := extractQuality(e)
	if q.ArticleID == "" {
		return nil
	}
	return h.recorder.RecordQuality(ctx, q)
}

// extractQuality 从 BehaviorEvent.Payload 提取 ArticleQuality。
func extractQuality(e domain.BehaviorEvent) domain.ArticleQuality {
	q := domain.ArticleQuality{ArticleID: e.ArticleID}
	q.Authority = payloadFloat(e, "authority")
	q.Depth = payloadFloat(e, "depth")
	q.Freshness = payloadFloat(e, "freshness")
	q.Completeness = payloadFloat(e, "completeness")
	q.Readability = payloadFloat(e, "readability")
	q.Citation = payloadFloat(e, "citation")
	q.Overall = payloadFloat(e, "overall")
	if v, ok := e.Payload["grade"]; ok {
		if s, ok := v.(string); ok {
			q.Grade = s
		}
	}
	return q
}

// ============================================================================
// 6. CFFeedbackHook — 协同过滤反馈 Hook
// ============================================================================

// CFFeedbackHook 在点击事件时更新共现矩阵 + 写入图谱 CO_OCCURRED_WITH 边。
// 二开：通过 NewCFFeedbackHook 注入 CoOccurrenceUpdater（适配 CF 引擎 + GraphQuerier）。
type CFFeedbackHook struct {
	updater CoOccurrenceUpdater
}

// NewCFFeedbackHook 创建 CF 反馈 Hook。
func NewCFFeedbackHook(updater CoOccurrenceUpdater) *CFFeedbackHook {
	return &CFFeedbackHook{updater: updater}
}

// Name 返回 Hook 名称。
func (h *CFFeedbackHook) Name() string { return "cf_feedback" }

// OnEvent 过滤 click 事件，更新共现矩阵与图谱边。
func (h *CFFeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.updater == nil {
		return nil
	}
	if e.EventType != domain.EventClick {
		return nil
	}
	if e.UserID == "" || e.ArticleID == "" {
		return nil
	}
	if err := h.updater.UpdateCoOccurrence(ctx, e.UserID, e.ArticleID); err != nil {
		return err
	}
	// 若 Payload 携带 co_occurred_with（与当前文章共同点击的文章列表），写图谱边
	if related, ok := e.Payload["co_occurred_with"]; ok {
		if list, ok := related.([]string); ok {
			for _, other := range list {
				if other == "" || other == e.ArticleID {
					continue
				}
				if err := h.updater.AddCOOccurredEdge(ctx, e.ArticleID, other); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ============================================================================
// 7. RerankFeedbackHook — 重排反馈 Hook
// ============================================================================

// RerankFeedbackHook 记录 rerank 分数与实际点击，用于 rerank 模型训练。
// 二开：通过 NewRerankFeedbackHook 注入 RerankFeedbackRecorder（对接训练管线）。
//
// 处理两类事件：
//   - rerank 事件：记录 rerank 分数（clicked=false，待后续点击配对）
//   - click 事件：从 Payload 取 rerank_score，记录 clicked=true
type RerankFeedbackHook struct {
	recorder RerankFeedbackRecorder
}

// NewRerankFeedbackHook 创建 rerank 反馈 Hook。
func NewRerankFeedbackHook(recorder RerankFeedbackRecorder) *RerankFeedbackHook {
	return &RerankFeedbackHook{recorder: recorder}
}

// Name 返回 Hook 名称。
func (h *RerankFeedbackHook) Name() string { return "rerank_feedback" }

// OnEvent 过滤 rerank / click 事件，记录 rerank 反馈。
func (h *RerankFeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.recorder == nil {
		return nil
	}
	var clicked bool
	switch e.EventType {
	case domain.EventRerank:
		clicked = false
	case domain.EventClick:
		clicked = true
	default:
		return nil
	}
	if e.ArticleID == "" {
		return nil
	}
	score := payloadFloat(e, "rerank_score")
	return h.recorder.RecordRerankFeedback(ctx, e.UserID, e.ArticleID, score, clicked)
}

// ============================================================================
// 8. GraphFeedbackHook — 图谱反馈 Hook
// ============================================================================

// GraphFeedbackHook 记录图谱查询耗时与召回数，用于图谱性能监控。
// 二开：通过 NewGraphFeedbackHook 注入 GraphStatsRecorder（对接 obs 包）。
type GraphFeedbackHook struct {
	recorder GraphStatsRecorder
}

// NewGraphFeedbackHook 创建图谱反馈 Hook。
func NewGraphFeedbackHook(recorder GraphStatsRecorder) *GraphFeedbackHook {
	return &GraphFeedbackHook{recorder: recorder}
}

// Name 返回 Hook 名称。
func (h *GraphFeedbackHook) Name() string { return "graph_feedback" }

// OnEvent 过滤 graph_query 事件，记录耗时与召回数。
func (h *GraphFeedbackHook) OnEvent(ctx context.Context, e domain.BehaviorEvent) error {
	if h.recorder == nil {
		return nil
	}
	if e.EventType != domain.EventGraphQuery {
		return nil
	}
	cypher, _ := e.Payload["cypher"].(string)
	duration := time.Duration(0)
	if v, ok := e.Payload["duration"]; ok {
		if d, ok := toFloat64(v); ok {
			duration = time.Duration(d) * time.Millisecond
		}
	}
	recalled := 0
	if v, ok := e.Payload["recalled"]; ok {
		if n, ok := toFloat64(v); ok {
			recalled = int(n)
		}
	}
	return h.recorder.RecordGraphStats(ctx, cypher, duration, recalled)
}
