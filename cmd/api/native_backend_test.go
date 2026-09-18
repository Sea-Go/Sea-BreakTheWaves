package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	searchhttp "github.com/Sea-Go/Sea-BreakTheWaves/internal/transport/http/search"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type nativeNoopTrace struct{}

func (nativeNoopTrace) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (nativeNoopTrace) Shutdown(context.Context) error                             { return nil }

type nativeCurrentFixture struct{ value searchdomain.Snapshot }

func (f *nativeCurrentFixture) Current(ctx context.Context, moduleID string) (searchdomain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return searchdomain.Snapshot{}, err
	}
	if moduleID != f.value.ModuleID {
		return searchdomain.Snapshot{}, errNativeManifest
	}
	return copyNativeSnapshot(f.value), nil
}

type nativeDenseFixture struct{}
type nativeSparseFixture struct{}
type nativeMultiFixture struct{}

func (nativeDenseFixture) Search(_ context.Context, _ dense.Query) (dense.Result, error) {
	return dense.Result{Candidates: []dense.Candidate{}}, nil
}
func (nativeSparseFixture) Search(_ context.Context, _ sparse.Query) (sparse.Result, error) {
	return sparse.Result{Candidates: []sparse.Candidate{}}, nil
}
func (nativeMultiFixture) Search(_ context.Context, _ multivector.Query) (multivector.Result, error) {
	return multivector.Result{Candidates: []multivector.Candidate{}}, nil
}

type nativeFixture struct {
	objects  artifacts.Store
	current  *nativeCurrentFixture
	settings indexSettings
	indexes  map[searchdomain.Lane]corpus.LaneIndex
	manifest corpus.ChunkManifest
	loaded   [3]int
	probed   [3]int
	output   *bytes.Buffer
	observed *telemetry.Bundle
}

func newNativeFixture(t *testing.T) nativeFixture {
	t.Helper()
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inputHash := artifacts.Hash([]byte("fixed RTW release input"))
	chunk := corpus.Chunk{ID: "chunk-proof", RevisionID: "source-revision-proof",
		ContentID: "source-proof", SourceKind: "source", Text: "fixed source proof",
		TextHash: artifacts.Hash([]byte("fixed source proof")), Required: true}
	manifest := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "module-proof",
		ReleaseID: "release-proof", InputManifestHash: inputHash,
		Inputs: []corpus.Input{{RevisionID: chunk.RevisionID, ContentID: chunk.ContentID,
			SourceKind: chunk.SourceKind, ChunkCount: 1}}, Chunks: []corpus.Chunk{chunk}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestRef, err := objects.Put(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	settings := testSettings()
	profiles := expectedNativeProfiles(settings)
	indexes := make(map[searchdomain.Lane]corpus.LaneIndex, 3)
	refs := make(map[searchdomain.Lane]corpus.Ref, 3)
	for _, lane := range []searchdomain.Lane{searchdomain.Dense, searchdomain.Sparse, searchdomain.MultiVector} {
		idx := corpus.LaneIndex{SchemaVersion: 1, BuildID: "build-proof", Generation: 1,
			InputManifestHash: inputHash, ChunkManifest: manifestRef, Profile: profiles[lane],
			Shards: []corpus.IndexShard{{Artifact: manifestRef, ChunkIDs: []string{chunk.ID}}}}
		idxRaw, err := json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		refs[lane], err = objects.Put(context.Background(), idxRaw)
		if err != nil {
			t.Fatal(err)
		}
		indexes[lane] = idx
	}
	var output bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "sea-btw-search-api",
		Environment: "test", Version: "native-fixture", InstanceID: "native-fixture-one",
		Output: &output, Level: slog.LevelInfo, TraceExporter: nativeNoopTrace{}, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observed.Close(context.Background()) })
	snapshot := searchdomain.Snapshot{ModuleID: manifest.ModuleID, ReleaseID: manifest.ReleaseID,
		Generation: 1, PublicationRevision: "1", Indexes: refs,
		ValidRevisionIDs: []string{chunk.RevisionID}}
	return nativeFixture{objects: objects, current: &nativeCurrentFixture{value: snapshot},
		settings: settings, indexes: indexes, manifest: manifest,
		output: &output, observed: observed}
}

