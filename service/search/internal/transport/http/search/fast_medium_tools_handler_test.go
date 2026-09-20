package search

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

type mediumToolsProtocolExecutor struct {
	delivery     *searchdomain.Delivery
	searches     atomic.Int32
	preflights   atomic.Int32
	preflightErr error
}

func (e *mediumToolsProtocolExecutor) PreflightProfile(_ context.Context, q searchdomain.Request) error {
	e.preflights.Add(1)
	if q.Depth != searchdomain.Fast || q.Intelligence != searchdomain.Medium {
		return searchdomain.ErrUnavailable
	}
	return e.preflightErr
}

func (e *mediumToolsProtocolExecutor) Search(ctx context.Context, q searchdomain.ToolRunRequest) (searchdomain.SearchResult, error) {
	e.searches.Add(1)
	return e.delivery.SearchWithin(ctx, q.SearchID, q.Search, q.Limits)
}

func TestFastMediumToolsSignedHTTPIsExplicitAndBare(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_TOOLS_HTTP_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumToolsSignedHTTPIsExplicitAndBare$", "-test.v")
		command.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_TOOLS_HTTP_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("signed medium Tools HTTP child failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-medium-tools-http-fixture",
		Environment: "test", Version: "fixture-v1", InstanceID: "medium-http", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &traceSink{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	key := bytes.Repeat([]byte("t"), 32)
	now := time.Unix(1_700_000_000, 0)
	resolver, err := NewSignedToolsScopeResolver(key)
	if err != nil {
		t.Fatal(err)
	}
	resolver.now = func() time.Time { return now }
	body, scope, _ := toolsFixture(t, key, now)
	body.Intelligence = searchdomain.Medium
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	scope.RequestHash = hex.EncodeToString(hash[:])
	signed, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	header := signedHeader(key, signed)
	var accepted atomic.Bool
	var reads atomic.Int32
	executor := &mediumToolsProtocolExecutor{delivery: fixedDelivery(t, &accepted, fixedSnapshot(), &reads)}
	send := func(handler http.Handler) (int, []byte) {
		t.Helper()
		server := httptest.NewServer(handler)
		defer server.Close()
		request, err := http.NewRequest(http.MethodPost, server.URL+ToolsRoute, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(toolsScopeHeader, header)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, content
	}
	legacy, err := NewToolsHandler(resolver, executor, observed)
	if err != nil {
		t.Fatal(err)
	}
	status, response := send(legacy)
	if status != http.StatusServiceUnavailable || !bytes.Contains(response, []byte("TOOLS_PROFILE_UNAVAILABLE")) ||
		executor.searches.Load() != 0 || executor.preflights.Load() != 0 || reads.Load() != 0 || accepted.Load() {
		t.Fatalf("default-off medium Tools touched a dependency: status=%d body=%s", status, response)
	}
	enabled, err := NewToolsHandlerWithFastMedium(resolver, executor, observed)
	if err != nil {
		t.Fatal(err)
	}
	executor.preflightErr = searchdomain.ErrUnavailable
	status, response = send(enabled)
	if status != http.StatusServiceUnavailable || !bytes.Contains(response, []byte("TOOLS_PROFILE_UNAVAILABLE")) ||
		executor.searches.Load() != 0 || reads.Load() != 0 || accepted.Load() {
		t.Fatalf("signed medium without effective policy touched a dependency: status=%d body=%s", status, response)
	}
	executor.preflightErr = nil
	status, response = send(enabled)
	var public ToolsResponse
	if err := json.Unmarshal(response, &public); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || public.RequestedIntelligence != searchdomain.Medium ||
		public.EffectiveIntelligence != searchdomain.Medium || public.SearchID != body.SearchID ||
		public.SnapshotRef != scope.SnapshotRef || len(public.Evidence) != 1 || public.CitationReceipt == nil ||
		public.CitationReceipt.PackHash != public.PackHash || executor.searches.Load() != 1 ||
		reads.Load() != 1 || !accepted.Load() || bytes.Contains(response, []byte(`"answer"`)) {
		t.Fatalf("enabled signed medium HTTP did not return bare accepted evidence: status=%d result=%+v", status, public)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"search.http.tools"`)) &&
		!strings.Contains(logs.String(), "search.http.tools") {
		t.Fatal("structured Tools HTTP outcome missing")
	}
}
