package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

// RevisionSource is the worker product API. It does not grant access to RTW's
// database, nor resolve mutable current-head aliases.
type RevisionSource interface {
	GetBuild(context.Context, string) (ridethewind.Build, error)
	GetRelease(context.Context, string) (ridethewind.Release, error)
	GetRevision(context.Context, string) (ridethewind.Revision, error)
}

// ReleaseManifest mirrors the fixed H03 v1 object, separately from the product
// Release metadata envelope. Every value is compared to the exact hashed object.
type ReleaseManifest struct {
	SchemaVersion     int                            `json:"schema_version"`
	ModuleID          string                         `json:"module_id"`
	ReleaseID         string                         `json:"release_id"`
	SourceRevisionIDs []string                       `json:"source_revision_ids"`
	WikiRevisionIDs   []string                       `json:"wiki_revision_ids"`
	ChunkingProfile   string                         `json:"chunking_profile"`
	RetrievalProfiles []ridethewind.RetrievalProfile `json:"retrieval_profiles"`
}

type Prepared struct {
	Manifest corpus.ChunkManifest
	Ref      corpus.Ref
	Build    Build
}

type Preparer struct {
	source  RevisionSource
	objects artifacts.Store
	store   *Store
	chunker *Chunker
}

func NewPreparer(source RevisionSource, objects artifacts.Store, store *Store, chunker *Chunker) (*Preparer, error) {
	if source == nil || objects == nil || store == nil || chunker == nil {
		return nil, ErrInvalid
	}
	return &Preparer{source: source, objects: objects, store: store, chunker: chunker}, nil
}

func decodeObject(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: malformed artifact: %v", ErrInvalid, err)
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return fmt.Errorf("%w: trailing artifact JSON", ErrInvalid)
	}
	return nil
}

// Prepare consumes an RTW claim already granted for the DC attempt. External
// reads and chunking happen outside database transactions. RecordChunks performs
// the final local fence/expiry/tombstone check before committing the reference.
func (p *Preparer) Prepare(ctx context.Context, input BuildInput, fence Fence) (Prepared, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Prepared{}, err
	}
	remote, err := p.source.GetBuild(ctx, input.BuildID)
	if err != nil {
		return Prepared{}, fmt.Errorf("read fixed build: %w", err)
	}
	if remote.BuildId != input.BuildID || remote.ReleaseId != input.ReleaseID || remote.ModuleId != input.ModuleID ||
		remote.Generation != input.Generation || remote.ManifestHash != input.InputHash || remote.State != "BUILDING" ||
		remote.AttemptId != fence.AttemptID || remote.LeaseEpoch != fence.LeaseEpoch || remote.CancelVersion != fence.CancelVersion {
		return Prepared{}, fmt.Errorf("%w: RTW build claim differs from fixed job", ErrConflict)
	}
	expires, err := time.Parse(time.RFC3339Nano, remote.LeaseExpiresAt)
	if err != nil || !expires.Equal(fence.ExpiresAt) {
		return Prepared{}, fmt.Errorf("%w: RTW and DC lease expiry differ", ErrConflict)
	}
	release, err := p.source.GetRelease(ctx, input.ReleaseID)
	if err != nil {
		return Prepared{}, fmt.Errorf("read fixed release: %w", err)
	}
	if release.ModuleId != input.ModuleID || release.ReleaseId != input.ReleaseID || release.ManifestHash != input.InputHash || release.ChunkingProfile != p.chunker.config.ID {
		return Prepared{}, fmt.Errorf("%w: release identity, hash or chunk profile differs", ErrInvalid)
	}
	raw, err := p.objects.Get(ctx, corpus.Ref{Key: release.ManifestRef, SHA256: release.ManifestHash})
	if err != nil {
		return Prepared{}, fmt.Errorf("read frozen release object: %w", err)
	}
	var manifest ReleaseManifest
	if err := decodeObject(raw, &manifest); err != nil {
		return Prepared{}, err
	}
	expected := ReleaseManifest{1, release.ModuleId, release.ReleaseId, release.SourceRevisionIds, release.WikiRevisionIds, release.ChunkingProfile, release.RetrievalProfiles}
	if !reflect.DeepEqual(manifest, expected) {
		return Prepared{}, fmt.Errorf("%w: release metadata differs from immutable object", ErrInvalid)
	}
	ids := append(append([]string(nil), manifest.SourceRevisionIDs...), manifest.WikiRevisionIDs...)
	expectedInput := input
	expectedInput.Revisions = ids
	expectedInput, err = normalizeInput(expectedInput)
	if err != nil || !reflect.DeepEqual(input.Revisions, expectedInput.Revisions) {
		return Prepared{}, fmt.Errorf("%w: required revision set differs", ErrInvalid)
	}
	if err := p.store.bindProfile(ctx, p.chunker.config); err != nil {
		return Prepared{}, err
	}
	if _, err := p.store.Claim(ctx, input, fence); err != nil {
		return Prepared{}, err
	}
	revisions, err := loadRevisions(ctx, p.source, manifest)
	if err != nil {
		return Prepared{}, err
	}
	chunks, err := p.chunker.Build(ctx, ChunkInput{ModuleID: input.ModuleID, ReleaseID: input.ReleaseID, InputManifestHash: input.InputHash, Revisions: revisions})
	if err != nil {
		return Prepared{}, err
	}
	data, err := json.Marshal(chunks)
	if err != nil {
		return Prepared{}, err
	}
	ref, err := p.objects.Put(ctx, data)
	if err != nil {
		return Prepared{}, fmt.Errorf("save fixed chunks: %w", err)
	}
	if err := p.store.recordChunks(ctx, fence, ref); err != nil {
		return Prepared{}, err
	}
	build, err := p.store.Get(ctx, input.BuildID)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Manifest: chunks, Ref: ref, Build: build}, nil
}

func loadRevisions(ctx context.Context, source RevisionSource, manifest ReleaseManifest) ([]corpus.Revision, error) {
	revisions := make([]corpus.Revision, 0, len(manifest.SourceRevisionIDs)+len(manifest.WikiRevisionIDs))
	for _, kind := range []string{"source", "wiki"} {
		kindIDs := manifest.SourceRevisionIDs
		if kind == "wiki" {
			kindIDs = manifest.WikiRevisionIDs
		}
		for _, id := range kindIDs {
			r, err := source.GetRevision(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("read fixed revision %s: %w", id, err)
			}
			if r.RevisionId != id || r.ModuleId != manifest.ModuleID || r.Kind != kind {
				return nil, fmt.Errorf("%w: revision identity differs", ErrInvalid)
			}
			revisions = append(revisions, corpus.Revision{RevisionID: r.RevisionId, ModuleID: r.ModuleId, EntityID: r.EntityId, Kind: r.Kind,
				Title: r.Title, MediaType: r.MediaType, Object: corpus.Ref{Key: r.ObjectKey, SHA256: r.ContentHash}, Content: r.Content})
		}
	}
	return revisions, nil
}
