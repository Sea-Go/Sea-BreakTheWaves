package quality

import (
	"context"
	"errors"
	"testing"
	"time"

	"sea/internal/domain"
)

// mockFeedbackRepo mock FeedbackRepo。
type mockFeedbackRepo struct {
	saved   []QualityFeedback
	saveErr error
	listErr error
}

func (m *mockFeedbackRepo) SaveFeedback(ctx context.Context, feedback QualityFeedback) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, feedback)
	return nil
}

func (m *mockFeedbackRepo) ListFeedback(ctx context.Context, articleID string) ([]QualityFeedback, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.saved, nil
}

// mockGraphQuerier mock GraphQuerier。
type mockGraphQuerier struct {
	reports []QualityReportInput
	err     error
}

func (m *mockGraphQuerier) UpsertQualityReport(ctx context.Context, report QualityReportInput) error {
	if m.err != nil {
		return m.err
	}
	m.reports = append(m.reports, report)
	return nil
}

func TestFeedbackHook_ImplementsHook(t *testing.T) {
	var _ domain.Hook = (*FeedbackHook)(nil)
}

func TestFeedbackHook_Name(t *testing.T) {
	h := NewFeedbackHook(&mockFeedbackRepo{}, &mockGraphQuerier{})
	if got := h.Name(); got != "quality_feedback" {
		t.Fatalf("expected name quality_feedback, got %s", got)
	}
}

func TestFeedbackHook_OnEvent_QualityJudge(t *testing.T) {
	repo := &mockFeedbackRepo{}
	graph := &mockGraphQuerier{}
	h := NewFeedbackHook(repo, graph)
	ts := time.Now()
	e := domain.BehaviorEvent{
		EventID:   "e1",
		EventType: "quality_judge",
		UserID:    "u1",
		ArticleID: "a1",
		Timestamp: ts,
		Payload: map[string]any{
			"authority":    0.8,
			"depth":        0.7,
			"freshness":    0.6,
			"completeness": 0.7,
			"readability":  0.8,
			"citation":     0.6,
			"overall":      0.75,
			"grade":        "A",
			"feedback":     "good article",
		},
	}
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("expected 1 saved feedback, got %d", len(repo.saved))
	}
	fb := repo.saved[0]
	if fb.ID != "e1" || fb.ArticleID != "a1" || fb.UserID != "u1" {
		t.Fatalf("unexpected feedback: %+v", fb)
	}
	if fb.Quality.Overall != 0.75 || fb.Quality.Grade != "A" {
		t.Fatalf("unexpected quality: %+v", fb.Quality)
	}
	if fb.Feedback != "good article" {
		t.Fatalf("expected feedback 'good article', got %s", fb.Feedback)
	}
	if len(graph.reports) != 1 {
		t.Fatalf("expected 1 graph report, got %d", len(graph.reports))
	}
	if graph.reports[0].ArticleID != "a1" {
		t.Fatalf("expected graph report article a1, got %s", graph.reports[0].ArticleID)
	}
	if graph.reports[0].Quality.Overall != 0.75 {
		t.Fatalf("expected graph report overall 0.75, got %f", graph.reports[0].Quality.Overall)
	}
}

