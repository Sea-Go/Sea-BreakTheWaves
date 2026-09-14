package content

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
)

// The three services keep their own representation and projection semantics.
// The content domain owns the fixed build identity, lease and READY transition.
type DenseIndexer interface {
	LaneVerifier
	Build(context.Context, dense.BuildRequest) (dense.BuildResult, error)
}

type SparseIndexer interface {
	LaneVerifier
	Build(context.Context, sparse.BuildRequest) (sparse.BuildResult, error)
}

type MultiVectorIndexer interface {
	LaneVerifier
	Build(context.Context, multivector.BuildRequest) (multivector.BuildResult, error)
}

type laneBuild func(context.Context, string, int64, corpus.Ref, corpus.Profile, *corpus.Ref) (corpus.LaneIndex, corpus.Ref, error)

type IndexCoordinator struct {
	source     RevisionSource
	objects    artifacts.Store
	store      *Store
	reconciler *Reconciler
	builders   map[string]laneBuild
	observed   *telemetry.Bundle
}

// NewIndexCoordinator borrows its dependencies. The owner closes the lane
// backends, artifact store and telemetry only after all build runs have ended.
func NewIndexCoordinator(source RevisionSource, objects artifacts.Store, store *Store, denseLane DenseIndexer,
	sparseLane SparseIndexer, multiLane MultiVectorIndexer, observed *telemetry.Bundle) (*IndexCoordinator, error) {
	if source == nil || objects == nil || store == nil || observed == nil || !observed.Installed() ||
		isNilIndexer(denseLane) || isNilIndexer(sparseLane) || isNilIndexer(multiLane) {
		return nil, ErrInvalid
	}
	verifiers := map[string]LaneVerifier{"dense": denseLane, "sparse": sparseLane, "multivector": multiLane}
	reconciler, err := NewReconciler(source, objects, store, verifiers, observed)
	if err != nil {
		return nil, err
	}
	builders := map[string]laneBuild{
		"dense": func(ctx context.Context, id string, generation int64, chunks corpus.Ref, profile corpus.Profile, resume *corpus.Ref) (corpus.LaneIndex, corpus.Ref, error) {
			result, err := denseLane.Build(ctx, dense.BuildRequest{BuildID: id, Generation: generation, ChunkManifest: chunks, Profile: profile, ResumeIndex: resume})
			return result.Index, result.Ref, err
		},
		"sparse": func(ctx context.Context, id string, generation int64, chunks corpus.Ref, profile corpus.Profile, resume *corpus.Ref) (corpus.LaneIndex, corpus.Ref, error) {
			result, err := sparseLane.Build(ctx, sparse.BuildRequest{BuildID: id, Generation: generation, ChunkManifest: chunks, Profile: profile, ResumeIndex: resume})
			return result.Index, result.Ref, err
		},
		"multivector": func(ctx context.Context, id string, generation int64, chunks corpus.Ref, profile corpus.Profile, resume *corpus.Ref) (corpus.LaneIndex, corpus.Ref, error) {
			result, err := multiLane.Build(ctx, multivector.BuildRequest{BuildID: id, Generation: generation, ChunkManifest: chunks, Profile: profile, ResumeIndex: resume})
			return result.Index, result.Ref, err
		},
	}
	return &IndexCoordinator{source: source, objects: objects, store: store, reconciler: reconciler, builders: builders, observed: observed}, nil
}

