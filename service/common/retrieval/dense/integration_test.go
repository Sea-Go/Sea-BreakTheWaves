package dense

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// Explicit endpoint opt-in only. No production discovery and no teardown that
// could drop a pre-existing collection. The test logs its dedicated namespace.
func TestRealMilvus(t *testing.T) {
	address := os.Getenv("DENSE_MILVUS_ADDRESS")
	if address == "" {
		t.Skip("real Milvus not configured; SDK wire fixture is not a server acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: address, APIKey: os.Getenv("DENSE_MILVUS_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	config := fixtureConfig()
	dir := os.Getenv("DENSE_MILVUS_EVIDENCE")
	if dir == "" {
		dir = t.TempDir()
	}
	objects, _ := artifacts.NewLocal(dir)
	m, ref := fixtureManifest(t, objects)
	namespace := "acceptance_" + time.Now().UTC().Format("20060102150405")
	var state struct {
		Namespace string     `json:"namespace"`
		IndexRef  corpus.Ref `json:"index_ref"`
	}
	if os.Getenv("DENSE_MILVUS_REOPEN") == "1" {
		raw, err := os.ReadFile(filepath.Join(dir, "milvus-state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = representation.Decode(raw, &state); err != nil {
			t.Fatal(err)
		}
		namespace = state.Namespace
	}
	engine := "milvus"
	if os.Getenv("DENSE_MILVUS_LITE") == "1" {
		engine = "lite"
	}
	service, err := NewMilvus(objects, fixtureEncoder(), config, client, MilvusConfig{Engine: engine, Namespace: namespace, M: 16, EFConstruction: 128, EFSearch: 64})
	if err != nil {
		t.Fatal(err)
	}
	var built BuildResult
	if os.Getenv("DENSE_MILVUS_REOPEN") == "1" {
		snapshot, err := service.Load(ctx, state.IndexRef)
		if err != nil {
			t.Fatal(err)
		}
		built = BuildResult{Index: snapshot.index, Ref: state.IndexRef}
	} else {
		built, err = service.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(config)})
		if err != nil {
			t.Fatal(err)
		}
		state.Namespace = namespace
		state.IndexRef = built.Ref
		raw, _ := json.Marshal(state)
		if err = os.WriteFile(filepath.Join(dir, "milvus-state.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	query := queryFor(built)
	result, err := service.Search(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 4 {
		t.Fatal(result)
	}
	for i, want := range []float64{1, .6, 0, -1} {
		backendWant := want
		if math.Abs(result.Candidates[i].Score-want) > 1e-12 || math.Abs(result.Candidates[i].BackendScore-backendWant) > 1e-5 || result.Candidates[i].Chunk.ID != string(rune('a'+i)) {
			t.Fatal(result)
		}
	}
	query.ValidRevisionIDs = []string{"r2"}
	query.TopK = 1
	result, err = service.Search(ctx, query)
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Chunk.ID != "c" {
		t.Fatal(result, err)
	}
	if _, err = service.VerifyAndProbe(ctx, built.Index, m, m.Chunks); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DENSE_MILVUS_REOPEN") != "1" {
		if _, err = service.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(config), ResumeIndex: &built.Ref}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := service.Open(ctx, built.Ref)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := service.backend.(*milvus).identity(snapshot)
	t.Logf("real Milvus passed, retained task collection=%s index_sha256=%s", name, built.Ref.SHA256)
}

func TestRealDC(t *testing.T) {
	address := os.Getenv("DENSE_DC_URL")
	if address == "" {
		t.Skip("real DC encoder not configured; HTTP fixture is not semantic encoding")
	}
	raw, err := os.ReadFile(os.Getenv("DENSE_DC_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err = representation.Decode(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	client, err := datacenter.New(httpclient.Config{BaseURL: address, Token: os.Getenv("DENSE_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("DENSE_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	objects, err := artifacts.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, ref := fixtureManifest(t, objects)
	s, err := New(objects, client, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	backendName := "exact"
	if os.Getenv("DENSE_REAL_BACKEND") == "milvus" {
		backendName = "milvus"
		engine := "milvus"
		if os.Getenv("DENSE_MILVUS_LITE") == "1" {
			engine = "lite"
			backendName = "milvus-lite-hnsw"
		}
		mc, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: os.Getenv("DENSE_MILVUS_ADDRESS"), APIKey: os.Getenv("DENSE_MILVUS_TOKEN")})
		if err != nil {
			t.Fatal(err)
		}
		defer mc.Close(context.Background())
		s, err = NewMilvus(objects, client, cfg, mc, MilvusConfig{Engine: engine, Namespace: "real_dc_" + time.Now().UTC().Format("20060102150405"), M: 16, EFConstruction: 128, EFSearch: 64})
		if err != nil {
			t.Fatal(err)
		}
	}
	built, err := s.Build(ctx, BuildRequest{BuildID: "build", Generation: 1, ChunkManifest: ref, Profile: fixtureProfile(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(ctx, queryFor(built))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 4 || result.Candidates[0].Chunk.ID != "a" {
		t.Fatal(result)
	}
	for _, candidate := range result.Candidates {
		if math.Abs(candidate.Score-candidate.BackendScore) > 1e-5 {
			t.Fatal("backend/raw vector score diverged", candidate)
		}
	}
	probes, err := s.VerifyAndProbe(ctx, built.Index, manifest, manifest.Chunks)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range probes {
		if len(p.CandidateIDs) == 0 || p.CandidateIDs[0] != manifest.Chunks[i].ID {
			t.Fatal("self query failed", probes)
		}
	}
	report := struct {
		Config  Config      `json:"config"`
		Build   BuildResult `json:"build"`
		Result  Result      `json:"result"`
		Backend string      `json:"backend"`
		Note    string      `json:"note"`
	}{cfg, built, result, backendName, "Real DC encoding; fixed four-chunk local retrieval, not large corpus relevance or production-scale acceptance."}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "real-dc-report.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("real DC dense passed: model=%s dimensions=%d encoded=%d artifact=%s", cfg.Document.PhysicalModel, cfg.Contract.Dimensions, built.EncodedChunks, built.Ref.SHA256)
}
