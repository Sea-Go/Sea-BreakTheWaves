package evaluation

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

type Judgment struct {
	ItemID    string `json:"item_id"`
	Relevance int    `json:"relevance"`
}

type QrelCase struct {
	CaseID    string     `json:"case_id"`
	Complete  bool       `json:"complete"`
	Judgments []Judgment `json:"judgments"`
}

type QrelSet struct {
	Revision       string     `json:"revision"`
	Source         string     `json:"source"`
	SourceHash     string     `json:"source_hash"`
	SourceKind     string     `json:"source_kind"`
	AvailableAt    time.Time  `json:"available_at"`
	CorpusSnapshot string     `json:"corpus_snapshot"`
	Cases          []QrelCase `json:"cases"`
}

type SearchResult struct {
	ItemID            string `json:"item_id"`
	CitationVerified  bool   `json:"citation_verified"`
	CitationReceiptID string `json:"citation_receipt_id"`
}

type SearchCase struct {
	Identity CaseIdentity   `json:"identity"`
	Outcome  string         `json:"outcome"` // completed, partial, failed
	Results  []SearchResult `json:"results"`
}

type SearchInput struct {
	BenchmarkRevision string       `json:"benchmark_revision"`
	RunRevision       string       `json:"run_revision"`
	CorpusSnapshot    string       `json:"corpus_snapshot"`
	EvaluationCutoff  time.Time    `json:"evaluation_cutoff"`
	SplitPlan         SplitPlan    `json:"split_plan"`
	K                 int          `json:"k"`
	Qrels             QrelSet      `json:"qrels"`
	Cases             []SearchCase `json:"cases"`
}

