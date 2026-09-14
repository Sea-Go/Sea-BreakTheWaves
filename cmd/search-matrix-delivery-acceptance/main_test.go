package main

import (
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/evaluation"
	search "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

func TestPartialProductMatrixKeepsElevenUnrunAndNoRelevance(t *testing.T) {
	value := witness{SearchID: "search-fixture", SnapshotRef: "published-fixture",
		SubjectAuthorityID: "rtw.identity", SubjectTenantID: "platform", SubjectID: "test-user",
		SessionID: "fixture-session", Query: "fixed synthetic question",
		RequestedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		EvidencePack: search.EvidencePack{Evidence: []search.Evidence{{Key: search.Key{
			SourceKind: "wiki", ContentID: "book", RevisionID: "rev-1", ChunkID: "chunk-1"}}}},
		CitationReceipt: search.CitationReceipt{DurableRef: "rtw-durable-test"}}
	report, err := run(value, "witness-sha-fixture")
	if err != nil || len(report.Cells) != 12 || report.ActualDeliveryCells != 1 ||
		report.NotExecutedCells != 11 || report.FormalMatrix.Recommendation != "incomplete" {
		t.Fatalf("single observed RTW cell changed matrix scope: %+v %v", report, err)
	}
	for _, cell := range report.Cells {
		observed := cell.Variant == (evaluation.SearchVariant{Depth: "fast", Intelligence: "low", Delivery: "tools"})
		if observed && (cell.DeliveryExecution != "observed" || len(cell.CandidateOrder) != 1 ||
			cell.CitationReceipt == nil) || !observed && cell.DeliveryExecution != "not_executed" {
			t.Fatalf("unrun delivery was promoted: %+v", cell)
		}
	}
	for _, cell := range report.FormalMatrix.Cells {
		for _, metric := range cell.Report.Metrics {
			if metric.State != evaluation.NotEvaluable || metric.Value != nil {
				t.Fatalf("unjudged fixture received relevance score: %+v", metric)
			}
		}
	}
}
