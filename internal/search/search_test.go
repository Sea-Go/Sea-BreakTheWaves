package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

type calls struct {
	sync.Mutex
	dense  []dense.Query
	sparse []sparse.Query
	multi  []multivector.Query
}
type denseStub struct {
	c  *calls
	fn func(dense.Query) (dense.Result, error)
}

func (s denseStub) Search(_ context.Context, q dense.Query) (dense.Result, error) {
	s.c.Lock()
	s.c.dense = append(s.c.dense, q)
	s.c.Unlock()
	return s.fn(q)
}

type sparseStub struct {
	c  *calls
	fn func(sparse.Query) (sparse.Result, error)
}

func (s sparseStub) Search(_ context.Context, q sparse.Query) (sparse.Result, error) {
	s.c.Lock()
	s.c.sparse = append(s.c.sparse, q)
	s.c.Unlock()
	return s.fn(q)
}

type multiStub struct {
	c  *calls
	fn func(multivector.Query) (multivector.Result, error)
}

func (s multiStub) Search(_ context.Context, q multivector.Query) (multivector.Result, error) {
	s.c.Lock()
	s.c.multi = append(s.c.multi, q)
	s.c.Unlock()
	return s.fn(q)
}
func ref(s string) corpus.Ref {
	for len(s) < 64 {
		s += s
	}
	s = s[:64]
	return corpus.Ref{Key: "sha256/" + s, SHA256: s}
}
func snapshot() Snapshot {
	return Snapshot{ModuleID: "module", ReleaseID: "release", Generation: 9, PublicationRevision: "pointer-7", Indexes: map[Lane]corpus.Ref{Dense: ref("a"), Sparse: ref("b"), MultiVector: ref("c")}, ValidRevisionIDs: []string{"rev-a", "rev-b"}}
}
func chunk(id, revision string) corpus.Chunk {
	return corpus.Chunk{ID: id, RevisionID: revision, ContentID: "book", SourceKind: "source", Location: corpus.Location{Locator: "p/1"}, Text: "fixed text"}
}
func policy() Policy {
	return Policy{Version: "test-v1", Profiles: map[Depth]map[Intelligence]Limits{
		Fast:     {Low: {1, 1, 2, 1, time.Second}, Medium: {1, 2, 3, 2, time.Second}, High: {1, 3, 4, 3, time.Second}},
		Detailed: {Low: {2, 2, 2, 4, time.Second}, Medium: {3, 3, 3, 4, time.Second}, High: {4, 4, 4, 4, time.Second}},
	}}
}
func service(c *calls, planner Planner, verifier EffectiveRevisionChecker, p Policy) *Service {
	d := denseStub{c, func(q dense.Query) (dense.Result, error) {
		return dense.Result{Candidates: []dense.Candidate{{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", Chunk: chunk("a", "rev-a"), IndexRef: q.IndexRef, Score: 0.9, Rank: 1}, {ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", Chunk: chunk("b", "rev-b"), IndexRef: q.IndexRef, Score: 0.8, Rank: 2}}}, nil
	}}
	s := sparseStub{c, func(q sparse.Query) (sparse.Result, error) {
		return sparse.Result{Candidates: []sparse.Candidate{{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "sparse", Chunk: chunk("b", "rev-b"), IndexRef: q.IndexRef, Score: 8, Rank: 1}}}, nil
	}}
	m := multiStub{c, func(q multivector.Query) (multivector.Result, error) {
		return multivector.Result{Candidates: []multivector.Candidate{{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "multivector", Chunk: chunk("a", "rev-a"), IndexRef: q.IndexRef, Score: 0.3, Rank: 1}}}, nil
	}}
	x, e := New(d, s, m, planner, verifier, p)
	if e != nil {
		panic(e)
	}
	return x
}
func oneQuery(_ context.Context, in PlanInput) ([]string, error) {
	if in.Round > 1 {
		return nil, nil
	}
	return []string{in.Query}, nil
}

