package content

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

// LaneVerifier is implemented by each retrieval lane. It validates actual shard
// representations (shape, model/space, IDs and numeric values), then queries its
// independent index with the supplied chunks. No default always-pass verifier
// exists. A transport ACK or a producer's probe_passed boolean is insufficient.
type LaneVerifier interface {
	VerifyAndProbe(context.Context, corpus.LaneIndex, corpus.ChunkManifest, []corpus.Chunk) ([]corpus.ProbeResult, error)
}

type Reconciler struct {
	objects   artifacts.Store
	store     *Store
	verifiers map[string]LaneVerifier
}

func NewReconciler(objects artifacts.Store, store *Store, verifiers map[string]LaneVerifier) (*Reconciler, error) {
	if objects == nil || store == nil {
		return nil, ErrInvalid
	}
	copy := map[string]LaneVerifier{}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		if verifiers[lane] == nil {
			return nil, fmt.Errorf("%w: %s verifier required", ErrInvalid, lane)
		}
		copy[lane] = verifiers[lane]
	}
	return &Reconciler{objects: objects, store: store, verifiers: copy}, nil
}

func (r *Reconciler) load(ctx context.Context, ref corpus.Ref, value any) error {
	raw, err := r.objects.Get(ctx, ref)
	if err != nil {
		return err
	}
	return decodeObject(raw, value)
}

