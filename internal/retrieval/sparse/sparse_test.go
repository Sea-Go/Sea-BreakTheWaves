package sparse

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

func fixtureConfig() Config {
	return Config{Document: Encoding{"docs", "00000000-0000-4000-8000-000000000001", "fixture-model"}, Query: Encoding{"queries", "00000000-0000-4000-8000-000000000002", "fixture-model"}, Contract: representation.Contract{ID: "fixture-sparse-v1", Kind: representation.Sparse, Dimensions: 1000, TokenizerID: "fixture-tokenizer-v1", VocabularyID: "fixture-vocab-v1", Normalization: "none", Metric: "dot", Aggregation: "max", MaxNonzero: 8}, Space: "fixture-sparse-space-v1", BatchSize: 2, ProbeTopK: 4}
}
func profile(c Config) corpus.Profile {
	return corpus.Profile{Lane: "sparse", Encoder: c.Document.PhysicalModel, Tokenizer: c.Contract.TokenizerID, Space: c.Space, Dimensions: c.Contract.Dimensions}
}

type fixture struct {
	calls     atomic.Int32
	malformed bool
}

func (f *fixture) Represent(ctx context.Context, q representation.Request, c representation.Contract, model string) (representation.Response, error) {
	if e := ctx.Err(); e != nil {
		return representation.Response{}, e
	}
	f.calls.Add(1)
	v := representation.Response{Model: model, ConfigurationID: q.ConfigurationID, OutputContract: q.OutputContract, ContractID: q.ContractID, Space: q.Space, Role: q.Role, TokenizerID: c.TokenizerID, VocabularyID: c.VocabularyID, Usage: &representation.Usage{PromptTokens: int64(len(q.Input)), TotalTokens: int64(len(q.Input))}}
	for i := len(q.Input) - 1; i >= 0; i-- {
		in := q.Input[i]
		vectors := map[string]representation.SparseValues{"a": {Indices: []int{1, 4}, Weights: []float64{2, 3}}, "b": {Indices: []int{1}, Weights: []float64{4}}, "c": {Indices: []int{8}, Weights: []float64{9}}, "d": {Indices: []int{4}, Weights: []float64{1}}, "query": {Indices: []int{1, 4}, Weights: []float64{1, 2}}, "scaled": {Indices: []int{1, 4}, Weights: []float64{2, 4}}, "nooverlap": {Indices: []int{999}, Weights: []float64{1}}}
		s, ok := vectors[in.Text]
		if !ok {
			return v, ErrInvalid
		}
		if f.malformed {
			s.Weights[0] = -1
		}
		v.Data = append(v.Data, representation.Item{ID: in.ID, Sparse: &s})
	}
	return v, nil
}
func fixtureManifest(t *testing.T, objects artifacts.Store) (corpus.ChunkManifest, corpus.Ref) {
	t.Helper()
	m := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "module", ReleaseID: "release", InputManifestHash: artifacts.Hash([]byte("input")), Profile: "chunk-v1", ParserVersion: "parser-v1", ChunkerVersion: "chunker-v1", ChunkSize: 64, Overlap: 0}
	for _, id := range []string{"a", "b", "c", "d"} {
		original := artifacts.Reference([]byte(id))
		e, _ := json.Marshal([]any{m.Profile, id})
		r := "r" + id
		m.Inputs = append(m.Inputs, corpus.Input{RevisionID: r, ContentID: id, SourceKind: "source", Original: original, ChunkCount: 1})
		m.Chunks = append(m.Chunks, corpus.Chunk{ID: id, RevisionID: r, ContentID: id, SourceKind: "source", Original: original, Location: corpus.Location{Locator: "paragraph:1", OriginalByteEnd: 1, NormalizedRuneEnd: 1}, Text: id, TextHash: artifacts.Hash([]byte(id)), EncodingKey: artifacts.Hash(e), Required: true})
	}
	ref, e := put(context.Background(), objects, m)
	if e != nil {
		t.Fatal(e)
	}
	return m, ref
}
func query(ref corpus.Ref) Query {
	return Query{IndexRef: ref, ModuleID: "module", ReleaseID: "release", Generation: 1, ValidRevisionIDs: []string{"ra", "rb", "rc", "rd"}, Text: "query", TopK: 4}
}
func setup(t *testing.T) (*Service, *fixture, artifacts.Store, corpus.ChunkManifest, BuildResult) {
	t.Helper()
	objects, e := artifacts.NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	m, ref := fixtureManifest(t, objects)
	encoder := &fixture{}
	cfg := fixtureConfig()
	s, e := New(objects, encoder, cfg)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if e != nil {
		t.Fatal(e)
	}
	return s, encoder, objects, m, b
}
func TestInvertedReferenceValidityAndCost(t *testing.T) {
	s, encoder, _, m, b := setup(t)
	if encoder.calls.Load() != 2 || b.Usage.TotalTokens != 4 || b.EncodedChunks != 4 {
		t.Fatal(b)
	}
	result, e := s.Search(context.Background(), query(b.Ref))
	if e != nil {
		t.Fatal(e)
	}
	if result.Usage.TotalTokens != 1 || result.ExaminedPostings == nil || *result.ExaminedPostings != 4 {
		t.Fatal(result)
	}
	for i, want := range []struct {
		id    string
		score float64
	}{{"a", 8}, {"b", 4}, {"d", 2}} {
		if len(result.Candidates) != 3 || result.Candidates[i].Chunk.ID != want.id || result.Candidates[i].Score != want.score {
			t.Fatal(result)
		}
	}
	q := query(b.Ref)
	q.ValidRevisionIDs = []string{"rb"}
	got, e := s.Search(context.Background(), q)
	if e != nil || len(got.Candidates) != 1 || got.Candidates[0].Chunk.ID != "b" {
		t.Fatal(got, e)
	}
	before := encoder.calls.Load()
	q.ValidRevisionIDs = nil
	got, e = s.Search(context.Background(), q)
	if e != nil || len(got.Candidates) != 0 || encoder.calls.Load() != before {
		t.Fatal("empty validity made encoder call", got, e)
	}
	q = query(b.Ref)
	q.Text = "nooverlap"
	got, e = s.Search(context.Background(), q)
	if e != nil || len(got.Candidates) != 0 {
		t.Fatal(got, e)
	}
	q = query(b.Ref)
	q.Text = "scaled"
	got, e = s.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	for i, c := range got.Candidates {
		if c.Score != 2*result.Candidates[i].Score {
			t.Fatal("not linear IP")
		}
	}
	probes, e := s.VerifyAndProbe(context.Background(), b.Index, m, m.Chunks)
	if e != nil || len(probes) != 4 {
		t.Fatal(probes, e)
	}
	before = encoder.calls.Load()
	resumed, e := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: b.Index.ChunkManifest, Profile: profile(fixtureConfig()), ResumeIndex: &b.Ref})
	if e != nil || !resumed.Reused || resumed.EncodedChunks != 0 || resumed.ReusedChunks != 4 || resumed.Usage.TotalTokens != 0 || resumed.StoredUsage.TotalTokens != 4 || encoder.calls.Load() != before {
		t.Fatal(resumed, e)
	}
}
func TestCorruptShardAndBindingRejected(t *testing.T) {
	s, _, objects, m, b := setup(t)
	part, e := load[shard](context.Background(), objects, b.Index.Shards[0].Artifact)
	if e != nil {
		t.Fatal(e)
	}
	part.Postings[0].Entries[0].Weight += 1
	bad, e := put(context.Background(), objects, part)
	if e != nil {
		t.Fatal(e)
	}
	idx := b.Index
	idx.Shards = append([]corpus.IndexShard(nil), idx.Shards...)
	idx.Shards[0].Artifact = bad
	ref, e := put(context.Background(), objects, idx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Open(context.Background(), ref); e == nil {
		t.Fatal("corrupt postings accepted")
	}
	cfg := fixtureConfig()
	cfg.Contract.VocabularyID = "another-vocabulary"
	other, e := New(objects, &fixture{}, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Open(context.Background(), b.Ref); e == nil {
		t.Fatal("vocabulary drift accepted")
	}
	idx = b.Index
	idx.Shards = idx.Shards[:1]
	ref, _ = put(context.Background(), objects, idx)
	if _, e = s.Open(context.Background(), ref); e == nil {
		t.Fatal("coverage loss accepted")
	}
	mutated := m
	mutated.Chunks = append([]corpus.Chunk(nil), m.Chunks...)
	mutated.Chunks[0].Location.Locator = "changed"
	if _, e = s.VerifyAndProbe(context.Background(), b.Index, mutated, m.Chunks); e == nil {
		t.Fatal("manifest drift accepted")
	}
}
func TestInvalidVectorsContextAndScope(t *testing.T) {
	cfg := fixtureConfig()
	for _, v := range []representation.SparseValues{{}, {Indices: []int{1, 1}, Weights: []float64{1, 2}}, {Indices: []int{1}, Weights: []float64{-1}}, {Indices: []int{1}, Weights: []float64{math.NaN()}}, {Indices: []int{1000}, Weights: []float64{1}}} {
		if e := validateVector(v, cfg.Contract); e == nil {
			t.Fatal("bad sparse accepted", v)
		}
	}
	s, encoder, _, _, b := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Search(ctx, query(b.Ref)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	for _, field := range []string{"module", "release", "generation", "index"} {
		q := query(b.Ref)
		switch field {
		case "module":
			q.ModuleID = "other"
		case "release":
			q.ReleaseID = "other"
		case "generation":
			q.Generation = 2
		case "index":
			q.IndexRef = artifacts.Reference([]byte("other"))
		}
		snap, e := s.Open(context.Background(), b.Ref)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = snap.Search(context.Background(), q); e == nil {
			t.Fatal("scope drift accepted", field)
		}
	}
	encoder.malformed = true
	if _, e := s.Search(context.Background(), query(b.Ref)); e == nil {
		t.Fatal("negative learned weight accepted")
	}
}
func TestDotIndependentReference(t *testing.T) {
	a := representation.SparseValues{Indices: []int{1, 4, 999}, Weights: []float64{2, 3, 4}}
	b := representation.SparseValues{Indices: []int{2, 4, 999}, Weights: []float64{9, 5, 6}}
	got, e := Dot(a, b)
	if e != nil || got != 39 {
		t.Fatal(got, e)
	}
	original := append([]float64(nil), a.Weights...)
	_, _ = Dot(a, b)
	if !reflect.DeepEqual(a.Weights, original) {
		t.Fatal("input mutated")
	}
}

type failsSecondBatch struct{ fixture }

func (f *failsSecondBatch) Represent(ctx context.Context, q representation.Request, c representation.Contract, model string) (representation.Response, error) {
	if f.calls.Load() == 1 {
		return representation.Response{}, errors.New("fixture second batch unavailable")
	}
	return f.fixture.Represent(ctx, q, c, model)
}
func TestFailedBuildPreservesKnownUsage(t *testing.T) {
	objects, e := artifacts.NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	_, ref := fixtureManifest(t, objects)
	cfg := fixtureConfig()
	encoder := &failsSecondBatch{}
	service, e := New(objects, encoder, cfg)
	if e != nil {
		t.Fatal(e)
	}
	result, e := service.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if e == nil || result.Usage.TotalTokens != 2 || result.EncodedChunks != 2 || !result.UsageUnknown || result.Ref != (corpus.Ref{}) {
		t.Fatalf("failed build lost known spend or published partial index: %+v %v", result, e)
	}
}
