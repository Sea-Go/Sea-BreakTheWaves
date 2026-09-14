package dense

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

type encoderFunc func(context.Context, representation.Request, representation.Contract, string) (representation.Response, error)

func (f encoderFunc) Represent(ctx context.Context, q representation.Request, c representation.Contract, model string) (representation.Response, error) {
	return f(ctx, q, c, model)
}
func fixtureConfig() Config {
	return Config{Document: Encoding{"dense-document", "11111111-1111-1111-1111-111111111111", "fixture-model"}, Query: Encoding{"dense-query", "22222222-2222-2222-2222-222222222222", "fixture-model"}, Contract: representation.Contract{ID: "fixture-dense-v1", Kind: representation.Dense, Dimensions: 2, TokenizerID: "fixture-tokenizer", Normalization: "none", Metric: "cosine", Aggregation: "none"}, Space: "fixture-space", BatchSize: 2, ProbeTopK: 4}
}
func fixtureProfile(c Config) corpus.Profile {
	return corpus.Profile{Lane: "dense", Encoder: c.Document.PhysicalModel, Tokenizer: c.Contract.TokenizerID, Space: c.Space, Dimensions: c.Contract.Dimensions}
}
func fixtureResponse(q representation.Request, c representation.Contract, model string) representation.Response {
	vectors := map[string][]float64{"east": {1, 0}, "northeast": {3, 4}, "north": {0, 1}, "west": {-1, 0}}
	response := representation.Response{Model: model, ConfigurationID: q.ConfigurationID, OutputContract: q.OutputContract, ContractID: q.ContractID, Space: q.Space, Role: q.Role, TokenizerID: c.TokenizerID, Data: []representation.Item{}, Usage: &representation.Usage{PromptTokens: int64(len(q.Input)), TotalTokens: int64(len(q.Input))}}
	// Reverse wire order deliberately: correlation must use IDs, not position.
	for i := len(q.Input) - 1; i >= 0; i-- {
		in := q.Input[i]
		response.Data = append(response.Data, representation.Item{ID: in.ID, Dense: &representation.DenseValues{Values: vectors[in.Text]}})
	}
	return response
}
func fixtureEncoder() Encoder {
	return encoderFunc(func(_ context.Context, q representation.Request, c representation.Contract, m string) (representation.Response, error) {
		return fixtureResponse(q, c, m), nil
	})
}
func fixtureManifest(t *testing.T, objects artifacts.Store) (corpus.ChunkManifest, corpus.Ref) {
	t.Helper()
	ctx := context.Background()
	m := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "module", ReleaseID: "release", InputManifestHash: artifacts.Hash([]byte("fixed-release")), Profile: "chunk-v1", ParserVersion: "text-v1", ChunkerVersion: "chunks-v1", ChunkSize: 100, Inputs: []corpus.Input{}, Chunks: []corpus.Chunk{}}
	for i, revision := range []string{"r1", "r2"} {
		ref, err := objects.Put(ctx, []byte("original "+revision))
		if err != nil {
			t.Fatal(err)
		}
		m.Inputs = append(m.Inputs, corpus.Input{RevisionID: revision, ContentID: revision, SourceKind: "wiki", Original: ref, ChunkCount: 2})
		texts := [][]string{{"east", "northeast"}, {"north", "west"}}[i]
		for j, text := range texts {
			id := string(rune('a' + i*2 + j))
			encoding, _ := json.Marshal([]any{m.Profile, text})
			m.Chunks = append(m.Chunks, corpus.Chunk{ID: id, RevisionID: revision, ContentID: revision, SourceKind: "wiki", Original: ref, Location: corpus.Location{Locator: "paragraph-1", OriginalByteStart: 0, OriginalByteEnd: 10, NormalizedRuneStart: 0, NormalizedRuneEnd: len(text)}, Text: text, TextHash: artifacts.Hash([]byte(text)), EncodingKey: artifacts.Hash(encoding), Required: true})
		}
	}
	ref, err := put(ctx, objects, m)
	if err != nil {
		t.Fatal(err)
	}
	return m, ref
}
func fixtureService(t *testing.T) (*Service, *artifacts.Local, corpus.ChunkManifest, BuildResult, string) {
	t.Helper()
	dir := t.TempDir()
	objects, err := artifacts.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, ref := fixtureManifest(t, objects)
	c := fixtureConfig()
	s, err := New(objects, fixtureEncoder(), c)
	if err != nil {
		t.Fatal(err)
	}
	built, err := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(c)})
	if err != nil {
		t.Fatal(err)
	}
	return s, objects, m, built, dir
}
func queryFor(b BuildResult) Query {
	return Query{IndexRef: b.Ref, ModuleID: "module", ReleaseID: "release", Generation: 1, ValidRevisionIDs: []string{"r1", "r2"}, Text: "east", TopK: 4}
}
func TestExactScoresScopesAndRestart(t *testing.T) {
	s, objects, m, b, dir := fixtureService(t)
	ctx := context.Background()
	if len(b.Index.Shards) != 2 || b.EncodedChunks != 4 || b.Usage.TotalTokens != 4 || b.StoredUsage.TotalTokens != 4 {
		t.Fatal(b)
	}
	q := queryFor(b)
	got, err := s.Search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{1, .6, 0, -1} {
		c := got.Candidates[i]
		if c.Chunk.ID != string(rune('a'+i)) || math.Abs(c.Score-want) > 1e-12 || c.Rank != i+1 || c.Chunk.Original != m.Chunks[i].Original || c.Generation != 1 {
			t.Fatal(got)
		}
	}
	q.ValidRevisionIDs = []string{"r2"}
	q.TopK = 1
	got, err = s.Search(ctx, q)
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0].Chunk.ID != "c" {
		t.Fatal(got, err)
	}
	q.ValidRevisionIDs = nil
	got, err = s.Search(ctx, q)
	if err != nil || len(got.Candidates) != 0 || got.Usage.TotalTokens != 0 {
		t.Fatal(got, err)
	}
	q = queryFor(b)
	q.ModuleID = "foreign"
	if _, err = s.Search(ctx, q); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	q = queryFor(b)
	q.ReleaseID = "old"
	if _, err = s.Search(ctx, q); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	q = queryFor(b)
	q.Generation = 2
	if _, err = s.Search(ctx, q); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	q = queryFor(b)
	q.ValidRevisionIDs = []string{"r1", "r1"}
	if _, err = s.Search(ctx, q); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	reopened, _ := artifacts.NewLocal(dir)
	s2, _ := New(reopened, fixtureEncoder(), fixtureConfig())
	if _, err = s2.Search(ctx, queryFor(b)); err != nil {
		t.Fatal(err)
	}
	resume, err := s2.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: b.Index.ChunkManifest, Profile: b.Index.Profile, ResumeIndex: &b.Ref})
	if err != nil || !resume.Reused || resume.Ref != b.Ref || resume.Usage.TotalTokens != 0 ||
		resume.StoredUsage.TotalTokens != 4 || resume.EncodedChunks != 0 || resume.ReusedChunks != 4 {
		t.Fatal(resume, err)
	}
	if _, err = s2.Build(ctx, BuildRequest{BuildID: "build", Generation: 2, ChunkManifest: b.Index.ChunkManifest, Profile: b.Index.Profile, ResumeIndex: &b.Ref}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	_ = objects
}