func isNilIndexer(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// IndexBuildResult is local content state only. It is not a DC technical-job
// completion, an RTW build acceptance, or a publication of the release.
// ResumeIndexes contains complete immutable lane encodings that failed backend
// projection; a caller must persist these hints before a later retry can reuse
// them. Lanes contains only refs committed under the current content fence.
type IndexBuildResult struct {
	BuildID       string                `json:"build_id"`
	ReleaseID     string                `json:"release_id"`
	Generation    int64                 `json:"generation"`
	ChunkManifest corpus.Ref            `json:"chunk_manifest"`
	Lanes         map[string]corpus.Ref `json:"lanes"`
	ResumeIndexes map[string]corpus.Ref `json:"resume_indexes,omitempty"`
	IndexManifest corpus.Ref            `json:"index_manifest"`
	State         string                `json:"state"`
}

// Index reclaims an already prepared build with the new DC/RTW attempt. The
// original BuildInput (including OperationID) comes from Store and must remain
// stable even when this index job has a different technical operation ID.
func (c *IndexCoordinator) Index(ctx context.Context, buildID string, fence Fence, resume map[string]corpus.Ref) (out IndexBuildResult, err error) {
	ctx, stage, startErr := c.observed.Begin(ctx, "content", "content.index_build", slog.String("build_id", buildID),
		slog.String("attempt_id", fence.AttemptID), slog.Int64("lease_epoch", fence.LeaseEpoch), slog.Int64("cancel_version", fence.CancelVersion))
	if startErr != nil {
		return out, startErr
	}
	defer func() {
		if value := recover(); value != nil {
			stage.End(ctx, "failed", "CONTENT_PANIC", fmt.Errorf("content panic (%T): %v", value, value))
			panic(value)
		}
		outcome, code := contentObservation(err)
		stage.End(ctx, outcome, code, err, slog.Int("lane_count", len(out.Lanes)), slog.String("index_manifest_hash", out.IndexManifest.SHA256))
	}()
	if buildID == "" || fence.BuildID != buildID || fence.AttemptID == "" || fence.LeaseEpoch <= 0 ||
		fence.CancelVersion < 0 || fence.ExpiresAt.IsZero() {
		return out, ErrInvalid
	}
	for lane, ref := range resume {
		if lane != "dense" && lane != "sparse" && lane != "multivector" || !validRef(ref) {
			return out, fmt.Errorf("%w: invalid resume lane or reference", ErrInvalid)
		}
	}
	initial, err := c.store.Get(ctx, buildID)
	if err != nil {
		return out, err
	}
	out = IndexBuildResult{BuildID: buildID, ReleaseID: initial.ReleaseID, Generation: initial.Generation,
		Lanes: map[string]corpus.Ref{}, ResumeIndexes: map[string]corpus.Ref{}, State: initial.State}
	if initial.Chunks == nil || !validRef(*initial.Chunks) {
		return out, fmt.Errorf("%w: prepared chunks required", ErrConflict)
	}
	out.ChunkManifest = *initial.Chunks
	if initial.State == "READY" {
		for lane, ref := range initial.Lanes {
			out.Lanes[lane] = ref
		}
		if err := checkRecordedResume(out.Lanes, resume); err != nil {
			return out, err
		}
		return c.recoverReady(ctx, initial, fence, out)
	}
	if initial.State != "BUILDING" {
		return out, ErrConflict
	}
	if err := c.checkRemoteBuild(ctx, initial.BuildInput, fence); err != nil {
		return out, err
	}
	profiles, chunkProfile, err := c.fixedProfiles(ctx, initial.BuildInput)
	if err != nil {
		return out, err
	}
	if err := c.checkChunks(ctx, initial, chunkProfile); err != nil {
		return out, err
	}
	current, err := c.store.Claim(ctx, initial.BuildInput, fence)
	if err != nil {
		return out, err
	}
	out.State = current.State
	for lane, ref := range current.Lanes {
		out.Lanes[lane] = ref
	}
	if err := checkRecordedResume(out.Lanes, resume); err != nil {
		return out, err
	}
	if current.State == "READY" {
		return c.recoverReady(ctx, current, fence, out)
	}
	if current.State != "BUILDING" || current.Chunks == nil || *current.Chunks != out.ChunkManifest {
		return out, ErrConflict
	}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		if _, exists := out.Lanes[lane]; exists {
			continue
		}
		var resumeRef *corpus.Ref
		if ref, exists := resume[lane]; exists {
			resumeRef = &ref
		}
		index, ref, buildErr := c.builders[lane](ctx, buildID, current.Generation, out.ChunkManifest, profiles[lane], resumeRef)
		if buildErr != nil {
			// A lane can return a complete encoding ref after a failed projection.
			// It is a retry hint, never a successful lane registration.
			if validRef(ref) && c.checkLaneArtifact(ctx, ref, index, current, profiles[lane]) == nil {
				out.ResumeIndexes[lane] = ref
			}
			return out, fmt.Errorf("build %s lane: %w", lane, buildErr)
		}
		if err := c.checkLaneArtifact(ctx, ref, index, current, profiles[lane]); err != nil {
			return out, fmt.Errorf("verify %s build result: %w", lane, err)
		}
		if err := c.store.RecordLane(ctx, fence, lane, ref); err != nil {
			return out, fmt.Errorf("record %s lane: %w", lane, err)
		}
		out.Lanes[lane] = ref
	}
	if err := c.checkRemoteBuild(ctx, current.BuildInput, fence); err != nil {
		return out, err
	}
	ref, err := c.reconciler.Ready(ctx, fence)
	if err != nil {
		return out, fmt.Errorf("reconcile fixed generation: %w", err)
	}
	out.IndexManifest = ref
	out.State = "READY"
	return out, nil
}

