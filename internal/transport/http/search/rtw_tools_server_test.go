package search

import (
	"context"
	"encoding/json"
	"errors"
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
)

// TestRTWRealToolsSearchServer is a process-isolated BTW consumer for RTW's
// real User Center/PG Tool child test. Its empty fixture proves signed routing,
// native Graph execution and absence of reader/citation effects; it does not
// prove a populated local-exact index or a production search profile.
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
	unwanted := errors.New("empty Tools search cannot touch source or citation acceptor")
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
		searches.Add(1)
		if !reflect.DeepEqual(request.Snapshot, published) || request.Depth != searchdomain.Fast ||
			request.Intelligence != searchdomain.Low {
			return searchdomain.Result{}, errors.New("signed Tools scope differs from RTW publication or fast/low")
		}
		return searchdomain.Result{Status: "empty", StopReason: "no_evidence",
			Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
				RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
				PolicyVersion: "rtw-real-tools-empty-v1"}, Snapshot: request.Snapshot, UsedSubqueries: 1}, nil
	})
	checker := searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
		return false, unwanted
	})
	source := searchdomain.SourceReadFunc(func(context.Context, searchdomain.Snapshot, searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
		sourceReads.Add(1)
		return corpus.Chunk{}, unwanted
	})
	accept := searchdomain.AcceptFunc(func(context.Context, searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
		citationWrites.Add(1)
		return searchdomain.CitationReceipt{}, unwanted
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
	resolver, err := NewSignedToolsScopeResolver([]byte(fixture.ScopeKey))
	if err != nil {
		t.Fatal(err)
	}
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
		if searches.Load() != 1 || sourceReads.Load() != 0 || citationWrites.Load() != 0 {
			t.Errorf("real Tools empty run crossed dependency contract: searches=%d source=%d citations=%d",
				searches.Load(), sourceReads.Load(), citationWrites.Load())
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
