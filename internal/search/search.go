// Package search executes fixed-snapshot, three-lane retrieval. It owns search
// policy and fusion; the lane packages own their representations and indexes.
package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

var (
	ErrInvalid     = errors.New("invalid search request or snapshot")
	ErrUnavailable = errors.New("requested search profile unavailable")
	ErrBudget      = errors.New("search budget exhausted")
	ErrLane        = errors.New("search lane contract violation")
)

type Depth string

const (
	Fast     Depth = "fast"
	Detailed Depth = "detailed"
)

type Intelligence string

const (
	Low    Intelligence = "low"
	Medium Intelligence = "medium"
	High   Intelligence = "high"
)

type Lane string

const (
	Dense       Lane = "dense"
	Sparse      Lane = "sparse"
	MultiVector Lane = "multivector"
)

var lanes = [...]Lane{Dense, Sparse, MultiVector}

// Snapshot is supplied by the authoritative publication owner. All three
// indexes must belong to one release and generation. ValidRevisionIDs is the
// effective set at the start of this request, not an unrestricted default.
type Snapshot struct {
	ModuleID            string              `json:"module_id"`
	ReleaseID           string              `json:"release_id"`
	Generation          int64               `json:"generation"`
	PublicationRevision string              `json:"publication_revision"`
	Indexes             map[Lane]corpus.Ref `json:"indexes"`
	ValidRevisionIDs    []string            `json:"valid_revision_ids"`
}

type Request struct {
	Query                  string
	Depth                  Depth
	Intelligence           Intelligence
	AllowLowerIntelligence bool
	AllowPartial           bool
	Snapshot               Snapshot
}

// Limits are explicit versioned product policy, never invisible fallback.
// One batch can contain several planned subqueries; Fast always has one batch.
type Limits struct {
	MaxBatches    int
	MaxSubqueries int
	TopKPerLane   int
	MaxEvidence   int
	WallTime      time.Duration
}
type Policy struct {
	Version  string
	Profiles map[Depth]map[Intelligence]Limits
}
type Profile struct {
	RequestedDepth        Depth        `json:"requested_depth"`
	EffectiveDepth        Depth        `json:"effective_depth"`
	RequestedIntelligence Intelligence `json:"requested_intelligence"`
	EffectiveIntelligence Intelligence `json:"effective_intelligence"`
	PolicyVersion         string       `json:"policy_version"`
	ChangeReason          string       `json:"change_reason,omitempty"`
}
type PlanInput struct {
	Query               string
	Depth               Depth
	Intelligence        Intelligence
	Round               int
	RemainingSubqueries int
	Previous            []VerifiedCandidate
}

// Planner may use rules or an upstream LLMAgent. It cannot alter Snapshot or
// limits; Execute rejects duplicate/empty queries and excessive output.
type Planner interface {
	Plan(context.Context, PlanInput) ([]string, error)
}
type PlanFunc func(context.Context, PlanInput) ([]string, error)

func (f PlanFunc) Plan(ctx context.Context, in PlanInput) ([]string, error) { return f(ctx, in) }

type DenseReader interface {
	Search(context.Context, dense.Query) (dense.Result, error)
}
type SparseReader interface {
	Search(context.Context, sparse.Query) (sparse.Result, error)
}
type MultiVectorReader interface {
	Search(context.Context, multivector.Query) (multivector.Result, error)
}

// EffectiveRevisionChecker checks current withdrawal/state after retrieval.
// This does not perform same-revision source reading or create an EvidencePack.
type EffectiveRevisionChecker interface {
	Check(context.Context, Snapshot, corpus.Chunk) (bool, error)
}
type CheckFunc func(context.Context, Snapshot, corpus.Chunk) (bool, error)

func (f CheckFunc) Check(ctx context.Context, s Snapshot, c corpus.Chunk) (bool, error) {
	return f(ctx, s, c)
}

type Service struct {
	dense   DenseReader
	sparse  SparseReader
	multi   MultiVectorReader
	planner Planner
	checker EffectiveRevisionChecker
	policy  Policy
}