// EvaluateSearch reports S09/S11/S12 on explicitly judged, fixed-version
// top-K results. A citation validation is counted separately from relevance.
func EvaluateSearch(input SearchInput) (EvalReport, error) {
	if input.K <= 0 || input.BenchmarkRevision == "" || input.RunRevision == "" || input.CorpusSnapshot == "" || input.EvaluationCutoff.IsZero() {
		return EvalReport{}, errors.New("search benchmark, run, corpus, cutoff and K are required")
	}
	if input.Qrels.CorpusSnapshot != "" && input.Qrels.CorpusSnapshot != input.CorpusSnapshot {
		return EvalReport{}, errors.New("qrels and search run use different corpus snapshots")
	}
	input.Qrels.Cases = append([]QrelCase(nil), input.Qrels.Cases...)
	sort.Slice(input.Qrels.Cases, func(i, j int) bool { return input.Qrels.Cases[i].CaseID < input.Qrels.Cases[j].CaseID })
	for i := range input.Qrels.Cases {
		input.Qrels.Cases[i].Judgments = append([]Judgment(nil), input.Qrels.Cases[i].Judgments...)
		sort.Slice(input.Qrels.Cases[i].Judgments, func(a, b int) bool {
			return input.Qrels.Cases[i].Judgments[a].ItemID < input.Qrels.Cases[i].Judgments[b].ItemID
		})
	}
	input.Cases = append([]SearchCase(nil), input.Cases...)
	sort.Slice(input.Cases, func(i, j int) bool { return input.Cases[i].Identity.CaseID < input.Cases[j].Identity.CaseID })
	checker := newSplitChecker()
	for _, c := range input.Cases {
		if err := checker.add(c.Identity); err != nil {
			return EvalReport{}, err
		}
		if err := input.SplitPlan.validate(c.Identity); err != nil {
			return EvalReport{}, err
		}
		if c.Outcome != "completed" && c.Outcome != "partial" && c.Outcome != "failed" {
			return EvalReport{}, fmt.Errorf("case %s: invalid outcome", c.Identity.CaseID)
		}
		seen := make(map[string]bool)
		for _, result := range c.Results {
			if result.ItemID == "" || seen[result.ItemID] {
				return EvalReport{}, fmt.Errorf("case %s: empty or duplicate candidate", c.Identity.CaseID)
			}
			if result.CitationVerified && result.CitationReceiptID == "" {
				return EvalReport{}, fmt.Errorf("case %s: verified citation needs a receipt ID", c.Identity.CaseID)
			}
			seen[result.ItemID] = true
		}
		if c.Outcome == "failed" && len(c.Results) != 0 {
			return EvalReport{}, fmt.Errorf("case %s: failed run cannot claim results", c.Identity.CaseID)
		}
	}
	qrels := make(map[string]QrelCase)
	for _, q := range input.Qrels.Cases {
		if q.CaseID == "" {
			return EvalReport{}, errors.New("qrel case ID is empty")
		}
		if _, exists := qrels[q.CaseID]; exists {
			return EvalReport{}, fmt.Errorf("duplicate qrel case %s", q.CaseID)
		}
		seen := make(map[string]bool)
		for _, judgment := range q.Judgments {
			if judgment.ItemID == "" || judgment.Relevance < 0 || seen[judgment.ItemID] {
				return EvalReport{}, fmt.Errorf("case %s: invalid or duplicate judgment", q.CaseID)
			}
			seen[judgment.ItemID] = true
		}
		qrels[q.CaseID] = q
	}
	hash, err := frozenHash(input)
	if err != nil {
		return EvalReport{}, err
	}
	report := EvalReport{
		Domain: "search", InputHash: hash, MetricDefinitionRevision: MetricDefinitionRevision,
		BenchmarkRevision: input.BenchmarkRevision, SourceRevision: input.Qrels.Revision,
		SourceHash: input.Qrels.SourceHash, SourceKind: input.Qrels.SourceKind, EvidenceLevel: input.Qrels.SourceKind,
		SplitRevision: input.SplitPlan.Revision, EvaluationCutoff: input.EvaluationCutoff, K: input.K,
		Counts:         Counts{InputCases: len(input.Cases)},
		Metrics:        emptyMetrics("S09.recall_at_k", "S11.mrr_at_k", "S12.ndcg_at_k"),
		Recommendation: "incomplete",
	}
	if !validSource(input.Qrels.SourceKind, input.Qrels.Revision, input.Qrels.SourceHash) || input.Qrels.Source == "" || input.Qrels.AvailableAt.IsZero() || input.Qrels.AvailableAt.After(input.EvaluationCutoff) {
		report.Counts.MissingCases = len(input.Cases)
		report.Limitations = []string{"no approved, available frozen qrels"}
		return report, nil
	}
	var recall, mrr, ndcg float64
	var relevantCases int
	for _, c := range input.Cases {
		if c.Outcome == "failed" {
			report.Counts.MissingCases++
			continue
		}
		if c.Outcome == "partial" {
			report.Counts.PartialResults++
		}
		if len(c.Results) == 0 {
			report.Counts.EmptyResults++
		}
		for rank, result := range c.Results {
			if rank >= input.K {
				break
			}
			if result.CitationVerified {
				report.Counts.ValidatedCitations++
			} else {
				report.Counts.CandidateOnly++
			}
		}
		q, exists := qrels[c.Identity.CaseID]
		if !exists || !q.Complete {
			report.Counts.MissingCases++
			continue
		}
		judged := make(map[string]int, len(q.Judgments))
		var ideal []int
		for _, j := range q.Judgments {
			judged[j.ItemID] = j.Relevance
			if j.Relevance > 0 {
				ideal = append(ideal, j.Relevance)
			}
		}
		if len(ideal) == 0 {
			// A complete no-answer case belongs to S22, not S09/S12.
			report.Counts.NoAnswerCases++
			continue
		}
		var ranks []int
		var hits int
		var first float64
		unjudged := false
		for i, result := range c.Results {
			if i >= input.K {
				break
			}
			relevance, ok := judged[result.ItemID]
			if !ok {
				unjudged = true
				report.Counts.UnjudgedTopK++
				continue
			}
			ranks = append(ranks, relevance)
			if relevance > 0 {
				hits++
				if first == 0 {
					first = 1 / float64(i+1)
				}
			}
		}
		if unjudged {
			report.Counts.MissingCases++
			continue
		}
		sort.Sort(sort.Reverse(sort.IntSlice(ideal)))
		var actual float64
		for i, relevance := range ranks {
			actual += gainAt(relevance, i)
		}
		recall += float64(hits) / float64(len(ideal))
		mrr += first
		ndcg += actual / discountedGain(ideal, input.K)
		relevantCases++
		report.Counts.EvaluableCases++
	}
	if relevantCases == 0 {
		if report.Counts.NoAnswerCases == report.Counts.InputCases && report.Counts.InputCases > 0 {
			for key := range report.Metrics {
				report.Metrics[key] = Metric{State: NotApplicable}
			}
			report.Limitations = append(report.Limitations, "complete no-answer cases require S22, not ranking relevance")
		} else {
			report.Limitations = append(report.Limitations, "no evaluable case with positive qrels")
		}
		return report, nil
	}
	report.Metrics["S09.recall_at_k"] = metric(recall, relevantCases, NotEvaluable)
	report.Metrics["S11.mrr_at_k"] = metric(mrr, relevantCases, NotEvaluable)
	report.Metrics["S12.ndcg_at_k"] = metric(ndcg, relevantCases, NotEvaluable)
	if report.Counts.MissingCases > 0 || report.Counts.PartialResults > 0 {
		report.Limitations = append(report.Limitations, "observed metrics use eligible cases only; missing and partial denominators are separate")
	}
	if input.Qrels.SourceKind == "synthetic_fixture" {
		report.Limitations = append(report.Limitations, "synthetic fixture is not real relevance acceptance")
	}
	return report, nil
}
