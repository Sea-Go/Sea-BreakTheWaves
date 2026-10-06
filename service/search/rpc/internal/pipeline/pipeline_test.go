package pipeline

import (
	"context"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// buildTestStore 构建三文档测试 Store（确定性）。
func buildTestStore(t *testing.T) *retrieval.Store {
	t.Helper()
	docs := map[string]retrieval.LoadedDoc{
		"doc-a": {
			Dense: []float32{1, 0, 0}, Sparse: map[uint32]float32{1: 0.8},
			Tree:  testTree("rev-a"), Source: []byte("# A\n\nalpha content about apples\n\nbeta data about bananas\n"),
		},
		"doc-b": {
			Dense: []float32{0, 1, 0}, Sparse: map[uint32]float32{2: 0.7},
			Tree:  testTree("rev-b"), Source: []byte("# B\n\ngamma info about grapes\n\ndelta notes about dates\n"),
		},
		"doc-c": {
			Dense: []float32{0.7, 0.7, 0}, Sparse: map[uint32]float32{1: 0.4, 2: 0.5},
			Tree:  testTree("rev-c"), Source: []byte("# C\n\ncombined apples and grapes\n\nextra content\n"),
		},
	}
	s, err := retrieval.NewStore(docs)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestPipelineToolsDelivery(t *testing.T) {
	p := NewDefaultPipeline(buildTestStore(t))
	res, err := p.Execute(context.Background(), PipelineRequest{
		Query: "apples", Tier: retrieval.TierFast, Delivery: DeliveryTools,
		Dense: []float32{1, 0, 0}, Sparse: map[uint32]float32{1: 0.8},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Pack.QueryID == "" {
		t.Fatal("tools delivery missing pack query_id")
	}
	if res.Answer != "" {
		t.Fatal("tools delivery should not have answer")
	}
	if len(res.Pack.Candidates) == 0 {
		t.Fatal("tools delivery should have candidates")
	}
}

func TestPipelineSummaryDelivery(t *testing.T) {
	p := NewDefaultPipeline(buildTestStore(t))
	res, err := p.Execute(context.Background(), PipelineRequest{
		Query: "apples and grapes", Tier: retrieval.TierBalanced, Delivery: DeliverySummary,
		Dense: []float32{0.7, 0.7, 0}, Sparse: map[uint32]float32{1: 0.5, 2: 0.5},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Answer == "" {
		t.Fatal("summary delivery missing answer")
	}
	if len(res.Citations) == 0 {
		t.Fatal("summary delivery missing citations")
	}
	if res.FormattedAnswer == res.Answer {
		t.Fatal("formatted answer should differ from raw (has anchor links)")
	}
}

func TestPipelineDeterministic(t *testing.T) {
	store := buildTestStore(t)
	p := NewDefaultPipeline(store)
	req := PipelineRequest{
		Query: "grapes", Tier: retrieval.TierFast, Delivery: DeliverySummary,
		Dense: []float32{0, 1, 0}, Sparse: map[uint32]float32{2: 0.7},
	}
	a, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute 1: %v", err)
	}
	b, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute 2: %v", err)
	}
	if a.Answer != b.Answer || len(a.Citations) != len(b.Citations) {
		t.Fatal("pipeline not deterministic")
	}
}

func TestPipelineEmptyQuery(t *testing.T) {
	p := NewDefaultPipeline(buildTestStore(t))
	_, err := p.Execute(context.Background(), PipelineRequest{Query: "", Tier: retrieval.TierFast})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}