func (r *Reconciler) Ready(ctx context.Context, fence Fence) (corpus.Ref, error) {
	b, err := r.store.Get(ctx, fence.BuildID)
	if err != nil {
		return corpus.Ref{}, err
	}
	if b.AttemptID != fence.AttemptID || b.LeaseEpoch != fence.LeaseEpoch || b.CancelVersion != fence.CancelVersion {
		return corpus.Ref{}, ErrConflict
	}
	if b.State == "READY" && b.Result != nil {
		if _, err := r.objects.Get(ctx, *b.Result); err != nil {
			return corpus.Ref{}, err
		}
		// Reuse the accepted artifact. Re-running approximate probes could change
		// ordering or consume model capacity while handling a lost receipt.
		if err := r.store.commitReady(ctx, fence, *b.Result); err != nil {
			return corpus.Ref{}, err
		}
		return *b.Result, nil
	}
	if b.State != "BUILDING" {
		return corpus.Ref{}, ErrConflict
	}
	if b.Chunks == nil || len(b.Lanes) != 3 {
		return corpus.Ref{}, fmt.Errorf("%w: missing chunk manifest or lane", ErrConflict)
	}
	// Profiles are read from the exact RTW input object, never supplied anew by
	// a worker at result submission.
	var release ReleaseManifest
	if err := r.load(ctx, corpus.Ref{Key: "sha256/" + b.InputHash, SHA256: b.InputHash}, &release); err != nil {
		return corpus.Ref{}, err
	}
	if release.SchemaVersion != 1 || release.ModuleID != b.ModuleID || release.ReleaseID != b.ReleaseID || len(release.RetrievalProfiles) != 3 {
		return corpus.Ref{}, ErrInvalid
	}
	var chunks corpus.ChunkManifest
	if err := r.load(ctx, *b.Chunks, &chunks); err != nil {
		return corpus.Ref{}, err
	}
	if chunks.SchemaVersion != 1 || chunks.ModuleID != b.ModuleID || chunks.ReleaseID != b.ReleaseID || chunks.InputManifestHash != b.InputHash ||
		chunks.Profile != release.ChunkingProfile || len(chunks.Chunks) == 0 {
		return corpus.Ref{}, fmt.Errorf("%w: chunk identity differs", ErrInvalid)
	}
	var definition string
	if err := r.store.db.QueryRow(ctx, "SELECT definition_hash FROM content_chunk_profiles WHERE profile_id=$1", chunks.Profile).Scan(&definition); err != nil {
		return corpus.Ref{}, err
	}
	if definition != profileDefinition(chunks.Profile, chunks.ChunkSize, chunks.Overlap, chunks.ParserVersion, chunks.ChunkerVersion) {
		return corpus.Ref{}, fmt.Errorf("%w: chunk profile definition differs", ErrInvalid)
	}
	inputs := map[string]corpus.Input{}
	for _, input := range chunks.Inputs {
		if _, exists := inputs[input.RevisionID]; exists {
			return corpus.Ref{}, ErrInvalid
		}
		inputs[input.RevisionID] = input
	}
	ids := make(map[string]corpus.Chunk, len(chunks.Chunks))
	for _, chunk := range chunks.Chunks {
		if !chunk.Required || chunk.ID == "" || chunk.TextHash != artifacts.Hash([]byte(chunk.Text)) || chunk.Text == "" {
			return corpus.Ref{}, ErrInvalid
		}
		input, exists := inputs[chunk.RevisionID]
		if !exists || chunk.Original != input.Original || chunk.ContentID != input.ContentID || chunk.SourceKind != input.SourceKind || !validRef(chunk.Original) {
			return corpus.Ref{}, ErrInvalid
		}
		encoding, _ := json.Marshal([]any{chunks.Profile, chunk.Text})
		if chunk.EncodingKey != artifacts.Hash(encoding) {
			return corpus.Ref{}, fmt.Errorf("%w: encoding text binding differs", ErrInvalid)
		}
		if _, exists := ids[chunk.ID]; exists {
			return corpus.Ref{}, fmt.Errorf("%w: duplicate logical chunk", ErrInvalid)
		}
		ids[chunk.ID] = chunk
	}
	counts := map[string]int{}
	for _, chunk := range chunks.Chunks {
		counts[chunk.RevisionID]++
	}
	if len(chunks.Inputs) != len(b.Revisions) {
		return corpus.Ref{}, fmt.Errorf("%w: missing required input", ErrInvalid)
	}
	inputIDs := make([]string, 0, len(chunks.Inputs))
	for _, input := range chunks.Inputs {
		if counts[input.RevisionID] != input.ChunkCount || input.ChunkCount == 0 {
			return corpus.Ref{}, ErrInvalid
		}
		inputIDs = append(inputIDs, input.RevisionID)
	}
	sort.Strings(inputIDs)
	if !reflect.DeepEqual(inputIDs, b.Revisions) {
		return corpus.Ref{}, fmt.Errorf("%w: revision coverage differs", ErrInvalid)
	}
	profileMap := map[string]corpus.Profile{}
	for _, p := range release.RetrievalProfiles {
		if _, exists := profileMap[p.Lane]; exists || r.verifiers[p.Lane] == nil {
			return corpus.Ref{}, ErrInvalid
		}
		profileMap[p.Lane] = corpus.Profile(p)
	}
	manifest := corpus.IndexManifest{SchemaVersion: 1, BuildID: fence.BuildID, ReleaseID: b.ReleaseID, Generation: b.Generation,
		InputManifestHash: b.InputHash, ChunkManifest: *b.Chunks, ChunkCount: int64(len(chunks.Chunks)), Lanes: []corpus.LaneManifest{}}
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		ref := b.Lanes[lane]
		var index corpus.LaneIndex
		if err := r.load(ctx, ref, &index); err != nil {
			return corpus.Ref{}, fmt.Errorf("read %s index: %w", lane, err)
		}
		if index.SchemaVersion != 1 || index.BuildID != fence.BuildID || index.Generation != b.Generation || index.InputManifestHash != b.InputHash ||
			index.ChunkManifest != *b.Chunks || index.Profile != profileMap[lane] || len(index.Shards) == 0 {
			return corpus.Ref{}, fmt.Errorf("%w: %s index binding differs", ErrInvalid, lane)
		}
		covered := map[string]bool{}
		seenShards := map[string]bool{}
		for _, shard := range index.Shards {
			if len(shard.ChunkIDs) == 0 || seenShards[shard.Artifact.SHA256] {
				return corpus.Ref{}, fmt.Errorf("%w: empty/duplicate %s shard", ErrInvalid, lane)
			}
			seenShards[shard.Artifact.SHA256] = true
			if _, err := r.objects.Get(ctx, shard.Artifact); err != nil {
				return corpus.Ref{}, fmt.Errorf("read %s shard: %w", lane, err)
			}
			for _, id := range shard.ChunkIDs {
				if _, exists := ids[id]; !exists || covered[id] {
					return corpus.Ref{}, fmt.Errorf("%w: %s foreign/duplicate chunk", ErrInvalid, lane)
				}
				covered[id] = true
			}
		}
		if len(covered) != len(ids) {
			return corpus.Ref{}, fmt.Errorf("%w: %s missing required chunks", ErrInvalid, lane)
		}
		// Deterministic first/middle/last probes are a readiness check. Exact
		// numeric recall metrics and large-scale performance are separate gates.
		probes := probeChunks(chunks.Chunks)
		results, err := r.verifiers[lane].VerifyAndProbe(ctx, index, chunks, probes)
		if err != nil {
			return corpus.Ref{}, fmt.Errorf("verify %s: %w", lane, err)
		}
		if err := validateProbes(probes, results, ids); err != nil {
			return corpus.Ref{}, fmt.Errorf("%s probe: %w", lane, err)
		}
		proof := corpus.LaneProbe{SchemaVersion: 1, Lane: lane, Index: ref, ChunkManifest: *b.Chunks, Results: results}
		proofBytes, err := json.Marshal(proof)
		if err != nil {
			return corpus.Ref{}, err
		}
		proofRef, err := r.objects.Put(ctx, proofBytes)
		if err != nil {
			return corpus.Ref{}, err
		}
		manifest.Lanes = append(manifest.Lanes, corpus.LaneManifest{Profile: index.Profile, Artifact: ref, ChunkCount: int64(len(covered)), Shards: len(index.Shards), ProbePassed: true, Probe: proofRef})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return corpus.Ref{}, err
	}
	ref, err := r.objects.Put(ctx, raw)
	if err != nil {
		return corpus.Ref{}, err
	}
	if err := r.store.commitReady(ctx, fence, ref); err != nil {
		return corpus.Ref{}, err
	}
	return ref, nil
}

func probeChunks(chunks []corpus.Chunk) []corpus.Chunk {
	ordered := append([]corpus.Chunk(nil), chunks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	var result []corpus.Chunk
	seen := map[string]bool{}
	for _, i := range []int{0, len(ordered) / 2, len(ordered) - 1} {
		if !seen[ordered[i].ID] {
			seen[ordered[i].ID] = true
			result = append(result, ordered[i])
		}
	}
	return result
}

func validateProbes(probes []corpus.Chunk, results []corpus.ProbeResult, chunks map[string]corpus.Chunk) error {
	if len(probes) != len(results) {
		return fmt.Errorf("%w: missing query results", ErrInvalid)
	}
	for i, p := range probes {
		result := results[i]
		if result.QueryChunkID != p.ID || len(result.CandidateIDs) == 0 || len(result.CandidateIDs) > 64 {
			return ErrInvalid
		}
		matched := false
		seen := map[string]bool{}
		for _, id := range result.CandidateIDs {
			candidate, exists := chunks[id]
			if !exists || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			if id == p.ID || candidate.EncodingKey == p.EncodingKey {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%w: query did not recover equivalent source text", ErrInvalid)
		}
	}
	return nil
}