func (f *nativeFixture) loads() nativeLaneLoads {
	return nativeLaneLoads{
		Dense: func(_ context.Context, _ corpus.Ref) (searchdomain.DenseReader, error) {
			f.loaded[0]++
			return nativeDenseFixture{}, nil
		},
		Sparse: func(_ context.Context, _ corpus.Ref) (searchdomain.SparseReader, error) {
			f.loaded[1]++
			return nativeSparseFixture{}, nil
		},
		MultiVector: func(_ context.Context, _ corpus.Ref) (searchdomain.MultiVectorReader, error) {
			f.loaded[2]++
			return nativeMultiFixture{}, nil
		},
		ProbeDense: func(_ context.Context, idx corpus.LaneIndex, m corpus.ChunkManifest, p []corpus.Chunk) error {
			f.probed[0]++
			if !reflect.DeepEqual(idx, f.indexes[searchdomain.Dense]) ||
				!reflect.DeepEqual(m, f.manifest) || len(p) != 1 || p[0] != m.Chunks[0] {
				return errNativeManifest
			}
			return nil
		},
		ProbeSparse: func(_ context.Context, idx corpus.LaneIndex, m corpus.ChunkManifest, p []corpus.Chunk) error {
			f.probed[1]++
			if !reflect.DeepEqual(idx, f.indexes[searchdomain.Sparse]) ||
				!reflect.DeepEqual(m, f.manifest) || len(p) != 1 || p[0] != m.Chunks[0] {
				return errNativeManifest
			}
			return nil
		},
		ProbeMulti: func(_ context.Context, idx corpus.LaneIndex, m corpus.ChunkManifest, p []corpus.Chunk) error {
			f.probed[2]++
			if !reflect.DeepEqual(idx, f.indexes[searchdomain.MultiVector]) ||
				!reflect.DeepEqual(m, f.manifest) || len(p) != 1 || p[0] != m.Chunks[0] {
				return errNativeManifest
			}
			return nil
		},
	}
}

