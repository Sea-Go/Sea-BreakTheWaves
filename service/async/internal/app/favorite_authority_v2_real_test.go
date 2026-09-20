package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// The dedicated shell script supplies one disposable PG16, actual RTW fact
// dispatcher/private authority, and actual DataCenter cmd/platform. This
// child owns tRPC-Agent-Go's process-global native telemetry installation.
func TestFactWorkerFavoriteAuthorityV2MixedUsers(t *testing.T) {
	for _, key := range []string{"SEA_FACT_AUTHORITY_URL", "SEA_FACT_AUTHORITY_TOKEN", "SEA_FACT_DC_URL",
		"SEA_FACT_DC_TOKEN", "SEA_FACT_EVENT_IDS", "SEA_FACT_SCHEMA_VERSIONS", "USERMODEL_TEST_POSTGRES_DSN"} {
		if os.Getenv(key) == "" {
			t.Skip("use favorite_subjectref_v2_acceptance.sh with isolated RTW/DC/PG16")
		}
	}
	if os.Getenv("SEA_FACT_V2_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactWorkerFavoriteAuthorityV2MixedUsers$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_FACT_V2_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("real favorite v2 mixed-user worker child: %v\n%s", err, output)
		}
		if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	assertFactWorkerFavoriteAuthorityV2MixedUsers(t)
}

