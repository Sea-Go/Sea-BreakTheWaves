package favoritesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type coverageFixture struct {
	mu           sync.Mutex
	items        []eventing.Item
	receipts     map[string]eventing.Receipt
	owners       map[string]sourcecoverage.SubjectRef
	predecessors map[string]string
	ack          map[string]int64
}

func fixtureFavoriteEvent(id, folder, user, target, operation string, offset int64) (eventing.Item, eventing.Receipt) {
	version, eventType := int64(1), "rtw.favorite.assert"
	if operation == "retract" {
		version, eventType = 2, "rtw.favorite.retract"
	}
	eventID := fmt.Sprintf("favorite.%s.v%d", id, version)
	subject := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: user}
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID, "subject_ref": subject,
		"target_type": "article", "target_id": target, "target_revision": target + ":r1", "operation": operation,
		"source_ref": "rtw.favorite/" + id, "event_time": "2026-09-15T00:00:00Z", "available_at": "2026-09-15T00:00:00Z",
		"favorite_id": id, "folder_id": folder})
	event := eventing.Event{EventID: eventID, EventType: eventType, SchemaVersion: 1, Producer: Producer,
		AggregateID: id, AggregateVersion: version, OperationID: eventID, OccurredAt: "2026-09-15T00:00:00Z", Payload: payload}
	canonical, _ := coverageCanonical(event)
	hash := coverageHash(canonical)
	receipt := eventing.Receipt{EventID: eventID, Producer: Producer, TechnicalStatus: "accepted",
		ReceiptID: fmt.Sprintf("fixture-dc-%d", offset), InputHash: hash, Offset: offset, ReceivedAt: fmt.Sprintf("2026-09-15T00:00:%02dZ", offset)}
	return eventing.Item{Offset: offset, InputHash: hash, Event: event}, receipt
}

func newCoverageFixture() *coverageFixture {
	f := &coverageFixture{receipts: map[string]eventing.Receipt{}, owners: map[string]sourcecoverage.SubjectRef{},
		predecessors: map[string]string{}, ack: map[string]int64{}}
	for _, spec := range []struct {
		id, folder, user, target, operation string
		offset                              int64
	}{
		{"9007199254741993", "9007199254741991", "1001", "article-u1", "assert", 1},
		{"9007199254742993", "9007199254742991", "1002", "article-u2", "assert", 2},
		{"9007199254741993", "9007199254741991", "1001", "article-u1", "retract", 3},
	} {
		item, receipt := fixtureFavoriteEvent(spec.id, spec.folder, spec.user, spec.target, spec.operation, spec.offset)
		f.items = append(f.items, item)
		f.receipts[item.Event.EventID] = receipt
		f.owners[item.Event.EventID] = sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: spec.user}
		if spec.operation == "retract" {
			f.predecessors[item.Event.EventID] = "favorite." + spec.id + ".v1"
		}
	}
	return f
}

func (f *coverageFixture) ReadEvents(_ context.Context, consumer, producer string, limit int) (eventing.Batch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if producer != Producer || limit < 1 {
		return eventing.Batch{}, ErrContract
	}
	from := f.ack[consumer] + 1
	b := eventing.Batch{Consumer: consumer, Producer: Producer, FromOffset: from, ToOffset: from - 1, Events: []eventing.Item{}}
	if from > int64(len(f.items)) {
		return b, nil
	}
	end := from + int64(limit) - 1
	if end > int64(len(f.items)) {
		end = int64(len(f.items))
	}
	b.ToOffset = end
	b.Events = append(b.Events, f.items[from-1:end]...)
	canonical, _ := coverageCanonical(b.Events)
	b.BatchHash = coverageHash(canonical)
	return b, nil
}
func (f *coverageFixture) EventReceipt(_ context.Context, producer, id string) (eventing.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if producer != Producer {
		return eventing.Receipt{}, ErrContract
	}
	r, ok := f.receipts[id]
	if !ok {
		return r, ErrCoveragePending
	}
	return r, nil
}
func (f *coverageFixture) AcknowledgeEvents(ctx context.Context, consumer string, q eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	b, err := f.ReadEvents(ctx, consumer, q.Producer, int(q.ToOffset-q.FromOffset+1))
	if err != nil || b.FromOffset != q.FromOffset || b.ToOffset != q.ToOffset || b.BatchHash != q.BatchHash {
		return eventing.DeliveryReceipt{}, ErrContract
	}
	f.mu.Lock()
	f.ack[consumer] = q.ToOffset
	f.mu.Unlock()
	return eventing.DeliveryReceipt{Consumer: consumer, Producer: Producer, AcknowledgedOffset: q.ToOffset, TechnicalStatus: "delivered"}, nil
}

