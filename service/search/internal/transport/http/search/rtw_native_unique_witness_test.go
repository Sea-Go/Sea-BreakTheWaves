package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/app"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const nativeUniqueQuery = "apple pie recipe"

var nativeUniqueTexts = map[string]string{
	"apple":  "Synthetic published passage about apple-guide; evidence is fixture only.",
	"banana": "Synthetic published passage about banana-guide; evidence is fixture only.",
	"orange": "Synthetic published passage about orange-guide; evidence is fixture only.",
	"coffee": "Synthetic published passage about coffee-guide; evidence is fixture only.",
}

type nativeUniqueFixture struct {
	RTWBase                  string `json:"rtw_base"`
	WorkerToken              string `json:"worker_token"`
	ModuleID                 string `json:"module_id"`
	DCRuntime                string `json:"dc_runtime"`
	NativeRuntime            string `json:"native_runtime"`
	ArtifactDir              string `json:"artifact_dir"`
	BuildResultPath          string `json:"build_result_path"`
	Query                    string `json:"query"`
	ExpectedOrangeRevisionID string `json:"expected_orange_revision_id"`
	ExpectedOrangeChunkID    string `json:"expected_orange_chunk_id"`
	ResultPath               string `json:"result_path"`
}

type nativeUniqueResult struct {
	SchemaVersion          string   `json:"schema_version"`
	ModuleID               string   `json:"module_id"`
	ReleaseID              string   `json:"release_id"`
	Generation             int64    `json:"generation"`
	PublicationRevision    string   `json:"publication_revision"`
	Query                  string   `json:"query"`
	NativeRuntimeSHA256    string   `json:"native_runtime_sha256"`
	EnginePackageSHA256    string   `json:"engine_package_sha256"`
	SettingsJCSSHA256      string   `json:"settings_jcs_sha256"`
	SparseBackend          string   `json:"sparse_backend"`
	SparseExaminedPostings int      `json:"sparse_examined_postings"`
	DenseChunkIDs          []string `json:"dense_chunk_ids"`
	SparseChunkIDs         []string `json:"sparse_chunk_ids"`
	MultiVectorChunkIDs    []string `json:"multivector_chunk_ids"`
	UniqueMultiChunkID     string   `json:"unique_multi_chunk_id"`
	UniqueRevisionID       string   `json:"unique_revision_id"`
	UniqueQuoteSHA256      string   `json:"unique_quote_sha256"`
	UniqueOriginalSHA256   string   `json:"unique_original_sha256"`
	UniqueLocator          string   `json:"unique_locator"`
	RTWCurrentAndReadable  bool     `json:"rtw_current_and_readable"`
	PhysicalQualified      bool     `json:"physical_qualified"`
	QrelEvaluable          bool     `json:"qrel_evaluable"`
	ProductionVerified     bool     `json:"production_verified"`
}

