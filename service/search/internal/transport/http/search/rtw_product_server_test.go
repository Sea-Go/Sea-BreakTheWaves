package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/app"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// TestRTWRealProductSearchServer is the BTW half of the RTW-owned real HTTP
// and PostgreSQL acceptance. The parent supplies only process-local endpoints,
// a test key and, for the cited path, a published chunk identity. The child
// rereads RTW's current publication and exact source before any citation.
func TestRTWRealProductSearchServer(t *testing.T) {
	fixturePath := os.Getenv("SEA_RTW_PRODUCT_SERVER_FIXTURE")
	if fixturePath == "" {
		t.Skip("requires RTW's real HTTP/PostgreSQL product search fixture")
	}
	info, err := os.Stat(fixturePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("product fixture must be a regular private file: %v", err)
	}
	var fixture struct {
		BuildOnly   bool             `json:"build_only"`
		RTWBase     string           `json:"rtw_base"`
		WorkerToken string           `json:"worker_token"`
		ScopeKey    string           `json:"scope_key"`
		ReadyPath   string           `json:"ready_path"`
		ModuleID    string           `json:"module_id"`
		RealIndex   realIndexFixture `json:"real_index"`
		Candidate   struct {
			RevisionID string `json:"revision_id"`
			ChunkID    string `json:"chunk_id"`
			QuoteHash  string `json:"quote_hash"`
		} `json:"candidate"`
		History struct {
			Budget     searchdomain.RootHistoryBudget `json:"budget"`
			ResultPath string                         `json:"result_path"`
		} `json:"history"`
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.RTWBase == "" ||
		fixture.WorkerToken == "" || len(fixture.ScopeKey) < 32 || fixture.ReadyPath == "" {
		t.Fatal("incomplete RTW product server fixture")
	}
	// The history round is a cited two-search same-session mode: the second
	// model prompt must carry RTW's first accepted turn under this budget.
	historyRound := fixture.History.ResultPath != ""
	if historyRound && (fixture.Candidate.ChunkID == "" || !fixture.History.Budget.Valid()) {
		t.Fatal("history round requires the cited path and one explicit budget pair")
	}
	if fixture.BuildOnly {
		if fixture.RealIndex.DCRuntime == "" || fixture.RealIndex.ResultPath == "" {
			t.Fatal("formal API mode requires a real three-lane index build")
		}
		if _, err := buildRTWRealThreeLane(context.Background(), fixture.RealIndex); err != nil {
			t.Fatalf("build real three-lane artifacts for formal cmd/api: %v", err)
		}
		return
	}
	client, err := ridethewind.New(httpclient.Config{BaseURL: fixture.RTWBase, Token: fixture.WorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	history, err := app.NewRTWAcceptedRootHistory(client)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewSignedScopeResolver([]byte(fixture.ScopeKey))
	if err != nil {
		t.Fatal(err)
	}
	exporter := &traceSink{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{
		Service: "btw-search-rtw-product-fixture", Environment: "test",
		Version: "real-http-acceptance", InstanceID: "btw-product-child",
		Output: os.Stdout, Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var searches, sourceReads, citationWrites, modelCalls atomic.Int32
	var searcher searchdomain.SearchExecutor
	var checker searchdomain.EffectiveRevisionChecker
	var source searchdomain.SourceReader
	var accept searchdomain.CitationAcceptor
	var productModel model.Model
	unexpected := errors.New("empty search must not read source, accept citations or call model")
	cited := fixture.Candidate.ChunkID != ""
	if fixture.RealIndex.DCRuntime != "" {
		if !cited || fixture.ModuleID == "" || fixture.Candidate.RevisionID == "" ||
			fixture.Candidate.QuoteHash == "" {
			t.Fatal("real three-lane path requires a fixed published source identity")
		}
		lanes, err := buildRTWRealThreeLane(context.Background(), fixture.RealIndex)
		if err != nil {
			t.Fatalf("build three actual DC BGE lanes before RTW publication: %v", err)
		}
		provider, err := app.NewRTWSearchSnapshotProvider(client)
		if err != nil {
			t.Fatal(err)
		}
		actualChecker, err := app.NewRTWEffectiveRevisionChecker(provider)
		if err != nil {
			t.Fatal(err)
		}
		planner := searchdomain.PlanFunc(func(_ context.Context, in searchdomain.PlanInput) ([]string, error) {
			if in.Round != 1 || in.Depth != searchdomain.Fast || in.Intelligence != searchdomain.Low {
				return nil, searchdomain.ErrUnavailable
			}
			return []string{in.Query}, nil
		})
		actual, err := searchdomain.New(lanes.dense, lanes.sparse, lanes.multi, planner, actualChecker,
			searchdomain.Policy{Version: "real-bge-three-lane-fast-low-v1", Profiles: map[searchdomain.Depth]map[searchdomain.Intelligence]searchdomain.Limits{
				searchdomain.Fast: {searchdomain.Low: {MaxBatches: 1, MaxSubqueries: 1, TopKPerLane: 2,
					MaxEvidence: 1, WallTime: 50 * time.Second}},
			}})
		if err != nil {
			t.Fatal(err)
		}
		searcher = searchdomain.ExecuteFunc(func(ctx context.Context, request searchdomain.Request) (searchdomain.Result, error) {
			searches.Add(1)
			for _, lane := range []searchdomain.Lane{searchdomain.Dense, searchdomain.Sparse, searchdomain.MultiVector} {
				if request.Snapshot.Indexes[lane] != lanes.result.Indexes[string(lane)] {
					return searchdomain.Result{}, errors.New("RTW published index differs from actual BGE lane")
				}
			}
			found, err := actual.Execute(ctx, request)
			if err != nil {
				return found, err
			}
			if len(found.Verified) == 0 || found.Verified[0].Key.ChunkID != fixture.Candidate.ChunkID ||
				len(found.Verified[0].Sources) != 3 || len(found.LaneStatus) != 3 {
				return searchdomain.Result{}, errors.New("real three-lane recall did not rank the RTW source first in all lanes")
			}
			for _, status := range found.LaneStatus {
				if !status.Executed || status.Failed || status.CandidateCount < 1 {
					return searchdomain.Result{}, errors.New("real three-lane recall was incomplete")
				}
			}
			return found, nil
		})
		adapter, err := app.NewRTWSearchCitationAdapter(client)
		if err != nil {
			t.Fatal(err)
		}
		checker = actualChecker
		source = searchdomain.SourceReadFunc(func(ctx context.Context, fixed searchdomain.Snapshot, candidate searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
			sourceReads.Add(1)
			return adapter.Read(ctx, fixed, candidate)
		})
		accept = searchdomain.AcceptFunc(func(ctx context.Context, pack searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
			citationWrites.Add(1)
			return adapter.Accept(ctx, pack)
		})
		productModel = &citedProductModel{client: client, calls: &modelCalls,
			expectedQuote: fixture.RealIndex.ExpectedQuote, expectedHash: fixture.Candidate.QuoteHash}
	} else if cited {
		if fixture.ModuleID == "" || fixture.Candidate.RevisionID == "" || fixture.Candidate.QuoteHash == "" {
			t.Fatal("incomplete published candidate identity")
		}
		provider, err := app.NewRTWSearchSnapshotProvider(client)
		if err != nil {
			t.Fatal(err)
		}
		published, err := provider.Current(context.Background(), fixture.ModuleID)
		if err != nil {
			t.Fatalf("read actual RTW published snapshot: %v", err)
		}
		original, err := client.ReadSearchSource(context.Background(), ridethewind.ReadSearchSourceReq{
			ModuleId: published.ModuleID, ReleaseId: published.ReleaseID,
			Generation: published.Generation, PublicationRevision: published.PublicationRevision,
			RevisionId: fixture.Candidate.RevisionID, ChunkId: fixture.Candidate.ChunkID,
		})
		if err != nil || original.TextHash != fixture.Candidate.QuoteHash {
			t.Fatalf("candidate is not RTW's published same-revision source: %+v %v", original, err)
		}
		indexed := productIndexedChunk(original)
		key := searchdomain.Key{SourceKind: indexed.SourceKind, ContentID: indexed.ContentID,
			RevisionID: indexed.RevisionID, ChunkID: indexed.ID}
		hit := searchdomain.LaneHit{Lane: searchdomain.Dense, Index: published.Indexes[searchdomain.Dense],
			Rank: 1, RawScore: 1}
		searcher = searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
			searches.Add(1)
			if !reflect.DeepEqual(request.Snapshot, published) {
				return searchdomain.Result{}, errors.New("signed snapshot differs from RTW publication")
			}
			return searchdomain.Result{Status: "complete", StopReason: "batch_complete",
				Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
					RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
					PolicyVersion: "rtw-published-candidate-acceptance-v1"}, Snapshot: request.Snapshot,
				Candidates: []searchdomain.Candidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
					Sources: []searchdomain.LaneHit{hit}}},
				Verified: []searchdomain.VerifiedCandidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
					Sources: []searchdomain.LaneHit{hit}}}, UsedSubqueries: 1}, nil
		})
		adapter, err := app.NewRTWSearchCitationAdapter(client)
		if err != nil {
			t.Fatal(err)
		}
		checker = searchdomain.CheckFunc(func(ctx context.Context, fixed searchdomain.Snapshot, chunk corpus.Chunk) (bool, error) {
			current, err := provider.Current(ctx, fixed.ModuleID)
			if err != nil {
				return false, err
			}
			return reflect.DeepEqual(current, fixed) && chunk.RevisionID == indexed.RevisionID, nil
		})
		source = searchdomain.SourceReadFunc(func(ctx context.Context, fixed searchdomain.Snapshot, candidate searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
			sourceReads.Add(1)
			return adapter.Read(ctx, fixed, candidate)
		})
		accept = searchdomain.AcceptFunc(func(ctx context.Context, pack searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
			citationWrites.Add(1)
			return adapter.Accept(ctx, pack)
		})
		productModel = &citedProductModel{client: client, calls: &modelCalls,
			expectedQuote: original.Text, expectedHash: original.TextHash}
	} else {
		searcher = searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
			searches.Add(1)
			return searchdomain.Result{Status: "empty", StopReason: "no_evidence",
				Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
					RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
					PolicyVersion: "rtw-real-http-empty-v1"}, Snapshot: request.Snapshot,
				UsedSubqueries: 1}, nil
		})
		checker = searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
			return false, unexpected
		})
		source = searchdomain.SourceReadFunc(func(context.Context, searchdomain.Snapshot, searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
			sourceReads.Add(1)
			return corpus.Chunk{}, unexpected
		})
		accept = searchdomain.AcceptFunc(func(context.Context, searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
			citationWrites.Add(1)
			return searchdomain.CitationReceipt{}, unexpected
		})
		productModel = &unexpectedProductModel{calls: &modelCalls, err: unexpected}
	}
	delivery, err := searchdomain.NewDelivery(searcher, checker, source, accept,
		searchdomain.EvidenceLimits{MaxReads: 1, MaxQuoteRunes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	var boundary *searchdomain.RootSessionBoundary
	if historyRound {
		boundary, err = searchdomain.NewRootSessionBoundaryWithHistorySeed(delivery, productModel, history, bundle,
			fixture.History.Budget)
	} else {
		boundary, err = searchdomain.NewRootSessionBoundary(delivery, productModel, history, bundle)
	}
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(resolver, boundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var historyModel *citedProductModel
	if historyRound {
		citedModel, ok := productModel.(*citedProductModel)
		if !ok {
			t.Fatal("history round requires the cited fixed model")
		}
		historyModel = citedModel
	}
	var summarized atomic.Int32
	counting := http.Handler(handler)
	if historyRound {
		counting = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r)
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/search/summary") {
				return
			}
			// Write the receipt as soon as the second same-session search has
			// finished, so the RTW parent can reconcile it mid-test.
			if summarized.Add(1) == 2 {
				writeHistoryRoundReceipt(t, fixture.History.ResultPath, historyModel, fixture.History.Budget)
			}
		})
	}
	server := httptest.NewServer(counting)
	if err := os.WriteFile(fixture.ReadyPath, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		server.Close()
		if err := boundary.Close(); err != nil {
			t.Error(err)
		}
		if err := bundle.Close(context.Background()); err != nil {
			t.Error(err)
		}
		want := int32(1)
		if historyRound {
			want = 2
		}
		if searches.Load() != want || (cited && (sourceReads.Load() != want || citationWrites.Load() != want || modelCalls.Load() != want)) ||
			(!cited && (sourceReads.Load() != 0 || citationWrites.Load() != 0 || modelCalls.Load() != 0)) {
			t.Errorf("real signed product run crossed dependency contract: cited=%v history=%v searches=%d source=%d citation=%d model=%d",
				cited, historyRound, searches.Load(), sourceReads.Load(), citationWrites.Load(), modelCalls.Load())
		}
		var nativeRoot bool
		for _, span := range exporter.snapshot() {
			if span.Name() == "invoke_agent search_summary_root" && span.InstrumentationScope().Name == "trpc.agent.go" {
				nativeRoot = true
			}
		}
		if !nativeRoot {
			t.Error("framework-native root Agent span missing from real signed HTTP run")
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	defer signal.Stop(stop)
	parentTimeout := 120 * time.Second
	if value := os.Getenv("SEA_RTW_PRODUCT_SERVER_PARENT_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 120*time.Second || parsed > 20*time.Minute {
			t.Fatalf("invalid RTW product server parent timeout: %q", value)
		}
		parentTimeout = parsed
	}
	select {
	case <-stop:
	case <-time.After(parentTimeout):
		t.Errorf("RTW parent did not terminate product search child within %s", parentTimeout)
	}
}