func (f *nativeFixture) backend(t *testing.T) *nativeBackend {
	t.Helper()
	b, err := newNativeBackend(f.current, f.objects, f.settings, f.loads(), f.observed)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNativeThreeLanePublicationLoadsAndProbesBeforeAnySearch(t *testing.T) {
	f := newNativeFixture(t)
	b := f.backend(t)
	q := dense.Query{IndexRef: f.current.value.Indexes[searchdomain.Dense],
		ModuleID: f.current.value.ModuleID, ReleaseID: f.current.value.ReleaseID,
		Generation: 1, ValidRevisionIDs: f.current.value.ValidRevisionIDs,
		Text: "fixed proof", TopK: 1}
	if _, err := (nativeDenseReader{b}).Search(context.Background(), q); !errors.Is(err, errNativeProjection) ||
		f.loaded != [3]int{} {
		t.Fatalf("physical search called before signed publication preparation: %v %+v", err, f.loaded)
	}
	if err := b.Prepare(context.Background(), f.current.value); err != nil ||
		f.loaded != [3]int{1, 1, 1} || f.probed != [3]int{1, 1, 1} {
		t.Fatalf("one native release did not load and probe all three lanes: %+v %+v %v", f.loaded, f.probed, err)
	}
	if err := b.Prepare(context.Background(), f.current.value); err != nil ||
		f.loaded != [3]int{1, 1, 1} || f.probed != [3]int{1, 1, 1} {
		t.Fatalf("same signed release reloaded or reprobed physical collections: %+v %+v %v", f.loaded, f.probed, err)
	}
	if result, err := (nativeDenseReader{b}).Search(context.Background(), q); err != nil || len(result.Candidates) != 0 {
		t.Fatalf("native dense snapshot routing failed: %+v %v", result, err)
	}
	q.IndexRef = f.current.value.Indexes[searchdomain.MultiVector]
	if _, err := (nativeDenseReader{b}).Search(context.Background(), q); !errors.Is(err, errNativeProjection) {
		t.Fatalf("wrong lane IndexRef was accepted by native reader: %v", err)
	}
	f.current.value.PublicationRevision = "2" // RTW moved or withdrew after the old set loaded.
	if err := b.Prepare(context.Background(), searchdomain.Snapshot{ModuleID: "module-proof",
		ReleaseID: "release-proof", Generation: 1, PublicationRevision: "1",
		Indexes:          copyNativeSnapshot(f.current.value).Indexes,
		ValidRevisionIDs: []string{"source-revision-proof"}}); !errors.Is(err, errNativeManifest) {
		t.Fatalf("moved RTW publication served an old cached physical set: %v", err)
	}
	if !bytes.Contains(f.output.Bytes(), []byte(`"event":"search.native.prepare.finished"`)) {
		t.Fatal("physical preparation omitted its structured trace-linked completion")
	}
}

func TestNativePublishedManifestRejectsCrossLaneScopeAndMaskBeforeSDKLoad(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*nativeFixture) error
	}{
		{"wrong_multivector_mask", func(f *nativeFixture) error {
			idx := f.indexes[searchdomain.MultiVector]
			idx.Profile.Mask = "dense" // token mask cannot be treated as one vector.
			return replaceNativeIndex(f, searchdomain.MultiVector, idx)
		}},
		{"wrong_sparse_space", func(f *nativeFixture) error {
			idx := f.indexes[searchdomain.Sparse]
			idx.Profile.Space = "other-vocabulary"
			return replaceNativeIndex(f, searchdomain.Sparse, idx)
		}},
		{"missing_dense_lane", func(f *nativeFixture) error {
			delete(f.current.value.Indexes, searchdomain.Dense)
			return nil
		}},
		{"duplicate_cross_lane_ref", func(f *nativeFixture) error {
			f.current.value.Indexes[searchdomain.MultiVector] = f.current.value.Indexes[searchdomain.Dense]
			return nil
		}},
		{"wrong_effective_source_set", func(f *nativeFixture) error {
			f.current.value.ValidRevisionIDs = []string{"source-revision-not-in-fixed-build"}
			return nil
		}},
		{"wrong_generation", func(f *nativeFixture) error {
			f.current.value.Generation = 2
			return nil
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			f := newNativeFixture(t)
			if err := mutate.fn(&f); err != nil {
				t.Fatal(err)
			}
			b := f.backend(t)
			if err := b.Prepare(context.Background(), f.current.value); !errors.Is(err, errNativeManifest) ||
				f.loaded != [3]int{} || f.probed != [3]int{} {
				t.Fatalf("invalid published lane reached SDK Load/Probe: %+v %+v %v", f.loaded, f.probed, err)
			}
		})
	}
}

func replaceNativeIndex(f *nativeFixture, lane searchdomain.Lane, idx corpus.LaneIndex) error {
	raw, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	ref, err := f.objects.Put(context.Background(), raw)
	if err != nil {
		return err
	}
	f.current.value.Indexes[lane] = ref
	f.indexes[lane] = idx
	return nil
}