func TestOptionalMediumProfileVersionPreservesLowAndFollowsExplicitDowngrade(t *testing.T) {
	p := Policy{Version: "low-v1", Profiles: map[Depth]map[Intelligence]Limits{
		Fast: {Low: {1, 1, 2, 1, time.Second}, Medium: {1, 3, 2, 2, time.Second}},
	}, ProfileVersions: map[Depth]map[Intelligence]string{Fast: {Medium: "medium-v1"}}}
	s := service(new(calls), PlanFunc(oneQuery), CheckFunc(allow), p)
	low, _, err := s.effective(Request{Depth: Fast, Intelligence: Low})
	medium, _, mediumErr := s.effective(Request{Depth: Fast, Intelligence: Medium})
	shifted, _, shiftErr := s.effective(Request{Depth: Fast, Intelligence: High, AllowLowerIntelligence: true})
	if err != nil || mediumErr != nil || shiftErr != nil || low.PolicyVersion != "low-v1" ||
		medium.PolicyVersion != "medium-v1" || shifted.PolicyVersion != "medium-v1" ||
		shifted.EffectiveIntelligence != Medium || shifted.ChangeReason == "" {
		t.Fatalf("versioned profiles low=%+v medium=%+v shifted=%+v errors=%v/%v/%v", low, medium, shifted, err, mediumErr, shiftErr)
	}
	p.ProfileVersions[Fast][High] = "unconfigured"
	if _, err := New(denseStub{new(calls), nil}, sparseStub{new(calls), nil}, multiStub{new(calls), nil}, PlanFunc(oneQuery), CheckFunc(allow), p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("version for absent high profile accepted: %v", err)
	}
}
func allow(_ context.Context, _ Snapshot, _ corpus.Chunk) (bool, error) { return true, nil }

func TestThreeIndependentLanesAndRRF(t *testing.T) {
	c := new(calls)
	s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
	r, e := s.Execute(context.Background(), Request{Query: "focus", Depth: Fast, Intelligence: High, Snapshot: snapshot()})
	if e != nil {
		t.Fatal(e)
	}
	if r.Status != "complete" || r.StopReason != "batch_complete" || len(r.Batches) != 1 || len(r.Batches[0].LaneStatus) != 3 || len(r.LaneStatus) != 3 {
		t.Fatalf("state: %+v", r)
	}
	if len(c.dense) != 1 || len(c.sparse) != 1 || len(c.multi) != 1 {
		t.Fatalf("independent calls: %+v", c)
	}
	if c.dense[0].IndexRef != ref("a") || c.sparse[0].IndexRef != ref("b") || c.multi[0].IndexRef != ref("c") || c.multi[0].Generation != 9 || !reflect.DeepEqual(c.multi[0].ValidRevisionIDs, []string{"rev-a", "rev-b"}) {
		t.Fatal("fixed lane inputs differ")
	}
	if len(r.Candidates) != 2 || r.Candidates[0].Key.ChunkID != "a" || len(r.Candidates[0].Sources) != 2 || len(r.Verified) != 2 {
		t.Fatalf("fusion: %+v", r.Candidates)
	}
	if r.Verified[0].Chunk.Text != "" {
		t.Fatal("indexed text escaped into verified candidate")
	}
	for _, candidate := range r.Candidates {
		if candidate.Chunk.Text != "" {
			t.Fatal("indexed text escaped into public candidate set")
		}
	}
	wantA := 2.0 / 61.0
	wantB := 1.0/62.0 + 1.0/61.0
	if math.Abs(r.Candidates[0].RRFScore-wantA) > 1e-12 || math.Abs(r.Candidates[1].RRFScore-wantB) > 1e-12 {
		t.Fatalf("RRF %v %v", r.Candidates[0].RRFScore, r.Candidates[1].RRFScore)
	}
	for _, st := range r.Batches[0].LaneStatus {
		if !st.Requested || !st.Executed || st.Failed || st.Index != snapshot().Indexes[st.Lane] {
			t.Fatalf("lane status: %+v", st)
		}
	}
	if r.LaneStatus[0].CandidateCount != 2 || r.LaneStatus[1].CandidateCount != 1 || r.LaneStatus[2].CandidateCount != 1 {
		t.Fatalf("aggregated lane status: %+v", r.LaneStatus)
	}
}

