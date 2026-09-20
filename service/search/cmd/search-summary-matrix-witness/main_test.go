package main

import (
	"os"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/evaluation"
)

// The real two-file fixture is supplied by the same-run RTW/DC acceptance.
// It is intentionally external: local model output, temporary IDs and private
// acceptance receipts do not become repository fixtures.
func TestVerifiedRTWDCReportProjectsOneSummaryCell(t *testing.T) {
	usagePath, rtwPath := os.Getenv("SEA_SUMMARY_USAGE_REPORT"), os.Getenv("SEA_SUMMARY_RTW_REPORT")
	if usagePath == "" || rtwPath == "" {
		t.Skip("set same-run RTW/DC private live summary reports")
	}
	var usage boundReport
	usageRaw, err := read(usagePath, &usage)
	if err != nil {
		t.Fatal(err)
	}
	var rtw rtwReport
	rtwRaw, err := read(rtwPath, &rtw)
	if err != nil {
		t.Fatal(err)
	}
	report, err := project(usage, digest(usageRaw), rtw, digest(rtwRaw))
	if err != nil || report.ActualDeliveryCells != 1 || report.NotExecutedCells != 11 ||
		report.QualityGate != "not_passed_unsupported_interpretation" ||
		report.Recommendation != "incomplete" || len(report.Cells) != 12 {
		t.Fatalf("real same-snapshot summary projection differs: %+v %v", report, err)
	}
	for i, cell := range report.Cells {
		if cell.Variant != evaluation.SearchVariants()[i] {
			t.Fatalf("variant order changed: %+v", cell.Variant)
		}
		observed := cell.Variant == (evaluation.SearchVariant{Depth: "fast", Intelligence: "low", Delivery: "summary"})
		if observed && (cell.DeliveryExecution != "observed" ||
			cell.ProductStatus != "functional_success_quality_failed" || cell.SearchID != rtw.SearchID ||
			cell.ModelInteractionID != usage.ModelInteractionID || cell.ProviderUsage == nil) ||
			!observed && (cell.DeliveryExecution != "not_executed" || cell.SearchID != "" ||
				cell.ModelInteractionID != "" || cell.ProviderUsage != nil) {
			t.Fatalf("observed summary was copied into another path: %+v", cell)
		}
		for _, metric := range cell.Metrics {
			if metric.State != "not_evaluable" || metric.Value != nil {
				t.Fatalf("unjudged relevance became numeric: %+v", metric)
			}
		}
	}
	changed := usage
	changed.QualityGate = "passed"
	if _, err := project(changed, digest(usageRaw), rtw, digest(rtwRaw)); err == nil {
		t.Fatal("failed semantic quality was promoted")
	}
	changed = usage
	changed.RTWAnswerSHA256 = digest([]byte("another RTW answer"))
	if _, err := project(changed, digest(usageRaw), rtw, digest(rtwRaw)); err == nil {
		t.Fatal("different RTW answer bytes passed source binding")
	}
	other := rtw
	other.Delivery = "tools"
	if _, err := project(usage, digest(usageRaw), other, digest(rtwRaw)); err == nil {
		t.Fatal("Tools snapshot was merged into Summary matrix")
	}
}
