package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Native Graph event emission can finish after the caller observes Runner
// completion. The process logger owns a concurrency-safe Writer; fixtures
// must provide one as well when inspecting logs under -race.
type wikiNativeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *wikiNativeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *wikiNativeLogBuffer) Snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func wikiFactorySession(t *testing.T, account string, material byte) WikiCompileModelSession {
	t.Helper()
	bearer := "wh_access_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{material}, 32))
	session, err := NewWikiCompileModelSession(account, bearer)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func wikiFactoryInput() content.WikiCompileInput {
	first := "甲来源第一段。\n\n甲来源第二段注明版本。"
	second := "乙来源介绍外部知识。\n\n乙来源说明维护。"
	return content.WikiCompileInput{CompileID: "compile-17", ModuleID: "module-wiki", PageID: "《维护知识》",
		BaseRevision: "wiki-base-3", Generation: 7, InputHash: strings.Repeat("a", 64),
		AttemptID: "attempt-9", LeaseEpoch: 4, CancelVersion: 2, Guidance: "只使用已冻结的资料来源。",
		SourceIDs: []string{"source-rev-1", "source-rev-2"}, Sources: []content.WikiCompileSource{
			{RevisionID: "source-rev-1", Kind: "source", Title: "甲来源", Content: first, SHA256: artifacts.Hash([]byte(first))},
			{RevisionID: "source-rev-2", Kind: "source", Title: "乙来源", Content: second, SHA256: artifacts.Hash([]byte(second))},
		}}
}

func TestWikiNativeRunFactoryRejectsBindOrderAccountBAndWrongSourceBeforeHTTP(t *testing.T) {
	if os.Getenv("SEA_WIKI_NATIVE_FACTORY_ORDER_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWikiNativeRunFactoryRejectsBindOrderAccountBAndWrongSourceBeforeHTTP$")
		cmd.Env = append(os.Environ(), "SEA_WIKI_NATIVE_FACTORY_ORDER_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native factory order child failed: %v\n%s", err, output)
		}
		return
	}
	var outbound atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		outbound.Add(1)
		http.Error(w, "unexpected model call", http.StatusBadRequest)
	}))
	defer server.Close()
	accountA := "11111111-1111-4111-8111-111111111111"
	accountB := "22222222-2222-4222-8222-222222222222"
	a, b := wikiFactorySession(t, accountA, 7), wikiFactorySession(t, accountB, 8)
	var providers atomic.Int32
	provided := a
	provider := WikiCompileNativeSessionFunc(func(context.Context) (WikiCompileModelSession, error) {
		providers.Add(1)
		return provided, nil
	})
	factory, err := NewWikiCompileNativeRunFactory(WikiCompileNativeRunFactoryConfig{DCGatewayRoot: server.URL}, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	var logs wikiNativeLogBuffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-wiki-native-factory-order",
		Environment: "test", Version: "fixture-v1", InstanceID: "factory-order", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: tracetest.NewInMemoryExporter()})
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	if err := factory.BindTelemetry(observed); !errors.Is(err, ErrWikiCompileNativeFactory) {
		t.Fatalf("telemetry bound before native session/InstallGlobals: %v", err)
	}
	current, err := factory.CurrentNativeSession(context.Background())
	if err != nil || current.Proof() != a.Proof() || providers.Load() != 1 {
		t.Fatalf("factory did not freeze exactly one native account: %+v %v", current.Proof(), err)
	}
	provided = b
	if _, err := factory.CurrentNativeSession(context.Background()); !errors.Is(err, ErrWikiCompileModelIdentity) {
		t.Fatalf("DC session provider switched to account B after freeze: %v", err)
	}
	provided = wikiFactorySession(t, accountA, 9)
	if _, err := factory.CurrentNativeSession(context.Background()); !errors.Is(err, ErrWikiCompileModelIdentity) {
		t.Fatalf("same account token rotation reused a frozen worker: %v", err)
	}
	provided = a
	if err := factory.BindTelemetry(observed); !errors.Is(err, ErrWikiCompileNativeFactory) {
		t.Fatalf("uninstalled framework telemetry was accepted: %v", err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	if err := factory.BindTelemetry(observed); err != nil {
		t.Fatal(err)
	}
	if err := factory.BindTelemetry(observed); !errors.Is(err, ErrWikiCompileNativeFactory) {
		t.Fatalf("double BindTelemetry was accepted: %v", err)
	}
	if _, err := factory.Open(context.Background(), wikiFactoryInput(), b); !errors.Is(err, ErrWikiCompileModelIdentity) || outbound.Load() != 0 {
		t.Fatalf("different valid DC account reached model: %v calls=%d", err, outbound.Load())
	}
	wrong := wikiFactoryInput()
	wrong.Sources[0], wrong.Sources[1] = wrong.Sources[1], wrong.Sources[0]
	if _, err := factory.Open(context.Background(), wrong, a); !errors.Is(err, ErrWikiCompileSource) || outbound.Load() != 0 {
		t.Fatalf("source order differed from RTW frozen IDs: %v calls=%d", err, outbound.Load())
	}
	badHash := wikiFactoryInput()
	badHash.Sources[0].SHA256 = strings.Repeat("f", 64)
	if _, err := factory.Open(context.Background(), badHash, a); !errors.Is(err, ErrWikiCompileSource) || outbound.Load() != 0 {
		t.Fatalf("RTW source body hash changed: %v calls=%d", err, outbound.Load())
	}
	badToken := WikiCompileModelSession{AccountID: a.AccountID}
	if _, err := factory.Open(context.Background(), wikiFactoryInput(), badToken); !errors.Is(err, ErrWikiCompileModelIdentity) || outbound.Load() != 0 {
		t.Fatalf("unissued bearer reached model: %v calls=%d", err, outbound.Load())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := factory.Open(cancelled, wikiFactoryInput(), a); !errors.Is(err, context.Canceled) || outbound.Load() != 0 {
		t.Fatalf("cancelled Open constructed model: %v calls=%d", err, outbound.Load())
	}
	run, err := factory.Open(context.Background(), wikiFactoryInput(), a)
	if err != nil || run.ModelSessionProof() != a.Proof() {
		t.Fatalf("same frozen account failed to open official candidate: %v", err)
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background(), wikiFactoryInput(), a); !errors.Is(err, ErrWikiCompileNativeFactory) || outbound.Load() != 0 {
		t.Fatalf("closed telemetry Bundle allowed Open: %v calls=%d", err, outbound.Load())
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background(), wikiFactoryInput(), a); !errors.Is(err, ErrWikiCompileNativeFactory) || outbound.Load() != 0 {
		t.Fatalf("closed Factory opened a model: %v calls=%d", err, outbound.Load())
	}
	if bytes.Contains(logs.Snapshot(), []byte(a.NativeBearer())) || bytes.Contains(logs.Snapshot(), []byte(b.NativeBearer())) {
		t.Fatal("native bearer entered factory logs")
	}
}
