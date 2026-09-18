package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
)

var (
	errNativeManifest   = errors.New("published three-lane native index manifest invalid")
	errNativeProjection = errors.New("published native lane projection unavailable")
	errNativeCapacity   = errors.New("native loaded release capacity reached")
)

const maxLoadedNativeReleases = 4

type nativeCurrentSnapshot interface {
	Current(context.Context, string) (searchdomain.Snapshot, error)
}

type nativeLaneLoads struct {
	Dense       func(context.Context, corpus.Ref) (searchdomain.DenseReader, error)
	Sparse      func(context.Context, corpus.Ref) (searchdomain.SparseReader, error)
	MultiVector func(context.Context, corpus.Ref) (searchdomain.MultiVectorReader, error)
	ProbeDense  func(context.Context, corpus.LaneIndex, corpus.ChunkManifest, []corpus.Chunk) error
	ProbeSparse func(context.Context, corpus.LaneIndex, corpus.ChunkManifest, []corpus.Chunk) error
	ProbeMulti  func(context.Context, corpus.LaneIndex, corpus.ChunkManifest, []corpus.Chunk) error
}

type nativeLoaded struct {
	snapshot searchdomain.Snapshot
	dense    searchdomain.DenseReader
	sparse   searchdomain.SparseReader
	multi    searchdomain.MultiVectorReader
}

// nativeBackend is the only physical search assembly. It never builds,
// upserts, repairs or drops a published collection. Signed HTTP scope calls
// Prepare before the framework Runner starts; Search only uses a loaded set.
type nativeBackend struct {
	current  nativeCurrentSnapshot
	objects  artifacts.Store
	settings indexSettings
	loads    nativeLaneLoads
	observed *telemetry.Bundle
	gate     chan struct{}
	mu       sync.RWMutex
	loaded   map[string]nativeLoaded
}

func newNativeBackend(current nativeCurrentSnapshot, objects artifacts.Store,
	settings indexSettings, loads nativeLaneLoads, observed *telemetry.Bundle) (*nativeBackend, error) {
	if current == nil || objects == nil || observed == nil || observed.Closed() ||
		loads.Dense == nil || loads.Sparse == nil || loads.MultiVector == nil ||
		loads.ProbeDense == nil || loads.ProbeSparse == nil || loads.ProbeMulti == nil {
		return nil, errNativeManifest
	}
	b := &nativeBackend{current: current, objects: objects, settings: settings,
		loads: loads, observed: observed, gate: make(chan struct{}, 1),
		loaded: make(map[string]nativeLoaded)}
	b.gate <- struct{}{}
	return b, nil
}

func nativeSnapshotKey(snapshot searchdomain.Snapshot) (string, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", errNativeManifest
	}
	return artifacts.Hash(raw), nil
}

func (b *nativeBackend) Prepare(parent context.Context, fixed searchdomain.Snapshot) (resultErr error) {
	if b == nil || parent == nil {
		return errNativeManifest
	}
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, stage, err := b.observed.Begin(parent, "search", "search.native.prepare",
		slog.String("module_id", fixed.ModuleID), slog.String("release_id", fixed.ReleaseID),
		slog.Int64("generation", fixed.Generation))
	if err != nil {
		return nativeFailure(parent, errNativeProjection)
	}
	defer func() {
		if resultErr != nil {
			stage.End(ctx, "failed", nativeErrorCode(resultErr), resultErr)
		} else {
			stage.End(ctx, "succeeded", "", nil)
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.gate:
	}
	defer func() { b.gate <- struct{}{} }()
	active, err := b.current.Current(ctx, fixed.ModuleID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !reflect.DeepEqual(active, fixed) {
		return errNativeManifest
	}
	key, err := nativeSnapshotKey(fixed)
	if err != nil {
		return err
	}
	b.mu.RLock()
	_, exists := b.loaded[key]
	loadedCount := len(b.loaded)
	b.mu.RUnlock()
	if exists {
		return nil
	}
	if loadedCount >= maxLoadedNativeReleases {
		return errNativeCapacity // restart is explicit; never evict an in-flight release.
	}
	indexes, chunks, probes, err := inspectNativePublished(ctx, b.objects, b.settings, fixed)
	if err != nil {
		return nativeFailure(ctx, errNativeManifest)
	}
	loaded := nativeLoaded{snapshot: copyNativeSnapshot(fixed)}
	loaded.dense, err = b.loads.Dense(ctx, fixed.Indexes[searchdomain.Dense])
	if err != nil || loaded.dense == nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	loaded.sparse, err = b.loads.Sparse(ctx, fixed.Indexes[searchdomain.Sparse])
	if err != nil || loaded.sparse == nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	loaded.multi, err = b.loads.MultiVector(ctx, fixed.Indexes[searchdomain.MultiVector])
	if err != nil || loaded.multi == nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	if err := b.loads.ProbeDense(ctx, indexes[searchdomain.Dense], chunks, probes); err != nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	if err := b.loads.ProbeSparse(ctx, indexes[searchdomain.Sparse], chunks, probes); err != nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	if err := b.loads.ProbeMulti(ctx, indexes[searchdomain.MultiVector], chunks, probes); err != nil {
		return nativeFailure(ctx, errNativeProjection)
	}
	active, err = b.current.Current(ctx, fixed.ModuleID)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !reflect.DeepEqual(active, fixed) {
		return errNativeManifest
	}
	b.mu.Lock()
	b.loaded[key] = loaded
	b.mu.Unlock()
	return nil
}

func nativeFailure(ctx context.Context, category error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return category
}

func nativeErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "NATIVE_CANCELLED"
	case errors.Is(err, errNativeManifest):
		return "NATIVE_MANIFEST_REJECTED"
	case errors.Is(err, errNativeCapacity):
		return "NATIVE_CAPACITY_REACHED"
	default:
		return "NATIVE_PROJECTION_UNAVAILABLE"
	}
}