type unexpectedProductModel struct {
	calls *atomic.Int32
	err   error
}

func (*unexpectedProductModel) Info() model.Info { return model.Info{Name: "must-not-run"} }

func (m *unexpectedProductModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	return nil, fmt.Errorf("unexpected product model call: %w", m.err)
}

var _ model.Model = (*unexpectedProductModel)(nil)

func productIndexedChunk(c ridethewind.CitationChunk) corpus.Chunk {
	return corpus.Chunk{ID: c.ChunkId, RevisionID: c.RevisionId, ContentID: c.ContentId,
		SourceKind: c.SourceKind, Original: corpus.Ref{Key: c.Original.Key, SHA256: c.Original.Sha256},
		Location: corpus.Location{Locator: c.Location.Locator, OriginalByteStart: c.Location.OriginalByteStart,
			OriginalByteEnd: c.Location.OriginalByteEnd, NormalizedRuneStart: c.Location.NormalizedRuneStart,
			NormalizedRuneEnd: c.Location.NormalizedRuneEnd},
		TextHash: c.TextHash, EncodingKey: c.EncodingKey, DuplicateOf: c.DuplicateOf,
		PreviousID: c.PreviousId, NextID: c.NextId, Required: c.Required}
}

// This fixed model exercises the framework response boundary only. It does
// not claim provider quality; the quote and receipt must come from RTW.
type citedProductModel struct {
	client        *ridethewind.Client
	calls         *atomic.Int32
	expectedQuote string
	expectedHash  string
	historyMu     sync.Mutex
	historyBlocks []string
}