func readNativeUniqueFixture(path string) (nativeUniqueFixture, error) {
	var fixture nativeUniqueFixture
	if !filepath.IsAbs(path) {
		return fixture, errors.New("native witness fixture path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fixture, errors.New("native witness fixture must be ordinary private bytes")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 || len(raw) > 16<<10 {
		return fixture, errors.New("native witness fixture size/read differs")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF ||
		fixture.Query != nativeUniqueQuery || fixture.ModuleID == "" ||
		fixture.RTWBase == "" || fixture.WorkerToken == "" ||
		fixture.ExpectedOrangeRevisionID == "" || fixture.ExpectedOrangeChunkID == "" ||
		!filepath.IsAbs(fixture.DCRuntime) || !filepath.IsAbs(fixture.NativeRuntime) ||
		!filepath.IsAbs(fixture.ArtifactDir) || !filepath.IsAbs(fixture.BuildResultPath) ||
		!filepath.IsAbs(fixture.ResultPath) ||
		filepath.Dir(fixture.ResultPath) != filepath.Dir(path) {
		return nativeUniqueFixture{}, errors.New("native witness fixture lacks fixed published four-source ABI")
	}
	parsed, err := url.Parse(fixture.RTWBase)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nativeUniqueFixture{}, errors.New("native witness RTW source must be an isolated loopback process")
	}
	return fixture, nil
}

func TestRTWNativePublishedIndependentMultiDiscovery(t *testing.T) {
	path := os.Getenv("SEA_BTW_NATIVE_PUBLISHED_WITNESS_FIXTURE")
	if path == "" {
		t.Skip("requires one RTW-published four-source release, live DC BGE and task-owned native Lite")
	}
	fixture, err := readNativeUniqueFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rtw, err := ridethewind.New(httpclient.Config{BaseURL: fixture.RTWBase,
		Token: fixture.WorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	current, err := app.NewRTWSearchSnapshotProvider(rtw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := current.Current(ctx, fixture.ModuleID)
	if err != nil || len(snapshot.Indexes) != 3 || len(snapshot.ValidRevisionIDs) != 4 {
		t.Fatalf("four real RTW SourceRevisions are not in one manually active release: %+v %v", snapshot, err)
	}
	runtimeRaw, err := os.ReadFile(fixture.DCRuntime)
	if err != nil {
		t.Fatal(err)
	}
	var dcRuntime realDCRuntime
	if json.Unmarshal(runtimeRaw, &dcRuntime) != nil || dcRuntime.Endpoint == "" ||
		dcRuntime.Token == "" || len(dcRuntime.Configurations) != 3 {
		t.Fatal("actual DC BGE typed representation runtime is incomplete")
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: dcRuntime.Endpoint,
		Token: dcRuntime.Token})
	if err != nil {
		t.Fatal(err)
	}
	gate, err := datacenter.NewRepresentationGate(dc, 1)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := artifacts.NewLocal(fixture.ArtifactDir)
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := os.ReadFile(fixture.BuildResultPath)
	if err != nil {
		t.Fatal(err)
	}
	var built realIndexResult
	if json.Unmarshal(resultRaw, &built) != nil || built.NativeProjection == nil ||
		built.NativeProjection.PhysicalQualified ||
		built.NativeProjection.Status != "test_projection_before_RTW_READY" ||
		len(built.Indexes) != 3 ||
		len(built.APIIndexSettings) == 0 || built.ChunkCount != 4 {
		t.Fatal("four-chunk native projection result did not stay test-only")
	}
	for _, lane := range []searchdomain.Lane{searchdomain.Dense, searchdomain.Sparse, searchdomain.MultiVector} {
		if snapshot.Indexes[lane] != built.Indexes[string(lane)] {
			t.Fatalf("RTW active %s physical ref differs from accepted immutable build", lane)
		}
	}
	var settings struct {
		Dense       dense.Config       `json:"dense"`
		Sparse      sparse.Config      `json:"sparse"`
		MultiVector multivector.Config `json:"multivector"`
	}
	if json.Unmarshal(built.APIIndexSettings, &settings) != nil {
		t.Fatal("native API and source build encoding settings differ")
	}
	indexRaw, err := objects.Get(ctx, built.IndexManifest)
	if err != nil {
		t.Fatal(err)
	}
	var index corpus.IndexManifest
	if json.Unmarshal(indexRaw, &index) != nil || index.ReleaseID != snapshot.ReleaseID ||
		index.Generation != snapshot.Generation || index.ChunkCount != 4 ||
		len(index.Lanes) != 3 {
		t.Fatal("RTW published index manifest is not the four-source native candidate")
	}
	chunkRaw, err := objects.Get(ctx, index.ChunkManifest)
	if err != nil {
		t.Fatal(err)
	}
	var chunks corpus.ChunkManifest
	if json.Unmarshal(chunkRaw, &chunks) != nil || chunks.ModuleID != snapshot.ModuleID ||
		chunks.ReleaseID != snapshot.ReleaseID || len(chunks.Chunks) != 4 || len(chunks.Inputs) != 4 {
		t.Fatal("fixed RTW four-source chunk manifest is not complete")
	}
	byName := map[string]corpus.Chunk{}
	for _, chunk := range chunks.Chunks {
		if !chunk.Required || artifacts.Hash([]byte(chunk.Text)) != chunk.TextHash {
			t.Fatal("four-source native witness has non-required or changed content")
		}
		for name, text := range nativeUniqueTexts {
			if chunk.Text == text {
				if byName[name].ID != "" {
					t.Fatal("published release repeats one oracle source")
				}
				byName[name] = chunk
			}
		}
	}
	if len(byName) != 4 || byName["orange"].ID != fixture.ExpectedOrangeChunkID ||
		byName["orange"].RevisionID != fixture.ExpectedOrangeRevisionID {
		t.Fatal("Orange source identity is not dynamically mapped to RTW's approved revision")
	}
	projector, err := newRealNativeProjector(ctx, fixture.NativeRuntime, index.BuildID,
		objects, gate, settings.Dense, settings.Sparse, settings.MultiVector)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := projector.Close(); err != nil {
			t.Errorf("close native witness SDK: %v", err)
		}
	}()
	receipt := projector.Receipt()
	if receipt == nil || receipt.Endpoint != built.NativeProjection.Endpoint ||
		receipt.RuntimeSHA256 != built.NativeProjection.RuntimeSHA256 ||
		receipt.EnginePackageSHA256 != built.NativeProjection.EnginePackageSHA256 ||
		receipt.SettingsJCSSHA256 != built.NativeProjection.SettingsJCSSHA256 ||
		!reflect.DeepEqual(receipt.Settings, built.NativeProjection.Settings) {
		t.Fatal("native physical settings drifted between Build and published Search")
	}
	settingsJCS, err := jsoncanonicalizer.Transform(receipt.Settings)
	if err != nil || artifacts.Hash(settingsJCS) != receipt.SettingsJCSSHA256 ||
		projector.setting.SparseBackend != "frozen_ip_postings" {
		t.Fatal("published Hybrid settings did not pin the actual frozen learned-IP posting backend")
	}
	denseSnap, err := projector.dense.Load(ctx, snapshot.Indexes[searchdomain.Dense])
	if err != nil {
		t.Fatalf("load published native Dense: %v", err)
	}
	sparseSnap, err := projector.sparse.Load(ctx, snapshot.Indexes[searchdomain.Sparse])
	if err != nil {
		t.Fatalf("load published native learned Sparse: %v", err)
	}
	multiSnap, err := projector.multi.Load(ctx, snapshot.Indexes[searchdomain.MultiVector])
	if err != nil {
		t.Fatalf("load published native token Multi-vector: %v", err)
	}
	denseResult, err := denseSnap.Search(ctx, dense.Query{IndexRef: snapshot.Indexes[searchdomain.Dense],
		ModuleID: snapshot.ModuleID, ReleaseID: snapshot.ReleaseID,
		Generation: snapshot.Generation, ValidRevisionIDs: snapshot.ValidRevisionIDs,
		Text: fixture.Query, TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	sparseResult, err := sparseSnap.Search(ctx, sparse.Query{IndexRef: snapshot.Indexes[searchdomain.Sparse],
		ModuleID: snapshot.ModuleID, ReleaseID: snapshot.ReleaseID,
		Generation: snapshot.Generation, ValidRevisionIDs: snapshot.ValidRevisionIDs,
		Text: fixture.Query, TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if sparseResult.ExaminedPostings == nil || *sparseResult.ExaminedPostings < 1 {
		t.Fatal("learned Sparse search did not examine any actual frozen query postings")
	}
	multiResult, err := multiSnap.Search(ctx, multivector.Query{
		IndexRef: snapshot.Indexes[searchdomain.MultiVector], ModuleID: snapshot.ModuleID,
		ReleaseID: snapshot.ReleaseID, Generation: snapshot.Generation,
		ValidRevisionIDs: snapshot.ValidRevisionIDs, Text: fixture.Query, TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	getDense := make([]string, 0, len(denseResult.Candidates))
	for _, item := range denseResult.Candidates {
		getDense = append(getDense, item.Chunk.ID)
	}
	getSparse := make([]string, 0, len(sparseResult.Candidates))
	for _, item := range sparseResult.Candidates {
		getSparse = append(getSparse, item.Chunk.ID)
	}
	getMulti := make([]string, 0, len(multiResult.Candidates))
	var unique multivector.Candidate
	for _, item := range multiResult.Candidates {
		getMulti = append(getMulti, item.Chunk.ID)
		if item.Chunk.ID == byName["orange"].ID {
			unique = item
		}
	}
	if !reflect.DeepEqual(getDense, []string{byName["apple"].ID, byName["banana"].ID}) ||
		!reflect.DeepEqual(getSparse, []string{byName["apple"].ID}) ||
		!reflect.DeepEqual(getMulti, []string{byName["apple"].ID, byName["orange"].ID}) ||
		unique.Chunk.ID == "" {
		t.Fatalf("native ANN did not independently find Orange outside Dense/Sparse TopK: dense=%v sparse=%v multi=%v", getDense, getSparse, getMulti)
	}
	adapter, err := app.NewRTWSearchCitationAdapter(rtw)
	if err != nil {
		t.Fatal(err)
	}
	indexed := unique.Chunk
	indexed.Text = ""
	key := searchdomain.Key{SourceKind: indexed.SourceKind, ContentID: indexed.ContentID,
		RevisionID: indexed.RevisionID, ChunkID: indexed.ID}
	original, err := adapter.Read(ctx, snapshot, searchdomain.VerifiedCandidate{Key: key,
		Chunk: indexed, Sources: []searchdomain.LaneHit{{Lane: searchdomain.MultiVector,
			Index: snapshot.Indexes[searchdomain.MultiVector], Rank: unique.Rank,
			RawScore: unique.Score}}})
	if err != nil || original.Text != nativeUniqueTexts["orange"] ||
		original.TextHash != artifacts.Hash([]byte(nativeUniqueTexts["orange"])) ||
		original.Location.Locator == "" || !artifacts.ValidHash(original.Original.SHA256) {
		t.Fatalf("Orange native hit was not an actual RTW same-revision readable source: %+v %v", original, err)
	}
	effective, err := app.NewRTWEffectiveRevisionChecker(current)
	if err != nil {
		t.Fatal(err)
	}
	active, err := effective.Check(ctx, snapshot, original)
	if err != nil || !active {
		t.Fatalf("Orange original source is withdrawn or publication moved: %t %v", active, err)
	}
	result := nativeUniqueResult{SchemaVersion: "sea.search.native-independent-discovery.v1",
		ModuleID: snapshot.ModuleID, ReleaseID: snapshot.ReleaseID,
		Generation: snapshot.Generation, PublicationRevision: snapshot.PublicationRevision,
		Query: fixture.Query, NativeRuntimeSHA256: receipt.RuntimeSHA256,
		EnginePackageSHA256:    receipt.EnginePackageSHA256,
		SettingsJCSSHA256:      receipt.SettingsJCSSHA256,
		SparseBackend:          projector.setting.SparseBackend,
		SparseExaminedPostings: *sparseResult.ExaminedPostings,
		DenseChunkIDs:          getDense, SparseChunkIDs: getSparse,
		MultiVectorChunkIDs: getMulti, UniqueMultiChunkID: unique.Chunk.ID,
		UniqueRevisionID:      unique.Chunk.RevisionID,
		UniqueQuoteSHA256:     original.TextHash,
		UniqueOriginalSHA256:  original.Original.SHA256,
		UniqueLocator:         original.Location.Locator,
		RTWCurrentAndReadable: true}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(fixture.ResultPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("native unique witness result must be one private immutable file")
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(fixture.ResultPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("native unique discovery receipt must remain ordinary private bytes: %v %v", info, err)
	}
	t.Logf("native Lite/BGE independent Multi-vector discovery: one RTW Release, Dense=%v Sparse=%v Multi=%v OrangeRevision=%s",
		getDense, getSparse, getMulti, unique.Chunk.RevisionID)
}