func copyNativeSnapshot(value searchdomain.Snapshot) searchdomain.Snapshot {
	out := value
	out.Indexes = make(map[searchdomain.Lane]corpus.Ref, len(value.Indexes))
	for lane, ref := range value.Indexes {
		out.Indexes[lane] = ref
	}
	out.ValidRevisionIDs = append([]string{}, value.ValidRevisionIDs...)
	return out
}

func (b *nativeBackend) prepared(index corpus.Ref, moduleID, releaseID string,
	generation int64, lane searchdomain.Lane) (nativeLoaded, error) {
	if b == nil || !artifacts.ValidHash(index.SHA256) ||
		index.Key != "sha256/"+index.SHA256 {
		return nativeLoaded{}, errNativeManifest
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, value := range b.loaded {
		if value.snapshot.ModuleID == moduleID && value.snapshot.ReleaseID == releaseID &&
			value.snapshot.Generation == generation && value.snapshot.Indexes[lane] == index {
			return value, nil
		}
	}
	return nativeLoaded{}, errNativeProjection
}

type nativeDenseReader struct{ backend *nativeBackend }
type nativeSparseReader struct{ backend *nativeBackend }
type nativeMultiReader struct{ backend *nativeBackend }

func (r nativeDenseReader) Search(ctx context.Context, q dense.Query) (dense.Result, error) {
	value, err := r.backend.prepared(q.IndexRef, q.ModuleID, q.ReleaseID, q.Generation, searchdomain.Dense)
	if err != nil {
		return dense.Result{}, err
	}
	return value.dense.Search(ctx, q)
}
func (r nativeSparseReader) Search(ctx context.Context, q sparse.Query) (sparse.Result, error) {
	value, err := r.backend.prepared(q.IndexRef, q.ModuleID, q.ReleaseID, q.Generation, searchdomain.Sparse)
	if err != nil {
		return sparse.Result{}, err
	}
	return value.sparse.Search(ctx, q)
}
func (r nativeMultiReader) Search(ctx context.Context, q multivector.Query) (multivector.Result, error) {
	value, err := r.backend.prepared(q.IndexRef, q.ModuleID, q.ReleaseID, q.Generation, searchdomain.MultiVector)
	if err != nil {
		return multivector.Result{}, err
	}
	return value.multi.Search(ctx, q)
}

func expectedNativeProfiles(s indexSettings) map[searchdomain.Lane]corpus.Profile {
	return map[searchdomain.Lane]corpus.Profile{
		searchdomain.Dense: {Lane: "dense", Encoder: s.Dense.Document.PhysicalModel,
			Tokenizer: s.Dense.Contract.TokenizerID, Space: s.Dense.Space,
			Dimensions: s.Dense.Contract.Dimensions},
		searchdomain.Sparse: {Lane: "sparse", Encoder: s.Sparse.Document.PhysicalModel,
			Tokenizer: s.Sparse.Contract.TokenizerID, Space: s.Sparse.Space,
			Dimensions: s.Sparse.Contract.Dimensions},
		searchdomain.MultiVector: {Lane: "multivector", Encoder: s.MultiVector.Document.PhysicalModel,
			Tokenizer: s.MultiVector.Contract.TokenizerID, Space: s.MultiVector.Space,
			Dimensions: s.MultiVector.Contract.Dimensions, Mask: "valid",
			Aggregation: s.MultiVector.Contract.Aggregation},
	}
}

func decodeNativeArtifact[T any](ctx context.Context, objects artifacts.Store,
	ref corpus.Ref) (T, error) {
	var value T
	raw, err := objects.Get(ctx, ref)
	if err != nil {
		return value, errNativeManifest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return value, errNativeManifest
	}
	return value, nil
}

func inspectNativePublished(ctx context.Context, objects artifacts.Store,
	settings indexSettings, fixed searchdomain.Snapshot) (map[searchdomain.Lane]corpus.LaneIndex,
	corpus.ChunkManifest, []corpus.Chunk, error) {
	var empty corpus.ChunkManifest
	if ctx == nil || ctx.Err() != nil || objects == nil || fixed.ModuleID == "" ||
		fixed.ReleaseID == "" || fixed.Generation < 1 || len(fixed.Indexes) != 3 ||
		len(fixed.ValidRevisionIDs) == 0 {
		return nil, empty, nil, errNativeManifest
	}
	profiles := expectedNativeProfiles(settings)
	indexes := make(map[searchdomain.Lane]corpus.LaneIndex, 3)
	seenRefs := map[string]bool{}
	var buildID, inputHash string
	var chunkRef corpus.Ref
	for _, lane := range []searchdomain.Lane{searchdomain.Dense, searchdomain.Sparse, searchdomain.MultiVector} {
		ref := fixed.Indexes[lane]
		if !artifacts.ValidHash(ref.SHA256) || ref.Key != "sha256/"+ref.SHA256 || seenRefs[ref.SHA256] {
			return nil, empty, nil, errNativeManifest
		}
		seenRefs[ref.SHA256] = true
		index, err := decodeNativeArtifact[corpus.LaneIndex](ctx, objects, ref)
		if err != nil || index.SchemaVersion != 1 || index.BuildID == "" ||
			index.Generation != fixed.Generation || !artifacts.ValidHash(index.InputManifestHash) ||
			index.Profile != profiles[lane] || len(index.Shards) == 0 ||
			!artifacts.ValidHash(index.ChunkManifest.SHA256) ||
			index.ChunkManifest.Key != "sha256/"+index.ChunkManifest.SHA256 {
			return nil, empty, nil, errNativeManifest
		}
		if buildID != "" && (index.BuildID != buildID || index.InputManifestHash != inputHash ||
			index.ChunkManifest != chunkRef) {
			return nil, empty, nil, errNativeManifest
		}
		buildID, inputHash, chunkRef = index.BuildID, index.InputManifestHash, index.ChunkManifest
		indexes[lane] = index
	}
	manifest, err := decodeNativeArtifact[corpus.ChunkManifest](ctx, objects, chunkRef)
	if err != nil || manifest.SchemaVersion != 1 || manifest.ModuleID != fixed.ModuleID ||
		manifest.ReleaseID != fixed.ReleaseID || manifest.InputManifestHash != inputHash ||
		len(manifest.Inputs) == 0 || len(manifest.Chunks) == 0 {
		return nil, empty, nil, errNativeManifest
	}
	want := make([]string, 0, len(fixed.ValidRevisionIDs))
	seen := map[string]bool{}
	for _, id := range fixed.ValidRevisionIDs {
		if id == "" || seen[id] {
			return nil, empty, nil, errNativeManifest
		}
		seen[id] = true
		want = append(want, id)
	}
	got := make([]string, 0, len(manifest.Inputs))
	for _, input := range manifest.Inputs {
		if input.RevisionID == "" || !seen[input.RevisionID] || input.ChunkCount < 1 {
			return nil, empty, nil, errNativeManifest
		}
		got = append(got, input.RevisionID)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		return nil, empty, nil, errNativeManifest
	}
	chunkIDs := map[string]bool{}
	for _, chunk := range manifest.Chunks {
		if chunk.ID == "" || chunkIDs[chunk.ID] || !chunk.Required ||
			!seen[chunk.RevisionID] || chunk.Text == "" ||
			artifacts.Hash([]byte(chunk.Text)) != chunk.TextHash {
			return nil, empty, nil, errNativeManifest
		}
		chunkIDs[chunk.ID] = true
	}
	return indexes, manifest, []corpus.Chunk{manifest.Chunks[0]}, nil
}
