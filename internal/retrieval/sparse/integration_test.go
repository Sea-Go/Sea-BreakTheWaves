package sparse

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

func realClient(t *testing.T, ctx context.Context) *milvusclient.Client {
	t.Helper()
	address := os.Getenv("SPARSE_MILVUS_ADDRESS")
	if address == "" {
		t.Skip("explicit isolated sparse Milvus address required")
	}
	client, e := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}
func evidenceDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("SPARSE_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	return dir
}
func writeReport(t *testing.T, dir, name string, v any) {
	t.Helper()
	raw, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

type projectionRecord struct {
	Config     Config       `json:"config"`
	Milvus     MilvusConfig `json:"milvus"`
	Build      BuildResult  `json:"build"`
	Result     Result       `json:"result"`
	Collection string       `json:"collection"`
}

func TestRealMilvus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := realClient(t, ctx)
	dir := evidenceDir(t)
	objects, e := artifacts.NewLocal(dir)
	if e != nil {
		t.Fatal(e)
	}
	m, ref := fixtureManifest(t, objects)
	cfg := fixtureConfig()
	backend := MilvusConfig{Namespace: "fixture_" + time.Now().UTC().Format("20060102150405"), Engine: "native-lite"}
	s, e := NewMilvus(objects, &fixture{}, cfg, client, backend)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.Search(ctx, query(b.Ref))
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Candidates) != 3 {
		t.Fatal(out)
	}
	for i, want := range []float64{8, 4, 2} {
		if out.Candidates[i].Score != want || math.Abs(out.Candidates[i].BackendScore-want) > 1e-6 {
			t.Fatal("backend did not return IP", out)
		}
	}
	q := query(b.Ref)
	q.Text = "scaled"
	scaled, e := s.Search(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	for i, got := range scaled.Candidates {
		if got.BackendScore != 2*out.Candidates[i].BackendScore {
			t.Fatal("weights did not scale linearly", scaled)
		}
	}
	q = query(b.Ref)
	q.Text = "b"
	doubledDocument, e := s.Search(ctx, q)
	if e != nil || len(doubledDocument.Candidates) != 2 || doubledDocument.Candidates[0].Chunk.ID != "b" || doubledDocument.Candidates[0].BackendScore != 16 || doubledDocument.Candidates[1].BackendScore != 8 {
		t.Fatal("document token weight must score linearly", doubledDocument, e)
	}
	q = query(b.Ref)
	q.ValidRevisionIDs = []string{"rb"}
	selected, e := s.Search(ctx, q)
	if e != nil || len(selected.Candidates) != 1 || selected.Candidates[0].Chunk.ID != "b" {
		t.Fatal(selected, e)
	}
	if _, e = s.VerifyAndProbe(ctx, b.Index, m, m.Chunks); e != nil {
		t.Fatal(e)
	}
	snap, e := s.Load(ctx, b.Ref)
	if e != nil {
		t.Fatal(e)
	}
	name, _ := s.backend.(*milvus).identity(snap)
	writeReport(t, dir, "milvus-projection.json", projectionRecord{cfg, backend, b, out, name})
	t.Logf("real native SPARSE_INVERTED_INDEX/IP; linear scores=8,4,2 collection=%s", name)
}
func TestRestoreMilvus(t *testing.T) {
	path := os.Getenv("SPARSE_RESTORE_RECORD")
	if path == "" {
		t.Skip("no prior native process record")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var record projectionRecord
	if e = json.Unmarshal(raw, &record); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := realClient(t, ctx)
	objects, e := artifacts.NewLocal(filepath.Dir(path))
	if e != nil {
		t.Fatal(e)
	}
	encoder := &fixture{}
	s, e := NewMilvus(objects, encoder, record.Config, client, record.Milvus)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.Load(ctx, record.Build.Ref)
	if e != nil {
		t.Fatal(e)
	}
	if encoder.calls.Load() != 0 {
		t.Fatal("load re-encoded")
	}
	out, e := snap.Search(ctx, query(record.Build.Ref))
	if e != nil || len(out.Candidates) != 3 || out.Candidates[0].BackendScore != 8 {
		t.Fatal(out, e)
	}
	q := query(record.Build.Ref)
	q.Text = "nooverlap"
	empty, err := snap.Search(ctx, q)
	if err != nil || len(empty.Candidates) != 0 {
		t.Fatal("no-overlap must be empty", empty, err)
	}
	q.ValidRevisionIDs = nil
	empty, err = snap.Search(ctx, q)
	if err != nil || len(empty.Candidates) != 0 {
		t.Fatal("empty validity must be empty", empty, err)
	}
	t.Logf("restored native collection=%s without Build/Upsert; no-overlap and empty validity return empty", record.Collection)
}
func TestRealDCSparseToMilvus(t *testing.T) {
	runtimeFile := os.Getenv("SPARSE_DC_RUNTIME")
	if runtimeFile == "" {
		t.Skip("DC+BGE owner runtime file required")
	}
	raw, e := os.ReadFile(runtimeFile)
	if e != nil {
		t.Fatal(e)
	}
	var runtime struct {
		Endpoint       string `json:"endpoint"`
		Token          string `json:"access_token"`
		Configurations map[string]struct {
			Callpoint       string `json:"callpoint"`
			ConfigurationID string `json:"configuration_id"`
			Profile         struct {
				Model    string          `json:"model"`
				Space    string          `json:"representation_space"`
				Contract json.RawMessage `json:"representation_contract"`
			} `json:"profile"`
		} `json:"configurations"`
	}
	if e = json.Unmarshal(raw, &runtime); e != nil {
		t.Fatal(e)
	}
	wire := runtime.Configurations["sparse"]
	cfg := Config{Document: Encoding{wire.Callpoint, wire.ConfigurationID, wire.Profile.Model}, Query: Encoding{wire.Callpoint, wire.ConfigurationID, wire.Profile.Model}, Space: wire.Profile.Space, BatchSize: 2, ProbeTopK: 4}
	if e = json.Unmarshal(wire.Profile.Contract, &cfg.Contract); e != nil {
		t.Fatal(e)
	}
	encoder, e := datacenter.New(httpclient.Config{BaseURL: runtime.Endpoint, Token: runtime.Token})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dir := evidenceDir(t)
	objects, e := artifacts.NewLocal(dir)
	if e != nil {
		t.Fatal(e)
	}
	m, _ := fixtureManifest(t, objects)
	texts := []string{"whale mammal ocean", "whale whale large mammal", "quantum computing qubits", "ocean mammals and whales"}
	for i, text := range texts {
		orig := artifacts.Reference([]byte(text))
		encoded, _ := json.Marshal([]any{m.Profile, text})
		m.Inputs[i].Original = orig
		c := &m.Chunks[i]
		c.Text = text
		c.TextHash = artifacts.Hash([]byte(text))
		c.Original = orig
		c.EncodingKey = artifacts.Hash(encoded)
		c.Location.OriginalByteEnd = len(text)
		c.Location.NormalizedRuneEnd = utf8.RuneCountInString(text)
	}
	ref, e := put(ctx, objects, m)
	if e != nil {
		t.Fatal(e)
	}
	exact, e := New(objects, encoder, cfg)
	if e != nil {
		t.Fatal(e)
	}
	built, e := exact.Build(ctx, BuildRequest{BuildID: "real-bge-sparse", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if e != nil {
		t.Fatal(e)
	}
	q := query(built.Ref)
	q.Text = "whale"
	truth, e := exact.Search(ctx, q)
	if e != nil || len(truth.Candidates) == 0 {
		t.Fatal(truth, e)
	}
	client := realClient(t, ctx)
	backend := MilvusConfig{Namespace: "bge_" + time.Now().UTC().Format("20060102150405"), Engine: "native-lite"}
	service, e := NewMilvus(objects, encoder, cfg, client, backend)
	if e != nil {
		t.Fatal(e)
	}
	projected, e := service.Build(ctx, BuildRequest{BuildID: "real-bge-sparse", Generation: 1, ChunkManifest: ref, Profile: profile(cfg), ResumeIndex: &built.Ref})
	if e != nil {
		t.Fatal(e)
	}
	if !projected.Reused || projected.Usage.TotalTokens != 0 {
		t.Fatal("projection re-encoded", projected)
	}
	found, e := service.Search(ctx, q)
	if e != nil || len(found.Candidates) != len(truth.Candidates) {
		t.Fatal(found, e)
	}
	for i, c := range found.Candidates {
		if c.Chunk.ID != truth.Candidates[i].Chunk.ID || math.Abs(c.Score-truth.Candidates[i].Score) > 1e-10 || math.Abs(c.BackendScore-c.Score) > 1e-5 {
			t.Fatal("real sparse truth mismatch", found, truth)
		}
	}
	if _, e = service.VerifyAndProbe(ctx, projected.Index, m, m.Chunks); e != nil {
		t.Fatal(e)
	}
	snap, e := service.Load(ctx, projected.Ref)
	if e != nil {
		t.Fatal(e)
	}
	name, _ := service.backend.(*milvus).identity(snap)
	writeReport(t, dir, "real-dc-sparse.json", projectionRecord{cfg, backend, projected, found, name})
	t.Logf("actual DC+BGE learned sparse to native inverted IP passed; candidates=%d collection=%s", len(found.Candidates), name)
}

func TestRealMilvusRejectsMutatedProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := realClient(t, ctx)
	objects, e := artifacts.NewLocal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	_, ref := fixtureManifest(t, objects)
	cfg := fixtureConfig()
	backend := MilvusConfig{Namespace: "corrupt_" + time.Now().UTC().Format("20060102150405"), Engine: "native-lite"}
	service, e := NewMilvus(objects, &fixture{}, cfg, client, backend)
	if e != nil {
		t.Fatal(e)
	}
	built, e := service.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if e != nil {
		t.Fatal(e)
	}
	snap, e := service.Open(ctx, built.Ref)
	if e != nil {
		t.Fatal(e)
	}
	name, _ := service.backend.(*milvus).identity(snap)
	original := snap.rows["a"]
	corrupt, e := toMilvus(representation.SparseValues{Indices: []int{1, 4}, Weights: []float64{20, 30}})
	if e != nil {
		t.Fatal(e)
	}
	update := milvusclient.NewColumnBasedInsertOption(name).WithVarcharColumn("chunk_id", []string{"a"}).WithVarcharColumn("revision_id", []string{original.Chunk.RevisionID}).WithVarcharColumn("index_hash", []string{built.Ref.SHA256}).WithVarcharColumn("vector_hash", []string{original.VectorHash}).WithColumns(column.NewColumnSparseVectors("vector", []entity.SparseEmbedding{corrupt}))
	if _, e = client.Upsert(ctx, update); e != nil {
		t.Fatal(e)
	}
	flushed, e := client.Flush(ctx, milvusclient.NewFlushOption(name))
	if e != nil {
		t.Fatal(e)
	}
	if e = flushed.Await(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Load(ctx, built.Ref); !errors.Is(e, ErrInvalid) || !strings.Contains(e.Error(), "sparse projected values differ") {
		t.Fatalf("Load did not detect actual vector corruption: %v", e)
	}
	if _, e = snap.Search(ctx, query(built.Ref)); !errors.Is(e, ErrInvalid) || !strings.Contains(e.Error(), "backend score is not sparse inner product") {
		t.Fatalf("Search did not detect actual score corruption: %v", e)
	}
	t.Logf("actual changed vector rejected during Load and Search; collection=%s", name)
}