func assertFactWorkerFavoriteAuthorityV2MixedUsers(t *testing.T) {
	var ids []string
	var schemas []int
	if json.Unmarshal([]byte(os.Getenv("SEA_FACT_EVENT_IDS")), &ids) != nil ||
		json.Unmarshal([]byte(os.Getenv("SEA_FACT_SCHEMA_VERSIONS")), &schemas) != nil ||
		len(ids) != 3 || len(schemas) != 3 || schemas[0] != 2 || schemas[1] != 1 || schemas[2] != 2 {
		t.Fatal("RTW v2/v1/v2 source handoff contract")
	}
	ctx := context.Background()
	var logs bytes.Buffer
	exporter := &factSpanExporter{}
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "btw-favorite-subjectref-v2-test", Environment: "test",
		Version: "candidate-r1", InstanceID: "mixed-favorite", Output: &logs,
		Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	pool := factWorkerPool(t)
	store := usermodel.NewStore(pool, observed)
	graph, err := usermodel.NewFactGraphRuntime("favorite-subjectref-v2", store, inmemory.NewSessionService(), observed)
	if err != nil {
		t.Fatal(err)
	}
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			if err := graph.Close(); err != nil {
				t.Error(err)
			}
			if err := observed.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(closeAll)
	client, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv("SEA_FACT_DC_URL"), Token: os.Getenv("SEA_FACT_DC_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	const consumer = "btw-favorite-authority"
	batch, err := client.ReadEvents(ctx, consumer, favoriteFactProducer, 10)
	if err != nil || batch.FromOffset != 1 || batch.ToOffset != 3 || len(batch.Events) != 3 {
		t.Fatalf("mixed RTW/DC producer batch: %+v %v", batch, err)
	}
	for i, item := range batch.Events {
		id, parseErr := strconv.ParseInt(item.Event.AggregateID, 10, 64)
		if item.Event.EventID != ids[i] || item.Event.SchemaVersion != schemas[i] ||
			parseErr != nil || id <= maxLegacyExactJSONID || item.Offset != int64(i+1) {
			t.Fatalf("RTW/DC mixed source item %d: %+v", i, item)
		}
	}
	authorityURL, authorityToken := os.Getenv("SEA_FACT_AUTHORITY_URL"), os.Getenv("SEA_FACT_AUTHORITY_TOKEN")
	newBinder := func(endpoint, token string) *FavoriteAuthorityBinder {
		t.Helper()
		binder, err := NewFavoriteAuthorityBinder(FavoriteAuthorityBinderConfig{BaseURL: endpoint, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		return binder
	}
	wrongToken := strings.Repeat("x", len(authorityToken))
	noAuthority := favoriteAuthorityWorker(t, client, newBinder(authorityURL, wrongToken), graph, store, observed)
	if result, err := noAuthority.RunOnce(ctx); !errors.Is(err, ErrFactAuthorityUnavailable) ||
		result.Count != 0 || result.AckedOffset != 0 {
		t.Fatalf("invalid RTW authority token escaped Graph/ACK: %+v %v", result, err)
	}
	for _, mode := range []string{"missing", "hash", "subject"} {
		proxy := favoriteAuthorityTamperProxy(t, authorityURL, mode)
		worker := favoriteAuthorityWorker(t, client, newBinder(proxy.URL, authorityToken), graph, store, observed)
		expected := ErrFactDeliveryContract
		if mode == "missing" {
			expected = ErrFactAuthorityUnavailable
		}
		if result, err := worker.RunOnce(ctx); !errors.Is(err, expected) || result.Count != 0 || result.AckedOffset != 0 {
			t.Fatalf("RTW %s divergence escaped Graph/ACK: %+v %v", mode, result, err)
		}
	}
	if before, err := client.ReadEvents(ctx, consumer, favoriteFactProducer, 10); err != nil ||
		before.FromOffset != 1 || before.ToOffset != 3 {
		t.Fatalf("failed RTW reads advanced DC cursor: %+v %v", before, err)
	}
	real := favoriteAuthorityWorker(t, &failFirstFactACK{Client: client, fail: true},
		newBinder(authorityURL, authorityToken), graph, store, observed)
	first, err := real.RunOnce(ctx)
	if err == nil || first.Count != 3 || first.NewFacts != 3 || first.AckedOffset != 0 {
		t.Fatalf("native Graph/PG did not commit before uncertain ACK: %+v %v", first, err)
	}
	u1 := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	for _, check := range []struct {
		subject usermodel.SubjectRef
		version int64
		active  int
		outbox  int
	}{
		{u1, 2, 0, 2},
		{u2, 1, 1, 1},
	} {
		current, err := store.Current(ctx, check.subject)
		if err != nil || current.StateVersion != check.version || len(current.Active) != check.active {
			t.Fatalf("UID %s fact state: %+v %v", check.subject.SubjectID, current, err)
		}
		rows, err := store.OutboxAfter(ctx, check.subject, 0, 10)
		if err != nil || len(rows) != check.outbox {
			t.Fatalf("UID %s accepted Outbox count: %d %v", check.subject.SubjectID, len(rows), err)
		}
	}
	for i, item := range batch.Events {
		subject := u1
		if i == 1 {
			subject = u2
		}
		receipt, err := store.CurrentReceipt(ctx, subject, usermodel.EventKey{Producer: favoriteFactProducer, EventID: item.Event.EventID})
		if err != nil || receipt.Status != "accepted" || receipt.StateVersion < 1 {
			t.Fatalf("mixed source item %d lacks domain+Outbox receipt: %+v %v", i, receipt, err)
		}
	}
	replay, err := real.RunOnce(ctx)
	if err != nil || replay.Count != 3 || replay.Replayed != 3 || replay.NewFacts != 0 || replay.AckedOffset != 3 {
		t.Fatalf("one DC cursor ACK after same-key replay: %+v %v", replay, err)
	}
	if empty, err := real.RunOnce(ctx); err != nil || !empty.Empty {
		t.Fatalf("mixed source not drained: %+v %v", empty, err)
	}
	// DC accepts a syntactically valid unknown schema as technical data; the
	// fixed BTW (type,version) allowlist rejects it before authority/Graph/ACK.
	unknown := batch.Events[0].Event
	unknown.AggregateID = "9007199254745991"
	unknown.EventID = "favorite." + unknown.AggregateID + ".v1"
	unknown.OperationID = unknown.EventID
	unknown.SchemaVersion = 3
	var body map[string]any
	if err := json.Unmarshal(unknown.Payload, &body); err != nil {
		t.Fatal(err)
	}
	body["schema_version"] = float64(3)
	body["event_id"] = unknown.EventID
	body["favorite_id"] = unknown.AggregateID
	body["source_ref"] = "rtw.favorite/" + unknown.AggregateID
	unknown.Payload, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := client.PublishEvent(ctx, unknown); err != nil || receipt.Offset != 4 {
		t.Fatalf("DC technical unknown-schema fixture: %+v %v", receipt, err)
	}
	if blocked, err := real.RunOnce(ctx); !errors.Is(err, ErrFactDeliveryContract) || blocked.AckedOffset != 0 {
		t.Fatalf("unknown v3 version escaped BTW allowlist/ACK: %+v %v", blocked, err)
	}
	if outstanding, err := client.ReadEvents(ctx, consumer, favoriteFactProducer, 10); err != nil ||
		outstanding.FromOffset != 4 || outstanding.ToOffset != 4 {
		t.Fatalf("unknown version was ACKed: %+v %v", outstanding, err)
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		strings.Contains(metrics.Body.String(), "1001") || strings.Contains(metrics.Body.String(), "1002") {
		t.Fatalf("framework metrics missing or UID became a metric label: %d", metrics.Code)
	}
	closeAll()
	native := false
	for _, span := range exporter.Snapshot() {
		if span.Name() == "workflow execute_graph usermodel_fact" && span.InstrumentationScope().Name == "trpc.agent.go" {
			native = true
			break
		}
	}
	if !native {
		t.Fatal("real v2/v1/v2 fact run lacked tRPC-Agent-Go native Graph span")
	}
	linked := false
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			t.Fatalf("non-JSON structured worker log: %s", line)
		}
		if record["event"] == "usermodel.worker.event.finished" && record["event_id"] == ids[0] &&
			record["trace_id"] != nil && record["span_id"] != nil {
			linked = true
		}
	}
	if !linked {
		t.Fatal("real v2 source lacked correlated structured event terminal")
	}
}
