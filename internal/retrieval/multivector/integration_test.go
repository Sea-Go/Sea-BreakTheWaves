package multivector

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// Explicit isolated engine endpoint only. No production collection discovery
// or deletion; the dedicated namespace and immutable index hash own the rows.
func TestRealMilvus(t *testing.T) {
	address := os.Getenv("MULTIVECTOR_MILVUS_ADDRESS")
	if address == "" {
		t.Skip("isolated token-row Milvus endpoint not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	dir := os.Getenv("MULTIVECTOR_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	objects, err := artifacts.NewLocal(filepath.Join(dir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	state := struct {
		Namespace string     `json:"namespace"`
		Ref       corpus.Ref `json:"ref"`
	}{}
	reopen := os.Getenv("MULTIVECTOR_REOPEN") == "1"
	if reopen {
		raw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
	} else {
		state.Namespace = "acceptance_" + time.Now().UTC().Format("20060102150405")
	}
	config := fixtureConfig("mean_maxsim")
	engine := "milvus"
	if os.Getenv("MULTIVECTOR_MILVUS_LITE") == "1" {
		engine = "lite"
	}
	s, err := NewMilvus(objects, &fixtureEncoder{}, config, client, MilvusConfig{Namespace: state.Namespace, Engine: engine, M: 16, EFConstruction: 128, EFSearch: 64})
	if err != nil {
		t.Fatal(err)
	}
	if reopen {
		if _, err := s.Load(ctx, state.Ref); err != nil {
			t.Fatal("load after engine process restart", err)
		}
	} else {
		m, ref := fixtureManifest(t, objects)
		b, err := s.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(config)})
		if err != nil {
			t.Fatal(err)
		}
		state.Ref = b.Ref
		if _, err := s.VerifyAndProbe(ctx, b.Index, m, m.Chunks[:2]); err != nil {
			t.Fatal("native probe", err)
		}
		raw, _ := json.MarshalIndent(state, "", "  ")
		if err := os.WriteFile(statePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Search(ctx, query(state.Ref))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		id    string
		score float64
	}{{"a", 1}, {"b", .9}, {"d", .5}, {"c", 0}} {
		if len(result.Candidates) != 4 || result.Candidates[i].Chunk.ID != want.id || math.Abs(result.Candidates[i].Score-want.score) > 1e-12 {
			t.Fatal(result)
		}
	}
	if result.Cost.CandidateChunks != 4 || result.Cost.BackendTokenRowsObserved < 4 {
		t.Fatal(result.Cost)
	}
	q := query(state.Ref)
	q.ValidRevisionIDs = []string{"rd"}
	q.Text = "query_d"
	selected, err := s.Search(ctx, q)
	if err != nil || len(selected.Candidates) != 1 || selected.Candidates[0].Chunk.ID != "d" {
		t.Fatal("pre-ANN revision filter", selected, err)
	}
	t.Logf("native token-row HNSW/IP; restore=%t rows=%d candidates=%d cost=%+v", reopen, 7, len(result.Candidates), result.Cost)
}

func TestRealMilvusRejectsMutatedProjection(t *testing.T) {
	address := os.Getenv("MULTIVECTOR_MILVUS_ADDRESS")
	if address == "" {
		t.Skip("isolated token-row Milvus endpoint not configured")
	}
	if os.Getenv("MULTIVECTOR_REOPEN") == "1" {
		t.Skip("mutation test belongs to fresh isolated engine process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, ref := fixtureManifest(t, objects)
	cfg := fixtureConfig("mean_maxsim")
	engine := "milvus"
	if os.Getenv("MULTIVECTOR_MILVUS_LITE") == "1" {
		engine = "lite"
	}
	s, err := NewMilvus(objects, &fixtureEncoder{}, cfg, client, MilvusConfig{Namespace: "mutation_" + time.Now().UTC().Format("20060102150405"), Engine: engine, M: 16, EFConstruction: 128, EFSearch: 64})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Open(ctx, b.Ref)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := s.backend.(*milvus).identity(snap)
	r := snap.rows["a"]
	option := milvusclient.NewColumnBasedInsertOption(name).WithVarcharColumn("token_id", []string{tokenID("a", 0)}).WithVarcharColumn("chunk_id", []string{"a"}).WithVarcharColumn("revision_id", []string{r.Chunk.RevisionID}).WithVarcharColumn("index_hash", []string{b.Ref.SHA256}).WithVarcharColumn("matrix_hash", []string{r.MatrixHash}).WithInt64Column("position", []int64{0}).WithFloatVectorColumn("vector", cfg.Contract.Dimensions, [][]float32{{2, 0}})
	if _, err := client.Upsert(ctx, option); err != nil {
		t.Fatal(err)
	}
	flush, err := client.Flush(ctx, milvusclient.NewFlushOption(name))
	if err != nil {
		t.Fatal(err)
	}
	if err := flush.Await(ctx); err != nil {
		t.Fatal(err)
	}
	load, err := client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		t.Fatal(err)
	}
	if err := load.Await(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, b.Ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("mutated token row with unchanged metadata was not classified as corruption", err)
	}
	if _, err := snap.Search(ctx, query(b.Ref)); !errors.Is(err, ErrInvalid) {
		t.Fatal("mutated native IP score was not rejected", err)
	}
}

// The owner of the disposable DC/BGE runtime writes the file and holds its
// processes until this consumer completes. The report never copies the token.
func TestRealDC(t *testing.T) {
	runtimePath := os.Getenv("MULTIVECTOR_DC_RUNTIME")
	if runtimePath == "" {
		t.Skip("actual DC/BGE runtime not configured")
	}
	raw, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var runtime struct {
		Endpoint       string `json:"endpoint"`
		Token          string `json:"access_token"`
		Configurations map[string]struct {
			Callpoint       string `json:"callpoint"`
			ConfigurationID string `json:"configuration_id"`
			Profile         struct {
				Model    string                  `json:"model"`
				Space    string                  `json:"representation_space"`
				Contract representation.Contract `json:"representation_contract"`
			} `json:"profile"`
		} `json:"configurations"`
	}
	if err := json.Unmarshal(raw, &runtime); err != nil {
		t.Fatal(err)
	}
	wire, ok := runtime.Configurations["token_matrix"]
	if !ok || runtime.Endpoint == "" || runtime.Token == "" {
		t.Fatal("runtime is missing actual token-matrix callpoint")
	}
	cfg := Config{Document: Encoding{wire.Callpoint, wire.ConfigurationID, wire.Profile.Model}, Query: Encoding{wire.Callpoint, wire.ConfigurationID, wire.Profile.Model}, Contract: wire.Profile.Contract, Space: wire.Profile.Space, BatchSize: 2, TokenTopK: 256, ProbeTopK: 4}
	if cfg.Contract.Aggregation != "mean_maxsim" || cfg.Contract.Dimensions != 1024 {
		t.Fatal("unexpected actual model contract")
	}
	encoder, err := datacenter.New(httpclient.Config{BaseURL: runtime.Endpoint, Token: runtime.Token})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dir := os.Getenv("MULTIVECTOR_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	objects, err := artifacts.NewLocal(filepath.Join(dir, "real-artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := fixtureManifest(t, objects)
	texts := []string{"whale mammal ocean", "whale whale large mammal", "quantum computing qubits", "ocean mammals and whales"}
	for i, text := range texts {
		original := artifacts.Reference([]byte(text))
		encoded, _ := json.Marshal([]any{m.Profile, text})
		m.Inputs[i].Original = original
		c := &m.Chunks[i]
		c.Text = text
		c.TextHash = artifacts.Hash([]byte(text))
		c.Original = original
		c.EncodingKey = artifacts.Hash(encoded)
		c.Location.OriginalByteEnd = len(text)
		c.Location.NormalizedRuneEnd = utf8.RuneCountInString(text)
	}
	ref, err := put(ctx, objects, m)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := New(objects, encoder, cfg)
	if err != nil {
		t.Fatal(err)
	}
	built, err := exact.Build(ctx, BuildRequest{BuildID: "real-bge-multi", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	q := query(built.Ref)
	q.Text = "whale"
	q.TokenTopK = 256
	truth, err := exact.Search(ctx, q)
	if err != nil || len(truth.Candidates) != 4 {
		t.Fatal(truth, err)
	}
	backendName := "exact"
	result := truth
	if address := os.Getenv("MULTIVECTOR_MILVUS_ADDRESS"); address != "" {
		mc, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address})
		if err != nil {
			t.Fatal(err)
		}
		defer mc.Close(context.Background())
		engine := "milvus"
		if os.Getenv("MULTIVECTOR_MILVUS_LITE") == "1" {
			engine = "lite"
		}
		native, err := NewMilvus(objects, encoder, cfg, mc, MilvusConfig{Namespace: "real_bge_" + time.Now().UTC().Format("20060102150405"), Engine: engine, M: 16, EFConstruction: 128, EFSearch: 64})
		if err != nil {
			t.Fatal(err)
		}
		projected, err := native.Build(ctx, BuildRequest{BuildID: "real-bge-multi", Generation: 1, ChunkManifest: ref, Profile: profile(cfg), ResumeIndex: &built.Ref})
		if err != nil || !projected.Reused || projected.Usage.TotalTokens != 0 {
			t.Fatal("projection re-encoded", projected, err)
		}
		result, err = native.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates) != len(truth.Candidates) {
			t.Fatal("native token candidate coverage differs", result, truth)
		}
		for i, c := range result.Candidates {
			if c.Chunk.ID != truth.Candidates[i].Chunk.ID || math.Abs(c.Score-truth.Candidates[i].Score) > 1e-10 || math.Abs(c.BackendTokenScore-truth.Candidates[i].BackendTokenScore) > 1e-4 {
				t.Fatal("native candidate differs from exact token-row truth", result, truth)
			}
		}
		if _, err := native.VerifyAndProbe(ctx, projected.Index, m, m.Chunks); err != nil {
			t.Fatal("native true-matrix probe", err)
		}
		if _, err := native.Load(ctx, projected.Ref); err != nil {
			t.Fatal("native projection load", err)
		}
		backendName = "milvus-" + engine + "-hnsw-ip"
	}
	report := struct {
		Build      BuildResult `json:"build"`
		Result     Result      `json:"result"`
		Backend    string      `json:"backend"`
		Model      string      `json:"model"`
		Dimensions int         `json:"dimensions"`
		Note       string      `json:"note"`
	}{built, result, backendName, cfg.Document.PhysicalModel, cfg.Contract.Dimensions, "Actual DC/BGE token matrices; four fixed chunks; not production relevance or Collector acceptance."}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real-dc-report.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual DC+BGE token matrices -> %s; document tokens=%d query cost=%+v", backendName, len(m.Chunks), result.Cost)
}