// recoverReady acknowledges an immutable local result under a fresh technical
// attempt without rewriting its original content fence or duplicating Outbox.
// The new RTW claim is checked separately; the old fence is used only to read
// and replay the already-committed READY artifact.
func (c *IndexCoordinator) recoverReady(ctx context.Context, ready Build, current Fence, out IndexBuildResult) (IndexBuildResult, error) {
	if ready.State != "READY" || ready.Result == nil || !validRef(*ready.Result) {
		return out, ErrConflict
	}
	if err := c.checkRemoteBuildState(ctx, ready.BuildInput, current, ready.Result); err != nil {
		return out, err
	}
	_, chunkProfile, err := c.fixedProfiles(ctx, ready.BuildInput)
	if err != nil {
		return out, err
	}
	if err := c.checkChunks(ctx, ready, chunkProfile); err != nil {
		return out, err
	}
	ref, err := c.reconciler.Ready(ctx, ready.Fence)
	if err != nil {
		return out, err
	}
	if ref != *ready.Result {
		return out, ErrConflict
	}
	if err := c.checkRemoteBuildState(ctx, ready.BuildInput, current, ready.Result); err != nil {
		return out, err
	}
	out.IndexManifest = ref
	out.State = "READY"
	return out, nil
}

func checkRecordedResume(recorded, resume map[string]corpus.Ref) error {
	for lane, hint := range resume {
		if fixed, exists := recorded[lane]; exists && hint != fixed {
			return fmt.Errorf("%w: %s lane already has a different fixed index", ErrConflict, lane)
		}
	}
	return nil
}

func (c *IndexCoordinator) checkRemoteBuild(ctx context.Context, input BuildInput, fence Fence) error {
	return c.checkRemoteBuildState(ctx, input, fence, nil)
}

func (c *IndexCoordinator) checkRemoteBuildState(ctx context.Context, input BuildInput, fence Fence, ready *corpus.Ref) error {
	remote, err := c.source.GetBuild(ctx, input.BuildID)
	if err != nil {
		return fmt.Errorf("read fixed build: %w", err)
	}
	if remote.BuildId != input.BuildID || remote.ReleaseId != input.ReleaseID || remote.ModuleId != input.ModuleID ||
		remote.Generation != input.Generation || remote.ManifestHash != input.InputHash ||
		remote.AttemptId != fence.AttemptID || remote.LeaseEpoch != fence.LeaseEpoch || remote.CancelVersion != fence.CancelVersion {
		return fmt.Errorf("%w: RTW build claim differs from fixed content input", ErrConflict)
	}
	if remote.State != "BUILDING" {
		if ready == nil || remote.State != "READY" || remote.IndexManifestRef != ready.Key || remote.IndexManifestHash != ready.SHA256 {
			return fmt.Errorf("%w: RTW build result differs from local READY", ErrConflict)
		}
	}
	expires, err := time.Parse(time.RFC3339Nano, remote.LeaseExpiresAt)
	if err != nil || !expires.Equal(fence.ExpiresAt) {
		return fmt.Errorf("%w: RTW and DC lease expiry differ", ErrConflict)
	}
	return nil
}

