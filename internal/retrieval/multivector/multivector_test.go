package multivector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func fixtureConfig(aggregation string) Config {
	return Config{Document: Encoding{"documents", "11111111-1111-4111-8111-111111111111", "fixture-model"}, Query: Encoding{"queries", "22222222-2222-4222-8222-222222222222", "fixture-model"}, Contract: representation.Contract{ID: "fixture-token-matrix-v1", Kind: representation.TokenMatrix, Dimensions: 2, TokenizerID: "fixture-tokenizer-v1", Normalization: "none", Metric: "maxsim", Aggregation: aggregation, MaxTokens: 8}, Space: "fixture-matrix-space-v1", BatchSize: 2, TokenTopK: 8, ProbeTopK: 2}
}
func profile(c Config) corpus.Profile {
	return corpus.Profile{Lane: "multivector", Encoder: c.Document.PhysicalModel, Tokenizer: c.Contract.TokenizerID, Space: c.Space, Dimensions: c.Contract.Dimensions, Mask: "valid", Aggregation: c.Contract.Aggregation}
}

type fixtureEncoder struct {
	calls   atomic.Int32
	invalid bool
}

func (f *fixtureEncoder) Represent(ctx context.Context, q representation.Request, c representation.Contract, model string) (representation.Response, error) {
	if err := ctx.Err(); err != nil {
		return representation.Response{}, err
	}
	f.calls.Add(1)
	m := map[string]representation.TokenValues{
		"a":       {Shape: []int{3, 2}, Values: [][]float64{{1, 0}, {0, 1}, {0, 0}}, Mask: []bool{true, true, false}},
		"b":       {Shape: []int{2, 2}, Values: [][]float64{{.9, .1}, {.1, .9}}, Mask: []bool{true, true}},
		"c":       {Shape: []int{2, 2}, Values: [][]float64{{-1, 0}, {0, -1}}, Mask: []bool{true, true}},
		"d":       {Shape: []int{1, 2}, Values: [][]float64{{.8, .2}}, Mask: []bool{true}},
		"query":   {Shape: []int{3, 2}, Values: [][]float64{{1, 0}, {0, 1}, {0, 0}}, Mask: []bool{true, true, false}},
		"query_d": {Shape: []int{1, 2}, Values: [][]float64{{.8, .2}}, Mask: []bool{true}},
	}
	r := representation.Response{Model: model, ConfigurationID: q.ConfigurationID, OutputContract: q.OutputContract, ContractID: q.ContractID, Space: q.Space, Role: q.Role, TokenizerID: c.TokenizerID, Usage: &representation.Usage{PromptTokens: int64(len(q.Input)), TotalTokens: int64(len(q.Input))}}
	for i := len(q.Input) - 1; i >= 0; i-- {
		in := q.Input[i]
		v, ok := m[in.Text]
		if !ok {
			return r, ErrInvalid
		}
		if f.invalid {
			v.Mask[0] = false
		}
		r.Data = append(r.Data, representation.Item{ID: in.ID, TokenMatrix: &v})
	}
	return r, nil
}
func fixtureManifest(t *testing.T, objects artifacts.Store) (corpus.ChunkManifest, corpus.Ref) {
	t.Helper()
	m := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "module", ReleaseID: "release", InputManifestHash: artifacts.Hash([]byte("input")), Profile: "chunk-v1", ParserVersion: "parser-v1", ChunkerVersion: "chunker-v1", ChunkSize: 64}
	for _, id := range []string{"a", "b", "c", "d"} {
		original := artifacts.Reference([]byte("original-" + id))
		encoded, _ := json.Marshal([]any{m.Profile, id})
		revision := "r" + id
		m.Inputs = append(m.Inputs, corpus.Input{RevisionID: revision, ContentID: id, SourceKind: "wiki", Original: original, ChunkCount: 1})
		m.Chunks = append(m.Chunks, corpus.Chunk{ID: id, RevisionID: revision, ContentID: id, SourceKind: "wiki", Original: original, Location: corpus.Location{Locator: "paragraph:1", OriginalByteEnd: 1, NormalizedRuneEnd: 1}, Text: id, TextHash: artifacts.Hash([]byte(id)), EncodingKey: artifacts.Hash(encoded), Required: true})
	}
	ref, err := put(context.Background(), objects, m)
	if err != nil {
		t.Fatal(err)
	}
	return m, ref
}
func setup(t *testing.T, c Config) (*Service, *fixtureEncoder, artifacts.Store, corpus.ChunkManifest, BuildResult) {
	t.Helper()
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, ref := fixtureManifest(t, objects)
	encoder := &fixtureEncoder{}
	s, err := New(objects, encoder, c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(c)})
	if err != nil {
		t.Fatal(err)
	}
	return s, encoder, objects, m, b
}
func query(ref corpus.Ref) Query {
	return Query{IndexRef: ref, ModuleID: "module", ReleaseID: "release", Generation: 1, ValidRevisionIDs: []string{"ra", "rb", "rc", "rd"}, Text: "query", TopK: 4}
}
func TestMaxSimMaskAndAggregation(t *testing.T) {
	for _, tc := range []struct {
		aggregation string
		want        float64
	}{{"sum_maxsim", 1.8}, {"mean_maxsim", .9}} {
		c := fixtureConfig(tc.aggregation).Contract
		q := representation.TokenValues{Shape: []int{3, 2}, Values: [][]float64{{1, 0}, {0, 1}, {0, 0}}, Mask: []bool{true, true, false}}
		d := representation.TokenValues{Shape: []int{3, 2}, Values: [][]float64{{.9, .1}, {.1, .9}, {0, 0}}, Mask: []bool{true, true, false}}
		got, err := MaxSim(q, d, c)
		if err != nil || math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("%s: %v %v", tc.aggregation, got, err)
		}
		bad := d
		bad.Values = [][]float64{{.9, .1}, {.1, .9}, {1, 0}}
		if _, err := MaxSim(q, bad, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("nonzero padding accepted", err)
		}
		bad = q
		bad.Mask = []bool{false, false, false}
		if _, err := MaxSim(bad, d, c); !errors.Is(err, ErrInvalid) {
			t.Fatal("empty query accepted", err)
		}
	}
}
func TestIndependentCandidatesRecoveryAndCosts(t *testing.T) {
	s, encoder, objects, m, b := setup(t, fixtureConfig("mean_maxsim"))
	if encoder.calls.Load() != 2 || b.EncodedChunks != 4 || b.Usage.TotalTokens != 4 || len(b.Index.Shards) != 2 {
		t.Fatal(b)
	}
	result, err := s.Search(context.Background(), query(b.Ref))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		id    string
		score float64
	}{{"a", 1}, {"b", .9}, {"d", .5}, {"c", 0}} {
		if len(result.Candidates) != 4 || result.Candidates[i].Chunk.ID != want.id || math.Abs(result.Candidates[i].Score-want.score) > 1e-12 || result.Candidates[i].Rank != i+1 || result.Candidates[i].Aggregation != "mean_maxsim" {
			t.Fatal(result)
		}
	}
	if result.Cost.BackendTokenRowsObserved != 14 || result.Cost.CandidateChunks != 4 || result.Cost.ExactDotProducts != 14 || result.Cost.ValidQueryTokens != 2 || result.Cost.ScoredMatrixValueBytes != 128 {
		t.Fatal(result.Cost)
	}
	q := query(b.Ref)
	q.ValidRevisionIDs = []string{"rd"}
	q.Text = "query_d"
	selected, err := s.Search(context.Background(), q)
	if err != nil || len(selected.Candidates) != 1 || selected.Candidates[0].Chunk.ID != "d" {
		t.Fatal("independent multivector-only candidate", selected, err)
	}
	before := encoder.calls.Load()
	q.ValidRevisionIDs = nil
	selected, err = s.Search(context.Background(), q)
	if err != nil || len(selected.Candidates) != 0 || encoder.calls.Load() != before {
		t.Fatal("empty revision set encoded", selected, err)
	}
	other, err := New(objects, encoder, fixtureConfig("mean_maxsim"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := other.Load(context.Background(), b.Ref)
	if err != nil {
		t.Fatal("persistent token-row restore", err)
	}
	got, err := snap.Search(context.Background(), query(b.Ref))
	if err != nil || !reflect.DeepEqual(got.Candidates, result.Candidates) {
		t.Fatal("reopened result changed", got, err)
	}
	resumed, err := other.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: b.Index.ChunkManifest, Profile: profile(fixtureConfig("mean_maxsim")), ResumeIndex: &b.Ref})
	if err != nil || !resumed.Reused || resumed.EncodedChunks != 0 || resumed.ReusedChunks != 4 || resumed.Usage.TotalTokens != 0 || resumed.StoredUsage.TotalTokens != 4 || encoder.calls.Load() != before+1 {
		t.Fatal(resumed, err)
	}
	probes, err := s.VerifyAndProbe(context.Background(), b.Index, m, m.Chunks[:2])
	if err != nil || len(probes) != 2 {
		t.Fatal(probes, err)
	}
}
func TestCorruptMaskCoverageAndStrictTruth(t *testing.T) {
	s, _, objects, m, b := setup(t, fixtureConfig("sum_maxsim"))
	part, err := load[shard](context.Background(), objects, b.Index.Shards[0].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	part.TokenRows = part.TokenRows[:1]
	badRef, _ := put(context.Background(), objects, part)
	idx := b.Index
	idx.Shards = append([]corpus.IndexShard(nil), idx.Shards...)
	idx.Shards[0].Artifact = badRef
	ref, _ := put(context.Background(), objects, idx)
	if _, err := s.Open(context.Background(), ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("token index corruption accepted", err)
	}
	idx = b.Index
	idx.Shards = idx.Shards[:1]
	ref, _ = put(context.Background(), objects, idx)
	if _, err := s.Open(context.Background(), ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing matrix shard accepted", err)
	}
	changed := fixtureConfig("mean_maxsim")
	other, err := New(objects, &fixtureEncoder{}, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(context.Background(), b.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("aggregation drift accepted", err)
	}
	q := query(b.Ref)
	q.TopK = 2
	q.TokenTopK = 1
	snap, err := s.Open(context.Background(), b.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snap.search(context.Background(), q, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("missed exact top-k accepted", err)
	}
	if _, err := s.VerifyAndProbe(context.Background(), b.Index, m, m.Chunks[:1]); err != nil {
		t.Fatal("adequate-budget probe", err)
	}
	q = query(b.Ref)
	q.Generation = 2
	if _, err := s.Search(context.Background(), q); !errors.Is(err, ErrInvalid) {
		t.Fatal("wrong generation accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, query(b.Ref)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation dropped", err)
	}
}

func TestUnifiedTelemetryWiresNestedStages(t *testing.T) {
	var output bytes.Buffer
	exporter := &captureExporter{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-multi-test", Environment: "test", Version: "fixed-sha", InstanceID: "worker-1", Output: &output, Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, ref := fixtureManifest(t, objects)
	c := fixtureConfig("mean_maxsim")
	s, err := New(objects, &fixtureEncoder{}, c, WithTelemetry(bundle))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Build(context.Background(), BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(c)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), query(b.Ref)); err != nil {
		t.Fatal(err)
	}
	invalid := query(b.Ref)
	invalid.Generation = 2
	if _, err := s.Search(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	seen := map[string]int{}
	for _, line := range records {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal("non-JSON production log", err)
		}
		for _, key := range []string{"timestamp", "level", "service", "environment", "service_version", "instance_id", "component", "log_source", "event", "trace_id", "span_id"} {
			if r[key] == nil {
				t.Fatalf("missing %s: %s", key, line)
			}
		}
		if r["component"] != "multivector" || r["service_version"] != "fixed-sha" {
			t.Fatal("wrong logger identity", r)
		}
		seen[r["event"].(string)]++
		if r["event"] == "multivector.search.finished" && r["outcome"] == "rejected" && r["error_code"] != "INVALID_CONTRACT_OR_ARTIFACT" {
			t.Fatal("invalid query not classified", r)
		}
	}
	for _, event := range []string{"multivector.build.started", "multivector.build.finished", "multivector.encode.finished", "multivector.project.finished", "multivector.search.finished"} {
		if seen[event] == 0 {
			t.Fatal("missing stage", event, seen)
		}
	}
	exporter.mu.Lock()
	spans := append([]sdktrace.ReadOnlySpan(nil), exporter.spans...)
	exporter.mu.Unlock()
	if len(spans) != 7 {
		t.Fatal("expected build+2 doc encode+project+search+query encode+rejected search", len(spans))
	}
	var buildID string
	for _, span := range spans {
		if span.Name() == "multivector.build" {
			buildID = span.SpanContext().SpanID().String()
		}
	}
	if buildID == "" {
		t.Fatal("build span missing")
	}
	children := 0
	for _, span := range spans {
		if span.Parent().IsValid() && span.Parent().SpanID().String() == buildID {
			children++
		}
	}
	if children != 3 {
		t.Fatal("build did not parent encode/project spans", children)
	}
	w := httptest.NewRecorder()
	bundle.MetricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	metrics := w.Body.String()
	if !strings.Contains(metrics, `sea_btw_operations_total{component="multivector",outcome="succeeded"} 6`) || !strings.Contains(metrics, `sea_btw_operations_total{component="multivector",outcome="rejected"} 1`) || strings.Contains(metrics, `build_id="build"`) {
		t.Fatal("unbounded or missing multivector metrics", metrics)
	}
}

type captureExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (c *captureExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spans = append(c.spans, spans...)
	return nil
}
func (*captureExporter) Shutdown(context.Context) error { return nil }