func TestFailedSecondEncodingPreservesKnownCostAndFlagsUnknownUse(t *testing.T) {
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, ref := fixtureManifest(t, objects)
	config := fixtureConfig()
	var calls int
	encoder := encoderFunc(func(_ context.Context, q representation.Request, c representation.Contract, model string) (representation.Response, error) {
		calls++
		if calls == 2 {
			return representation.Response{}, errors.New("model outcome unknown after request")
		}
		return fixtureResponse(q, c, model), nil
	})
	service, err := New(objects, encoder, config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1,
		ChunkManifest: ref, Profile: fixtureProfile(config)})
	if err == nil || result.Usage.TotalTokens != 2 || result.EncodedChunks != 2 || !result.UsageUnknown ||
		result.StoredUsage.TotalTokens != 0 || result.ReusedChunks != 0 {
		t.Fatalf("failed request erased or duplicated model use: %+v err=%v", result, err)
	}
}
func TestTypedHTTPEncodingAndQuerySpaceMismatch(t *testing.T) {
	cfg := fixtureConfig()
	var bad atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/representations" || r.Method != "POST" {
			t.Error("wrong HTTP boundary")
		}
		var q representation.Request
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
			return
		}
		want := cfg.Document
		if q.Role == "query" {
			want = cfg.Query
		}
		if q.ConfigurationID != want.ConfigurationID || q.Model != want.Callpoint || q.Space != cfg.Space {
			t.Error("fixed config changed")
		}
		calls.Add(1)
		response := fixtureResponse(q, cfg.Contract, want.PhysicalModel)
		if bad.Load() {
			response.Space = "foreign-space"
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := datacenter.New(httpclient.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	objects, _ := artifacts.NewLocal(t.TempDir())
	_, ref := fixtureManifest(t, objects)
	s, _ := New(objects, client, cfg)
	b, err := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Search(context.Background(), queryFor(b)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
	bad.Store(true)
	if _, err = s.Search(context.Background(), queryFor(b)); err == nil {
		t.Fatal("accepted mismatched query space")
	}
}
func TestNumericContracts(t *testing.T) {
	for _, test := range []struct {
		name          string
		vector        []float64
		normalization string
	}{{"empty", nil, "none"}, {"zero", []float64{0, 0}, "none"}, {"dimension", []float64{1}, "none"}, {"nan", []float64{math.NaN(), 0}, "none"}, {"infinity", []float64{math.Inf(1), 0}, "none"}, {"overflow", []float64{math.MaxFloat64, 1}, "none"}, {"l2", []float64{1, 1}, "l2"}} {
		t.Run(test.name, func(t *testing.T) {
			cfg := fixtureConfig()
			cfg.Contract.Normalization = test.normalization
			encoder := encoderFunc(func(_ context.Context, q representation.Request, c representation.Contract, m string) (representation.Response, error) {
				r := fixtureResponse(q, c, m)
				r.Data[0].Dense.Values = test.vector
				return r, nil
			})
			objects, _ := artifacts.NewLocal(t.TempDir())
			_, ref := fixtureManifest(t, objects)
			s, _ := New(objects, encoder, cfg)
			if _, err := s.Build(context.Background(), BuildRequest{BuildID: "b", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(cfg)}); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
	if got, err := similarity([]float64{1, 0}, []float64{3, 4}, "dot"); err != nil || got != 3 {
		t.Fatal(got, err)
	}
	if got, err := similarity([]float64{1, 0}, []float64{3, 4}, "cosine"); err != nil || math.Abs(got-.6) > 1e-15 {
		t.Fatal(got, err)
	}
}
func TestCorruptArtifactsAndBindings(t *testing.T) {
	for _, scenario := range []string{"vector_hash", "wrong_dimension", "missing_row", "foreign_revision", "other_config", "shard_generation", "unknown_json", "missing_shard", "changed_file", "missing_input", "text_hash"} {
		t.Run(scenario, func(t *testing.T) {
			s, objects, m, b, dir := fixtureService(t)
			ctx := context.Background()
			index := b.Index
			if scenario == "missing_shard" {
				index.Shards = index.Shards[:1]
			} else if scenario == "changed_file" {
				if err := os.WriteFile(filepath.Join(dir, index.Shards[0].Artifact.Key), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "missing_input" || scenario == "text_hash" {
				if scenario == "missing_input" {
					m.Inputs = m.Inputs[:1]
				} else {
					m.Chunks[0].Text = "changed"
				}
				ref, err := put(ctx, objects, m)
				if err != nil {
					t.Fatal(err)
				}
				index.ChunkManifest = ref
			} else {
				part, err := load[shard](ctx, objects, index.Shards[0].Artifact)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "vector_hash":
					part.Rows[0].VectorHash = artifacts.Hash([]byte("wrong"))
				case "wrong_dimension":
					part.Rows[0].Vector = []float64{1}
					part.Rows[0].VectorHash = vectorHash(part.Rows[0].Vector)
				case "missing_row":
					part.Rows = part.Rows[:1]
				case "foreign_revision":
					part.Rows[0].Chunk.RevisionID = "foreign"
				case "other_config":
					part.Config.Query.ConfigurationID = "33333333-3333-3333-3333-333333333333"
				case "shard_generation":
					part.Generation++
				case "unknown_json":
				}
				ref, err := put(ctx, objects, part)
				if scenario == "unknown_json" {
					raw, _ := json.Marshal(part)
					var value map[string]any
					_ = json.Unmarshal(raw, &value)
					value["unexpected"] = true
					ref, err = put(ctx, objects, value)
				}
				if err != nil {
					t.Fatal(err)
				}
				index.Shards[0].Artifact = ref
			}
			ref, err := put(ctx, objects, index)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Open(ctx, ref); err == nil {
				t.Fatal("accepted corrupt index")
			}
		})
	}
}
func TestCancelAtEncodingBoundaryAndBeforeQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	encoder := encoderFunc(func(_ context.Context, q representation.Request, c representation.Contract, m string) (representation.Response, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return fixtureResponse(q, c, m), nil
	})
	objects, _ := artifacts.NewLocal(t.TempDir())
	_, ref := fixtureManifest(t, objects)
	cfg := fixtureConfig()
	s, _ := New(objects, encoder, cfg)
	result, err := s.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(cfg)})
	if !errors.Is(err, context.Canceled) || result.Ref.Key != "" || calls != 2 {
		t.Fatal(result, err, calls)
	}
	s2, _, _, b, _ := fixtureService(t)
	if _, err = s2.Search(ctx, queryFor(b)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestLaneVerifierRealReadsAndIndependentProbe(t *testing.T) {
	s, _, m, b, _ := fixtureService(t)
	results, err := s.VerifyAndProbe(context.Background(), b.Index, m, []corpus.Chunk{m.Chunks[0], m.Chunks[2]})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].CandidateIDs[0] != "a" || results[1].CandidateIDs[0] != "c" {
		t.Fatal(results)
	}
	changed := m
	changed.ReleaseID = "foreign"
	if _, err = s.VerifyAndProbe(context.Background(), b.Index, changed, m.Chunks[:1]); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestSnapshotConcurrentQueries(t *testing.T) {
	s, _, _, b, _ := fixtureService(t)
	snapshot, err := s.Open(context.Background(), b.Ref)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := queryFor(b)
			q.TopK = 1
			result, err := snapshot.Search(context.Background(), q)
			if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Chunk.ID != "a" {
				t.Error(result, err)
			}
		}()
	}
	wg.Wait()
}
func TestProfileAndConfigurationArePinned(t *testing.T) {
	s, _, _, b, _ := fixtureService(t)
	for _, mutate := range []func(*corpus.Profile){func(p *corpus.Profile) { p.Space = "other" }, func(p *corpus.Profile) { p.Encoder = "other" }, func(p *corpus.Profile) { p.Tokenizer = "other" }, func(p *corpus.Profile) { p.Dimensions = 3 }} {
		profile := b.Index.Profile
		mutate(&profile)
		if _, err := s.Build(context.Background(), BuildRequest{BuildID: "b", Generation: 1, ChunkManifest: b.Index.ChunkManifest, Profile: profile}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	cfg := fixtureConfig()
	cfg.Query.ConfigurationID = "33333333-3333-3333-3333-333333333333"
	other, _ := New(s.objects, fixtureEncoder(), cfg)
	if _, err := other.Open(context.Background(), b.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	// Returned index slices cannot mutate a separately reopened snapshot.
	snap, err := s.Open(context.Background(), b.Ref)
	if err != nil {
		t.Fatal(err)
	}
	b.Index.Shards[0].ChunkIDs[0] = "changed"
	if reflect.DeepEqual(snap.index, b.Index) {
		t.Fatal("snapshot shares external slices")
	}
}