func New(d DenseReader, s SparseReader, m MultiVectorReader, p Planner, v EffectiveRevisionChecker, policy Policy) (*Service, error) {
	if isNil(d) || isNil(s) || isNil(m) || isNil(p) || isNil(v) || policy.Version == "" || len(policy.Profiles) == 0 {
		return nil, ErrInvalid
	}
	for depth, byLevel := range policy.Profiles {
		if (depth != Fast && depth != Detailed) || len(byLevel) == 0 {
			return nil, ErrInvalid
		}
		for level, limits := range byLevel {
			if (depth != Fast && depth != Detailed) || (level != Low && level != Medium && level != High) || limits.MaxBatches < 1 || limits.MaxSubqueries < 1 || limits.TopKPerLane < 1 || limits.TopKPerLane > 1000 || limits.MaxEvidence < 1 || limits.WallTime <= 0 || (depth == Fast && limits.MaxBatches != 1) {
				return nil, ErrInvalid
			}
		}
		for _, pair := range [][2]Intelligence{{Low, Medium}, {Medium, High}} {
			a, aOK := byLevel[pair[0]]
			b, bOK := byLevel[pair[1]]
			if aOK && bOK && a == b {
				return nil, ErrInvalid
			}
		}
		if depth == Detailed {
			for level, detail := range byLevel {
				if fast, ok := policy.Profiles[Fast][level]; ok && detail == fast {
					return nil, ErrInvalid
				}
			}
		}
	}
	copyPolicy := Policy{Version: policy.Version, Profiles: make(map[Depth]map[Intelligence]Limits, len(policy.Profiles))}
	for depth, levels := range policy.Profiles {
		copyPolicy.Profiles[depth] = make(map[Intelligence]Limits, len(levels))
		for level, limits := range levels {
			copyPolicy.Profiles[depth][level] = limits
		}
	}
	return &Service{d, s, m, p, v, copyPolicy}, nil
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

type Key struct {
	SourceKind string `json:"source_kind"`
	ContentID  string `json:"content_id"`
	RevisionID string `json:"revision_id"`
	ChunkID    string `json:"chunk_id"`
}

func key(c corpus.Chunk) Key { return Key{c.SourceKind, c.ContentID, c.RevisionID, c.ID} }

type LaneHit struct {
	Lane     Lane       `json:"lane"`
	RawScore float64    `json:"raw_score"`
	Rank     int        `json:"rank"`
	Index    corpus.Ref `json:"index"`
}
type Candidate struct {
	Key        Key          `json:"key"`
	Chunk      corpus.Chunk `json:"chunk"`
	RRFScore   float64      `json:"rrf_score"`
	Sources    []LaneHit    `json:"sources"`
	Subqueries []string     `json:"subqueries"`
}

// VerifiedCandidate passed the current effective-state check. Indexed Chunk
// text is not yet re-read from RTW and MUST NOT be cited as source text.
type VerifiedCandidate struct {
	Key        Key          `json:"key"`
	Chunk      corpus.Chunk `json:"chunk"`
	RRFScore   float64      `json:"rrf_score"`
	Sources    []LaneHit    `json:"sources"`
	Subqueries []string     `json:"subqueries"`
}
type LaneStatus struct {
	Lane           Lane          `json:"lane"`
	Requested      bool          `json:"requested"`
	Executed       bool          `json:"executed"`
	Empty          bool          `json:"empty"`
	Failed         bool          `json:"failed"`
	Skipped        bool          `json:"skipped"`
	CandidateCount int           `json:"candidate_count"`
	Duration       time.Duration `json:"duration"`
	Index          corpus.Ref    `json:"index"`
	Reason         string        `json:"reason,omitempty"`
}
type Batch struct {
	Round      int          `json:"round"`
	Queries    []string     `json:"queries"`
	LaneStatus []LaneStatus `json:"lane_status"`
}
type Result struct {
	Status          string              `json:"status"`
	StopReason      string              `json:"stop_reason"`
	Profile         Profile             `json:"profile"`
	Snapshot        Snapshot            `json:"snapshot"`
	Batches         []Batch             `json:"batches"`
	LaneStatus      []LaneStatus        `json:"lane_status"`
	Candidates      []Candidate         `json:"candidates"`
	Verified        []VerifiedCandidate `json:"verified_candidates"`
	DegradedReasons []string            `json:"degraded_reasons,omitempty"`
	UsedSubqueries  int                 `json:"used_subqueries"`
}

func (s *Service) effective(r Request) (Profile, Limits, error) {
	p := Profile{r.Depth, r.Depth, r.Intelligence, r.Intelligence, s.policy.Version, ""}
	byLevel := s.policy.Profiles[r.Depth]
	if x, ok := byLevel[r.Intelligence]; ok {
		return p, x, nil
	}
	if !r.AllowLowerIntelligence {
		return p, Limits{}, ErrUnavailable
	}
	var choices []Intelligence
	switch r.Intelligence {
	case High:
		choices = []Intelligence{Medium, Low}
	case Medium:
		choices = []Intelligence{Low}
	}
	for _, level := range choices {
		if x, ok := byLevel[level]; ok {
			p.EffectiveIntelligence = level
			p.ChangeReason = "requested_level_unavailable"
			return p, x, nil
		}
	}
	return p, Limits{}, ErrUnavailable
}
func validSnapshot(s Snapshot) bool {
	if s.ModuleID == "" || s.ReleaseID == "" || s.Generation < 1 || s.PublicationRevision == "" || len(s.Indexes) != 3 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range s.ValidRevisionIDs {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, l := range lanes {
		ref := s.Indexes[l]
		if !artifacts.ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 {
			return false
		}
	}
	return true
}
func cloneSnapshot(s Snapshot) Snapshot {
	out := s
	out.ValidRevisionIDs = append([]string(nil), s.ValidRevisionIDs...)
	out.Indexes = make(map[Lane]corpus.Ref, 3)
	for k, v := range s.Indexes {
		out.Indexes[k] = v
	}
	return out
}

// Execute spends explicit batch/subquery budgets and keeps partial lane results
// only when the caller has opted in. Context cancellation always wins.
func (s *Service) Execute(ctx context.Context, r Request) (Result, error) {
	if ctx == nil || strings.TrimSpace(r.Query) == "" || (r.Depth != Fast && r.Depth != Detailed) || (r.Intelligence != Low && r.Intelligence != Medium && r.Intelligence != High) || !validSnapshot(r.Snapshot) {
		return Result{}, ErrInvalid
	}
	profile, limit, err := s.effective(r)
	if err != nil {
		return Result{Profile: profile}, err
	}
	r.Snapshot = cloneSnapshot(r.Snapshot)
	out := Result{Profile: profile, Snapshot: cloneSnapshot(r.Snapshot), Candidates: []Candidate{}, Verified: []VerifiedCandidate{}}
	for _, lane := range lanes {
		out.LaneStatus = append(out.LaneStatus, LaneStatus{Lane: lane, Requested: true, Skipped: true, Index: r.Snapshot.Indexes[lane], Reason: "not_started"})
	}
	if len(r.Snapshot.ValidRevisionIDs) == 0 {
		batch := Batch{Round: 1, Queries: []string{}}
		for _, lane := range lanes {
			batch.LaneStatus = append(batch.LaneStatus, LaneStatus{Lane: lane, Requested: true, Skipped: true, Index: r.Snapshot.Indexes[lane], Reason: "no_effective_revision"})
		}
		out.Batches = []Batch{batch}
		out.LaneStatus = append([]LaneStatus(nil), batch.LaneStatus...)
		out.Status = "empty"
		out.StopReason = "no_evidence"
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, limit.WallTime)
	defer cancel()
	seenQuery := map[string]bool{}
	seenVerified := map[Key]bool{}
	for round := 1; round <= limit.MaxBatches; round++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		remaining := limit.MaxSubqueries - out.UsedSubqueries
		if remaining == 0 {
			out.StopReason = "budget_exhausted"
			break
		}
		queries, err := s.planner.Plan(ctx, PlanInput{r.Query, profile.EffectiveDepth, profile.EffectiveIntelligence, round, remaining, append([]VerifiedCandidate(nil), out.Verified...)})
		if err != nil {
			return out, fmt.Errorf("plan search: %w", err)
		}
		if len(queries) == 0 {
			out.StopReason = "no_new_query"
			break
		}
		if len(queries) > remaining {
			return out, ErrBudget
		}
		for i, q := range queries {
			q = strings.TrimSpace(q)
			if q == "" || seenQuery[q] {
				return out, ErrInvalid
			}
			seenQuery[q] = true
			queries[i] = q
		}
		out.UsedSubqueries += len(queries)
		batch := Batch{Round: round, Queries: append([]string(nil), queries...)}
		var accumulated []Candidate
		for _, q := range queries {
			hits, statuses := s.recall(ctx, r.Snapshot, q, limit.TopKPerLane)
			batch.LaneStatus = append(batch.LaneStatus, statuses...)
			mergeStatuses(out.LaneStatus, statuses)
			if err := ctx.Err(); err != nil {
				out.Batches = append(out.Batches, batch)
				return out, err
			}
			failed := false
			for _, st := range statuses {
				if st.Failed {
					failed = true
					out.DegradedReasons = append(out.DegradedReasons, string(st.Lane)+":"+st.Reason)
				}
			}
			if failed && !r.AllowPartial {
				out.Batches = append(out.Batches, batch)
				return out, ErrLane
			}
			if failed && len(hits) == 0 && allFailed(statuses) {
				out.Batches = append(out.Batches, batch)
				return out, ErrLane
			}
			accumulated = append(accumulated, fuse(q, hits)...)
		}
		out.Batches = append(out.Batches, batch)
		for _, c := range accumulated {
			if err := mergeCandidate(&out.Candidates, c); err != nil {
				return out, err
			}
		}
		sortCandidates(out.Candidates)
		newVerified := 0
		for _, c := range out.Candidates {
			if len(out.Verified) >= limit.MaxEvidence {
				break
			}
			if seenVerified[c.Key] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return out, err
			}
			location := c.Chunk
			location.Text = "" // indexed text is not a same-revision source quote
			valid, err := s.checker.Check(ctx, r.Snapshot, location)
			if err != nil {
				return out, fmt.Errorf("verify effective revision: %w", err)
			}
			if !valid {
				continue
			}
			out.Verified = append(out.Verified, VerifiedCandidate{c.Key, location, c.RRFScore, append([]LaneHit(nil), c.Sources...), append([]string(nil), c.Subqueries...)})
			seenVerified[c.Key] = true
			newVerified++
		}
		if r.Depth == Fast {
			out.StopReason = "batch_complete"
			break
		}
		if newVerified == 0 {
			out.StopReason = "no_new_evidence"
			break
		}
		if len(out.Verified) >= limit.MaxEvidence {
			out.StopReason = "evidence_limit"
			break
		}
	}
	if out.StopReason == "" {
		out.StopReason = "batch_limit"
	}
	// A batch can finish after a revision is withdrawn. Recheck immediately
	// before returning, and synchronize its accumulated retrieval provenance.
	final := make([]VerifiedCandidate, 0, len(out.Verified))
	byKey := make(map[Key]Candidate, len(out.Candidates))
	for _, c := range out.Candidates {
		byKey[c.Key] = c
	}
	for _, v := range out.Verified {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		ok, err := s.checker.Check(ctx, r.Snapshot, v.Chunk)
		if err != nil {
			return out, fmt.Errorf("recheck effective revision: %w", err)
		}
		if !ok {
			continue
		}
		c := byKey[v.Key]
		v.RRFScore = c.RRFScore
		v.Sources = append([]LaneHit(nil), c.Sources...)
		v.Subqueries = append([]string(nil), c.Subqueries...)
		final = append(final, v)
	}
	out.Verified = final
	if len(out.Verified) == 0 {
		out.Status = "empty"
		if out.StopReason == "batch_complete" {
			out.StopReason = "no_evidence"
		}
	} else if len(out.DegradedReasons) > 0 || out.StopReason == "budget_exhausted" || r.Depth == Detailed {
		out.Status = "partial"
	} else {
		out.Status = "complete"
	}
	return out, nil
}
func mergeStatuses(total, round []LaneStatus) {
	for _, st := range round {
		for i := range total {
			if total[i].Lane != st.Lane {
				continue
			}
			dst := &total[i]
			dst.Executed = dst.Executed || st.Executed
			dst.Skipped = dst.Skipped && st.Skipped
			dst.Failed = dst.Failed || st.Failed
			dst.CandidateCount += st.CandidateCount
			dst.Duration += st.Duration
			dst.Empty = dst.Executed && !dst.Failed && dst.CandidateCount == 0
			if st.Reason != "" {
				dst.Reason = st.Reason
			} else if dst.Reason == "not_started" {
				dst.Reason = ""
			}
			break
		}
	}
}