type fixtureLostACK struct {
	Source
	fail bool
}

func (s *fixtureLostACK) AcknowledgeEvents(ctx context.Context, consumer string, q eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	if s.fail {
		s.fail = false
		return eventing.DeliveryReceipt{}, errors.New("fixture ACK loss")
	}
	return s.Source.AcknowledgeEvents(ctx, consumer, q)
}

func TestCoveragePublisherTwoSubjects(t *testing.T) {
	if os.Getenv("COVERAGE_TWO_DSN") == "" || os.Getenv("COVERAGE_S3_PREFIX") == "" {
		t.Skip("requires isolated PG/S3 fixture")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("COVERAGE_TWO_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := InitializeCoverage(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := newCoverageFixture()
	const token = "coverage-fixture-authority-token-at-least-32"
	var wrongSubject atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		f.mu.Lock()
		receipt, ok := f.receipts[id]
		owner := f.owners[id]
		predecessor := f.predecessors[id]
		var event eventing.Event
		for _, item := range f.items {
			if item.Event.EventID == id {
				event = item.Event
				break
			}
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if wrongSubject.Load() && strings.Contains(id, "9007199254742993") {
			owner.SubjectID = "1001"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"event": event, "subject_ref": owner,
			"predecessor_event_id": predecessor, "technical_receipt": receipt, "source_event_hash": receipt.InputHash})
	}))
	defer server.Close()
	binder, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{BaseURL: server.URL, Token: token, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	lost := &fixtureLostACK{Source: f, fail: true}
	consumer := CoverageConsumer{DB: pool, Source: lost, Binder: binder, Limit: 1}
	publisher := CoveragePublisher{DB: pool, Source: f, Binder: binder, S3Prefix: os.Getenv("COVERAGE_S3_PREFIX")}
	if got, err := consumer.RunOnce(ctx); err == nil || got.CommittedOffset != 1 {
		t.Fatalf("first ACK loss: %+v %v", got, err)
	}
	g1, err := publisher.PublishPrefix(ctx, 1, "coverage_two_g1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 1 {
		t.Fatalf("first replay: %+v %v", got, err)
	}
	for _, want := range []int64{2, 3} {
		if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != want {
			t.Fatalf("offset %d: %+v %v", want, got, err)
		}
	}
	g3, err := publisher.PublishPrefix(ctx, 3, "coverage_two_g3")
	if err != nil {
		t.Fatal(err)
	}
	u1 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	u3 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1003"}
	s1, err := publisher.PublishSubject(ctx, g3.Ref, u1)
	if err != nil || s1.Ref.EventCount != 2 {
		t.Fatalf("u1 sparse [1,3]: %+v %v", s1, err)
	}
	s2, err := publisher.PublishSubject(ctx, g3.Ref, u2)
	if err != nil || s2.Ref.EventCount != 1 {
		t.Fatalf("u2 sparse [2]: %+v %v", s2, err)
	}
	s3, err := publisher.PublishSubject(ctx, g3.Ref, u3)
	if err != nil || s3.Ref.EventCount != 0 || s3.Ref.SparseIndexSHA256 != coverageHash(nil) {
		t.Fatalf("u3 proved empty: %+v %v", s3, err)
	}
	if g1.Ref.EventIndexSHA256 == g3.Ref.EventIndexSHA256 || g3.Ref.ThroughOffset != "3" {
		t.Fatalf("global generations not distinct: %+v %+v", g1, g3)
	}
	graph, err := f.ReadEvents(ctx, "btw-favorite-authority", Producer, 10)
	if err != nil || graph.FromOffset != 1 || graph.ToOffset != 3 {
		t.Fatalf("warehouse ACK moved Graph cursor: %+v %v", graph, err)
	}
	fullBody, status, err := publisher.get(ctx, g3.Ref.EventIndexURL)
	if err != nil || status != http.StatusOK {
		t.Fatalf("read fixed global index: %v %d", err, status)
	}
	full := []sourcecoverage.EventIndexRow{}
	for _, line := range bytes.Split(bytes.TrimSuffix(fullBody, []byte{'\n'}), []byte{'\n'}) {
		var row sourcecoverage.EventIndexRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		full = append(full, row)
	}
	otherWindows := []sourcecoverage.BatchEvidence{{FromOffset: "1", ToOffset: "3", BatchHash: graph.BatchHash}}
	if err := sourcecoverage.VerifyBatchEvidence(Producer, full, otherWindows); err != nil {
		t.Fatalf("different consumer segmentation rejected: %v", err)
	}
	_, otherBatchHash, err := sourcecoverage.BatchEvidenceJSONL(otherWindows, 3)
	if err != nil || otherBatchHash == g3.Ref.BatchEvidenceSHA256 {
		t.Fatalf("different windows confused with event-index root: %s %v", otherBatchHash, err)
	}
	if again, err := publisher.PublishPrefix(ctx, 3, "coverage_two_g3"); err != nil || again.Ref != g3.Ref {
		t.Fatalf("global replay changed root: %+v %v", again, err)
	}
	if again, err := publisher.PublishSubject(ctx, g3.Ref, u1); err != nil || again.Ref != s1.Ref {
		t.Fatalf("sparse replay changed receipt: %+v %v", again, err)
	}
	if path := os.Getenv("COVERAGE_TWO_ODS_OUTPUT"); path != "" {
		body, err := ExportODS(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if path := os.Getenv("COVERAGE_TWO_REPORT"); path != "" {
		body, _ := json.Marshal(struct {
			G1, G3     sourcecoverage.GlobalPrefixRef
			U1, U2, U3 sourcecoverage.SubjectCoverageRef
		}{g1.Ref, g3.Ref, s1.Ref, s2.Ref, s3.Ref})
		if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	putFixture := func(target string, body []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.Fatal(resp.Status)
		}
	}
	for _, target := range []string{g3.Ref.EventIndexURL, g3.Ref.BatchEvidenceURL,
		publisher.objectURL(g3.Ref.WarehouseGeneration, "manifest", g3.Ref.ManifestSHA256, ".json"), s1.Ref.SparseIndexURL} {
		original, status, err := publisher.get(ctx, target)
		if err != nil || status != http.StatusOK {
			t.Fatalf("read fixture object: %v %d", err, status)
		}
		putFixture(target, []byte("tampered"))
		if _, err := publisher.PublishSubject(ctx, g3.Ref, u1); !errors.Is(err, ErrCoverageConflict) {
			t.Fatalf("tampered S3 source was trusted: %s %v", target, err)
		}
		putFixture(target, original)
	}
	wrongSubject.Store(true)
	if _, err := publisher.PublishPrefix(ctx, 3, "coverage_wrong_subject"); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("RTW wrong subject published: %v", err)
	}
	wrongSubject.Store(false)
	f.mu.Lock()
	original := f.receipts[f.items[1].Event.EventID]
	changed := original
	changed.InputHash = strings.Repeat("b", 64)
	f.receipts[original.EventID] = changed
	f.mu.Unlock()
	if _, err := publisher.PublishPrefix(ctx, 3, "coverage_wrong_receipt"); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("DC wrong receipt published: %v", err)
	}
	f.mu.Lock()
	f.receipts[original.EventID] = original
	f.mu.Unlock()
	if _, err := pool.Exec(ctx, `DELETE FROM warehouse_favorite.ods_event WHERE producer=$1 AND source_offset=2`, Producer); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishPrefix(ctx, 3, "coverage_missing_offset"); !errors.Is(err, ErrCoveragePending) {
		t.Fatalf("missing offset published: %v", err)
	}
	t.Logf("L2 synthetic authority/real PG/S3 global W1=%s W3=%s u1=%s u2=%s empty=%s", g1.Ref.EventIndexSHA256, g3.Ref.EventIndexSHA256, s1.Ref.SparseIndexSHA256, s2.Ref.SparseIndexSHA256, s3.Ref.SparseIndexSHA256)
}
