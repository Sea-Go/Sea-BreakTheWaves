package event

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 内置 Hook 单测（Task 5.3）
//
// 每个 Hook 用 mock 依赖测试：验证 OnEvent 事件过滤 + 依赖调用。
// 事件类型常量引用 domain.EventXxx，确保与 Registry.EmitTyped 分发一致。
// ============================================================================

// newEvent 构造测试用 BehaviorEvent。
func newEvent(eventType, userID, articleID string, payload map[string]any) domain.BehaviorEvent {
	return domain.BehaviorEvent{
		EventID:   "evt-1",
		EventType: eventType,
		UserID:    userID,
		ArticleID: articleID,
		Channel:   "tech",
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

// ----------------------------------------------------------------------------
// mock 依赖实现
// ----------------------------------------------------------------------------

// mockMetricsRecorder 记录 IncCounter / ObserveHistogram 调用。
type mockMetricsRecorder struct {
	counters   []counterCall
	histograms []histogramCall
}

type counterCall struct {
	name   string
	labels map[string]string
}

type histogramCall struct {
	name   string
	value  float64
	labels map[string]string
}

func (m *mockMetricsRecorder) IncCounter(name string, labels map[string]string) {
	m.counters = append(m.counters, counterCall{name: name, labels: labels})
}

func (m *mockMetricsRecorder) ObserveHistogram(name string, value float64, labels map[string]string) {
	m.histograms = append(m.histograms, histogramCall{name: name, value: value, labels: labels})
}

// mockEvalBuffer 记录 Append 的事件。
type mockEvalBuffer struct {
	events []domain.BehaviorEvent
	flush  int
}

func (m *mockEvalBuffer) Append(event domain.BehaviorEvent) { m.events = append(m.events, event) }
func (m *mockEvalBuffer) Flush() error                      { m.flush++; return nil }

// mockProfileUpdater 记录 UpdateOnAction 调用。
type mockProfileUpdater struct {
	calls []profileCall
	err   error
}

type profileCall struct {
	key       domain.UserKey
	action    string
	articleID string
}

func (m *mockProfileUpdater) UpdateOnAction(ctx context.Context, key domain.UserKey, action, articleID string) error {
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, profileCall{key: key, action: action, articleID: articleID})
	return nil
}

// mockQualityRecorder 记录 RecordQuality 调用。
type mockQualityRecorder struct {
	qualities []domain.ArticleQuality
	err       error
}

func (m *mockQualityRecorder) RecordQuality(ctx context.Context, quality domain.ArticleQuality) error {
	if m.err != nil {
		return m.err
	}
	m.qualities = append(m.qualities, quality)
	return nil
}

// mockCoOccurrenceUpdater 记录共现与边写入调用。
type mockCoOccurrenceUpdater struct {
	coEdges     []string // articleA,articleB 对
	updates     []string // userID,articleID 对
	edgeErr     error
	coUpdateErr error
}

func (m *mockCoOccurrenceUpdater) UpdateCoOccurrence(ctx context.Context, userID string, articleID string) error {
	if m.coUpdateErr != nil {
		return m.coUpdateErr
	}
	m.updates = append(m.updates, userID, articleID)
	return nil
}

func (m *mockCoOccurrenceUpdater) AddCOOccurredEdge(ctx context.Context, articleA, articleB string) error {
	if m.edgeErr != nil {
		return m.edgeErr
	}
	m.coEdges = append(m.coEdges, articleA, articleB)
	return nil
}

// mockRerankFeedbackRecorder 记录 rerank 反馈调用。
type mockRerankFeedbackRecorder struct {
	calls []rerankFeedbackCall
	err   error
}

type rerankFeedbackCall struct {
	userID      string
	articleID   string
	rerankScore float64
	clicked     bool
}

func (m *mockRerankFeedbackRecorder) RecordRerankFeedback(ctx context.Context, userID, articleID string, rerankScore float64, clicked bool) error {
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, rerankFeedbackCall{userID: userID, articleID: articleID, rerankScore: rerankScore, clicked: clicked})
	return nil
}

// mockGraphStatsRecorder 记录图谱统计调用。
type mockGraphStatsRecorder struct {
	calls []graphStatsCall
	err   error
}

