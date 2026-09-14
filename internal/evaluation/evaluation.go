// Package evaluation computes deterministic, frozen-input offline diagnostics.
// It does not own benchmark approval, warehouse facts, experiments, or releases.
package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const MetricDefinitionRevision = "offline-v1-linear-gain"

type MeasurementState string

const (
	Observed      MeasurementState = "observed"
	NotEvaluable  MeasurementState = "not_evaluable"
	NotApplicable MeasurementState = "not_applicable"
)

type Metric struct {
	State       MeasurementState `json:"state"`
	Value       *float64         `json:"value"`
	Numerator   float64          `json:"numerator"`
	Denominator int              `json:"denominator"`
}

type Counts struct {
	InputCases         int `json:"input_cases"`
	EvaluableCases     int `json:"evaluable_cases"`
	MissingCases       int `json:"missing_cases"`
	EmptyResults       int `json:"empty_results"`
	NoAnswerCases      int `json:"no_answer_cases"`
	PartialResults     int `json:"partial_results"`
	UnjudgedTopK       int `json:"unjudged_top_k"`
	ValidatedCitations int `json:"validated_citations"`
	CandidateOnly      int `json:"candidate_only"`
	ServedItems        int `json:"served_items"`
	VisibleItems       int `json:"visible_items"`
	MaturedLabels      int `json:"matured_labels"`
	PendingLabels      int `json:"pending_labels"`
}

type EvalReport struct {
	Domain                   string            `json:"domain"`
	InputHash                string            `json:"input_hash"`
	MetricDefinitionRevision string            `json:"metric_definition_revision"`
	BenchmarkRevision        string            `json:"benchmark_revision"`
	SourceRevision           string            `json:"source_revision"`
	SourceHash               string            `json:"source_hash"`
	SourceKind               string            `json:"source_kind"`
	EvidenceLevel            string            `json:"evidence_level"`
	SplitRevision            string            `json:"split_revision"`
	EvaluationCutoff         time.Time         `json:"evaluation_cutoff"`
	K                        int               `json:"k"`
	Counts                   Counts            `json:"counts"`
	Metrics                  map[string]Metric `json:"metrics"`
	Limitations              []string          `json:"limitations"`
	Recommendation           string            `json:"recommendation"`
}

// SubjectRef matches H01's full subject identity. A bare UID is not a global key.
type SubjectRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
}

// CaseIdentity freezes group scope: sessions are always subject-local;
// query families are explicitly subject-local or global across subjects.
type CaseIdentity struct {
	CaseID             string     `json:"case_id"`
	Subject            SubjectRef `json:"subject_ref"`
	SessionID          string     `json:"session_id"`
	QueryFamily        string     `json:"query_family"`
	QueryFamilyScope   string     `json:"query_family_scope"` // subject or global
	Split              string     `json:"split"`
	RequestedAt        time.Time  `json:"requested_at"`
	FeatureAvailableAt time.Time  `json:"feature_available_at"`
}

// SplitPlan freezes chronological cutoffs in addition to grouping keys.
// Embargo prevents a long observation window from touching the next split.
type SplitPlan struct {
	Revision       string    `json:"revision"`
	TrainEnd       time.Time `json:"train_end"`
	ValidationEnd  time.Time `json:"validation_end"`
	TestEnd        time.Time `json:"test_end"`
	EmbargoSeconds int64     `json:"embargo_seconds"`
}

func (p SplitPlan) validate(id CaseIdentity) error {
	if p.Revision == "" || p.TrainEnd.IsZero() || !p.ValidationEnd.After(p.TrainEnd) || !p.TestEnd.After(p.ValidationEnd) || p.EmbargoSeconds < 0 || p.EmbargoSeconds > 366*24*3600 {
		return errors.New("frozen split plan is invalid")
	}
	embargo := time.Duration(p.EmbargoSeconds) * time.Second
	switch id.Split {
	case "train":
		if id.RequestedAt.After(p.TrainEnd) {
			return fmt.Errorf("case %s: outside train window", id.CaseID)
		}
	case "validation":
		if !id.RequestedAt.After(p.TrainEnd.Add(embargo)) || id.RequestedAt.After(p.ValidationEnd) {
			return fmt.Errorf("case %s: outside validation window", id.CaseID)
		}
	case "test":
		if !id.RequestedAt.After(p.ValidationEnd.Add(embargo)) || id.RequestedAt.After(p.TestEnd) {
			return fmt.Errorf("case %s: outside test window", id.CaseID)
		}
	default:
		return fmt.Errorf("case %s: invalid split", id.CaseID)
	}
	return nil
}