func (c *IndexCoordinator) fixedProfiles(ctx context.Context, input BuildInput) (map[string]corpus.Profile, string, error) {
	release, err := c.source.GetRelease(ctx, input.ReleaseID)
	if err != nil {
		return nil, "", fmt.Errorf("read fixed release: %w", err)
	}
	if release.ModuleId != input.ModuleID || release.ReleaseId != input.ReleaseID || release.ManifestHash != input.InputHash ||
		release.ManifestRef != "sha256/"+input.InputHash {
		return nil, "", fmt.Errorf("%w: release identity or manifest differs", ErrConflict)
	}
	raw, err := c.objects.Get(ctx, corpus.Ref{Key: release.ManifestRef, SHA256: release.ManifestHash})
	if err != nil {
		return nil, "", err
	}
	var manifest ReleaseManifest
	if err := decodeObject(raw, &manifest); err != nil {
		return nil, "", err
	}
	expected := ReleaseManifest{1, release.ModuleId, release.ReleaseId, release.SourceRevisionIds, release.WikiRevisionIds,
		release.ChunkingProfile, release.RetrievalProfiles}
	if !reflect.DeepEqual(manifest, expected) || len(manifest.RetrievalProfiles) != 3 {
		return nil, "", fmt.Errorf("%w: release object or three profiles differ", ErrInvalid)
	}
	profiles := make(map[string]corpus.Profile, 3)
	for _, profile := range manifest.RetrievalProfiles {
		if c.builders[profile.Lane] == nil || profiles[profile.Lane].Lane != "" {
			return nil, "", fmt.Errorf("%w: duplicate or unknown retrieval lane", ErrInvalid)
		}
		profiles[profile.Lane] = corpus.Profile(profile)
	}
	if len(profiles) != 3 {
		return nil, "", ErrInvalid
	}
	return profiles, manifest.ChunkingProfile, nil
}

func (c *IndexCoordinator) checkChunks(ctx context.Context, b Build, profile string) error {
	if b.Chunks == nil {
		return ErrConflict
	}
	raw, err := c.objects.Get(ctx, *b.Chunks)
	if err != nil {
		return err
	}
	var manifest corpus.ChunkManifest
	if err := decodeObject(raw, &manifest); err != nil {
		return err
	}
	if manifest.SchemaVersion != 1 || manifest.ModuleID != b.ModuleID || manifest.ReleaseID != b.ReleaseID ||
		manifest.InputManifestHash != b.InputHash || manifest.Profile != profile || len(manifest.Chunks) == 0 {
		return ErrInvalid
	}
	return nil
}

func (c *IndexCoordinator) checkLaneArtifact(ctx context.Context, ref corpus.Ref, result corpus.LaneIndex, b Build, profile corpus.Profile) error {
	if !validRef(ref) || b.Chunks == nil {
		return ErrInvalid
	}
	raw, err := c.objects.Get(ctx, ref)
	if err != nil {
		return err
	}
	var stored corpus.LaneIndex
	if err := decodeObject(raw, &stored); err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, result) || stored.SchemaVersion != 1 || stored.BuildID != b.BuildInput.BuildID ||
		stored.Generation != b.Generation || stored.InputManifestHash != b.InputHash || stored.ChunkManifest != *b.Chunks ||
		stored.Profile != profile || len(stored.Shards) == 0 {
		return ErrInvalid
	}
	return nil
}
