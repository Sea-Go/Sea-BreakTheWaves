package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func TestFactWorkerFavoriteAuthorityReal(t *testing.T) {
	for _, key := range []string{"SEA_FACT_AUTHORITY_URL", "SEA_FACT_AUTHORITY_TOKEN", "SEA_FACT_DC_URL",
		"SEA_FACT_DC_TOKEN", "SEA_FACT_ASSERT_EVENT_ID", "SEA_FACT_RETRACT_EVENT_ID", "USERMODEL_TEST_POSTGRES_DSN"} {
		if os.Getenv(key) == "" {
			t.Skip("run through isolated shared RTW/DC/BTW acceptance")
		}
	}
	if os.Getenv("SEA_FACT_AUTHORITY_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactWorkerFavoriteAuthorityReal$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_FACT_AUTHORITY_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("real favorite authority child: %v\n%s", err, output)
		}
		if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	assertFactWorkerFavoriteAuthorityReal(t)
}

func favoriteAuthorityWorker(t *testing.T, source FactEventSource, binder TrustedFactEvidenceBinder,
	graph FactGraphCommitter, store CurrentFactReceiptReader, observed *telemetry.Bundle) *FactWorker {
	t.Helper()
	worker, err := NewFactWorker(FactWorkerConfig{Consumer: "btw-favorite-authority", Producer: favoriteFactProducer, BatchLimit: 10,
		Bindings: []FactEventBinding{
			{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, EvidenceBinder: binder},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, EvidenceBinder: binder},
		}}, source, nil, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func favoriteAuthorityTamperProxy(t *testing.T, upstream, mode string) *httptest.Server {
	t.Helper()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.RequestURI()
		if mode == "missing" {
			path = "/internal/v1/favorite/facts/rtw.community.favorite/favorite.unknown.v1"
		}
		request, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+path, nil)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		request.Header = r.Header.Clone()
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			w.WriteHeader(response.StatusCode)
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var fact map[string]json.RawMessage
		if json.Unmarshal(body, &fact) != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch mode {
		case "hash":
			fact["source_event_hash"] = json.RawMessage(`"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
		case "subject":
			fact["subject_ref"] = json.RawMessage(`{"authority_id":"forged","tenant_id":"other","subject_id":"impostor"}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fact)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

func assertFactWorkerFavoriteAuthorityReal(t *testing.T) {
	ctx := context.Background()
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "btw-favorite-authority-test", Environment: "test",
		Version: "fixed-v1.8.1", InstanceID: "authority-real", Output: os.Stderr, Level: slog.LevelInfo,
		TraceExporter: &factSpanExporter{}, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observed.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	pool := factWorkerPool(t)
	store := usermodel.NewStore(pool, observed)
	graph, err := usermodel.NewFactGraphRuntime("favorite-authority-real", store, inmemory.NewSessionService(), observed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := graph.Close(); err != nil {
			t.Error(err)
		}
	}()
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := client.ReadEvents(ctx, "btw-favorite-authority", favoriteFactProducer, 10)
	if err != nil || len(batch.Events) != 2 || batch.FromOffset < 1 || batch.ToOffset != batch.FromOffset+1 ||
		batch.Events[0].Event.EventID != os.Getenv("SEA_FACT_ASSERT_EVENT_ID") ||
		batch.Events[1].Event.EventID != os.Getenv("SEA_FACT_RETRACT_EVENT_ID") {
		t.Fatalf("real RTW/DC favorite source batch: %+v %v", batch, err)
	}
	for _, item := range batch.Events {
		id, err := strconv.ParseInt(item.Event.AggregateID, 10, 64)
		if err != nil || id <= maxLegacyExactJSONID || item.Offset < 1 {
			t.Fatalf("high-ID source was not used: %+v", item)
		}
	}
	newBinder := func(endpoint, token string) *FavoriteAuthorityBinder {
		t.Helper()
		binder, err := NewFavoriteAuthorityBinder(FavoriteAuthorityBinderConfig{BaseURL: endpoint, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		return binder
	}
	authorityURL, authorityToken := os.Getenv("SEA_FACT_AUTHORITY_URL"), os.Getenv("SEA_FACT_AUTHORITY_TOKEN")
	wrongToken := strings.Repeat("x", len(authorityToken))
	noAuthority := favoriteAuthorityWorker(t, client, newBinder(authorityURL, wrongToken), graph, store, observed)
	if result, err := noAuthority.RunOnce(ctx); !errors.Is(err, ErrFactAuthorityUnavailable) || result.AckedOffset != 0 || result.Count != 0 {
		t.Fatalf("unauthorized authority escaped ACK: %+v %v", result, err)
	}
	for _, mode := range []string{"missing", "hash", "subject"} {
		proxy := favoriteAuthorityTamperProxy(t, authorityURL, mode)
		worker := favoriteAuthorityWorker(t, client, newBinder(proxy.URL, authorityToken), graph, store, observed)
		expected := ErrFactDeliveryContract
		if mode == "missing" {
			expected = ErrFactAuthorityUnavailable
		}
		if result, err := worker.RunOnce(ctx); !errors.Is(err, expected) || result.AckedOffset != 0 || result.Count != 0 {
			t.Fatalf("tampered RTW %s escaped ACK: %+v %v", mode, result, err)
		}
	}
	before, err := client.ReadEvents(ctx, "btw-favorite-authority", favoriteFactProducer, 10)
	if err != nil || before.FromOffset != batch.FromOffset || before.ToOffset != batch.ToOffset {
		t.Fatalf("failed binds moved DC cursor: %+v %v", before, err)
	}
	real := favoriteAuthorityWorker(t, &failFirstFactACK{Client: client, fail: true},
		newBinder(authorityURL, authorityToken), graph, store, observed)
	first, err := real.RunOnce(ctx)
	if err == nil || first.Count != 2 || first.NewFacts != 2 || first.AckedOffset != 0 {
		t.Fatalf("real authority facts committed before uncertain ACK: %+v %v", first, err)
	}
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	current, err := store.Current(ctx, subject)
	if err != nil || current.StateVersion != 2 || len(current.Active) != 0 {
		t.Fatalf("real authority assert/retract state: %+v %v", current, err)
	}
	outbox, err := store.OutboxAfter(ctx, subject, 0, 10)
	if err != nil || len(outbox) != 2 {
		t.Fatalf("real authority accepted Outbox: %+v %v", outbox, err)
	}
	replayed, err := real.RunOnce(ctx)
	if err != nil || replayed.Replayed != 2 || replayed.NewFacts != 0 || replayed.AckedOffset != batch.ToOffset {
		t.Fatalf("real authority replay/ACK: %+v %v", replayed, err)
	}
	for _, item := range batch.Events {
		receipt, err := store.CurrentReceipt(ctx, subject, usermodel.EventKey{Producer: favoriteFactProducer, EventID: item.Event.EventID})
		if err != nil || receipt.Status != "accepted" || receipt.StateVersion < 1 {
			t.Fatalf("real source item lacks accepted receipt: %+v %v", receipt, err)
		}
	}
	remaining, err := client.ReadEvents(ctx, "btw-favorite-authority", favoriteFactProducer, 10)
	if err != nil || len(remaining.Events) != 0 || remaining.FromOffset != batch.ToOffset+1 {
		t.Fatalf("DC cursor not acknowledged exactly once: %+v %v", remaining, err)
	}
}
