package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// favoriteFixtureBinder deliberately has no RTW HTTP authority: this test
// verifies DC technical delivery and BTW routing, not H09 domain admission.
// The subject is fixed from the RTW acceptance fixture, never read from DC.
type favoriteFixtureBinder struct {
	action usermodel.Action
}

type favoriteWrongActionBinder struct{}

func (favoriteWrongActionBinder) BindFact(ctx context.Context, source eventing.Event) (usermodel.Event, error) {
	fact, err := (favoriteFixtureBinder{action: usermodel.Assert}).BindFact(ctx, source)
	fact.Action = usermodel.Retract
	return fact, err
}

func (b favoriteFixtureBinder) BindFact(_ context.Context, source eventing.Event) (usermodel.Event, error) {
	favoriteID, err := strconv.ParseInt(source.AggregateID, 10, 64)
	if err != nil || favoriteID < 1 {
		return usermodel.Event{}, ErrFactDeliveryContract
	}
	version := int64(1)
	if b.action == usermodel.Retract {
		version = 2
	}
	if source.AggregateVersion != version || source.EventID != fmt.Sprintf("favorite.%d.v%d", favoriteID, version) ||
		source.OperationID != source.EventID || source.EventType != "rtw.favorite."+string(b.action) {
		return usermodel.Event{}, ErrFactDeliveryContract
	}
	var payload struct {
		EventID    string          `json:"event_id"`
		Operation  string          `json:"operation"`
		FavoriteID json.RawMessage `json:"favorite_id"`
		TargetID   string          `json:"target_id"`
	}
	var payloadFavoriteID string
	if err := json.Unmarshal(source.Payload, &payload); err != nil || payload.EventID != source.EventID ||
		payload.Operation != string(b.action) || payload.TargetID != "article-77" {
		return usermodel.Event{}, ErrFactDeliveryContract
	}
	if len(payload.FavoriteID) > 0 && payload.FavoriteID[0] == '"' {
		if err := json.Unmarshal(payload.FavoriteID, &payloadFavoriteID); err != nil {
			return usermodel.Event{}, ErrFactDeliveryContract
		}
	} else {
		payloadFavoriteID = string(payload.FavoriteID) // legacy small JSON integer
	}
	if payloadFavoriteID != source.AggregateID {
		return usermodel.Event{}, ErrFactDeliveryContract
	}
	fact := usermodel.Event{Subject: usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"},
		Action: b.action, Kind: usermodel.ProductAction, Predicate: "favorite", ValueRef: "article/article-77", ItemID: "article-77"}
	if b.action == usermodel.Retract {
		fact.Supersedes = &usermodel.EventKey{Producer: source.Producer, EventID: fmt.Sprintf("favorite.%d.v1", favoriteID)}
	}
	return fact, nil
}

type failFirstFactACK struct {
	*datacenter.Client
	fail bool
}

func (s *failFirstFactACK) AcknowledgeEvents(ctx context.Context, consumer string, ack eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.fail {
		s.fail = false
		return eventing.DeliveryReceipt{}, errors.New("test transport loss before DC ACK")
	}
	return s.Client.AcknowledgeEvents(ctx, consumer, ack)
}

// The shell acceptance starts isolated PostgreSQL, real DC cmd/platform and
// the RTW favorite dispatcher/test first. Each child installs one Graph tracer.
func TestFactWorkerFavoriteRealDC(t *testing.T) {
	if os.Getenv("SEA_FACT_DC_URL") == "" || os.Getenv("SEA_FACT_DC_TOKEN") == "" || os.Getenv("USERMODEL_TEST_POSTGRES_DSN") == "" {
		t.Skip("run with isolated DC cmd/platform and PG16")
	}
	if os.Getenv("SEA_FACT_FAVORITE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactWorkerFavoriteRealDC$", "-test.v")
		cmd.Env = append(os.Environ(), "SEA_FACT_FAVORITE_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("real DC favorite fact child: %v\n%s", err, output)
		}
		if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	assertFactWorkerFavoriteRealDC(t)
}