type laneResult struct {
	lane    Lane
	hits    []Candidate
	err     error
	elapsed time.Duration
}

func (s *Service) recall(ctx context.Context, snap Snapshot, q string, k int) ([]Candidate, []LaneStatus) {
	ch := make(chan laneResult, 3)
	for _, lane := range lanes {
		go func(l Lane) {
			start := time.Now()
			b := laneResult{lane: l}
			revs := append([]string(nil), snap.ValidRevisionIDs...)
			switch l {
			case Dense:
				x, e := s.dense.Search(ctx, dense.Query{IndexRef: snap.Indexes[l], ModuleID: snap.ModuleID, ReleaseID: snap.ReleaseID, Generation: snap.Generation, ValidRevisionIDs: revs, Text: q, TopK: k})
				b.err = e
				for _, v := range x.Candidates {
					if v.ModuleID != snap.ModuleID || v.ReleaseID != snap.ReleaseID || v.Generation != snap.Generation || v.Lane != "dense" {
						b.err = ErrLane
						break
					}
					b.hits = append(b.hits, Candidate{Key: key(v.Chunk), Chunk: v.Chunk, Sources: []LaneHit{{l, v.Score, v.Rank, v.IndexRef}}})
				}
			case Sparse:
				x, e := s.sparse.Search(ctx, sparse.Query{IndexRef: snap.Indexes[l], ModuleID: snap.ModuleID, ReleaseID: snap.ReleaseID, Generation: snap.Generation, ValidRevisionIDs: revs, Text: q, TopK: k})
				b.err = e
				for _, v := range x.Candidates {
					if v.ModuleID != snap.ModuleID || v.ReleaseID != snap.ReleaseID || v.Generation != snap.Generation || v.Lane != "sparse" {
						b.err = ErrLane
						break
					}
					b.hits = append(b.hits, Candidate{Key: key(v.Chunk), Chunk: v.Chunk, Sources: []LaneHit{{l, v.Score, v.Rank, v.IndexRef}}})
				}
			case MultiVector:
				x, e := s.multi.Search(ctx, multivector.Query{IndexRef: snap.Indexes[l], ModuleID: snap.ModuleID, ReleaseID: snap.ReleaseID, Generation: snap.Generation, ValidRevisionIDs: revs, Text: q, TopK: k})
				b.err = e
				for _, v := range x.Candidates {
					if v.ModuleID != snap.ModuleID || v.ReleaseID != snap.ReleaseID || v.Generation != snap.Generation || v.Lane != "multivector" {
						b.err = ErrLane
						break
					}
					b.hits = append(b.hits, Candidate{Key: key(v.Chunk), Chunk: v.Chunk, Sources: []LaneHit{{l, v.Score, v.Rank, v.IndexRef}}})
				}
			}
			b.elapsed = time.Since(start)
			ch <- b
		}(lane)
	}
	results := map[Lane]laneResult{}
	for i := 0; i < 3; i++ {
		select {
		case b := <-ch:
			results[b.lane] = b
		case <-ctx.Done():
			return nil, []LaneStatus{{Lane: Dense, Requested: true, Failed: true, Reason: classify(ctx.Err())}, {Lane: Sparse, Requested: true, Failed: true, Reason: classify(ctx.Err())}, {Lane: MultiVector, Requested: true, Failed: true, Reason: classify(ctx.Err())}}
		}
	}
	var hits []Candidate
	statuses := make([]LaneStatus, 0, 3)
	valid := map[string]bool{}
	for _, id := range snap.ValidRevisionIDs {
		valid[id] = true
	}
	canonical := map[Key]corpus.Chunk{}
	for _, l := range lanes {
		b := results[l]
		st := LaneStatus{Lane: l, Requested: true, Executed: true, Index: snap.Indexes[l], Duration: b.elapsed}
		if b.err == nil {
			b.err = validateHits(b.hits, l, snap, valid, k)
		}
		if b.err == nil {
			for _, h := range b.hits {
				if prior, ok := canonical[h.Key]; ok && prior != h.Chunk {
					b.err = ErrLane
					break
				}
			}
		}
		if b.err != nil {
			st.Failed = true
			st.Reason = classify(b.err)
		} else {
			for _, h := range b.hits {
				canonical[h.Key] = h.Chunk
			}
			st.CandidateCount = len(b.hits)
			st.Empty = len(b.hits) == 0
			hits = append(hits, b.hits...)
		}
		statuses = append(statuses, st)
	}
	return hits, statuses
}
func allFailed(statuses []LaneStatus) bool {
	for _, st := range statuses {
		if !st.Failed {
			return false
		}
	}
	return true
}
func classify(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrLane):
		return "contract_violation"
	default:
		return "backend_failure"
	}
}
func validateHits(hits []Candidate, l Lane, s Snapshot, valid map[string]bool, k int) error {
	if len(hits) > k {
		return ErrLane
	}
	seen := map[Key]bool{}
	for i, h := range hits {
		c := h.Chunk
		source := h.Sources[0]
		if c.ID == "" || c.ContentID == "" || c.RevisionID == "" || (c.SourceKind != "source" && c.SourceKind != "wiki") || !valid[c.RevisionID] || source.Lane != l || source.Index != s.Indexes[l] || source.Rank != i+1 || math.IsNaN(source.RawScore) || math.IsInf(source.RawScore, 0) || seen[h.Key] || h.Key != key(c) || c.Location.Locator == "" {
			return ErrLane
		}
		seen[h.Key] = true
	}
	return nil
}
func fuse(query string, hits []Candidate) []Candidate {
	byKey := map[Key]Candidate{}
	for _, h := range hits {
		v := byKey[h.Key]
		if v.Key.ChunkID == "" {
			v.Key = h.Key
			v.Chunk = h.Chunk
		}
		v.Sources = append(v.Sources, h.Sources...)
		v.RRFScore += 1 / float64(60+h.Sources[0].Rank)
		v.Subqueries = []string{query}
		byKey[h.Key] = v
	}
	out := make([]Candidate, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	sortCandidates(out)
	return out
}
func mergeCandidate(dst *[]Candidate, c Candidate) error {
	for i := range *dst {
		if (*dst)[i].Key == c.Key {
			if (*dst)[i].Chunk != c.Chunk {
				return ErrLane
			}
			(*dst)[i].Sources = append((*dst)[i].Sources, c.Sources...)
			(*dst)[i].Subqueries = append((*dst)[i].Subqueries, c.Subqueries...)
			(*dst)[i].RRFScore = rrf((*dst)[i].Sources)
			return nil
		}
	}
	*dst = append(*dst, c)
	return nil
}
func rrf(sources []LaneHit) float64 {
	best := map[Lane]int{}
	for _, h := range sources {
		if old := best[h.Lane]; old == 0 || h.Rank < old {
			best[h.Lane] = h.Rank
		}
	}
	sum := 0.0
	for _, rank := range best {
		sum += 1 / float64(60+rank)
	}
	return sum
}
func sortCandidates(c []Candidate) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].RRFScore == c[j].RRFScore {
			a, b := c[i].Key, c[j].Key
			if a.SourceKind != b.SourceKind {
				return a.SourceKind < b.SourceKind
			}
			if a.ContentID != b.ContentID {
				return a.ContentID < b.ContentID
			}
			if a.RevisionID != b.RevisionID {
				return a.RevisionID < b.RevisionID
			}
			return a.ChunkID < b.ChunkID
		}
		return c[i].RRFScore > c[j].RRFScore
	})
}
