package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/signal"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
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
		RTWBase     string `json:"rtw_base"`
		WorkerToken string `json:"worker_token"`
		ScopeKey    string `json:"scope_key"`
		ReadyPath   string `json:"ready_path"`
		ModuleID    string `json:"module_id"`
		Candidate   struct {
			RevisionID string `json:"revision_id"`
			ChunkID    string `json:"chunk_id"`
			QuoteHash  string `json:"quote_hash"`
		} `json:"candidate"`
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.RTWBase == "" ||
		fixture.WorkerToken == "" || len(fixture.ScopeKey) < 32 || fixture.ReadyPath == "" {
		t.Fatal("incomplete RTW product server fixture")
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
	if cited {
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
	boundary, err := searchdomain.NewRootSessionBoundary(delivery, productModel, history, bundle)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(resolver, boundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
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
		if searches.Load() != 1 || (cited && (sourceReads.Load() != 1 || citationWrites.Load() != 1 || modelCalls.Load() != 1)) ||
			(!cited && (sourceReads.Load() != 0 || citationWrites.Load() != 0 || modelCalls.Load() != 0)) {
			t.Errorf("real signed product run crossed dependency contract: cited=%v searches=%d source=%d citation=%d model=%d",
				cited,
				searches.Load(), sourceReads.Load(), citationWrites.Load(), modelCalls.Load())
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
	select {
	case <-stop:
	case <-time.After(120 * time.Second):
		t.Error("RTW parent did not terminate product search child within 120 seconds")
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
}

func (*citedProductModel) Info() model.Info { return model.Info{Name: "fixed-cited-product-model"} }

func (m *citedProductModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	var prompt struct {
		Pack searchdomain.EvidencePack `json:"fixed_evidence_pack"`
	}
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == model.RoleUser {
			if err := json.Unmarshal([]byte(request.Messages[i].Content), &prompt); err != nil {
				return nil, err
			}
			break
		}
	}
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
