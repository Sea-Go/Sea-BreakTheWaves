package search

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
)

// TestRTWRealToolsSearchServer is a process-isolated BTW consumer for RTW's
// real User Center/PG Tool child test. An optional candidate names a genuinely
// published RTW chunk to exercise same-revision source and citation acceptance.
// Candidate selection is injected; this is not local-exact three-lane recall.
func TestRTWRealToolsSearchServer(t *testing.T) {
	fixturePath := os.Getenv("SEA_RTW_TOOLS_SERVER_FIXTURE")
	if fixturePath == "" {
		t.Skip("requires RTW's real HTTP/PostgreSQL Tools fixture")
	}
	info, err := os.Stat(fixturePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("Tools fixture must be a regular private file: %v", err)
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
	if err != nil || json.Unmarshal(raw, &fixture) != nil || fixture.RTWBase == "" ||
		fixture.WorkerToken == "" || len(fixture.ScopeKey) < 32 || fixture.ReadyPath == "" || fixture.ModuleID == "" {
		t.Fatal("incomplete RTW Tools server fixture")
	}
	client, err := ridethewind.New(httpclient.Config{BaseURL: fixture.RTWBase, Token: fixture.WorkerToken})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := app.NewRTWSearchSnapshotProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	published, err := provider.Current(context.Background(), fixture.ModuleID)
	if err != nil {
		t.Fatalf("read RTW current published snapshot: %v", err)
	}
	exporter := &traceSink{}
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-rtw-tools-fixture",
		Environment: "test", Version: "real-http-acceptance", InstanceID: "btw-tools-child",
		Output: os.Stdout, Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	var searches, sourceReads, citationWrites atomic.Int32
	witness := &toolWitnessRecorder{}
	unwanted := errors.New("empty Tools search cannot touch source or citation acceptor")
	cited := fixture.Candidate.ChunkID != ""
	var indexed corpus.Chunk
	var citationAdapter *app.RTWSearchCitationAdapter
	if cited {
		if fixture.Candidate.RevisionID == "" || fixture.Candidate.QuoteHash == "" {
			t.Fatal("incomplete published Tool candidate identity")
		}
		original, err := client.ReadSearchSource(context.Background(), ridethewind.ReadSearchSourceReq{
			ModuleId: published.ModuleID, ReleaseId: published.ReleaseID, Generation: published.Generation,
			PublicationRevision: published.PublicationRevision, RevisionId: fixture.Candidate.RevisionID,
			ChunkId: fixture.Candidate.ChunkID})
		if err != nil || original.TextHash != fixture.Candidate.QuoteHash {
			t.Fatalf("Tool candidate is not RTW's published same-revision source: %+v %v", original, err)
		}
		indexed = productIndexedChunk(original)
		citationAdapter, err = app.NewRTWSearchCitationAdapter(client)
		if err != nil {
			t.Fatal(err)
		}
	}
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
		searches.Add(1)
		if !reflect.DeepEqual(request.Snapshot, published) || request.Depth != searchdomain.Fast ||
			request.Intelligence != searchdomain.Low {
			return searchdomain.Result{}, errors.New("signed Tools scope differs from RTW publication or fast/low")
		}
		if cited {
			key := searchdomain.Key{SourceKind: indexed.SourceKind, ContentID: indexed.ContentID,
				RevisionID: indexed.RevisionID, ChunkID: indexed.ID}
			hit := searchdomain.LaneHit{Lane: searchdomain.Dense, Index: published.Indexes[searchdomain.Dense],
				Rank: 1, RawScore: 1}
			return searchdomain.Result{Status: "complete", StopReason: "batch_complete",
				Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
					RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
					PolicyVersion: "rtw-published-tools-candidate-v1"}, Snapshot: request.Snapshot,
				Candidates: []searchdomain.Candidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
					Sources: []searchdomain.LaneHit{hit}}},
				Verified: []searchdomain.VerifiedCandidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
					Sources: []searchdomain.LaneHit{hit}}}, UsedSubqueries: 1}, nil
		}
		return searchdomain.Result{Status: "empty", StopReason: "no_evidence",
			Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
				RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
				PolicyVersion: "rtw-real-tools-empty-v1"}, Snapshot: request.Snapshot, UsedSubqueries: 1}, nil
	})
	checker := searchdomain.CheckFunc(func(ctx context.Context, fixed searchdomain.Snapshot, candidate corpus.Chunk) (bool, error) {
		if !cited {
			return false, unwanted
		}
		current, err := provider.Current(ctx, fixed.ModuleID)
		if err != nil {
			return false, err
		}
		return reflect.DeepEqual(current, fixed) && candidate.RevisionID == indexed.RevisionID, nil
	})
	source := searchdomain.SourceReadFunc(func(ctx context.Context, fixed searchdomain.Snapshot, candidate searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
		sourceReads.Add(1)
		if !cited {
			return corpus.Chunk{}, unwanted
		}
		return citationAdapter.Read(ctx, fixed, candidate)
	})
	accept := searchdomain.AcceptFunc(func(ctx context.Context, pack searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
		citationWrites.Add(1)
		if !cited {
			return searchdomain.CitationReceipt{}, unwanted
		}
		receipt, err := citationAdapter.Accept(ctx, pack)
		if err != nil {
			return receipt, err
		}
		if os.Getenv("SEA_BTW_TOOLS_MATRIX_WITNESS_DIR") != "" {
			durable, err := client.GetSearchCitations(ctx, pack.SearchID)
			if err != nil {
				return searchdomain.CitationReceipt{}, err
			}
			witness.accepted(pack, receipt, durable)
		}
		return receipt, nil
	})
	delivery, err := searchdomain.NewDelivery(searcher, checker, source, accept,
		searchdomain.EvidenceLimits{MaxReads: 8, MaxQuoteRunes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := searchdomain.NewToolRunBoundary(delivery, bundle)
	if err != nil {
		t.Fatal(err)
	}
	signedResolver, err := NewSignedToolsScopeResolver([]byte(fixture.ScopeKey))
	if err != nil {
		t.Fatal(err)
	}
	resolver := ToolsScopeFunc(func(ctx context.Context, request *http.Request, body ToolsRequest) (TrustedToolsScope, error) {
		scope, err := signedResolver.ResolveTools(ctx, request, body)
		if err == nil && cited && os.Getenv("SEA_BTW_TOOLS_MATRIX_WITNESS_DIR") != "" {
			witness.scope(scope, body)
		}
		return scope, err
	})
	handler, err := NewToolsHandler(resolver, boundary, bundle)
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
		if searches.Load() != 1 || (!cited && (sourceReads.Load() != 0 || citationWrites.Load() != 0)) ||
			(cited && (sourceReads.Load() != 1 || citationWrites.Load() != 1)) {
			t.Errorf("real Tools run crossed dependency contract: cited=%t searches=%d source=%d citations=%d",
				cited, searches.Load(), sourceReads.Load(), citationWrites.Load())
		}
		var nativeRoot bool
		for _, span := range exporter.snapshot() {
			if span.Name() == "invoke_agent search_tools_root" && span.InstrumentationScope().Name == "trpc.agent.go" {
				nativeRoot = true
			}
		}
		if !nativeRoot {
			t.Error("native Tool Graph root Agent span missing")
		}
		if cited {
			if path, err := witness.write(os.Getenv("SEA_BTW_TOOLS_MATRIX_WITNESS_DIR"),
				sourceReads.Load(), citationWrites.Load(), exporter.snapshot()); err != nil {
				t.Errorf("write real RTW Tool matrix witness: %v", err)
			} else if path != "" {
				t.Logf("real RTW Tool matrix witness: %s", path)
			}
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	defer signal.Stop(stop)
	select {
	case <-stop:
	case <-time.After(120 * time.Second):
		t.Error("RTW parent did not terminate Tools child within 120 seconds")
	}
}
