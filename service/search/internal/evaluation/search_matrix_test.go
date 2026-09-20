package evaluation

import (
	"strings"
	"testing"
)

func matrixFixture(t *testing.T) SearchMatrixInput {
	t.Helper()
	base := fixture[SearchInput](t, "search_fixture.json")
	result := SearchMatrixInput{BenchmarkRevision: base.BenchmarkRevision, CorpusSnapshot: base.CorpusSnapshot,
		EvaluationCutoff: base.EvaluationCutoff, SplitPlan: base.SplitPlan, K: base.K, Qrels: base.Qrels}
	for _, variant := range SearchVariants() {
		result.Runs = append(result.Runs, SearchMatrixRun{Variant: variant,
			RunRevision: "synthetic-" + variant.Depth + "-" + variant.Intelligence + "-" + variant.Delivery,
			Cases:       append([]SearchCase(nil), base.Cases...)})
	}
	return result
}

func TestSearchMatrixRequiresAllTwelveFixedPaths(t *testing.T) {
	input := matrixFixture(t)
	report, err := EvaluateSearchMatrix(input)
	if err != nil || len(report.Cells) != 12 || report.InputHash == "" || report.Recommendation != "incomplete" {
		t.Fatalf("complete synthetic matrix: %+v %v", report, err)
	}
	if report.Cells[0].Variant != (SearchVariant{Depth: "fast", Intelligence: "low", Delivery: "summary"}) ||
		report.Cells[11].Variant != (SearchVariant{Depth: "detailed", Intelligence: "high", Delivery: "tools"}) {
		t.Fatalf("matrix order/coverage differs: %+v", report.Cells)
	}
	for _, cell := range report.Cells {
		if cell.Report.EvidenceLevel != "synthetic_fixture" || cell.Report.Recommendation != "incomplete" ||
			cell.Report.Counts.EvaluableCases != 2 {
			t.Fatalf("synthetic cell escaped its evidence limit: %+v", cell)
		}
	}
	for i, j := 0, len(input.Runs)-1; i < j; i, j = i+1, j-1 {
		input.Runs[i], input.Runs[j] = input.Runs[j], input.Runs[i]
	}
	again, err := EvaluateSearchMatrix(input)
	if err != nil || again.InputHash != report.InputHash {
		t.Fatalf("matrix input hash changed under run ordering: %v", err)
	}
}

func TestSearchMatrixRejectsMissingDuplicateAndChangedScope(t *testing.T) {
	input := matrixFixture(t)
	input.Runs = input.Runs[:11]
	if _, err := EvaluateSearchMatrix(input); err == nil || !strings.Contains(err.Error(), "12 product paths") {
		t.Fatalf("missing product path accepted: %v", err)
	}
	input = matrixFixture(t)
	input.Runs[11].Variant = input.Runs[0].Variant
	if _, err := EvaluateSearchMatrix(input); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate product path accepted: %v", err)
	}
	input = matrixFixture(t)
	input.Runs[0].Cases = append([]SearchCase(nil), input.Runs[0].Cases...)
	input.Runs[0].Cases[0].Identity.Subject.SubjectID = "other"
	if _, err := EvaluateSearchMatrix(input); err == nil || !strings.Contains(err.Error(), "scope differs") {
		t.Fatalf("variant changed its case subject: %v", err)
	}
	input = matrixFixture(t)
	input.Runs[0].Cases = input.Runs[0].Cases[:1]
	if _, err := EvaluateSearchMatrix(input); err == nil || !strings.Contains(err.Error(), "case set differs") {
		t.Fatalf("variant omitted one case: %v", err)
	}
}

func TestSearchMatrixDoesNotTurnIncompleteQrelsIntoScores(t *testing.T) {
	input := matrixFixture(t)
	for i := range input.Qrels.Cases {
		input.Qrels.Cases[i].Complete = false
	}
	report, err := EvaluateSearchMatrix(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range report.Cells {
		for name, metric := range cell.Report.Metrics {
			if metric.State != NotEvaluable || metric.Value != nil || metric.Denominator != 0 {
				t.Fatalf("incomplete qrels yielded %s in %+v: %+v", name, cell.Variant, metric)
			}
		}
	}
	input = matrixFixture(t)
	input.Runs[0].Cases = append([]SearchCase(nil), input.Runs[0].Cases...)
	input.Runs[0].Cases[0].Results = []SearchResult{{ItemID: "unjudged"}}
	report, err = EvaluateSearchMatrix(input)
	if err != nil || report.Cells[0].Report.Counts.UnjudgedTopK != 1 ||
		report.Cells[0].Report.Counts.MissingCases != 1 || report.Recommendation != "incomplete" {
		t.Fatalf("unjudged top-K became a comparable score: %+v %v", report, err)
	}
}