func TestSixExecutionProfilesAndDetailedStops(t *testing.T) {
	for _, depth := range []Depth{Fast, Detailed} {
		for _, level := range []Intelligence{Low, Medium, High} {
			t.Run(string(depth)+"_"+string(level), func(t *testing.T) {
				c := new(calls)
				var planned []PlanInput
				planner := PlanFunc(func(_ context.Context, in PlanInput) ([]string, error) {
					planned = append(planned, in)
					return []string{in.Query + string(rune('0'+in.Round))}, nil
				})
				s := service(c, planner, CheckFunc(allow), policy())
				r, e := s.Execute(context.Background(), Request{Query: "q", Depth: depth, Intelligence: level, Snapshot: snapshot()})
				if e != nil {
					t.Fatal(e)
				}
				limits := policy().Profiles[depth][level]
				if len(r.Batches) != (func() int {
					if depth == Fast {
						return 1
					}
					return 2
				}()) || r.Batches[0].LaneStatus[0].CandidateCount != 2 || c.dense[0].TopK != limits.TopKPerLane || r.Profile.EffectiveIntelligence != level {
					t.Fatalf("profile not effective: %+v", r)
				}
				if depth == Fast && r.StopReason != "batch_complete" {
					t.Fatalf("fast upgraded: %+v", r)
				}
				if depth == Detailed && r.StopReason != "no_new_evidence" {
					t.Fatalf("detailed did not stop on no progress: %+v", r)
				}
				if len(planned) != len(r.Batches) {
					t.Fatalf("planning rounds: %+v", planned)
				}
			})
		}
	}
}

func TestUnavailableAndExplicitDowngrade(t *testing.T) {
	p := policy()
	delete(p.Profiles[Fast], High)
	c := new(calls)
	s := service(c, PlanFunc(oneQuery), CheckFunc(allow), p)
	_, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: High, Snapshot: snapshot()})
	if !errors.Is(e, ErrUnavailable) || len(c.dense) != 0 {
		t.Fatalf("silent downgrade: %v", e)
	}
	r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: High, AllowLowerIntelligence: true, Snapshot: snapshot()})
	if e != nil || r.Profile.EffectiveIntelligence != Medium || r.Profile.ChangeReason == "" {
		t.Fatalf("approved downgrade: %+v %v", r.Profile, e)
	}
}