func (*citedProductModel) Info() model.Info { return model.Info{Name: "fixed-cited-product-model"} }

// writeHistoryRoundReceipt is the child half of the same-father injection
// gate: after two cited same-session searches it pins exactly what the second
// model prompt carried from RTW's accepted history. One private O_EXCL file;
// the RTW parent reconciles it against its own committed rows.
func writeHistoryRoundReceipt(t *testing.T, path string, m *citedProductModel, budget searchdomain.RootHistoryBudget) {
	t.Helper()
	m.historyMu.Lock()
	blocks := append([]string(nil), m.historyBlocks...)
	m.historyMu.Unlock()
	if len(blocks) != 2 {
		t.Fatalf("history round expected two model calls, saw %d", len(blocks))
	}
	if blocks[0] != "" {
		t.Fatalf("first same-session model call already carried history: %s", blocks[0])
	}
	var injected struct {
		Turns []struct {
			SearchID  string `json:"search_id"`
			AnswerID  string `json:"answer_id"`
			Question  string `json:"question"`
			Answer    string `json:"answer"`
			Citations []struct {
				EvidenceID string `json:"evidence_id"`
				Quote      string `json:"quote"`
			} `json:"verified_citations"`
		} `json:"accepted_session_history"`
		Budget searchdomain.RootHistoryBudget `json:"accepted_history_budget"`
	}
	if err := json.Unmarshal([]byte(blocks[1]), &injected); err != nil || len(injected.Turns) != 1 ||
		injected.Budget != budget || injected.Turns[0].Answer == "" ||
		len(injected.Turns[0].Citations) != 1 || injected.Turns[0].Citations[0].Quote == "" {
		t.Fatalf("second model call did not carry exactly the first accepted turn: %s", blocks[1])
	}
	receipt := struct {
		SchemaVersion string                         `json:"schema_version"`
		Budget        searchdomain.RootHistoryBudget `json:"budget"`
		BlockSHA256   string                         `json:"block_sha256"`
		SearchID      string                         `json:"first_search_id"`
		AnswerID      string                         `json:"first_answer_id"`
		Question      string                         `json:"first_question"`
		Answer        string                         `json:"first_answer"`
		EvidenceID    string                         `json:"first_evidence_id"`
		Quote         string                         `json:"first_quote"`
	}{"sea.btw.search.history-injection-real.v1", budget, "",
		injected.Turns[0].SearchID, injected.Turns[0].AnswerID,
		injected.Turns[0].Question, injected.Turns[0].Answer,
		injected.Turns[0].Citations[0].EvidenceID, injected.Turns[0].Citations[0].Quote}
	// Hash the raw block bytes, not a re-encoding.
	digest := sha256.Sum256([]byte(blocks[1]))
	receipt.BlockSHA256 = hex.EncodeToString(digest[:])
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func (m *citedProductModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	var prompt struct {
		Pack searchdomain.EvidencePack `json:"fixed_evidence_pack"`
		Hist json.RawMessage           `json:"accepted_session_history"`
	}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == model.RoleUser {
			if err := json.Unmarshal([]byte(request.Messages[i].Content), &prompt); err != nil {
				return nil, err
			}
			break
		}
	}
	m.historyMu.Lock()
	m.historyBlocks = append(m.historyBlocks, string(prompt.Hist))
	m.historyMu.Unlock()
	if len(prompt.Pack.Evidence) != 1 || prompt.Pack.Evidence[0].Quote != m.expectedQuote ||
		prompt.Pack.Evidence[0].QuoteHash != m.expectedHash || len(request.Tools) != 0 {
		return nil, errors.New("model received unverified or unfixed evidence")
	}
	record, err := m.client.GetSearchCitations(ctx, prompt.Pack.SearchID)
	if err != nil {
		return nil, fmt.Errorf("read RTW durable citation before model response: %w", err)
	}
	packHash, err := prompt.Pack.Hash()
	if err != nil {
		return nil, err
	}
	if record.PackHash != packHash || record.DurableRef == "" || len(record.Evidence) != 1 ||
		record.Evidence[0].QuoteHash != m.expectedHash {
		return nil, errors.New("model ran before RTW durable citation acceptance")
	}
	answer, err := json.Marshal(struct {
		Answer    string   `json:"answer"`
		Citations []string `json:"citations"`
	}{"The published source states: " + m.expectedQuote, []string{prompt.Pack.Evidence[0].ID}})
	if err != nil {
		return nil, err
	}
	ch := make(chan *model.Response, 1)
	finish := "stop"
	ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(string(answer)), FinishReason: &finish}}}
	close(ch)
	return ch, nil
}

var _ model.Model = (*citedProductModel)(nil)
