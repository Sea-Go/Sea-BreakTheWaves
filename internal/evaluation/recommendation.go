package evaluation

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type RecommendationItem struct {
	ItemID              string    `json:"item_id"`
	Score               float64   `json:"score"`
	Served              bool      `json:"served"`
	ImpressionID        string    `json:"impression_id"`
	Visible             bool      `json:"visible"`
	LabelState          string    `json:"label_state"` // mature_positive, mature_negative, pending, excluded
	LabelSource         string    `json:"label_source"`
	LabelAvailableAt    time.Time `json:"label_available_at"`
	WindowMaturedAt     time.Time `json:"window_matured_at"`
	AttributionRevision string    `json:"attribution_revision"`
}

type RecommendationCase struct {
	Identity CaseIdentity         `json:"identity"`
	Items    []RecommendationItem `json:"items"`
}

type RecommendationInput struct {
	BenchmarkRevision   string               `json:"benchmark_revision"`
	DatasetRevision     string               `json:"dataset_revision"`
	DatasetHash         string               `json:"dataset_hash"`
	SourceKind          string               `json:"source_kind"`
	AttributionRevision string               `json:"attribution_revision"`
	EvaluationCutoff    time.Time            `json:"evaluation_cutoff"`
	SplitPlan           SplitPlan            `json:"split_plan"`
	K                   int                  `json:"k"`
	EligibleItemIDs     []string             `json:"eligible_item_ids"`
	Cases               []RecommendationCase `json:"cases"`
}

type scoredLabel struct {
	score    float64
	positive bool
}