func TestFailureEmptyCancellationAndRevisionBoundary(t *testing.T) {
	t.Run("one lane failed requires partial authorization", func(t *testing.T) {
		c := new(calls)
		s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
		s.sparse = sparseStub{c, func(sparse.Query) (sparse.Result, error) { return sparse.Result{}, errors.New("backend down") }}
		_, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
		if !errors.Is(e, ErrLane) {
			t.Fatal(e)
		}
		r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, AllowPartial: true, Snapshot: snapshot()})
		if e != nil || r.Status != "partial" || len(r.DegradedReasons) != 1 || !r.Batches[0].LaneStatus[1].Failed {
			t.Fatalf("partial: %+v %v", r, e)
		}
	})
	t.Run("invalid lane revision is never evidence", func(t *testing.T) {
		c := new(calls)
		s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
		s.dense = denseStub{c, func(q dense.Query) (dense.Result, error) {
			return dense.Result{Candidates: []dense.Candidate{{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", Chunk: chunk("old", "rev-old"), IndexRef: q.IndexRef, Score: 1, Rank: 1}}}, nil
		}}
		r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, AllowPartial: true, Snapshot: snapshot()})
		if e != nil || r.Status != "partial" || len(r.Verified) != 1 || r.Verified[0].Key.ChunkID == "old" || r.Batches[0].LaneStatus[0].Reason != "contract_violation" {
			t.Fatalf("revision: %+v %v", r, e)
		}
	})
	t.Run("current withdrawal excludes evidence", func(t *testing.T) {
		c := new(calls)
		s := service(c, PlanFunc(oneQuery), CheckFunc(func(_ context.Context, _ Snapshot, _ corpus.Chunk) (bool, error) { return false, nil }), policy())
		r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
		if e != nil || r.Status != "empty" || len(r.Verified) != 0 || r.StopReason != "no_evidence" {
			t.Fatalf("withdrawn: %+v %v", r, e)
		}
	})
	t.Run("cancelled before all lanes", func(t *testing.T) {
		c := new(calls)
		s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, e := s.Execute(ctx, Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
		if !errors.Is(e, context.Canceled) || len(c.dense) != 0 {
			t.Fatalf("cancel: %v", e)
		}
	})
	t.Run("planner cannot spend more than budget", func(t *testing.T) {
		c := new(calls)
		s := service(c, PlanFunc(func(context.Context, PlanInput) ([]string, error) { return []string{"a", "b"}, nil }), CheckFunc(allow), policy())
		_, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
		if !errors.Is(e, ErrBudget) || len(c.dense) != 0 {
			t.Fatalf("budget: %v", e)
		}
	})
}

func TestStableChunkIdentityIncludesRevision(t *testing.T) {
	a := chunk("same", "rev-a")
	b := chunk("same", "rev-b")
	if key(a) == key(b) {
		t.Fatal("revision collapsed")
	}
	c := chunk("same", "rev-a")
	c.SourceKind = "wiki"
	if key(a) == key(c) {
		t.Fatal("source kind collapsed")
	}
}

func TestSixProfileLimitsChangeActualExecution(t *testing.T) {
	for _, level := range []Intelligence{Low, Medium, High} {
		t.Run("fast_"+string(level), func(t *testing.T) {
			c := new(calls)
			s := service(c, PlanFunc(func(_ context.Context, in PlanInput) ([]string, error) {
				queries := make([]string, in.RemainingSubqueries)
				for i := range queries {
					queries[i] = fmt.Sprintf("q%d", i)
				}
				return queries, nil
			}), CheckFunc(allow), policy())
			r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: level, Snapshot: snapshot()})
			if e != nil {
				t.Fatal(e)
			}
			want := policy().Profiles[Fast][level].MaxSubqueries
			if len(r.Batches) != 1 || r.UsedSubqueries != want || len(c.dense) != want || len(c.sparse) != want || len(c.multi) != want || c.dense[0].TopK != policy().Profiles[Fast][level].TopKPerLane {
				t.Fatalf("limits did not change execution: %+v", r)
			}
		})
	}
	for _, level := range []Intelligence{Low, Medium, High} {
		t.Run("detailed_"+string(level), func(t *testing.T) {
			c := new(calls)
			s := service(c, PlanFunc(func(_ context.Context, in PlanInput) ([]string, error) {
				return []string{fmt.Sprintf("q%d", in.Round)}, nil
			}), CheckFunc(allow), policy())
			s.dense = denseStub{c, func(q dense.Query) (dense.Result, error) {
				return dense.Result{Candidates: []dense.Candidate{{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", Chunk: chunk(q.Text, "rev-a"), IndexRef: q.IndexRef, Score: 1, Rank: 1}}}, nil
			}}
			s.sparse = sparseStub{c, func(sparse.Query) (sparse.Result, error) { return sparse.Result{Candidates: []sparse.Candidate{}}, nil }}
			s.multi = multiStub{c, func(multivector.Query) (multivector.Result, error) {
				return multivector.Result{Candidates: []multivector.Candidate{}}, nil
			}}
			r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Detailed, Intelligence: level, Snapshot: snapshot()})
			if e != nil {
				t.Fatal(e)
			}
			want := policy().Profiles[Detailed][level].MaxBatches
			if len(r.Batches) != want || len(c.dense) != want || r.Status != "partial" || len(r.Verified) != want {
				t.Fatalf("detailed limits: %+v", r)
			}
		})
	}
}

func TestRejectWrongLaneVersionAndInvalidSnapshotRef(t *testing.T) {
	c := new(calls)
	s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
	bad := snapshot()
	bad.Indexes[Dense] = corpus.Ref{Key: "sha256/not-a-hash", SHA256: "not-a-hash"}
	_, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: bad})
	if !errors.Is(e, ErrInvalid) || len(c.dense) > 0 {
		t.Fatalf("invalid ref reached lane: %v", e)
	}
	s.dense = denseStub{c, func(q dense.Query) (dense.Result, error) {
		return dense.Result{Candidates: []dense.Candidate{{ModuleID: q.ModuleID, ReleaseID: "stale", Generation: q.Generation, Lane: "dense", Chunk: chunk("old", "rev-a"), IndexRef: q.IndexRef, Score: 1, Rank: 1}}}, nil
	}}
	r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, AllowPartial: true, Snapshot: snapshot()})
	if e != nil || r.Status != "partial" || r.Batches[0].LaneStatus[0].Reason != "contract_violation" {
		t.Fatalf("stale lane: %+v %v", r, e)
	}
}

