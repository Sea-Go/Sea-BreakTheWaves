package main

import (
	"math"
	"testing"

	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
)

func TestBEIRJudgmentsAndFusionAreItemBased(t *testing.T) {
	relevant := map[string]bool{"a": true, "b": true}
	got := evaluateBEIR([]string{"a", "a", "x", "b"}, relevant)
	if got.Recall10 != 1 || got.MRR10 != 1 || math.Abs(got.NDCG10-(1+1/math.Log2(5))/(1+1/math.Log2(3))) > 1e-9 {
		t.Fatalf("duplicate item inflated retrieval quality: %+v", got)
	}
	if missing := evaluateBEIR([]string{"x"}, relevant); missing.Recall10 != 0 || missing.NDCG10 != 0 || missing.MRR10 != 0 {
		t.Fatalf("unjudged item counted relevant: %+v", missing)
	}
	a := []idx.Candidate{{ItemID: "a"}, {ItemID: "b"}}
	b := []idx.Candidate{{ItemID: "b"}, {ItemID: "a"}}
	fused := beirFuse(a, b)
	if len(fused) != 2 || fused[0] != "a" || fused[1] != "b" {
		t.Fatalf("fusion did not tie-break by whole item: %v", fused)
	}
}
