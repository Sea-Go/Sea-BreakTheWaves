package evaluation

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture[T any](t *testing.T, name string) T {
	t.Helper()
	contents, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(contents, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func near(t *testing.T, actual *float64, expected float64) {
	t.Helper()
	if actual == nil || math.Abs(*actual-expected) > 1e-10 {
		t.Fatalf("got %v, want %f", actual, expected)
	}
}

func TestSearchHandCalculatedAndFrozen(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	report, err := EvaluateSearch(input)
	if err != nil {
		t.Fatal(err)
	}
	near(t, report.Metrics["S09.recall_at_k"].Value, 0.5)
	near(t, report.Metrics["S11.mrr_at_k"].Value, 0.5)
	near(t, report.Metrics["S12.ndcg_at_k"].Value, (1+2/math.Log2(3))/(2+1/math.Log2(3))/2)
	if report.Counts.EmptyResults != 1 || report.Counts.EvaluableCases != 2 || report.Counts.ValidatedCitations != 1 || report.Counts.CandidateOnly != 1 {
		t.Fatalf("unexpected denominator or citation layer: %+v", report.Counts)
	}
	if report.Recommendation != "incomplete" || report.EvidenceLevel != "synthetic_fixture" || report.InputHash == "" {
		t.Fatalf("synthetic fixture must not yield release advice: %+v", report)
	}
	// Case/judgment enumeration is not part of ranking. Freeze is independent
	// of map iteration or fixture file ordering; ranked results remain ordered.
	input.Cases[0], input.Cases[1] = input.Cases[1], input.Cases[0]
	input.Qrels.Cases[0], input.Qrels.Cases[1] = input.Qrels.Cases[1], input.Qrels.Cases[0]
	input.Qrels.Cases[1].Judgments[0], input.Qrels.Cases[1].Judgments[1] = input.Qrels.Cases[1].Judgments[1], input.Qrels.Cases[1].Judgments[0]
	again, err := EvaluateSearch(input)
	if err != nil || again.InputHash != report.InputHash {
		t.Fatalf("frozen input drift: %v, %s != %s", err, again.InputHash, report.InputHash)
	}
}

func TestSearchMissingQrelsAndUnjudgedAreNotZero(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	input.Qrels.SourceKind = "unapproved"
	report, err := EvaluateSearch(input)
	if err != nil || report.Metrics["S09.recall_at_k"].State != NotEvaluable || report.Metrics["S09.recall_at_k"].Value != nil {
		t.Fatalf("unapproved qrels must not score: %+v, %v", report, err)
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.Cases[0].Results[0].ItemID = "unknown"
	report, err = EvaluateSearch(input)
	if err != nil || report.Counts.UnjudgedTopK != 1 || report.Counts.MissingCases != 1 {
		t.Fatalf("unjudged top K must leave missing denominator: %+v, %v", report, err)
	}
	near(t, report.Metrics["S09.recall_at_k"].Value, 0)
	if report.Metrics["S09.recall_at_k"].Denominator != 1 {
		t.Fatalf("only the completed empty result is evaluable: %+v", report.Metrics["S09.recall_at_k"])
	}
	input.Qrels.AvailableAt = input.EvaluationCutoff.Add(time.Second)
	report, err = EvaluateSearch(input)
	if err != nil || report.Metrics["S09.recall_at_k"].State != NotEvaluable {
		t.Fatalf("future qrels must not score: %+v, %v", report, err)
	}
}

func TestSearchPartialAndFailureDenominators(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	input.Cases[0].Outcome = "partial"
	input.Cases[1].Outcome = "failed"
	report, err := EvaluateSearch(input)
	if err != nil || report.Counts.PartialResults != 1 || report.Counts.MissingCases != 1 || report.Metrics["S09.recall_at_k"].Denominator != 1 {
		t.Fatalf("partial and failed runs must differ: %+v, %v", report, err)
	}
	input.Cases[0].Outcome = "failed"
	if _, err := EvaluateSearch(input); err == nil {
		t.Fatal("failed case with candidates must be rejected")
	}
}

func TestSearchVersionLeakAndDuplicateRejected(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	input.Qrels.CorpusSnapshot = "other"
	if _, err := EvaluateSearch(input); err == nil {
		t.Fatal("cross-corpus qrels must fail")
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.Cases[1].Identity.Split = "train"
	input.Cases[1].Identity.Subject = input.Cases[0].Identity.Subject
	if _, err := EvaluateSearch(input); err == nil || !strings.Contains(err.Error(), "leaks") {
		t.Fatalf("subject split leak should fail, got %v", err)
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.Cases[0].Identity.FeatureAvailableAt = input.Cases[0].Identity.RequestedAt.Add(time.Second)
	if _, err := EvaluateSearch(input); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future feature should fail, got %v", err)
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.Cases[0].Results = append(input.Cases[0].Results, input.Cases[0].Results[0])
	if _, err := EvaluateSearch(input); err == nil {
		t.Fatal("duplicate candidates should fail")
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.Cases[0].Results[0].CitationReceiptID = ""
	if _, err := EvaluateSearch(input); err == nil {
		t.Fatal("verified citation requires a receipt ID")
	}
	input = fixture[SearchInput](t, "search_fixture.json")
	input.SplitPlan.ValidationEnd = input.Cases[0].Identity.RequestedAt
	if _, err := EvaluateSearch(input); err == nil {
		t.Fatal("test case inside validation window must fail")
	}
}

func TestSearchNoAnswerIsNotApplicableAndRevisionChangesHash(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	first, err := EvaluateSearch(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Qrels.Revision = "qrels-fixture-v2"
	second, err := EvaluateSearch(input)
	if err != nil || second.InputHash == first.InputHash {
		t.Fatalf("qrel revision must change hash: %v", err)
	}
	input.Qrels.Cases[0].Judgments = []Judgment{{ItemID: "a", Relevance: 0}, {ItemID: "b", Relevance: 0}}
	input.Qrels.Cases[1].Judgments = []Judgment{{ItemID: "d", Relevance: 0}}
	second, err = EvaluateSearch(input)
	if err != nil || second.Counts.NoAnswerCases != 2 || second.Metrics["S09.recall_at_k"].State != NotApplicable || second.Metrics["S09.recall_at_k"].Value != nil {
		t.Fatalf("complete no-answer cases are not ranking zeros: %+v, %v", second, err)
	}
}

func TestSubjectTripleAndGroupScopes(t *testing.T) {
	input := fixture[SearchInput](t, "search_fixture.json")
	first := input.Cases[0].Identity
	second := &input.Cases[1].Identity
	second.Subject = first.Subject
	second.Subject.TenantID = "other-tenant"
	second.SessionID = first.SessionID
	second.QueryFamily = first.QueryFamily
	second.Split = "train"
	second.RequestedAt = time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	second.FeatureAvailableAt = second.RequestedAt.Add(-time.Hour)
	if _, err := EvaluateSearch(input); err != nil {
		t.Fatalf("same UID/session/family in another tenant is a separate subject: %v", err)
	}
	second.QueryFamilyScope = "global"
	if _, err := EvaluateSearch(input); err == nil || !strings.Contains(err.Error(), "mixed scopes") {
		t.Fatalf("same query family cannot switch scope within a benchmark, got %v", err)
	}
	second.QueryFamilyScope = "subject"
	second.Subject = first.Subject
	second.Subject.AuthorityID = "other.identity"
	if _, err := EvaluateSearch(input); err != nil {
		t.Fatalf("same UID in another authority is a separate subject: %v", err)
	}
	second.Subject = first.Subject
	if _, err := EvaluateSearch(input); err == nil || !strings.Contains(err.Error(), "full subject leaks") {
		t.Fatalf("same full subject across splits must fail, got %v", err)
	}
	second.Subject.TenantID = "other-tenant"
	input.Cases[0].Identity.QueryFamilyScope = "global"
	second.QueryFamilyScope = "global"
	if _, err := EvaluateSearch(input); err == nil || !strings.Contains(err.Error(), "global query family leaks") {
		t.Fatalf("explicit global query family must not cross splits, got %v", err)
	}
}

func TestRecommendationHandCalculatedAndFrozen(t *testing.T) {
	input := fixture[RecommendationInput](t, "recommendation_fixture.json")
	report, err := EvaluateRecommendation(input)
	if err != nil {
		t.Fatal(err)
	}
	near(t, report.Metrics["R06.auc"].Value, 0.75)
	near(t, report.Metrics["R05.ndcg_at_k"].Value, (1+1/math.Log2(3))/2)
	near(t, report.Metrics["R12.visible_item_coverage"].Value, 0.8)
	if report.Counts.ServedItems != 5 || report.Counts.VisibleItems != 4 || report.Counts.MaturedLabels != 4 || report.Metrics["R06.auc"].Denominator != 4 {
		t.Fatalf("served is not visible and AUC is pairwise: %+v", report)
	}
	if report.K != 2 || report.SplitRevision != "split-fixture-v1" || report.SourceHash == "" || report.MetricDefinitionRevision != MetricDefinitionRevision {
		t.Fatalf("report must expose selector and frozen revisions: %+v", report)
	}
	input.Cases[0], input.Cases[1] = input.Cases[1], input.Cases[0]
	input.EligibleItemIDs[0], input.EligibleItemIDs[4] = input.EligibleItemIDs[4], input.EligibleItemIDs[0]
	again, err := EvaluateRecommendation(input)
	if err != nil || again.InputHash != report.InputHash {
		t.Fatalf("frozen recommendation drift: %v", err)
	}
}

func TestRecommendationAUCCreditForScoreTies(t *testing.T) {
	input := fixture[RecommendationInput](t, "recommendation_fixture.json")
	for i := range input.Cases {
		for j := range input.Cases[i].Items {
			input.Cases[i].Items[j].Score = 0.5
		}
	}
	report, err := EvaluateRecommendation(input)
	if err != nil {
		t.Fatal(err)
	}
	near(t, report.Metrics["R06.auc"].Value, 0.5)
}

func TestRecommendationNoExposureOrMatureClasses(t *testing.T) {
	input := fixture[RecommendationInput](t, "recommendation_fixture.json")
	for i := range input.Cases {
		for j := range input.Cases[i].Items {
			input.Cases[i].Items[j].Visible = false
			input.Cases[i].Items[j].ImpressionID = ""
		}
	}
	report, err := EvaluateRecommendation(input)
	if err != nil || report.Counts.ServedItems != 5 || report.Counts.VisibleItems != 0 || report.Metrics["R06.auc"].State != NotEvaluable || report.Metrics["R12.visible_item_coverage"].State != NotEvaluable {
		t.Fatalf("served-only slate cannot be evaluated: %+v, %v", report, err)
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[0].Items[1].LabelState = "pending"
	input.Cases[1].Items[0].LabelState = "pending"
	report, err = EvaluateRecommendation(input)
	if err != nil || report.Counts.PendingLabels != 2 || report.Metrics["R06.auc"].State != NotEvaluable || report.Metrics["R05.ndcg_at_k"].State != NotEvaluable {
		t.Fatalf("pending negatives cannot become negatives: %+v, %v", report, err)
	}
	input.SourceKind = "unapproved"
	report, err = EvaluateRecommendation(input)
	if err != nil || report.Metrics["R06.auc"].Value != nil {
		t.Fatalf("unapproved attribution cannot score: %+v, %v", report, err)
	}
}

func TestRecommendationAttributionAndSplitCounterexamples(t *testing.T) {
	input := fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[0].Items[0].Visible = true
	input.Cases[0].Items[0].Served = false
	if _, err := EvaluateRecommendation(input); err == nil {
		t.Fatal("unserved visible item should fail")
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[1].Items[0].ImpressionID = input.Cases[0].Items[0].ImpressionID
	if _, err := EvaluateRecommendation(input); err == nil {
		t.Fatal("duplicate impression should fail")
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[1].Identity.QueryFamily = input.Cases[0].Identity.QueryFamily
	input.Cases[0].Identity.QueryFamilyScope = "global"
	input.Cases[1].Identity.QueryFamilyScope = "global"
	input.Cases[1].Identity.Split = "train"
	if _, err := EvaluateRecommendation(input); err == nil || !strings.Contains(err.Error(), "global query family leaks") {
		t.Fatalf("global query-family split leak should fail, got %v", err)
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[0].Items[1].LabelSource = "candidate_served"
	if _, err := EvaluateRecommendation(input); err == nil {
		t.Fatal("served cannot be a negative label source")
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[0].Items[0].LabelAvailableAt = input.EvaluationCutoff.Add(time.Second)
	report, err := EvaluateRecommendation(input)
	if err != nil || report.Counts.PendingLabels != 1 || report.Metrics["R05.ndcg_at_k"].Denominator != 1 {
		t.Fatalf("future label must not enter its slate: %+v, %v", report, err)
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.Cases[0].Items[0].Visible = false
	input.Cases[0].Items[0].ImpressionID = ""
	report, err = EvaluateRecommendation(input)
	if err != nil || report.Metrics["R05.ndcg_at_k"].Denominator != 1 {
		t.Fatalf("unexposed top-K position must exclude its slate from nDCG: %+v, %v", report, err)
	}
	input = fixture[RecommendationInput](t, "recommendation_fixture.json")
	input.SplitPlan.TestEnd = input.Cases[0].Identity.RequestedAt.Add(-time.Second)
	if _, err := EvaluateRecommendation(input); err == nil {
		t.Fatal("case after test cutoff must fail")
	}
}
