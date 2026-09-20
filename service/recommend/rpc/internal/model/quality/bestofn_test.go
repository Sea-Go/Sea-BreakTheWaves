package quality

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// bestOfNLLMMock mock LLMClient（复用 candidate.go/judge.go 定义的 LLMClient interface）。
// 按 LLMOptions 区分候选调用（StructuredOutputJSONSchema 非空）与裁判调用（Logprobs）。
// 线程安全（候选调用在 BestOfN 内并行，需 mutex 保护计数器）。
type bestOfNLLMMock struct {
	mu             sync.Mutex
	candidateJSONs []string // 候选调用循环返回的 JSON 列表
	candIdx        int
	judgeResp      string // 裁判调用返回的 logprobs JSON
	err            error
}

func (m *bestOfNLLMMock) Complete(ctx context.Context, prompt string, opts LLMOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	if opts.StructuredOutputJSONSchema != "" {
		idx := m.candIdx % len(m.candidateJSONs)
		m.candIdx++
		return m.candidateJSONs[idx], nil
	}
	if opts.Logprobs {
		return m.judgeResp, nil
	}
	return "{}", nil
}

func TestBestOfN_DefaultAttempts(t *testing.T) {
	b := NewBestOfN(nil, nil, 0)
	if b.attempts != 3 {
		t.Fatalf("expected default attempts 3, got %d", b.attempts)
	}
	if b.selectionMode != SelectionModePairwise {
		t.Fatalf("expected default selectionMode %s, got %s", SelectionModePairwise, b.selectionMode)
	}
}

func TestBestOfN_Evaluate_Pairwise(t *testing.T) {
	// 3 个候选质量不同，裁判统一返回 A 标签。pairwise 应选 6 维最优（overall 0.9）。
	llm := &bestOfNLLMMock{
		candidateJSONs: []string{
			`{"authority":0.6,"depth":0.6,"freshness":0.6,"completeness":0.6,"readability":0.6,"citation":0.6,"overall":0.6}`,
			`{"authority":0.9,"depth":0.9,"freshness":0.9,"completeness":0.9,"readability":0.9,"citation":0.9,"overall":0.9}`,
			`{"authority":0.5,"depth":0.5,"freshness":0.5,"completeness":0.5,"readability":0.5,"citation":0.5,"overall":0.5}`,
		},
		judgeResp: `{"logprobs":[{"token":"A","logprob":0.0}]}`,
	}
	candidate := NewCandidateAgent(llm, nil)
	judge := NewJudgeAgent(llm, nil)
	b := NewBestOfN(candidate, judge, 3)

	result, err := b.Evaluate(context.Background(), ArticleInput{ID: "a1", Content: "content"})
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if len(result.AllCandidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(result.AllCandidates))
	}
	if len(result.JudgeResults) != 3 {
		t.Fatalf("expected 3 judge results, got %d", len(result.JudgeResults))
	}
	// 最优候选 overall 应为 0.9（6 维全优）。
	if result.Best.Overall != 0.9 {
		t.Fatalf("expected best overall 0.9, got %f", result.Best.Overall)
	}
	if result.Best.ArticleID != "a1" {
		t.Fatalf("expected best article id a1, got %s", result.Best.ArticleID)
	}
}

func TestBestOfN_Evaluate_AllFail(t *testing.T) {
	llm := &bestOfNLLMMock{err: errors.New("llm fail")}
	candidate := NewCandidateAgent(llm, nil)
	judge := NewJudgeAgent(llm, nil)
	b := NewBestOfN(candidate, judge, 3)

	_, err := b.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err == nil {
		t.Fatal("expected error when all candidates fail, got nil")
	}
}

func TestBestOfN_Evaluate_BestMode(t *testing.T) {
	// BestMode 直接选最高 Overall；候选 2 的 overall 0.95 最高（但 6 维低）。
	llm := &bestOfNLLMMock{
		candidateJSONs: []string{
			`{"authority":0.6,"depth":0.6,"freshness":0.6,"completeness":0.6,"readability":0.6,"citation":0.6,"overall":0.6}`,
			`{"authority":0.5,"depth":0.5,"freshness":0.5,"completeness":0.5,"readability":0.5,"citation":0.5,"overall":0.95}`,
			`{"authority":0.5,"depth":0.5,"freshness":0.5,"completeness":0.5,"readability":0.5,"citation":0.5,"overall":0.5}`,
		},
		judgeResp: `{"logprobs":[{"token":"A","logprob":0.0}]}`,
	}
	candidate := NewCandidateAgent(llm, nil)
	judge := NewJudgeAgent(llm, nil)
	b := NewBestOfN(candidate, judge, 3)
	b.selectionMode = SelectionModeBest

	result, err := b.Evaluate(context.Background(), ArticleInput{ID: "a1"})
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if result.Best.Overall != 0.95 {
		t.Fatalf("expected best overall 0.95 in best mode, got %f", result.Best.Overall)
	}
}

func TestBestOfN_PairwiseCompare(t *testing.T) {
	b := &BestOfN{selectionMode: SelectionModePairwise}
	high := domain.ArticleQuality{Authority: 0.8, Depth: 0.8, Freshness: 0.8, Completeness: 0.8, Readability: 0.8, Citation: 0.8, Overall: 0.8}
	low := domain.ArticleQuality{Authority: 0.5, Depth: 0.5, Freshness: 0.5, Completeness: 0.5, Readability: 0.5, Citation: 0.5, Overall: 0.5}

	if got := b.pairwiseCompare(high, low); got != 1 {
		t.Fatalf("expected high > low => 1, got %d", got)
	}
	if got := b.pairwiseCompare(low, high); got != -1 {
		t.Fatalf("expected low < high => -1, got %d", got)
	}
	if got := b.pairwiseCompare(high, high); got != 0 {
		t.Fatalf("expected equal => 0, got %d", got)
	}
}

func TestBestOfN_PairwiseCompare_TiebreakOverall(t *testing.T) {
	b := &BestOfN{selectionMode: SelectionModePairwise}
	// 3 维胜 / 3 维负 → 平局，比 Overall。
	a := domain.ArticleQuality{Authority: 0.9, Depth: 0.9, Freshness: 0.9, Completeness: 0.1, Readability: 0.1, Citation: 0.1, Overall: 0.7}
	c := domain.ArticleQuality{Authority: 0.1, Depth: 0.1, Freshness: 0.1, Completeness: 0.9, Readability: 0.9, Citation: 0.9, Overall: 0.5}
	if got := b.pairwiseCompare(a, c); got != 1 {
		t.Fatalf("expected a wins by overall tiebreak => 1, got %d", got)
	}
}
