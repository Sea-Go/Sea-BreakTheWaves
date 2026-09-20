package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const SearchMatrixRevision = "search-product-matrix.v1"

// SearchVariant is one product path, not a separate qrel or corpus source.
// The complete matrix has fast/detailed × low/medium/high × summary/tools.
type SearchVariant struct {
	Depth        string `json:"depth"`
	Intelligence string `json:"intelligence"`
	Delivery     string `json:"delivery"`
}

func (v SearchVariant) valid() bool {
	return (v.Depth == "fast" || v.Depth == "detailed") &&
		(v.Intelligence == "low" || v.Intelligence == "medium" || v.Intelligence == "high") &&
		(v.Delivery == "summary" || v.Delivery == "tools")
}

func SearchVariants() []SearchVariant {
	variants := make([]SearchVariant, 0, 12)
	for _, depth := range []string{"fast", "detailed"} {
		for _, intelligence := range []string{"low", "medium", "high"} {
			for _, delivery := range []string{"summary", "tools"} {
				variants = append(variants, SearchVariant{Depth: depth, Intelligence: intelligence, Delivery: delivery})
			}
		}
	}
	return variants
}

type SearchMatrixRun struct {
	Variant     SearchVariant `json:"variant"`
	RunRevision string        `json:"run_revision"`
	Cases       []SearchCase  `json:"cases"`
}

type SearchMatrixInput struct {
	BenchmarkRevision string            `json:"benchmark_revision"`
	CorpusSnapshot    string            `json:"corpus_snapshot"`
	EvaluationCutoff  time.Time         `json:"evaluation_cutoff"`
	SplitPlan         SplitPlan         `json:"split_plan"`
	K                 int               `json:"k"`
	Qrels             QrelSet           `json:"qrels"`
	Runs              []SearchMatrixRun `json:"runs"`
}

type SearchMatrixCell struct {
	Variant SearchVariant `json:"variant"`
	Report  EvalReport    `json:"report"`
}

type SearchMatrixReport struct {
	Revision          string             `json:"revision"`
	InputHash         string             `json:"input_hash"`
	BenchmarkRevision string             `json:"benchmark_revision"`
	CorpusSnapshot    string             `json:"corpus_snapshot"`
	QrelSourceHash    string             `json:"qrel_source_hash"`
	QrelSourceKind    string             `json:"qrel_source_kind"`
	Cells             []SearchMatrixCell `json:"cells"`
	Recommendation    string             `json:"recommendation"`
}

// EvaluateSearchMatrix refuses a missing path or a changed case scope before
// computing per-path metrics. It does not average unlike product paths or
// promote synthetic/incomplete judgments to a release recommendation.
func EvaluateSearchMatrix(input SearchMatrixInput) (SearchMatrixReport, error) {
	expected := SearchVariants()
	if len(input.Runs) != len(expected) {
		return SearchMatrixReport{}, fmt.Errorf("search matrix requires %d product paths", len(expected))
	}
	byVariant := make(map[SearchVariant]SearchMatrixRun, len(expected))
	var fixedCases map[string]string
	for _, run := range input.Runs {
		if !run.Variant.valid() || run.RunRevision == "" {
			return SearchMatrixReport{}, errors.New("search matrix variant or run revision invalid")
		}
		if _, exists := byVariant[run.Variant]; exists {
			return SearchMatrixReport{}, fmt.Errorf("duplicate search matrix variant: %+v", run.Variant)
		}
		identities := make(map[string]string, len(run.Cases))
		for _, c := range run.Cases {
			if c.Identity.CaseID == "" {
				return SearchMatrixReport{}, errors.New("search matrix case ID required")
			}
			if _, exists := identities[c.Identity.CaseID]; exists {
				return SearchMatrixReport{}, fmt.Errorf("duplicate search matrix case %s", c.Identity.CaseID)
			}
			identity, err := json.Marshal(c.Identity)
			if err != nil {
				return SearchMatrixReport{}, err
			}
			identities[c.Identity.CaseID] = string(identity)
		}
		if len(identities) == 0 {
			return SearchMatrixReport{}, errors.New("search matrix has no fixed cases")
		}
		if fixedCases == nil {
			fixedCases = identities
		} else if len(identities) != len(fixedCases) {
			return SearchMatrixReport{}, errors.New("search matrix case set differs across variants")
		} else {
			for id, scope := range fixedCases {
				if identities[id] != scope {
					return SearchMatrixReport{}, fmt.Errorf("search matrix case %s scope differs across variants", id)
				}
			}
		}
		byVariant[run.Variant] = run
	}
	report := SearchMatrixReport{Revision: SearchMatrixRevision, BenchmarkRevision: input.BenchmarkRevision,
		CorpusSnapshot: input.CorpusSnapshot, QrelSourceHash: input.Qrels.SourceHash,
		QrelSourceKind: input.Qrels.SourceKind, Recommendation: "incomplete",
		Cells: make([]SearchMatrixCell, 0, len(expected))}
	for _, variant := range expected {
		run, ok := byVariant[variant]
		if !ok {
			return SearchMatrixReport{}, fmt.Errorf("missing search matrix variant: %+v", variant)
		}
		cell, err := EvaluateSearch(SearchInput{BenchmarkRevision: input.BenchmarkRevision,
			RunRevision: run.RunRevision, CorpusSnapshot: input.CorpusSnapshot,
			EvaluationCutoff: input.EvaluationCutoff, SplitPlan: input.SplitPlan,
			K: input.K, Qrels: input.Qrels, Cases: run.Cases})
		if err != nil {
			return SearchMatrixReport{}, fmt.Errorf("search matrix %+v: %w", variant, err)
		}
		report.Cells = append(report.Cells, SearchMatrixCell{Variant: variant, Report: cell})
	}
	hash, err := frozenHash(struct {
		Revision string             `json:"revision"`
		Cells    []SearchMatrixCell `json:"cells"`
	}{Revision: report.Revision, Cells: report.Cells})
	if err != nil {
		return SearchMatrixReport{}, err
	}
	report.InputHash = hash
	return report, nil
}
