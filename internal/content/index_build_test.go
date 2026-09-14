package content

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

type indexFixtureLane struct {
	objects        artifacts.Store
	calls          int
	failedOnce     bool
	projectionOnce bool
	badGeneration  bool
	resumed        bool
	probeFailure   bool
}

func (f *indexFixtureLane) build(ctx context.Context, id string, generation int64, chunks corpus.Ref, profile corpus.Profile, resume *corpus.Ref) (corpus.LaneIndex, corpus.Ref, error) {
	f.calls++
	if f.failedOnce && f.calls == 1 {
		return corpus.LaneIndex{}, corpus.Ref{}, errors.New("fixture model unavailable")
	}
	if resume != nil {
		f.resumed = true
		raw, err := f.objects.Get(ctx, *resume)
		if err != nil {
			return corpus.LaneIndex{}, corpus.Ref{}, err
		}
		var index corpus.LaneIndex
		if err := json.Unmarshal(raw, &index); err != nil {
			return corpus.LaneIndex{}, corpus.Ref{}, err
		}
		return index, *resume, nil
	}
	raw, err := f.objects.Get(ctx, chunks)
	if err != nil {
		return corpus.LaneIndex{}, corpus.Ref{}, err
	}
	var manifest corpus.ChunkManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return corpus.LaneIndex{}, corpus.Ref{}, err
	}
	ids := make([]string, 0, len(manifest.Chunks))
	for _, chunk := range manifest.Chunks {
		ids = append(ids, chunk.ID)
	}
	shard, err := f.objects.Put(ctx, []byte("synthetic numeric shard: "+profile.Lane))
	if err != nil {
		return corpus.LaneIndex{}, corpus.Ref{}, err
	}
	index := corpus.LaneIndex{SchemaVersion: 1, BuildID: id, Generation: generation,
		InputManifestHash: manifest.InputManifestHash, ChunkManifest: chunks, Profile: profile,
		Shards: []corpus.IndexShard{{Artifact: shard, ChunkIDs: ids}}}
	if f.badGeneration {
		index.Generation++
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return corpus.LaneIndex{}, corpus.Ref{}, err
	}
	ref, err := f.objects.Put(ctx, encoded)
	if err != nil {
		return corpus.LaneIndex{}, corpus.Ref{}, err
	}
	if f.projectionOnce && f.calls == 1 {
		return index, ref, errors.New("fixture backend projection unavailable")
	}
	return index, ref, nil
}

func (f *indexFixtureLane) VerifyAndProbe(_ context.Context, _ corpus.LaneIndex, _ corpus.ChunkManifest, probes []corpus.Chunk) ([]corpus.ProbeResult, error) {
	if f.probeFailure {
		return nil, errors.New("fixture independent query failed")
	}
	results := make([]corpus.ProbeResult, 0, len(probes))
	for _, probe := range probes {
		results = append(results, corpus.ProbeResult{QueryChunkID: probe.ID, CandidateIDs: []string{probe.ID}})
	}
	return results, nil
}

type denseIndexFixture struct{ *indexFixtureLane }

func (f denseIndexFixture) Build(ctx context.Context, q dense.BuildRequest) (dense.BuildResult, error) {
	index, ref, err := f.build(ctx, q.BuildID, q.Generation, q.ChunkManifest, q.Profile, q.ResumeIndex)
	return dense.BuildResult{Index: index, Ref: ref}, err
}

type sparseIndexFixture struct{ *indexFixtureLane }

func (f sparseIndexFixture) Build(ctx context.Context, q sparse.BuildRequest) (sparse.BuildResult, error) {
	index, ref, err := f.build(ctx, q.BuildID, q.Generation, q.ChunkManifest, q.Profile, q.ResumeIndex)
	return sparse.BuildResult{Index: index, Ref: ref}, err
}

type multiIndexFixture struct{ *indexFixtureLane }

func (f multiIndexFixture) Build(ctx context.Context, q multivector.BuildRequest) (multivector.BuildResult, error) {
	index, ref, err := f.build(ctx, q.BuildID, q.Generation, q.ChunkManifest, q.Profile, q.ResumeIndex)
	return multivector.BuildResult{Index: index, Ref: ref}, err
}

type reassignedBuildSource struct {
	RevisionSource
	build ridethewind.Build
}

func (s reassignedBuildSource) GetBuild(context.Context, string) (ridethewind.Build, error) {
	return s.build, nil
}