type graphStatsCall struct {
	cypher   string
	duration time.Duration
	recalled int
}

func (m *mockGraphStatsRecorder) RecordGraphStats(ctx context.Context, cypher string, duration time.Duration, recalled int) error {
	if m.err != nil {
		return m.err
	}
	m.calls = append(m.calls, graphStatsCall{cypher: cypher, duration: duration, recalled: recalled})
	return nil
}

// ----------------------------------------------------------------------------
// 1. LogHook 测试
// ----------------------------------------------------------------------------

func TestLogHook_Name(t *testing.T) {
	h := NewLogHook(nil)
	if got := h.Name(); got != "log" {
		t.Fatalf("Name() = %q, want %q", got, "log")
	}
}

func TestLogHook_OnEvent_NoError(t *testing.T) {
	// 用静默 logger 避免污染测试输出
	h := NewLogHook(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestLogHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*LogHook)(nil)
}

// ----------------------------------------------------------------------------
// 2. MetricsHook 测试
// ----------------------------------------------------------------------------

func TestMetricsHook_Name(t *testing.T) {
	h := NewMetricsHook(nil)
	if got := h.Name(); got != "metrics" {
		t.Fatalf("Name() = %q, want %q", got, "metrics")
	}
}

func TestMetricsHook_OnEvent_NilRecorder_NoOp(t *testing.T) {
	h := NewMetricsHook(nil)
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestMetricsHook_OnEvent_IncrementsCounter(t *testing.T) {
	rec := &mockMetricsRecorder{}
	h := NewMetricsHook(rec)
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.counters) != 1 {
		t.Fatalf("counters = %d, want 1", len(rec.counters))
	}
	if rec.counters[0].labels["event_type"] != domain.EventClick {
		t.Fatalf("label event_type = %q, want %q", rec.counters[0].labels["event_type"], domain.EventClick)
	}
	if len(rec.histograms) != 0 {
		t.Fatalf("histograms = %d, want 0 (no duration)", len(rec.histograms))
	}
}

func TestMetricsHook_OnEvent_WithDuration(t *testing.T) {
	rec := &mockMetricsRecorder{}
	h := NewMetricsHook(rec)
	e := newEvent(domain.EventGraphQuery, "u1", "a1", map[string]any{"duration_ms": float64(42)})
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.counters) != 1 {
		t.Fatalf("counters = %d, want 1", len(rec.counters))
	}
	if len(rec.histograms) != 1 {
		t.Fatalf("histograms = %d, want 1", len(rec.histograms))
	}
	if rec.histograms[0].value != 42 {
		t.Fatalf("histogram value = %v, want 42", rec.histograms[0].value)
	}
}

func TestMetricsHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*MetricsHook)(nil)
}

// ----------------------------------------------------------------------------
// 3. EvalHook 测试
// ----------------------------------------------------------------------------

func TestEvalHook_Name(t *testing.T) {
	h := NewEvalHook(nil)
	if got := h.Name(); got != "eval" {
		t.Fatalf("Name() = %q, want %q", got, "eval")
	}
}

func TestEvalHook_OnEvent_NilBuffer_NoOp(t *testing.T) {
	h := NewEvalHook(nil)
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestEvalHook_OnEvent_FiltersRelevantEvents(t *testing.T) {
	buf := &mockEvalBuffer{}
	h := NewEvalHook(buf)
	ctx := context.Background()

	// 相关事件应缓冲
	relevant := []string{
		eventRecall, eventRank,
		domain.EventRerank, domain.EventQualityJudge,
		domain.EventClick, domain.EventLike, domain.EventDislike,
		domain.EventFavorite, domain.EventReadComplete,
		domain.EventFastPathHit, domain.EventSlowPathHit, domain.EventHybridMerge,
	}
	for _, et := range relevant {
		if err := h.OnEvent(ctx, newEvent(et, "u1", "a1", nil)); err != nil {
			t.Fatalf("OnEvent(%s) error: %v", et, err)
		}
	}
	if len(buf.events) != len(relevant) {
		t.Fatalf("buffered = %d, want %d", len(buf.events), len(relevant))
	}

	// 无关事件不应缓冲
	buf.events = nil
	if err := h.OnEvent(ctx, newEvent(domain.EventToolCall, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(tool_call) error: %v", err)
	}
	if len(buf.events) != 0 {
		t.Fatalf("buffered tool_call = %d, want 0", len(buf.events))
	}
}

func TestEvalHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*EvalHook)(nil)
}