type splitChecker struct {
	cases    map[string]bool
	subjects map[SubjectRef]string
	sessions map[sessionGroup]string
	families map[familyGroup]string
	scopes   map[string]string
}

type sessionGroup struct {
	subject SubjectRef
	id      string
}

type familyGroup struct {
	scope   string
	subject SubjectRef
	id      string
}

func newSplitChecker() *splitChecker {
	return &splitChecker{
		cases: make(map[string]bool), subjects: make(map[SubjectRef]string),
		sessions: make(map[sessionGroup]string), families: make(map[familyGroup]string), scopes: make(map[string]string),
	}
}

func (s *splitChecker) add(id CaseIdentity) error {
	if id.CaseID == "" || id.Subject.AuthorityID == "" || id.Subject.TenantID == "" || id.Subject.SubjectID == "" || id.SessionID == "" || id.QueryFamily == "" || id.RequestedAt.IsZero() || id.FeatureAvailableAt.IsZero() {
		return errors.New("case identity and times must be complete")
	}
	if id.QueryFamilyScope != "subject" && id.QueryFamilyScope != "global" {
		return fmt.Errorf("case %s: query family scope must be subject or global", id.CaseID)
	}
	if previous, ok := s.scopes[id.QueryFamily]; ok && previous != id.QueryFamilyScope {
		return fmt.Errorf("case %s: query family %s has mixed scopes", id.CaseID, id.QueryFamily)
	}
	s.scopes[id.QueryFamily] = id.QueryFamilyScope
	if id.Split != "train" && id.Split != "validation" && id.Split != "test" {
		return fmt.Errorf("case %s: invalid split %q", id.CaseID, id.Split)
	}
	if id.FeatureAvailableAt.After(id.RequestedAt) {
		return fmt.Errorf("case %s: future feature leakage", id.CaseID)
	}
	if s.cases[id.CaseID] {
		return fmt.Errorf("duplicate case %s", id.CaseID)
	}
	s.cases[id.CaseID] = true
	if previous, ok := s.subjects[id.Subject]; ok && previous != id.Split {
		return fmt.Errorf("case %s: full subject leaks from %s to %s", id.CaseID, previous, id.Split)
	}
	s.subjects[id.Subject] = id.Split
	session := sessionGroup{subject: id.Subject, id: id.SessionID}
	if previous, ok := s.sessions[session]; ok && previous != id.Split {
		return fmt.Errorf("case %s: scoped session leaks from %s to %s", id.CaseID, previous, id.Split)
	}
	s.sessions[session] = id.Split
	family := familyGroup{scope: id.QueryFamilyScope, id: id.QueryFamily}
	if id.QueryFamilyScope == "subject" {
		family.subject = id.Subject
	}
	if previous, ok := s.families[family]; ok && previous != id.Split {
		return fmt.Errorf("case %s: %s query family leaks from %s to %s", id.CaseID, id.QueryFamilyScope, previous, id.Split)
	}
	s.families[family] = id.Split
	return nil
}

func frozenHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func metric(sum float64, count int, empty MeasurementState) Metric {
	if count == 0 {
		return Metric{State: empty}
	}
	value := sum / float64(count)
	return Metric{State: Observed, Value: &value, Numerator: sum, Denominator: count}
}

func emptyMetrics(keys ...string) map[string]Metric {
	result := make(map[string]Metric, len(keys))
	for _, key := range keys {
		result[key] = Metric{State: NotEvaluable}
	}
	return result
}

func validSource(kind, revision, hash string) bool {
	return (kind == "synthetic_fixture" || kind == "approved_frozen") && revision != "" && hash != ""
}

func gainAt(relevance int, rank int) float64 {
	return float64(relevance) / math.Log2(float64(rank+2))
}

func discountedGain(relevance []int, k int) float64 {
	var result float64
	for i := 0; i < len(relevance) && i < k; i++ {
		result += gainAt(relevance[i], i)
	}
	return result
}

func canonicalIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	sort.Strings(result)
	return result
}
