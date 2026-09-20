package quality

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// mockJudger mock domain.QualityJudger。
type mockJudger struct {
	qualities map[string]domain.ArticleQuality
	err       error
}

func (m *mockJudger) Judge(ctx context.Context, req domain.QualityRequest) (domain.ArticleQuality, error) {
	if m.err != nil {
		return domain.ArticleQuality{}, m.err
	}
	if q, ok := m.qualities[req.ArticleID]; ok {
		return q, nil
	}
	return domain.ArticleQuality{Overall: 0.5}, nil
}

// mockGroundingChecker mock GroundingChecker。
type mockGroundingChecker struct {
	score float64
	err   error
}

func (m *mockGroundingChecker) Check(ctx context.Context, candidate domain.Candidate) (float64, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.score, nil
}

func TestFilter_Filter(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{
			"a1": {Overall: 0.9},
			"a2": {Overall: 0.3},
			"a3": {Overall: 0.7},
		},
	}
	f := NewFilter(0.6, judger)
	candidates := []domain.Candidate{
		{ArticleID: "a1"},
		{ArticleID: "a2"},
		{ArticleID: "a3"},
	}
	result, err := f.Filter(context.Background(), candidates, 0.6)
	if err != nil {
		t.Fatalf("Filter failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 candidates pass threshold, got %d", len(result))
	}
	// a2 (0.3) 应被过滤
	for _, c := range result {
		if c.ArticleID == "a2" {
			t.Fatal("a2 should be filtered out")
		}
	}
}

func TestFilter_Filter_DefaultThreshold(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{
			"a1": {Overall: 0.9},
			"a2": {Overall: 0.3},
		},
	}
	f := NewFilter(0.6, judger)
	candidates := []domain.Candidate{{ArticleID: "a1"}, {ArticleID: "a2"}}
	// threshold=0 → 使用 struct 默认阈值 0.6
	result, err := f.Filter(context.Background(), candidates, 0)
	if err != nil {
		t.Fatalf("Filter failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 candidate pass default threshold, got %d", len(result))
	}
}

func TestFilter_Filter_JudgeError(t *testing.T) {
	judger := &mockJudger{err: errors.New("judge fail")}
	f := NewFilter(0.6, judger)
	candidates := []domain.Candidate{{ArticleID: "a1"}}
	result, err := f.Filter(context.Background(), candidates, 0.6)
	if err != nil {
		t.Fatalf("Filter should not return error on judge fail, got %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 candidates when judge fails, got %d", len(result))
	}
}

func TestFilter_Validate_Pass(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{
			"a1": {Overall: 0.9, Grade: "A"},
		},
	}
	grounding := &mockGroundingChecker{score: 0.9}
	f := NewFilter(0.6, judger).WithGrounding(grounding)
	result, err := f.Validate(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected pass, got fail: %v", result.Reasons)
	}
	if result.GroundingScore != 0.9 {
		t.Fatalf("expected grounding 0.9, got %f", result.GroundingScore)
	}
	if result.Quality.Overall != 0.9 {
		t.Fatalf("expected quality overall 0.9, got %f", result.Quality.Overall)
	}
}

func TestFilter_Validate_FailLowQuality(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{
			"a1": {Overall: 0.3},
		},
	}
	f := NewFilter(0.6, judger)
	result, err := f.Validate(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if result.Pass {
		t.Fatal("expected fail due to low quality, got pass")
	}
	if len(result.Reasons) == 0 {
		t.Fatal("expected non-empty reasons")
	}
}

func TestFilter_Validate_FailLowGrounding(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{
			"a1": {Overall: 0.9},
		},
	}
	grounding := &mockGroundingChecker{score: 0.3}
	f := NewFilter(0.6, judger).WithGrounding(grounding)
	result, err := f.Validate(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if result.Pass {
		t.Fatal("expected fail due to low grounding, got pass")
	}
	if result.GroundingScore != 0.3 {
		t.Fatalf("expected grounding 0.3, got %f", result.GroundingScore)
	}
}

func TestFilter_Validate_GroundingError(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{"a1": {Overall: 0.9}},
	}
	grounding := &mockGroundingChecker{err: errors.New("grounding fail")}
	f := NewFilter(0.6, judger).WithGrounding(grounding)
	_, err := f.Validate(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err == nil {
		t.Fatal("expected error on grounding check failure")
	}
}

func TestFilter_Validate_NoGroundingChecker(t *testing.T) {
	judger := &mockJudger{
		qualities: map[string]domain.ArticleQuality{"a1": {Overall: 0.9}},
	}
	f := NewFilter(0.6, judger)
	result, err := f.Validate(context.Background(), domain.Candidate{ArticleID: "a1"})
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if !result.Pass {
		t.Fatalf("expected pass without grounding checker, got fail: %v", result.Reasons)
	}
	if result.GroundingScore != 1.0 {
		t.Fatalf("expected default grounding 1.0, got %f", result.GroundingScore)
	}
}

func TestExtractContent(t *testing.T) {
	if got := extractContent(domain.Candidate{ArticleID: "a1"}); got != "" {
		t.Fatalf("expected empty content, got %s", got)
	}
	c := domain.Candidate{
		ArticleID: "a1",
		Extra:     map[string]any{"content": "hello"},
	}
	if got := extractContent(c); got != "hello" {
		t.Fatalf("expected hello, got %s", got)
	}
}