// ----------------------------------------------------------------------------
// 4. OnlineFeedbackHook 测试
// ----------------------------------------------------------------------------

func TestOnlineFeedbackHook_Name(t *testing.T) {
	h := NewOnlineFeedbackHook(nil)
	if got := h.Name(); got != "online_feedback" {
		t.Fatalf("Name() = %q, want %q", got, "online_feedback")
	}
}

func TestOnlineFeedbackHook_OnEvent_NilUpdater_NoOp(t *testing.T) {
	h := NewOnlineFeedbackHook(nil)
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestOnlineFeedbackHook_OnEvent_FiltersBehaviorEvents(t *testing.T) {
	updater := &mockProfileUpdater{}
	h := NewOnlineFeedbackHook(updater)
	ctx := context.Background()

	// 行为事件应触发更新
	for _, et := range []string{
		domain.EventClick, domain.EventLike, domain.EventDislike,
		domain.EventFavorite, domain.EventReadComplete,
	} {
		if err := h.OnEvent(ctx, newEvent(et, "u1", "a1", nil)); err != nil {
			t.Fatalf("OnEvent(%s) error: %v", et, err)
		}
	}
	if len(updater.calls) != 5 {
		t.Fatalf("updater calls = %d, want 5", len(updater.calls))
	}
	if updater.calls[0].action != domain.EventClick {
		t.Fatalf("first action = %q, want %q", updater.calls[0].action, domain.EventClick)
	}
	if updater.calls[0].key.UserID != "u1" {
		t.Fatalf("key UserID = %q, want u1", updater.calls[0].key.UserID)
	}
	if updater.calls[0].key.Channel != "tech" {
		t.Fatalf("key Channel = %q, want tech", updater.calls[0].key.Channel)
	}

	// 无关事件不应触发
	if err := h.OnEvent(ctx, newEvent(domain.EventRerank, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(rerank) error: %v", err)
	}
	if len(updater.calls) != 5 {
		t.Fatalf("updater calls after irrelevant = %d, want 5", len(updater.calls))
	}
}

func TestOnlineFeedbackHook_OnEvent_MissingIDs(t *testing.T) {
	updater := &mockProfileUpdater{}
	h := NewOnlineFeedbackHook(updater)
	ctx := context.Background()

	// 缺 UserID 不触发
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "", "a1", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	// 缺 ArticleID 不触发
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "u1", "", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(updater.calls) != 0 {
		t.Fatalf("updater calls = %d, want 0", len(updater.calls))
	}
}

func TestOnlineFeedbackHook_OnEvent_PropagatesError(t *testing.T) {
	expected := errors.New("update failed")
	updater := &mockProfileUpdater{err: expected}
	h := NewOnlineFeedbackHook(updater)
	err := h.OnEvent(context.Background(), newEvent(domain.EventClick, "u1", "a1", nil))
	if !errors.Is(err, expected) {
		t.Fatalf("err = %v, want %v", err, expected)
	}
}

func TestOnlineFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*OnlineFeedbackHook)(nil)
}

// ----------------------------------------------------------------------------
// 5. QualityFeedbackHook 测试
// ----------------------------------------------------------------------------

func TestQualityFeedbackHook_Name(t *testing.T) {
	h := NewQualityFeedbackHook(nil)
	if got := h.Name(); got != "quality_feedback" {
		t.Fatalf("Name() = %q, want %q", got, "quality_feedback")
	}
}