func TestFeedbackHook_OnEvent_OtherType(t *testing.T) {
	repo := &mockFeedbackRepo{}
	graph := &mockGraphQuerier{}
	h := NewFeedbackHook(repo, graph)
	e := domain.BehaviorEvent{
		EventID:   "e1",
		EventType: "click",
		ArticleID: "a1",
	}
	if err := h.OnEvent(context.Background(), e); err != nil {
		t.Fatalf("OnEvent should ignore non-quality_judge events, got %v", err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("expected 0 saved, got %d", len(repo.saved))
	}
	if len(graph.reports) != 0 {
		t.Fatalf("expected 0 reports, got %d", len(graph.reports))
	}
}

func TestFeedbackHook_OnEvent_SaveError(t *testing.T) {
	repo := &mockFeedbackRepo{saveErr: errors.New("db error")}
	graph := &mockGraphQuerier{}
	h := NewFeedbackHook(repo, graph)
	e := domain.BehaviorEvent{
		EventID:   "e1",
		EventType: "quality_judge",
		ArticleID: "a1",
	}
	if err := h.OnEvent(context.Background(), e); err == nil {
		t.Fatal("expected error on save failure, got nil")
	}
}

func TestFeedbackHook_SaveFeedback_Success(t *testing.T) {
	repo := &mockFeedbackRepo{}
	graph := &mockGraphQuerier{}
	h := NewFeedbackHook(repo, graph)
	fb := QualityFeedback{
		ID:        "f1",
		ArticleID: "a1",
		Quality:   domain.ArticleQuality{Overall: 0.8, Grade: "A"},
		CreatedAt: time.Now(),
	}
	if err := h.SaveFeedback(context.Background(), fb); err != nil {
		t.Fatalf("SaveFeedback failed: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("expected 1 saved, got %d", len(repo.saved))
	}
	if len(graph.reports) != 1 {
		t.Fatalf("expected 1 graph report, got %d", len(graph.reports))
	}
}

func TestFeedbackHook_SaveFeedback_RepoError(t *testing.T) {
	repo := &mockFeedbackRepo{saveErr: errors.New("db error")}
	graph := &mockGraphQuerier{}
	h := NewFeedbackHook(repo, graph)
	err := h.SaveFeedback(context.Background(), QualityFeedback{ArticleID: "a1"})
	if err == nil {
		t.Fatal("expected error on repo failure, got nil")
	}
}

func TestFeedbackHook_SaveFeedback_GraphError(t *testing.T) {
	repo := &mockFeedbackRepo{}
	graph := &mockGraphQuerier{err: errors.New("graph error")}
	h := NewFeedbackHook(repo, graph)
	err := h.SaveFeedback(context.Background(), QualityFeedback{ArticleID: "a1"})
	if err == nil {
		t.Fatal("expected error on graph failure, got nil")
	}
	// repo 已保存，graph 失败
	if len(repo.saved) != 1 {
		t.Fatalf("expected repo saved 1, got %d", len(repo.saved))
	}
}

func TestFeedbackHook_CalibrateRubrics(t *testing.T) {
	h := NewFeedbackHook(&mockFeedbackRepo{}, &mockGraphQuerier{})
	rubrics := DefaultRubrics()
	if err := h.CalibrateRubrics(context.Background(), rubrics); err != nil {
		t.Fatalf("CalibrateRubrics failed: %v", err)
	}
}

func TestFeedbackHook_CalibrateRubrics_Nil(t *testing.T) {
	h := NewFeedbackHook(&mockFeedbackRepo{}, &mockGraphQuerier{})
	if err := h.CalibrateRubrics(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil rubrics, got nil")
	}
}

func TestExtractQualityFromPayload(t *testing.T) {
	payload := map[string]any{
		"article_id":   "a1",
		"authority":    0.8,
		"depth":        0.7,
		"overall":      0.75,
		"grade":        "A",
		"feedback":     "good",
		"unknown_flag": true,
	}
	q := extractQualityFromPayload(payload)
	if q.ArticleID != "a1" {
		t.Fatalf("expected article a1, got %s", q.ArticleID)
	}
	if q.Authority != 0.8 {
		t.Fatalf("expected authority 0.8, got %f", q.Authority)
	}
	if q.Overall != 0.75 {
		t.Fatalf("expected overall 0.75, got %f", q.Overall)
	}
	if q.Grade != "A" {
		t.Fatalf("expected grade A, got %s", q.Grade)
	}
}

func TestExtractQualityFromPayload_Nil(t *testing.T) {
	q := extractQualityFromPayload(nil)
	if q.ArticleID != "" {
		t.Fatalf("expected empty article id, got %s", q.ArticleID)
	}
}

func TestExtractFloat(t *testing.T) {
	m := map[string]any{
		"f64":  float64(0.5),
		"int":  1,
		"i64":  int64(2),
		"str":  "not a number",
		"miss": nil,
	}
	if got := extractFloat(m, "f64"); got != 0.5 {
		t.Fatalf("expected 0.5, got %f", got)
	}
	if got := extractFloat(m, "int"); got != 1.0 {
		t.Fatalf("expected 1.0, got %f", got)
	}
	if got := extractFloat(m, "i64"); got != 2.0 {
		t.Fatalf("expected 2.0, got %f", got)
	}
	if got := extractFloat(m, "str"); got != 0 {
		t.Fatalf("expected 0 for string, got %f", got)
	}
	if got := extractFloat(m, "missing"); got != 0 {
		t.Fatalf("expected 0 for missing, got %f", got)
	}
}