func TestNoEffectiveRevisionSkipsAllLanes(t *testing.T) {
	c := new(calls)
	s := service(c, PlanFunc(oneQuery), CheckFunc(allow), policy())
	snap := snapshot()
	snap.ValidRevisionIDs = nil
	r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snap})
	if e != nil || r.Status != "empty" || len(c.dense) != 0 || len(c.sparse) != 0 || len(c.multi) != 0 {
		t.Fatalf("empty effective set: %+v %v", r, e)
	}
	for _, st := range r.Batches[0].LaneStatus {
		if !st.Requested || !st.Skipped || st.Executed || st.Reason != "no_effective_revision" {
			t.Fatalf("status: %+v", st)
		}
	}
	if !r.LaneStatus[0].Skipped || r.LaneStatus[0].Executed {
		t.Fatalf("aggregate empty lane status: %+v", r.LaneStatus)
	}
}

func TestPolicyIsFrozenAndDistinct(t *testing.T) {
	p := policy()
	p.Profiles[Fast][High] = p.Profiles[Fast][Medium]
	_, e := New(denseStub{new(calls), nil}, sparseStub{new(calls), nil}, multiStub{new(calls), nil}, PlanFunc(oneQuery), CheckFunc(allow), p)
	if !errors.Is(e, ErrInvalid) {
		t.Fatalf("indistinct levels accepted: %v", e)
	}
	p = policy()
	s := service(new(calls), PlanFunc(oneQuery), CheckFunc(allow), p)
	p.Profiles[Fast][Low] = Limits{1, 1, 999, 999, time.Second}
	r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Fast, Intelligence: Low, Snapshot: snapshot()})
	if e != nil || len(r.Verified) != 1 {
		t.Fatalf("policy mutated after assembly: %+v %v", r, e)
	}
}

func TestPlannerCannotMutateSavedQueryOrPrevious(t *testing.T) {
	shared := []string{"  focus  "}
	c := new(calls)
	p := PlanFunc(func(_ context.Context, in PlanInput) ([]string, error) {
		if in.Round == 1 {
			return shared, nil
		}
		if len(in.Previous) > 0 {
			in.Previous[0].Sources[0].Rank = 999
			in.Previous[0].Subqueries[0] = "changed"
		}
		return nil, nil
	})
	s := service(c, p, CheckFunc(allow), policy())
	r, e := s.Execute(context.Background(), Request{Query: "focus", Depth: Detailed, Intelligence: Low, Snapshot: snapshot()})
	if e != nil {
		t.Fatal(e)
	}
	if shared[0] != "  focus  " || len(r.Verified) == 0 || r.Verified[0].Sources[0].Rank == 999 || r.Verified[0].Subqueries[0] != "focus" {
		t.Fatalf("planner mutated owned state: %+v", r.Verified)
	}
}

func TestCandidateTextClearedOnErrorReturn(t *testing.T) {
	c := new(calls)
	p := PlanFunc(func(_ context.Context, in PlanInput) ([]string, error) {
		if in.Round == 1 {
			return []string{"first"}, nil
		}
		return []string{"second", "third"}, nil
	})
	s := service(c, p, CheckFunc(allow), policy())
	r, e := s.Execute(context.Background(), Request{Query: "q", Depth: Detailed, Intelligence: Low, Snapshot: snapshot()})
	if !errors.Is(e, ErrBudget) || len(r.Candidates) == 0 {
		t.Fatalf("expected partial error result: %+v %v", r, e)
	}
	for _, candidate := range r.Candidates {
		if candidate.Chunk.Text != "" {
			t.Fatal("indexed text escaped on error path")
		}
	}
}