func assertFactWorkerFavoriteRealDC(t *testing.T) {
	ctx := context.Background()
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "btw-favorite-multitype-test", Environment: "test",
		Version: "fixed-v1.8.1", InstanceID: "favorite-fixture", Output: os.Stderr, Level: slog.LevelInfo,
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
	graph, err := usermodel.NewFactGraphRuntime("favorite-multitype", store, inmemory.NewSessionService(), observed)
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
	const consumer = "btw-favorite-multitype"
	batch, err := client.ReadEvents(ctx, consumer, "rtw.community.favorite", 10)
	if err != nil || batch.FromOffset != 1 || batch.ToOffset != 2 || len(batch.Events) != 2 ||
		batch.Events[0].Event.EventType != "rtw.favorite.assert" || batch.Events[1].Event.EventType != "rtw.favorite.retract" ||
		batch.Events[0].Event.EventID != "favorite.662.v1" || batch.Events[1].Event.EventID != "favorite.662.v2" {
		t.Fatalf("RTW-dispatched real DC contiguous batch: %+v %v", batch, err)
	}
	source := &failFirstFactACK{Client: client, fail: true}
	worker, err := NewFactWorker(FactWorkerConfig{Consumer: consumer, Producer: "rtw.community.favorite", BatchLimit: 10,
		Bindings: []FactEventBinding{
			{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, Binder: favoriteFixtureBinder{action: usermodel.Assert}},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, Binder: favoriteFixtureBinder{action: usermodel.Retract}},
		}}, source, nil, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	badConfig := FactWorkerConfig{Consumer: consumer, Producer: "rtw.community.favorite", BatchLimit: 10,
		Bindings: []FactEventBinding{
			{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, Binder: favoriteWrongActionBinder{}},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, Binder: favoriteFixtureBinder{action: usermodel.Retract}},
		}}
	wrong, err := NewFactWorker(badConfig, client, nil, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	if rejected, err := wrong.RunOnce(ctx); !errors.Is(err, ErrFactDeliveryContract) || rejected.Count != 0 || rejected.AckedOffset != 0 {
		t.Fatalf("binder action mismatch escaped before commit/ACK: %+v %v", rejected, err)
	}
	badConfig.Bindings = append(badConfig.Bindings, badConfig.Bindings[0])
	if _, err := NewFactWorker(badConfig, client, nil, graph, store, observed); !errors.Is(err, ErrFactDeliveryContract) {
		t.Fatalf("duplicate event type admitted: %v", err)
	}
	first, err := worker.RunOnce(ctx)
	if err == nil || first.Count != 2 || first.NewFacts != 2 || first.AckedOffset != 0 {
		t.Fatalf("first committed facts with failed ACK: %+v %v", first, err)
	}
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	current, err := store.Current(ctx, subject)
	if err != nil || current.StateVersion != 2 || len(current.Active) != 0 {
		t.Fatalf("assert then retract state: %+v %v", current, err)
	}
	outbox, err := store.OutboxAfter(ctx, subject, 0, 10)
	if err != nil || len(outbox) != 2 {
		t.Fatalf("two committed facts/outbox: %+v %v", outbox, err)
	}
	replay, err := worker.RunOnce(ctx)
	if err != nil || replay.Count != 2 || replay.Replayed != 2 || replay.NewFacts != 0 || replay.AckedOffset != 2 {
		t.Fatalf("same DC offsets replay then ACK: %+v %v", replay, err)
	}
	empty, err := worker.RunOnce(ctx)
	if err != nil || !empty.Empty {
		t.Fatalf("expected drained source: %+v %v", empty, err)
	}
	// Synthetic missing predecessor is deliberately separate from RTW's real
	// ordered dispatcher: it proves that a DC receipt is insufficient for ACK.
	missing := batch.Events[1].Event
	missing.EventID, missing.OperationID, missing.AggregateID = "favorite.663.v2", "favorite.663.v2", "663"
	var payload map[string]any
	if err := json.Unmarshal(missing.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["event_id"], payload["favorite_id"], payload["source_ref"] = missing.EventID, "663", "rtw.favorite/663"
	payload["subject_ref"] = map[string]any{"authority_id": "forged", "tenant_id": "other", "subject_id": "someone-else"}
	missing.Payload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := client.PublishEvent(ctx, missing); err != nil || receipt.Offset != 3 {
		t.Fatalf("synthetic missing predecessor DC receipt: %+v %v", receipt, err)
	}
	pending, err := worker.RunOnce(ctx)
	if !errors.Is(err, ErrFactDeliveryPending) || pending.AckedOffset != 0 {
		t.Fatalf("missing predecessor escaped ACK: %+v %v", pending, err)
	}
	current, err = store.Current(ctx, subject)
	if err != nil || current.Pending != 1 || current.StateVersion != 2 {
		t.Fatalf("forged payload subject was used or missing predecessor accepted: %+v %v", current, err)
	}
	unknown := batch.Events[0].Event
	unknown.EventID, unknown.OperationID, unknown.AggregateID = "favorite.664.v1", "favorite.664.v1", "664"
	unknown.EventType = "rtw.favorite.unknown"
	unknown.Payload = json.RawMessage(`{"event_id":"favorite.664.v1","operation":"assert","favorite_id":664,"target_id":"article-77"}`)
	if receipt, err := client.PublishEvent(ctx, unknown); err != nil || receipt.Offset != 4 {
		t.Fatalf("synthetic unknown event DC receipt: %+v %v", receipt, err)
	}
	rejected, err := worker.RunOnce(ctx)
	if !errors.Is(err, ErrFactDeliveryContract) || rejected.AckedOffset != 0 {
		t.Fatalf("unknown type escaped allowlist or ACK: %+v %v", rejected, err)
	}
	remaining, err := client.ReadEvents(ctx, consumer, "rtw.community.favorite", 10)
	if err != nil || remaining.FromOffset != 3 || remaining.ToOffset != 4 || len(remaining.Events) != 2 {
		t.Fatalf("DC cursor advanced past blocked offsets: %+v %v", remaining, err)
	}
	outbox, err = store.OutboxAfter(ctx, subject, 0, 10)
	if err != nil || len(outbox) != 2 {
		t.Fatalf("unknown/pending events fabricated accepted facts: %+v %v", outbox, err)
	}
}