func TestNativePhysicalFailureAndCancellationLeaveNoSearchableSet(t *testing.T) {
	f := newNativeFixture(t)
	loads := f.loads()
	loads.MultiVector = func(context.Context, corpus.Ref) (searchdomain.MultiVectorReader, error) {
		f.loaded[2]++
		return nil, errors.New("task native token collection absent")
	}
	b, err := newNativeBackend(f.current, f.objects, f.settings, loads, f.observed)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Prepare(context.Background(), f.current.value); !errors.Is(err, errNativeProjection) ||
		f.loaded != [3]int{1, 1, 1} || f.probed != [3]int{} || len(b.loaded) != 0 {
		t.Fatalf("partial physical Load became searchable: %+v %+v %d %v", f.loaded, f.probed, len(b.loaded), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Prepare(ctx, f.current.value); err == nil || len(b.loaded) != 0 {
		t.Fatalf("cancelled native preparation inserted a release: %v", err)
	}
}

func TestNativeCancelledDuringPhysicalLoadNeverStartsAGraphOrCachesARelease(t *testing.T) {
	f := newNativeFixture(t)
	entered := make(chan struct{})
	loads := f.loads()
	loads.Dense = func(ctx context.Context, _ corpus.Ref) (searchdomain.DenseReader, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b, err := newNativeBackend(f.current, f.objects, f.settings, loads, f.observed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Prepare(ctx, f.current.value) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("physical Load never reached its cancellation boundary")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || len(b.loaded) != 0 ||
		f.probed != [3]int{} {
		t.Fatalf("cancelled physical Load became success/503 or ran Probe: %+v %v", f.probed, err)
	}
}

func TestNativeScopeSkipsUnsupportedTierBeforePhysicalLoad(t *testing.T) {
	f := newNativeFixture(t)
	b := f.backend(t)
	innerSummary := searchhttp.ScopeFunc(func(context.Context, *http.Request, searchhttp.PublicRequest) (searchhttp.TrustedScope, error) {
		return searchhttp.TrustedScope{Snapshot: copyNativeSnapshot(f.current.value)}, nil
	})
	policy := searchdomain.Policy{Profiles: map[searchdomain.Depth]map[searchdomain.Intelligence]searchdomain.Limits{
		searchdomain.Fast: {searchdomain.Low: {MaxBatches: 1}}}}
	resolver := nativeSummaryScope{inner: innerSummary, backend: b, policy: policy}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/search/summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveSearch(context.Background(), request,
		searchhttp.PublicRequest{Depth: searchdomain.Detailed, Intelligence: searchdomain.High}); err != nil || f.loaded != [3]int{} {
		t.Fatalf("unsupported signed Summary tier loaded native indexes: %+v %v", f.loaded, err)
	}
	innerTools := searchhttp.ToolsScopeFunc(func(context.Context, *http.Request, searchhttp.ToolsRequest) (searchhttp.TrustedToolsScope, error) {
		return searchhttp.TrustedToolsScope{Snapshot: copyNativeSnapshot(f.current.value)}, nil
	})
	tools := nativeToolsScope{inner: innerTools, backend: b, mediumEnabled: false}
	if _, err := tools.ResolveTools(context.Background(), request,
		searchhttp.ToolsRequest{Depth: searchdomain.Fast, Intelligence: searchdomain.High}); err != nil || f.loaded != [3]int{} {
		t.Fatalf("unsupported signed Tools tier loaded native indexes: %+v %v", f.loaded, err)
	}
	if _, err := resolver.ResolveSearch(context.Background(), request,
		searchhttp.PublicRequest{Depth: searchdomain.Fast, Intelligence: searchdomain.Low}); err != nil || f.loaded != [3]int{1, 1, 1} {
		t.Fatalf("supported signed tier did not prepare all native lanes: %+v %v", f.loaded, err)
	}
}

// Compile-time assertions protect the native reader seam used by SearchService.
var (
	_ searchdomain.DenseReader       = nativeDenseReader{}
	_ searchdomain.SparseReader      = nativeSparseReader{}
	_ searchdomain.MultiVectorReader = nativeMultiReader{}
	_ searchhttp.ScopeResolver       = nativeSummaryScope{}
	_ searchhttp.ToolsScopeResolver  = nativeToolsScope{}
)
