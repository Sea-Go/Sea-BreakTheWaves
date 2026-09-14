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
// and PostgreSQL acceptance. The parent supplies only process-local endpoints
// and a test key in a restricted fixture; it sends RTW's signed request itself.
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
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
		searches.Add(1)
		return searchdomain.Result{Status: "empty", StopReason: "no_evidence",
			Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
				RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
				PolicyVersion: "rtw-real-http-empty-v1"}, Snapshot: request.Snapshot,
			UsedSubqueries: 1}, nil
	})
	unexpected := errors.New("empty search must not read source, accept citations or call model")
	delivery, err := searchdomain.NewDelivery(searcher,
		searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
			return false, unexpected
		}),
		searchdomain.SourceReadFunc(func(context.Context, searchdomain.Snapshot, searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
			sourceReads.Add(1)
			return corpus.Chunk{}, unexpected
		}),
		searchdomain.AcceptFunc(func(context.Context, searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
			citationWrites.Add(1)
			return searchdomain.CitationReceipt{}, unexpected
		}), searchdomain.EvidenceLimits{MaxReads: 1, MaxQuoteRunes: 1})
	if err != nil {
		t.Fatal(err)
	}
	modelFixture := &unexpectedProductModel{calls: &modelCalls, err: unexpected}
	boundary, err := searchdomain.NewRootSessionBoundary(delivery, modelFixture, history, bundle)
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
		if searches.Load() == 0 || sourceReads.Load() != 0 || citationWrites.Load() != 0 || modelCalls.Load() != 0 {
			t.Errorf("real signed empty run missing or crossed forbidden dependency: searches=%d source=%d citation=%d model=%d",
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