func TestQualityFeedbackHook_OnEvent_NilRecorder_NoOp(t *testing.T) {
	h := NewQualityFeedbackHook(nil)
	e := newEvent(domain.EventQualityJudge, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestQualityFeedbackHook_OnEvent_FiltersQualityJudge(t *testing.T) {
	rec := &mockQualityRecorder{}
	h := NewQualityFeedbackHook(rec)
	ctx := context.Background()

	// quality_judge 事件应记录
	e := newEvent(domain.EventQualityJudge, "u1", "a1", map[string]any{
		"authority":    0.8,
		"depth":        0.7,
		"freshness":    0.9,
		"completeness": 0.6,
		"readability":  0.5,
		"citation":     0.4,
		"overall":      0.75,
		"grade":        "B",
	})
	if err := h.OnEvent(ctx, e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.qualities) != 1 {
		t.Fatalf("qualities = %d, want 1", len(rec.qualities))
	}
	q := rec.qualities[0]
	if q.ArticleID != "a1" {
		t.Fatalf("ArticleID = %q, want a1", q.ArticleID)
	}
	if q.Authority != 0.8 {
		t.Fatalf("Authority = %v, want 0.8", q.Authority)
	}
	if q.Grade != "B" {
		t.Fatalf("Grade = %q, want B", q.Grade)
	}
	if q.Overall != 0.75 {
		t.Fatalf("Overall = %v, want 0.75", q.Overall)
	}

	// 无关事件不应记录
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(click) error: %v", err)
	}
	if len(rec.qualities) != 1 {
		t.Fatalf("qualities after irrelevant = %d, want 1", len(rec.qualities))
	}
}

func TestQualityFeedbackHook_OnEvent_MissingArticleID(t *testing.T) {
	rec := &mockQualityRecorder{}
	h := NewQualityFeedbackHook(rec)
	if err := h.OnEvent(context.Background(), newEvent(domain.EventQualityJudge, "u1", "", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.qualities) != 0 {
		t.Fatalf("qualities = %d, want 0", len(rec.qualities))
	}
}

func TestQualityFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*QualityFeedbackHook)(nil)
}

// ----------------------------------------------------------------------------
// 6. CFFeedbackHook 测试
// ----------------------------------------------------------------------------

func TestCFFeedbackHook_Name(t *testing.T) {
	h := NewCFFeedbackHook(nil)
	if got := h.Name(); got != "cf_feedback" {
		t.Fatalf("Name() = %q, want %q", got, "cf_feedback")
	}
}

func TestCFFeedbackHook_OnEvent_NilUpdater_NoOp(t *testing.T) {
	h := NewCFFeedbackHook(nil)
	e := newEvent(domain.EventClick, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestCFFeedbackHook_OnEvent_FiltersClick(t *testing.T) {
	updater := &mockCoOccurrenceUpdater{}
	h := NewCFFeedbackHook(updater)
	ctx := context.Background()

	// click 事件应触发共现更新
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(click) error: %v", err)
	}
	if len(updater.updates) != 2 {
		t.Fatalf("updates = %v, want [u1, a1]", updater.updates)
	}

	// 无关事件不应触发
	if err := h.OnEvent(ctx, newEvent(domain.EventLike, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(like) error: %v", err)
	}
	if len(updater.updates) != 2 {
		t.Fatalf("updates after irrelevant = %v, want unchanged", updater.updates)
	}
}

func TestCFFeedbackHook_OnEvent_WritesGraphEdges(t *testing.T) {
	updater := &mockCoOccurrenceUpdater{}
	h := NewCFFeedbackHook(updater)
	ctx := context.Background()

	e := newEvent(domain.EventClick, "u1", "a1", map[string]any{
		"co_occurred_with": []string{"a2", "a3", "a1"}, // a1 应被跳过（自环）
	})
	if err := h.OnEvent(ctx, e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	// 应写入 a1-a2, a1-a3 两条边（a1-a1 自环跳过）
	if len(updater.coEdges) != 4 {
		t.Fatalf("coEdges = %v, want [a1,a2,a1,a3]", updater.coEdges)
	}
}

func TestCFFeedbackHook_OnEvent_MissingIDs(t *testing.T) {
	updater := &mockCoOccurrenceUpdater{}
	h := NewCFFeedbackHook(updater)
	ctx := context.Background()

	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "", "a1", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "u1", "", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(updater.updates) != 0 {
		t.Fatalf("updates = %v, want empty", updater.updates)
	}
}

func TestCFFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*CFFeedbackHook)(nil)
}

// ----------------------------------------------------------------------------
// 7. RerankFeedbackHook 测试
// ----------------------------------------------------------------------------

func TestRerankFeedbackHook_Name(t *testing.T) {
	h := NewRerankFeedbackHook(nil)
	if got := h.Name(); got != "rerank_feedback" {
		t.Fatalf("Name() = %q, want %q", got, "rerank_feedback")
	}
}

func TestRerankFeedbackHook_OnEvent_NilRecorder_NoOp(t *testing.T) {
	h := NewRerankFeedbackHook(nil)
	e := newEvent(domain.EventRerank, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestRerankFeedbackHook_OnEvent_RerankEvent(t *testing.T) {
	rec := &mockRerankFeedbackRecorder{}
	h := NewRerankFeedbackHook(rec)
	ctx := context.Background()

	e := newEvent(domain.EventRerank, "u1", "a1", map[string]any{"rerank_score": 0.9})
	if err := h.OnEvent(ctx, e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rec.calls))
	}
	if rec.calls[0].rerankScore != 0.9 {
		t.Fatalf("score = %v, want 0.9", rec.calls[0].rerankScore)
	}
	if rec.calls[0].clicked {
		t.Fatalf("clicked = true, want false")
	}
}

func TestRerankFeedbackHook_OnEvent_ClickEvent(t *testing.T) {
	rec := &mockRerankFeedbackRecorder{}
	h := NewRerankFeedbackHook(rec)
	ctx := context.Background()

	e := newEvent(domain.EventClick, "u1", "a1", map[string]any{"rerank_score": 0.5})
	if err := h.OnEvent(ctx, e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rec.calls))
	}
	if !rec.calls[0].clicked {
		t.Fatalf("clicked = false, want true")
	}
}

