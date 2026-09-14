package content

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

func putJSON(t *testing.T, objects artifacts.Store, value any) corpus.Ref {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := objects.Put(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// This fixture exercises real HTTP DTO consumption and PG state. Its content
// and lane verifier are synthetic; numeric indexing is deliberately not claimed.
func preparedFixture(t *testing.T) (*Store, *artifacts.Local, Prepared, Fence, []ridethewind.RetrievalProfile) {
	t.Helper()
	ctx := context.Background()
	store := testStore(t)
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profiles := []ridethewind.RetrievalProfile{{Lane: "dense", Encoder: "dense-model", Tokenizer: "dense-tokens", Space: "dense-space", Dimensions: 2},
		{Lane: "sparse", Encoder: "sparse-model", Tokenizer: "sparse-tokens", Space: "sparse-space", Dimensions: 100},
		{Lane: "multivector", Encoder: "multi-model", Tokenizer: "multi-tokens", Space: "multi-space", Dimensions: 2, Mask: "true-is-token", Aggregation: "maxsim"}}
	manifest := ReleaseManifest{SchemaVersion: 1, ModuleID: "module", ReleaseID: "release", SourceRevisionIDs: []string{"revision"}, WikiRevisionIDs: []string{}, ChunkingProfile: "paragraph-v1", RetrievalProfiles: profiles}
	ref := putJSON(t, objects, manifest)
	body := "First paragraph.\r\n\r\nSecond paragraph."
	bodyRef, err := objects.Put(ctx, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	fence := Fence{BuildID: "build", AttemptID: "attempt", LeaseEpoch: 1, ExpiresAt: time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)}
	build := ridethewind.Build{BuildId: "build", ModuleId: "module", ReleaseId: "release", Generation: 1, ManifestHash: ref.SHA256, State: "BUILDING", AttemptId: fence.AttemptID, LeaseEpoch: 1, LeaseExpiresAt: fence.ExpiresAt.Format(time.RFC3339Nano)}
	release := ridethewind.Release{ReleaseId: "release", ModuleId: "module", Ordinal: 1, SourceRevisionIds: manifest.SourceRevisionIDs, WikiRevisionIds: manifest.WikiRevisionIDs,
		ChunkingProfile: manifest.ChunkingProfile, RetrievalProfiles: profiles, ManifestHash: ref.SHA256, ManifestRef: ref.Key}
	revision := ridethewind.Revision{RevisionId: "revision", ModuleId: "module", EntityId: "book", Kind: "source", Title: "Book", MediaType: "text/plain", Content: body, ContentHash: bodyRef.SHA256, ObjectKey: bodyRef.Key}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch r.URL.Path {
		case "/internal/v1/knowledge/builds/build":
			data = build
		case "/internal/v1/knowledge/releases/release":
			data = release
		case "/internal/v1/knowledge/revisions/revision":
			data = revision
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
	}))
	t.Cleanup(server.Close)
	client, err := ridethewind.New(httpclient.Config{BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	chunker, err := NewChunker(ChunkConfig{ID: "paragraph-v1", Size: 16, Overlap: 2})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := NewPreparer(client, objects, store, chunker)
	if err != nil {
		t.Fatal(err)
	}
	input := BuildInput{BuildID: "build", ModuleID: "module", ReleaseID: "release", Generation: 1, InputHash: ref.SHA256, OperationID: "op", Revisions: []string{"revision"}}
	prepared, err := preparer.Prepare(ctx, input, fence)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := preparer.Prepare(ctx, input, fence)
	if err != nil || replay.Ref != prepared.Ref {
		t.Fatalf("prepare replay differs: %v", err)
	}
	if prepared.Build.State != "BUILDING" || prepared.Build.Result != nil {
		t.Fatal("chunking incorrectly marked ready")
	}
	return store, objects, prepared, fence, profiles
}

type fixtureLaneVerifier struct {
	fail  bool
	calls *int
}

func (v fixtureLaneVerifier) VerifyAndProbe(_ context.Context, _ corpus.LaneIndex, _ corpus.ChunkManifest, probes []corpus.Chunk) ([]corpus.ProbeResult, error) {
	if v.calls != nil {
		*v.calls++
	}
	if v.fail {
		return nil, errors.New("fixture independent query failed")
	}
	results := make([]corpus.ProbeResult, 0, len(probes))
	for _, p := range probes {
		results = append(results, corpus.ProbeResult{QueryChunkID: p.ID, CandidateIDs: []string{p.ID}})
	}
	return results, nil
}

func TestReconcileFixedCoverageAndIndependentProbe(t *testing.T) {
	for _, scenario := range []string{"valid", "missing_lane", "missing_chunk", "duplicate_chunk", "foreign_chunk", "wrong_space", "unreadable_shard", "probe_failed", "tombstone", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			store, objects, prepared, fence, profiles := preparedFixture(t)
			ctx := context.Background()
			verifiers := map[string]LaneVerifier{}
			calls := 0
			for _, profile := range profiles {
				lane := profile.Lane
				verifiers[lane] = fixtureLaneVerifier{fail: scenario == "probe_failed" && lane == "sparse", calls: &calls}
				if scenario == "missing_lane" && lane == "sparse" {
					continue
				}
				ids := make([]string, 0, len(prepared.Manifest.Chunks))
				for _, c := range prepared.Manifest.Chunks {
					ids = append(ids, c.ID)
				}
				shard := putJSON(t, objects, map[string]any{"fixture_lane": lane, "chunk_ids": ids, "numeric_validation": "performed by lane-owned verifier; this test is synthetic"})
				index := corpus.LaneIndex{SchemaVersion: 1, BuildID: fence.BuildID, Generation: 1, InputManifestHash: prepared.Build.InputHash, ChunkManifest: prepared.Ref, Profile: corpus.Profile(profile), Shards: []corpus.IndexShard{{Artifact: shard, ChunkIDs: ids}}}
				if lane == "sparse" {
					switch scenario {
					case "missing_chunk":
						index.Shards[0].ChunkIDs = ids[:len(ids)-1]
					case "duplicate_chunk":
						index.Shards[0].ChunkIDs = append(ids, ids[0])
					case "foreign_chunk":
						index.Shards[0].ChunkIDs[0] = "foreign"
					case "wrong_space":
						index.Profile.Space = "different"
					case "unreadable_shard":
						index.Shards[0].Artifact = artifacts.Reference([]byte("missing"))
					}
				}
				ref := putJSON(t, objects, index)
				if err := store.RecordLane(ctx, fence, lane, ref); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "tombstone" {
				if err := store.Tombstone(ctx, "module", "revision", 1, artifacts.Hash([]byte("withdraw"))); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "expired" {
				if _, err := store.db.Exec(ctx, "UPDATE content_builds SET lease_expires_at=clock_timestamp()-interval '1 second'"); err != nil {
					t.Fatal(err)
				}
			}
			reconciler, err := NewReconciler(objects, store, verifiers)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := reconciler.Ready(ctx, fence)
			if scenario != "valid" {
				if err == nil {
					t.Fatalf("%s accepted", scenario)
				}
				b, e := store.Get(ctx, "build")
				if e != nil || b.State != "BUILDING" || b.Result != nil {
					t.Fatalf("rejected result persisted: %+v %v", b, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := objects.Get(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			var manifest corpus.IndexManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if len(manifest.Lanes) != 3 || manifest.ChunkCount != int64(len(prepared.Manifest.Chunks)) {
				t.Fatalf("coverage: %+v", manifest)
			}
			if _, err := reconciler.Ready(ctx, fence); err != nil {
				t.Fatalf("ready replay: %v", err)
			}
			if calls != 3 {
				t.Fatalf("ready replay reran probes: %d calls", calls)
			}
		})
	}
}

func TestProfileDefinitionCannotChangeUnderSameID(t *testing.T) {
	store, _, _, _, _ := preparedFixture(t)
	err := store.bindProfile(context.Background(), ChunkConfig{ID: "paragraph-v1", Size: 512, Overlap: 0})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "new id") {
		t.Fatalf("mutated profile accepted: %v", err)
	}
}

func TestNoDefaultReadyVerifier(t *testing.T) {
	if _, err := NewReconciler(nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing verifier accepted")
	}
}