func indexFixture(t *testing.T) (*IndexCoordinator, *Store, Fence, map[string]*indexFixtureLane) {
	t.Helper()
	store, objects, _, fence, _, source := preparedFixture(t)
	lanes := map[string]*indexFixtureLane{}
	for _, name := range []string{"dense", "sparse", "multivector"} {
		lanes[name] = &indexFixtureLane{objects: objects}
	}
	coordinator, err := NewIndexCoordinator(source, objects, store,
		denseIndexFixture{lanes["dense"]}, sparseIndexFixture{lanes["sparse"]}, multiIndexFixture{lanes["multivector"]}, contentObservationForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, store, fence, lanes
}

func TestIndexCoordinatorThreeLanesAndReplay(t *testing.T) {
	coordinator, store, fence, lanes := indexFixture(t)
	ctx := context.Background()
	result, err := coordinator.Index(ctx, "build", fence, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "READY" || !validRef(result.IndexManifest) || len(result.Lanes) != 3 || len(result.ResumeIndexes) != 0 {
		t.Fatalf("incomplete local ready receipt: %+v", result)
	}
	remote, err := coordinator.source.GetBuild(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	remote.State = "READY" // RTW may already have accepted the local receipt on replay.
	remote.IndexManifestRef = result.IndexManifest.Key
	remote.IndexManifestHash = result.IndexManifest.SHA256
	coordinator.source = reassignedBuildSource{RevisionSource: coordinator.source, build: remote}
	replayed, err := coordinator.Index(ctx, "build", fence, nil)
	if err != nil || replayed.IndexManifest != result.IndexManifest {
		t.Fatalf("replay changed ready artifact: %+v, %v", replayed, err)
	}
	if _, err := coordinator.Index(ctx, "build", fence, map[string]corpus.Ref{"dense": artifacts.Reference([]byte("other"))}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting resume replaced fixed lane: %v", err)
	}
	for lane, service := range lanes {
		if service.calls != 1 {
			t.Fatalf("%s rebuilt on replay: %d", lane, service.calls)
		}
	}
	b, err := store.Get(ctx, "build")
	if err != nil || b.State != "READY" || b.Result == nil || *b.Result != result.IndexManifest {
		t.Fatalf("store ready differs: %+v %v", b, err)
	}
}

func TestIndexCoordinatorReadyRecoveryUnderNewTechnicalAttempt(t *testing.T) {
	coordinator, store, oldFence, lanes := indexFixture(t)
	ctx := context.Background()
	first, err := coordinator.Index(ctx, "build", oldFence, nil)
	if err != nil || first.State != "READY" {
		t.Fatalf("first local ready: %+v %v", first, err)
	}
	var before int
	if err := store.db.QueryRow(ctx, "SELECT COUNT(*) FROM content_outbox WHERE build_id=$1", "build").Scan(&before); err != nil {
		t.Fatal(err)
	}
	newFence := oldFence
	newFence.AttemptID = "ack-retry"
	newFence.LeaseEpoch++
	newFence.ExpiresAt = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	remote, err := coordinator.source.GetBuild(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	remote.AttemptId = newFence.AttemptID
	remote.LeaseEpoch = newFence.LeaseEpoch
	remote.LeaseExpiresAt = newFence.ExpiresAt.Format(time.RFC3339Nano)
	reassigned := reassignedBuildSource{RevisionSource: coordinator.source, build: remote}
	coordinator.source, coordinator.reconciler.source = reassigned, reassigned
	if _, err := coordinator.Index(ctx, "build", oldFence, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("old attempt replayed against new RTW claim: %v", err)
	}
	replayed, err := coordinator.Index(ctx, "build", newFence, nil)
	if err != nil || replayed.State != "READY" || replayed.IndexManifest != first.IndexManifest {
		t.Fatalf("new attempt did not reuse committed READY: %+v %v", replayed, err)
	}
	stored, err := store.Get(ctx, "build")
	if err != nil || stored.Fence.AttemptID != oldFence.AttemptID || stored.Result == nil || *stored.Result != first.IndexManifest {
		t.Fatalf("READY fence or artifact was rewritten: %+v %v", stored, err)
	}
	var after int
	if err := store.db.QueryRow(ctx, "SELECT COUNT(*) FROM content_outbox WHERE build_id=$1", "build").Scan(&after); err != nil || after != before {
		t.Fatalf("READY outbox replayed: before=%d after=%d err=%v", before, after, err)
	}
	for lane, service := range lanes {
		if service.calls != 1 {
			t.Fatalf("%s rebuilt on technical ACK retry: %d", lane, service.calls)
		}
	}
	remote.State = "READY"
	remote.IndexManifestRef, remote.IndexManifestHash = "sha256/"+strings.Repeat("e", 64), strings.Repeat("e", 64)
	reassigned = reassignedBuildSource{RevisionSource: reassigned.RevisionSource, build: remote}
	coordinator.source, coordinator.reconciler.source = reassigned, reassigned
	if _, err := coordinator.Index(ctx, "build", newFence, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong RTW accepted result replayed: %v", err)
	}
}

func TestIndexCoordinatorRetryAndProjectionResume(t *testing.T) {
	for _, failedLane := range []string{"sparse", "multivector"} {
		t.Run(failedLane, func(t *testing.T) {
			coordinator, store, fence, lanes := indexFixture(t)
			if failedLane == "sparse" {
				lanes[failedLane].failedOnce = true
			} else {
				lanes[failedLane].projectionOnce = true
			}
			ctx := context.Background()
			partial, err := coordinator.Index(ctx, "build", fence, nil)
			wantLanes := 1
			if failedLane == "multivector" {
				wantLanes = 2
			}
			if err == nil || partial.State != "BUILDING" || len(partial.Lanes) != wantLanes {
				t.Fatalf("failure manufactured ready: %+v %v", partial, err)
			}
			b, err := store.Get(ctx, "build")
			if err != nil || b.State != "BUILDING" || b.Result != nil {
				t.Fatalf("failure persisted ready: %+v %v", b, err)
			}
			resume := partial.ResumeIndexes
			if failedLane == "multivector" && !validRef(resume[failedLane]) {
				t.Fatal("complete projection retry hint missing")
			}
			ready, err := coordinator.Index(ctx, "build", fence, resume)
			if err != nil || ready.State != "READY" || len(ready.Lanes) != 3 {
				t.Fatalf("retry did not reconcile: %+v %v", ready, err)
			}
			if lanes["dense"].calls != 1 || lanes[failedLane].calls != 2 {
				t.Fatalf("retry regenerated committed lane: dense=%d failed=%d", lanes["dense"].calls, lanes[failedLane].calls)
			}
			if failedLane == "multivector" && !lanes[failedLane].resumed {
				t.Fatal("complete immutable index not resumed")
			}
		})
	}
}

func TestIndexCoordinatorRejectsStaleAndForeignGeneration(t *testing.T) {
	for _, scenario := range []string{"stale_lease", "foreign_generation", "probe_failure"} {
		t.Run(scenario, func(t *testing.T) {
			coordinator, store, fence, lanes := indexFixture(t)
			switch scenario {
			case "stale_lease":
				fence.LeaseEpoch++
			case "foreign_generation":
				lanes["sparse"].badGeneration = true
			case "probe_failure":
				lanes["multivector"].probeFailure = true
			}
			result, err := coordinator.Index(context.Background(), "build", fence, nil)
			if err == nil || result.State == "READY" {
				t.Fatalf("%s accepted: %+v %v", scenario, result, err)
			}
			b, err := store.Get(context.Background(), "build")
			if err != nil || b.State != "BUILDING" || b.Result != nil {
				t.Fatalf("%s manufactured store READY: %+v %v", scenario, b, err)
			}
		})
	}
}

func TestIndexCoordinatorRejectsUnknownResumeAndMissingIndexer(t *testing.T) {
	coordinator, _, fence, _ := indexFixture(t)
	_, err := coordinator.Index(context.Background(), "build", fence, map[string]corpus.Ref{"invented": artifacts.Reference([]byte("foreign"))})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown lane accepted: %v", err)
	}
	var missing *denseIndexFixture
	_, err = NewIndexCoordinator(coordinator.source, coordinator.objects, coordinator.store,
		missing, sparseIndexFixture{}, multiIndexFixture{}, contentObservationForTest(t))
	if err == nil {
		t.Fatal("nil typed lane accepted")
	}
}

func TestIndexCoordinatorNewAttemptFencesOldWorker(t *testing.T) {
	coordinator, store, oldFence, _ := indexFixture(t)
	ctx := context.Background()
	initial, err := store.Get(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	replacement := oldFence
	replacement.AttemptID = "replacement"
	replacement.LeaseEpoch++
	replacement.ExpiresAt = time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	remote, err := coordinator.source.GetBuild(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	remote.AttemptId = replacement.AttemptID
	remote.LeaseEpoch = replacement.LeaseEpoch
	remote.LeaseExpiresAt = replacement.ExpiresAt.Format(time.RFC3339Nano)
	reassigned := reassignedBuildSource{RevisionSource: coordinator.source, build: remote}
	coordinator.source = reassigned
	coordinator.reconciler.source = reassigned
	if _, err := store.Claim(ctx, initial.BuildInput, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Index(ctx, "build", oldFence, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("old worker accepted after lease replacement: %v", err)
	}
	result, err := coordinator.Index(ctx, "build", replacement, nil)
	if err != nil || result.State != "READY" || len(result.Lanes) != 3 {
		t.Fatalf("replacement did not complete: %+v %v", result, err)
	}
}