// EvaluateRecommendation computes R06 AUC, R05 request-level nDCG@K and R12
// visible-content coverage. It never derives an impression from Served.
func EvaluateRecommendation(input RecommendationInput) (EvalReport, error) {
	if input.K <= 0 || input.BenchmarkRevision == "" || input.DatasetRevision == "" || input.AttributionRevision == "" || input.EvaluationCutoff.IsZero() {
		return EvalReport{}, errors.New("recommendation benchmark, dataset, attribution, cutoff and K are required")
	}
	input.EligibleItemIDs = canonicalIDs(input.EligibleItemIDs)
	eligibleCorpus := make(map[string]bool)
	for _, id := range input.EligibleItemIDs {
		if id == "" || eligibleCorpus[id] {
			return EvalReport{}, errors.New("eligible corpus contains empty or duplicate item")
		}
		eligibleCorpus[id] = true
	}
	input.Cases = append([]RecommendationCase(nil), input.Cases...)
	sort.Slice(input.Cases, func(i, j int) bool { return input.Cases[i].Identity.CaseID < input.Cases[j].Identity.CaseID })
	checker := newSplitChecker()
	seenImpressions := make(map[string]bool)
	for _, c := range input.Cases {
		if err := checker.add(c.Identity); err != nil {
			return EvalReport{}, err
		}
		if err := input.SplitPlan.validate(c.Identity); err != nil {
			return EvalReport{}, err
		}
		seenItems := make(map[string]bool)
		for _, item := range c.Items {
			if item.ItemID == "" || seenItems[item.ItemID] || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
				return EvalReport{}, fmt.Errorf("case %s: duplicate item or invalid score", c.Identity.CaseID)
			}
			seenItems[item.ItemID] = true
			if item.Visible && (!item.Served || item.ImpressionID == "" || !eligibleCorpus[item.ItemID]) {
				return EvalReport{}, fmt.Errorf("case %s: visible item has no served/eligible impression", c.Identity.CaseID)
			}
			if item.Visible {
				if seenImpressions[item.ImpressionID] {
					return EvalReport{}, fmt.Errorf("duplicate impression %s", item.ImpressionID)
				}
				seenImpressions[item.ImpressionID] = true
			}
		}
	}
	hash, err := frozenHash(input)
	if err != nil {
		return EvalReport{}, err
	}
	report := EvalReport{
		Domain: "recommendation", InputHash: hash, MetricDefinitionRevision: MetricDefinitionRevision,
		BenchmarkRevision: input.BenchmarkRevision, SourceRevision: input.DatasetRevision,
		SourceHash: input.DatasetHash, SourceKind: input.SourceKind, EvidenceLevel: input.SourceKind,
		SplitRevision: input.SplitPlan.Revision, EvaluationCutoff: input.EvaluationCutoff, K: input.K,
		Counts:         Counts{InputCases: len(input.Cases)},
		Metrics:        emptyMetrics("R06.auc", "R05.ndcg_at_k", "R12.visible_item_coverage"),
		Recommendation: "incomplete",
	}
	if !validSource(input.SourceKind, input.DatasetRevision, input.DatasetHash) {
		report.Counts.MissingCases = len(input.Cases)
		report.Limitations = []string{"no approved, frozen attribution dataset"}
		return report, nil
	}
	var labels []scoredLabel
	var ndcgSum float64
	var ndcgCases int
	visibleItems := make(map[string]bool)
	for _, c := range input.Cases {
		var caseLabels []int
		var complete = true
		for rank, item := range c.Items {
			if item.Served {
				report.Counts.ServedItems++
			}
			if !item.Visible {
				if rank < input.K {
					complete = false // an unexposed top-K item has no valid label
				}
				continue
			}
			report.Counts.VisibleItems++
			visibleItems[item.ItemID] = true
			if item.AttributionRevision != input.AttributionRevision {
				return EvalReport{}, fmt.Errorf("case %s: attribution revision mismatch", c.Identity.CaseID)
			}
			if item.LabelState != "mature_positive" && item.LabelState != "mature_negative" && item.LabelState != "pending" && item.LabelState != "excluded" {
				return EvalReport{}, fmt.Errorf("case %s: invalid label state", c.Identity.CaseID)
			}
			if item.LabelState != "mature_positive" && item.LabelState != "mature_negative" {
				complete = false
				report.Counts.PendingLabels++
				continue
			}
			if item.LabelSource == "" || item.LabelAvailableAt.IsZero() || item.WindowMaturedAt.IsZero() || item.LabelAvailableAt.After(input.EvaluationCutoff) || item.WindowMaturedAt.After(input.EvaluationCutoff) {
				complete = false
				report.Counts.PendingLabels++
				continue
			}
			positive := item.LabelState == "mature_positive"
			if positive && item.LabelSource != "click" && item.LabelSource != "read" && item.LabelSource != "favorite" {
				return EvalReport{}, fmt.Errorf("case %s: positive label has no authorized source", c.Identity.CaseID)
			}
			if !positive && item.LabelSource != "explicit_negative" && item.LabelSource != "matured_no_positive" {
				return EvalReport{}, fmt.Errorf("case %s: negative label has no authorized source", c.Identity.CaseID)
			}
			labels = append(labels, scoredLabel{score: item.Score, positive: positive})
			report.Counts.MaturedLabels++
			if positive {
				caseLabels = append(caseLabels, 1)
			} else {
				caseLabels = append(caseLabels, 0)
			}
		}
		if !complete || len(caseLabels) == 0 {
			if len(c.Items) > 0 {
				report.Counts.MissingCases++
			}
			continue
		}
		var positives int
		for _, value := range caseLabels {
			positives += value
		}
		if positives == 0 {
			continue // IDCG=0: not applicable, not a perfect ranking.
		}
		ideal := append([]int(nil), caseLabels...)
		sort.Sort(sort.Reverse(sort.IntSlice(ideal)))
		ndcgSum += discountedGain(caseLabels, input.K) / discountedGain(ideal, input.K)
		ndcgCases++
		report.Counts.EvaluableCases++
	}
	if len(visibleItems) > 0 && len(eligibleCorpus) > 0 {
		report.Metrics["R12.visible_item_coverage"] = metric(float64(len(visibleItems)), len(eligibleCorpus), NotEvaluable)
	}
	var positives, negatives int
	for _, label := range labels {
		if label.positive {
			positives++
		} else {
			negatives++
		}
	}
	pairs := float64(positives) * float64(negatives)
	sort.Slice(labels, func(i, j int) bool { return labels[i].score < labels[j].score })
	var concordant, lowerNegatives float64
	for start := 0; start < len(labels); {
		end := start + 1
		for end < len(labels) && labels[end].score == labels[start].score {
			end++
		}
		var groupPositive, groupNegative float64
		for _, label := range labels[start:end] {
			if label.positive {
				groupPositive++
			} else {
				groupNegative++
			}
		}
		concordant += groupPositive*lowerNegatives + 0.5*groupPositive*groupNegative
		lowerNegatives += groupNegative
		start = end
	}
	if pairs > 0 {
		value := concordant / pairs
		report.Metrics["R06.auc"] = Metric{State: Observed, Value: &value, Numerator: concordant, Denominator: int(pairs)}
	}
	if ndcgCases > 0 {
		report.Metrics["R05.ndcg_at_k"] = metric(ndcgSum, ndcgCases, NotEvaluable)
	}
	if report.Counts.VisibleItems == 0 {
		report.Limitations = append(report.Limitations, "no real visible impressions; served items are not exposure")
	}
	if report.Counts.PendingLabels > 0 {
		report.Limitations = append(report.Limitations, "pending/censored/excluded or future labels omitted from mature cohort; incomplete slates omitted from nDCG")
	}
	if pairs == 0 {
		report.Limitations = append(report.Limitations, "AUC requires mature positive and negative labels")
	}
	if input.SourceKind == "synthetic_fixture" {
		report.Limitations = append(report.Limitations, "synthetic fixture is not a live impression or online effect")
	}
	return report, nil
}