func TestRerankFeedbackHook_OnEvent_FiltersIrrelevant(t *testing.T) {
	rec := &mockRerankFeedbackRecorder{}
	h := NewRerankFeedbackHook(rec)
	ctx := context.Background()

	if err := h.OnEvent(ctx, newEvent(domain.EventLike, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %d, want 0", len(rec.calls))
	}
}

func TestRerankFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*RerankFeedbackHook)(nil)
}

// ----------------------------------------------------------------------------
// 8. GraphFeedbackHook 测试
// ----------------------------------------------------------------------------

func TestGraphFeedbackHook_Name(t *testing.T) {
	h := NewGraphFeedbackHook(nil)
	if got := h.Name(); got != "graph_feedback" {
		t.Fatalf("Name() = %q, want %q", got, "graph_feedback")
	}
}

func TestGraphFeedbackHook_OnEvent_NilRecorder_NoOp(t *testing.T) {
	h := NewGraphFeedbackHook(nil)
	e := newEvent(domain.EventGraphQuery, "u1", "a1", nil)
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
}

func TestGraphFeedbackHook_OnEvent_FiltersGraphQuery(t *testing.T) {
	rec := &mockGraphStatsRecorder{}
	h := NewGraphFeedbackHook(rec)
	ctx := context.Background()

	e := newEvent(domain.EventGraphQuery, "u1", "a1", map[string]any{
		"cypher":   "MATCH (n) RETURN n",
		"duration": 150,
		"recalled": 12,
	})
	if err := h.OnEvent(ctx, e); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rec.calls))
	}
	if rec.calls[0].cypher != "MATCH (n) RETURN n" {
		t.Fatalf("cypher = %q, want MATCH...", rec.calls[0].cypher)
	}
	if rec.calls[0].duration != 150*time.Millisecond {
		t.Fatalf("duration = %v, want 150ms", rec.calls[0].duration)
	}
	if rec.calls[0].recalled != 12 {
		t.Fatalf("recalled = %d, want 12", rec.calls[0].recalled)
	}

	// 无关事件不应触发
	if err := h.OnEvent(ctx, newEvent(domain.EventClick, "u1", "a1", nil)); err != nil {
		t.Fatalf("OnEvent(click) error: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls after irrelevant = %d, want 1", len(rec.calls))
	}
}

func TestGraphFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*GraphFeedbackHook)(nil)
}
