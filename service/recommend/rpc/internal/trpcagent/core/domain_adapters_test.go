package recommendationv2

import (
	"context"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model/rank"
)

func TestDomainAdaptersDriveExistingDomainContracts(t *testing.T) {
	rt := NewRecommendationRuntime(
		WithDomainRecaller(testDomainRecaller{}),
		WithDomainRanker(rank.NewWeightedRanker(map[string]float64{"freshness": 0.7, "quality": 0.3})),
		WithDomainReranker(testDomainReranker{}),
		WithDomainQualityJudger(testDomainQualityJudger{thresholdPass: map[string]float64{
			"domain-b": 0.95,
			"domain-a": 0.80,
			"domain-c": 0.20,
		}}, 0.5),
	)

	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-domain",
		Channel:  "domain_channel",
		Query:    "domain query",
		User:     UserIdentity{UserID: "u-domain", Tags: []string{"vip"}},
		Context: map[string]any{
			"include_tags": []any{"ai", "travel"},
			"exclude_tags": []string{"blocked"},
		},
		TopK:     3,
		PathMode: PathHybrid,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if resp.PathTaken != PathHybrid {
		t.Fatalf("path = %q, want hybrid", resp.PathTaken)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("quality provider should filter one low-quality item, got %d: %+v", len(resp.Items), resp.Items)
	}
	if resp.Items[0].ArticleID != "domain-b" {
		t.Fatalf("domain rank/rerank order not preserved, first = %+v", resp.Items[0])
	}
	if resp.Items[0].Features["rerank_model"] != "domain-rerank" {
		t.Fatalf("domain rerank metadata missing: %+v", resp.Items[0].Features)
	}
	if resp.Items[0].Features["quality_score"] != 0.95 {
		t.Fatalf("domain quality score missing: %+v", resp.Items[0].Features)
	}
	if resp.Cost.ToolCalls < 3 {
		t.Fatalf("expected domain recall/rank/rerank tool calls, got cost %+v", resp.Cost)
	}
	if resp.Cost.RerankCost == 0 {
		t.Fatalf("expected rerank budget to be tracked in cost: %+v", resp.Cost)
	}
	trace := rt.Trace(TraceQueryRequest{TraceID: resp.TraceID, Skill: "rerank.self"})
	if trace.Total != 1 {
		t.Fatalf("expected trace query to find rerank.self, got %+v", trace)
	}
}

func TestDomainRecallAdapterPassesUnifiedContext(t *testing.T) {
	recaller := &capturingDomainRecaller{}
	rt := NewRecommendationRuntime(WithDomainRecaller(recaller))
	_, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-context",
		Channel:  "ctx_channel",
		User:     UserIdentity{UserID: "u-context", Tags: []string{"founder"}},
		Context: map[string]any{
			"include_tags": []string{"product"},
			"exclude_tags": []any{"spam"},
		},
		TopK:     4,
		PathMode: PathFast,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if recaller.last.UserKey.UserID != "u-context" || recaller.last.UserKey.Channel != "ctx_channel" {
		t.Fatalf("user key not mapped into domain recall request: %+v", recaller.last.UserKey)
	}
	if recaller.last.TopK != 50 {
		t.Fatalf("domain recall should use configured recall topK, got %d", recaller.last.TopK)
	}
	if got := recaller.last.TagFilter.Include; len(got) != 1 || got[0] != "product" {
		t.Fatalf("include tags not mapped: %+v", got)
	}
	if got := recaller.last.TagFilter.Exclude; len(got) != 1 || got[0] != "spam" {
		t.Fatalf("exclude tags not mapped: %+v", got)
	}
	if recaller.last.Intent == nil || recaller.last.Intent.Complexity != "simple" {
		t.Fatalf("fast path intent not mapped: %+v", recaller.last.Intent)
	}
	if recaller.last.Profile == nil || recaller.last.Profile.Static == nil {
		t.Fatalf("profile snapshot not mapped into domain profile: %+v", recaller.last.Profile)
	}
}

type testDomainRecaller struct{}

func (testDomainRecaller) Name() string { return "domain_test_recaller" }

func (testDomainRecaller) Recall(_ context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	return domain.RecallResult{
		Source: "domain",
		Candidates: []domain.Candidate{
			{
				ArticleID: "domain-a",
				Source:    "content",
				Score:     0.1,
				Scores:    map[string]float64{"freshness": 0.7, "quality": 0.6},
				Extra:     map[string]any{"content": "domain a content", "path": req.Intent.Complexity},
			},
			{
				ArticleID: "domain-b",
				Source:    "graph",
				Score:     0.2,
				Scores:    map[string]float64{"freshness": 1.0, "quality": 0.9},
				Extra:     map[string]any{"content": "domain b content", "channel": req.Channel},
			},
			{
				ArticleID: "domain-c",
				Source:    "cf",
				Score:     0.3,
				Scores:    map[string]float64{"freshness": 0.2, "quality": 0.1},
				Extra:     map[string]any{"content": "domain c content"},
			},
		},
	}, nil
}

type capturingDomainRecaller struct {
	last domain.RecallRequest
}

func (r *capturingDomainRecaller) Name() string { return "capturing_recaller" }

func (r *capturingDomainRecaller) Recall(_ context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	r.last = req
	return domain.RecallResult{
		Source:     "capture",
		Candidates: []domain.Candidate{{ArticleID: "captured", Score: 1, Source: "rule"}},
	}, nil
}

type testDomainReranker struct{}

func (testDomainReranker) Name() string { return "domain_test_reranker" }

func (testDomainReranker) Rerank(_ context.Context, req domain.RerankRequest) (domain.RerankResult, error) {
	candidates := append([]domain.Candidate(nil), req.Candidates...)
	for i := range candidates {
		candidates[i].Score += 0.01
		if candidates[i].Scores == nil {
			candidates[i].Scores = map[string]float64{}
		}
		candidates[i].Scores["domain_rerank"] = 1
	}
	return domain.RerankResult{Candidates: candidates, ModelUsed: "domain-rerank"}, nil
}

type testDomainQualityJudger struct {
	thresholdPass map[string]float64
}

func (j testDomainQualityJudger) Judge(_ context.Context, req domain.QualityRequest) (domain.ArticleQuality, error) {
	overall := j.thresholdPass[req.ArticleID]
	return domain.ArticleQuality{ArticleID: req.ArticleID, Overall: overall}, nil
}
