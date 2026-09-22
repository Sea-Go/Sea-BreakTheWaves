package favoritesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func favoriteV2TestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FAVORITE_V2_TEST_DSN")
	if dsn == "" {
		t.Skip("fresh local PostgreSQL 16 runner supplies FAVORITE_V2_TEST_DSN")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var database, user, server, client string
	var version int
	err = admin.QueryRow(ctx, `SELECT current_database(),current_user,host(inet_server_addr()),
	 host(inet_client_addr()),current_setting('server_version_num')::integer`).Scan(
		&database, &user, &server, &client, &version)
	if err != nil || database != "postgres" || user != "sea_btw_favorite_test" ||
		server != "127.0.0.1" || client != "127.0.0.1" || version < 160000 || version >= 170000 {
		admin.Close(ctx)
		t.Fatalf("favorite v2 test requires its dedicated loopback PG16 owner: db=%s user=%s server=%s client=%s version=%d err=%v",
			database, user, server, client, version, err)
	}
	name := "favorite_v2_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
		admin.Close(context.Background())
	})
	if err = Initialize(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = InitializeCoverage(ctx, pool); err != nil {
		t.Fatal(err)
	}
	nonce := os.Getenv("FAVORITE_V2_TEST_NONCE")
	if !favoriteV2LocalNonce.MatchString(nonce) {
		t.Fatal("test owner must supply a fresh random 64-hex local DB nonce")
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE public.favorite_subjectref_v2_local_test_gate (
	 nonce text PRIMARY KEY CHECK(nonce ~ '^[0-9a-f]{64}$'),
	 created_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO public.favorite_subjectref_v2_local_test_gate(nonce)
	 VALUES($1)`, nonce); err != nil {
		t.Fatal(err)
	}
	return pool
}

func favoriteV2Objects(t *testing.T) (*httptest.Server, func(string) []byte) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20+1))
			if err != nil || len(body) > 32<<20 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[r.URL.Path] = append([]byte(nil), body...)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, func(path string) []byte {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), objects[path]...)
	}
}

func favoriteV2Authority(t *testing.T, source *coverageFixture) *app.FavoriteAuthorityBinder {
	t.Helper()
	const token = "favorite-v2-isolated-authority-token-at-least-32"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		source.mu.Lock()
		receipt, ok := source.receipts[id]
		owner := source.owners[id]
		predecessor := source.predecessors[id]
		var event eventing.Event
		for _, item := range source.items {
			if item.Event.EventID == id {
				event = item.Event
				break
			}
		}
		source.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"event": event, "subject_ref": owner,
			"predecessor_event_id": predecessor, "technical_receipt": receipt, "source_event_hash": receipt.InputHash})
	}))
	t.Cleanup(server.Close)
	binder, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{
		BaseURL: server.URL, Token: token, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return binder
}

func favoriteV2Append(source *coverageFixture, id, folder, user, target string, offset int64) {
	item, receipt := fixtureFavoriteEvent(id, folder, user, target, "assert", offset)
	source.mu.Lock()
	source.items = append(source.items, item)
	source.receipts[item.Event.EventID] = receipt
	source.owners[item.Event.EventID] = sourcecoverage.SubjectRef{
		AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: user}
	source.mu.Unlock()
}

func favoriteV2Count(t *testing.T, db *pgxpool.Pool, table, predicate string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM warehouse_favorite."+table+
		" WHERE "+predicate, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func favoriteV2SHA(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func assertFavoriteV2SidecarsAbsent(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	var ods, receipt *string
	if err := db.QueryRow(context.Background(), `SELECT
	 to_regclass('warehouse_favorite.ods_event_subject_ref_v2')::text,
	 to_regclass('warehouse_favorite.coverage_subject_receipt_subject_ref_v2')::text`).Scan(&ods, &receipt); err != nil || ods != nil || receipt != nil {
		t.Fatalf("rejected locked preflight left partial sidecars: ods=%v receipt=%v err=%v", ods, receipt, err)
	}
}

func putFavoriteV2Object(t *testing.T, target string, body []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fixture object PUT status=%d", resp.StatusCode)
	}
}

// Keep the frozen EventSpec/receipt at RTW's nanosecond precision while the
// PostgreSQL timestamptz fields and official ExportODS retain microseconds.
// This is still an isolated authority HTTP fixture, not a real RTW account.
func favoriteV2NanosecondSource(t *testing.T, source *coverageFixture) {
	t.Helper()
	const eventTime = "2026-09-15T00:00:00.123456789Z"
	const receivedAt = "2026-09-15T00:00:01.987654321Z"
	source.mu.Lock()
	defer source.mu.Unlock()
	item := &source.items[0]
	var payload map[string]any
	if err := json.Unmarshal(item.Event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["event_time"], payload["available_at"] = eventTime, eventTime
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	item.Event.Payload = body
	item.Event.OccurredAt = eventTime
	canonical, err := coverageCanonical(item.Event)
	if err != nil {
		t.Fatal(err)
	}
	item.InputHash = coverageHash(canonical)
	receipt := source.receipts[item.Event.EventID]
	receipt.InputHash, receipt.ReceivedAt = item.InputHash, receivedAt
	source.receipts[item.Event.EventID] = receipt
}

func TestFavoriteSubjectRefV2StorageContinuousWritersAndFrozenArtifacts(t *testing.T) {
	ctx := context.Background()
	db := favoriteV2TestDB(t)
	objects, objectAt := favoriteV2Objects(t)
	source := newCoverageFixture()
	favoriteV2NanosecondSource(t, source)
	binder := favoriteV2Authority(t, source)
	consumer := CoverageConsumer{DB: db, Source: source, Binder: binder, Limit: 128}
	publisher := CoveragePublisher{DB: db, Source: source, Binder: binder, S3Prefix: objects.URL}
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 3 {
		t.Fatalf("old ODS prefix: %+v %v", got, err)
	}
	g3, err := publisher.PublishPrefix(ctx, 3, "favorite_v2_g3")
	if err != nil {
		t.Fatal(err)
	}
	u1 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	u3 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1003"}
	s1, err := publisher.PublishSubject(ctx, g3.Ref, u1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.PublishSubject(ctx, g3.Ref, u2); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.PublishSubject(ctx, g3.Ref, u3); err != nil {
		t.Fatal(err)
	}
	checker := &SubjectRefV2Preflight{DB: db, Objects: LocalCoverageReader{}, S3Prefix: objects.URL,
		LocalTestNonce: os.Getenv("FAVORITE_V2_TEST_NONCE")}
	preflight, err := checker.Run(ctx)
	if err != nil || !preflight.Clear() || len(preflight.VerifiedCoverageRoots) != 1 {
		t.Fatalf("old-schema read-only preflight before DDL: %+v %v", preflight.Findings, err)
	}
	oldODS, err := ExportODS(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(oldODS, []byte("2026-09-15T00:00:00.123456789Z")) ||
		!bytes.Contains(oldODS, []byte("2026-09-15T00:00:00.123456Z")) ||
		!bytes.Contains(oldODS, []byte("2026-09-15T00:00:01.987654321Z")) ||
		!bytes.Contains(oldODS, []byte("2026-09-15T00:00:01.987654Z")) {
		t.Fatal("official PG ExportODS did not preserve immutable nanos and projected microseconds")
	}
	oldManifest := objectAt(newURLPath(t, g3.ManifestURL))
	oldSubjectReceipt := objectAt(newURLPath(t, s1.ReceiptURL))
	if favoriteV2SHA(oldManifest) != g3.Ref.ManifestSHA256 || favoriteV2SHA(oldSubjectReceipt) != s1.Ref.ReceiptSHA256 {
		t.Fatal("old frozen coverage bytes did not match original hashes")
	}
	var logBody bytes.Buffer
	bundle, err := telemetry.New(ctx, telemetry.Config{Service: "btw-favorite-v2-local-test",
		Environment: "test", Version: "stage3-candidate", InstanceID: "fresh-pg16",
		Output: &logBody, Level: slog.LevelInfo, SampleRatio: 1,
		TraceExporter: tracetest.NewInMemoryExporter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := bundle.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	// The raw SQL body cannot admit itself. The official owner path must first
	// verify the current, locked PG snapshot and the original S3 objects.
	if _, err = db.Exec(ctx, favoriteV2MigrationBody, pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("raw candidate SQL applied without locked preflight")
	}
	assertFavoriteV2SidecarsAbsent(t, db)
	checker.LocalTestNonce = strings.Repeat("0", 64)
	if _, err = checker.ApplySubjectRefV2StorageCandidate(ctx, bundle); !errors.Is(err, ErrContract) {
		t.Fatalf("wrong local DB owner nonce admitted candidate: %v", err)
	}
	checker.LocalTestNonce = os.Getenv("FAVORITE_V2_TEST_NONCE")
	var oldHash, oldReceipt, oldSpec string
	if err = db.QueryRow(ctx, `SELECT source_event_hash,technical_receipt::text,event_spec::text
	 FROM warehouse_favorite.ods_event WHERE producer=$1 AND source_offset=2`, Producer).
		Scan(&oldHash, &oldReceipt, &oldSpec); err != nil {
		t.Fatal(err)
	}
	rejectApply := func(code string) {
		t.Helper()
		report, applyErr := checker.ApplySubjectRefV2StorageCandidate(ctx, bundle)
		if !errors.Is(applyErr, ErrContract) || report.Clear() || findingCount(report, code) < 1 {
			t.Fatalf("legal UID but corrupt %s passed locked preflight: findings=%+v err=%v",
				code, report.Findings, applyErr)
		}
		assertFavoriteV2SidecarsAbsent(t, db)
	}
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET event_spec='{}'::jsonb
	 WHERE producer=$1 AND source_offset=2`, Producer); err != nil {
		t.Fatal(err)
	}
	rejectApply("immutable_event_or_receipt_mismatch")
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET event_spec=$3::jsonb
	 WHERE producer=$1 AND source_offset=$2`, Producer, int64(2), oldSpec); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET source_event_hash=repeat('0',64)
	 WHERE producer=$1 AND source_offset=2`, Producer); err != nil {
		t.Fatal(err)
	}
	rejectApply("immutable_event_or_receipt_mismatch")
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET source_event_hash=$3
	 WHERE producer=$1 AND source_offset=$2`, Producer, int64(2), oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET technical_receipt=jsonb_set(
	 technical_receipt,'{input_hash}',to_jsonb(repeat('0',64)),true)
	 WHERE producer=$1 AND source_offset=2`, Producer); err != nil {
		t.Fatal(err)
	}
	rejectApply("immutable_event_or_receipt_mismatch")
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.ods_event SET technical_receipt=$3::jsonb
	 WHERE producer=$1 AND source_offset=$2`, Producer, int64(2), oldReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.consumer_cursor SET committed_offset=2
	 WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer); err != nil {
		t.Fatal(err)
	}
	rejectApply("cursor_or_scan_limit")
	if _, err = db.Exec(ctx, `UPDATE warehouse_favorite.consumer_cursor SET committed_offset=3
	 WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer); err != nil {
		t.Fatal(err)
	}
	putFavoriteV2Object(t, g3.ManifestURL, []byte(`{}`))
	rejectApply("coverage_manifest_bytes_mismatch")
	putFavoriteV2Object(t, g3.ManifestURL, oldManifest)
	for round := 0; round < 2; round++ {
		admitted, applyErr := checker.ApplySubjectRefV2StorageCandidate(ctx, bundle)
		if applyErr != nil || !admitted.Clear() || admitted.SnapshotSHA256 != preflight.SnapshotSHA256 {
			t.Fatalf("same locked old-row snapshot admission round %d: %+v %v", round, admitted.Findings, applyErr)
		}
	}
	candidate, err := NewSubjectRefV2StorageCandidate(ctx, checker, bundle)
	if err != nil {
		t.Fatalf("candidate runtime catalog gate: %v", err)
	}
	for _, spec := range []struct {
		offset int64
		uid    string
	}{{1, "1001"}, {2, "1002"}, {3, "1001"}} {
		got, readErr := candidate.ReadODSEventV2(ctx, Producer, spec.offset)
		if readErr != nil || got.SubjectID != spec.uid || got.Issuer != "rtw.identity" || got.SourceOffset != spec.offset {
			t.Fatalf("internal v2 ODS read offset %d: %+v %v", spec.offset, got, readErr)
		}
	}
	r1, err := candidate.ReadSubjectReceiptV2(ctx, g3.Ref.ManifestSHA256, "1001")
	if err != nil || r1.ReceiptSHA256 != s1.Ref.ReceiptSHA256 || r1.EventCount != 2 || r1.SubjectID != "1001" {
		t.Fatalf("internal v2 receipt differs from old manifest: %+v %v", r1, err)
	}
	handoffFavoriteV2PGToCH(t, db, oldODS, preflight, g3, s1)
	// A default-off writer after DDL creates only the original row. Its old
	// byte/offset contract survives until the owner explicitly replays it.
	favoriteV2Append(source, "9007199254743993", "9007199254741991", "1003", "article-u3", 4)
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 4 {
		t.Fatalf("default-off offset 4: %+v %v", got, err)
	}
	if favoriteV2Count(t, db, "ods_event_subject_ref_v2", "source_offset=4") != 0 {
		t.Fatal("default-off ODS unexpectedly projected")
	}
	if _, err = candidate.ReadODSEventV2(ctx, Producer, 4); !errors.Is(err, ErrSubjectRefV2ProjectionPending) {
		t.Fatalf("missing sidecar hidden: %v", err)
	}
	// An unchanged DC replay of the same offset repairs that missing sidecar
	// without another original ODS row or a second source offset.
	source.mu.Lock()
	source.ack[DefaultConsumer] = 3
	source.mu.Unlock()
	consumer.V2Candidate = candidate
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 4 {
		t.Fatalf("explicit v2 replay offset 4: %+v %v", got, err)
	}
	if favoriteV2Count(t, db, "ods_event", "source_offset=4") != 1 || favoriteV2Count(t, db, "ods_event_subject_ref_v2", "source_offset=4") != 1 {
		t.Fatal("ODS replay duplicated or missed original/projected offset")
	}
	g4, err := publisher.PublishPrefix(ctx, 4, "favorite_v2_g4")
	if err != nil {
		t.Fatal(err)
	}
	s4, err := publisher.PublishSubject(ctx, g4.Ref, u3)
	if err != nil {
		t.Fatal(err)
	}
	if favoriteV2Count(t, db, "coverage_subject_receipt_subject_ref_v2", "receipt_sha256=$1", s4.Ref.ReceiptSHA256) != 0 {
		t.Fatal("default-off coverage receipt unexpectedly projected")
	}
	publisher.V2Candidate = candidate
	if again, err := publisher.PublishSubject(ctx, g4.Ref, u3); err != nil || again.Ref != s4.Ref {
		t.Fatalf("old receipt replay: %+v %v", again, err)
	}
	if favoriteV2Count(t, db, "coverage_subject_receipt", "receipt_sha256=$1", s4.Ref.ReceiptSHA256) != 1 ||
		favoriteV2Count(t, db, "coverage_subject_receipt_subject_ref_v2", "receipt_sha256=$1", s4.Ref.ReceiptSHA256) != 1 {
		t.Fatal("receipt replay duplicated original or sidecar")
	}
	favoriteV2Append(source, "9007199254744993", "9007199254741991", "1002", "article-u2-next", 5)
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 5 {
		t.Fatalf("new candidate ODS: %+v %v", got, err)
	}
	g5, err := publisher.PublishPrefix(ctx, 5, "favorite_v2_g5")
	if err != nil {
		t.Fatal(err)
	}
	s5, err := publisher.PublishSubject(ctx, g5.Ref, u2)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := candidate.ReadSubjectReceiptV2(ctx, g5.Ref.ManifestSHA256, "1002"); err != nil || got.ReceiptSHA256 != s5.Ref.ReceiptSHA256 {
		t.Fatalf("same UID different manifest receipt collided: %+v %v", got, err)
	}
	if favoriteV2Count(t, db, "ods_event", "source_offset=5") != 1 || favoriteV2Count(t, db, "ods_event_subject_ref_v2", "source_offset=5") != 1 {
		t.Fatal("new candidate ODS committed more than one offset")
	}
	// Inject one extra sidecar constraint *after* the owner gate to force a PG
	// failure between the old INSERT and new projection. The cursor/ACK stay put.
	favoriteV2Append(source, "9007199254745993", "9007199254741991", "1004", "article-u4", 6)
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
	 ADD CONSTRAINT injected_ods_denial_ck CHECK(source_offset<>6)`); err != nil {
		t.Fatal(err)
	}
	if _, err = consumer.RunOnce(ctx); !errors.Is(err, ErrContract) {
		t.Fatalf("sidecar failure accepted old ODS: %v", err)
	}
	if favoriteV2Count(t, db, "ods_event", "source_offset=6") != 0 ||
		favoriteV2Count(t, db, "ods_event_subject_ref_v2", "source_offset=6") != 0 {
		t.Fatal("sidecar CHECK failure left old event or projection")
	}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
	 DROP CONSTRAINT injected_ods_denial_ck`); err != nil {
		t.Fatal(err)
	}
	if got, err := consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 6 {
		t.Fatalf("same offset after rollback: %+v %v", got, err)
	}
	g6, err := publisher.PublishPrefix(ctx, 6, "favorite_v2_g6")
	if err != nil {
		t.Fatal(err)
	}
	u4 := sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1004"}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.coverage_subject_receipt_subject_ref_v2
	 ADD CONSTRAINT injected_receipt_denial_ck CHECK(subject_uid<>1004)`); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.PublishSubject(ctx, g6.Ref, u4); !errors.Is(err, ErrContract) {
		t.Fatalf("receipt sidecar failure accepted old receipt: %v", err)
	}
	if favoriteV2Count(t, db, "coverage_subject_receipt", "manifest_sha256=$1 AND subject_id='1004'", g6.Ref.ManifestSHA256) != 0 {
		t.Fatal("receipt sidecar failure left an old receipt")
	}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.coverage_subject_receipt_subject_ref_v2
	 DROP CONSTRAINT injected_receipt_denial_ck`); err != nil {
		t.Fatal(err)
	}
	s6, err := publisher.PublishSubject(ctx, g6.Ref, u4)
	if err != nil {
		t.Fatalf("same fixed receipt retry after rollback: %v", err)
	}
	if favoriteV2Count(t, db, "coverage_subject_receipt", "receipt_sha256=$1", s6.Ref.ReceiptSHA256) != 1 ||
		favoriteV2Count(t, db, "coverage_subject_receipt_subject_ref_v2", "receipt_sha256=$1", s6.Ref.ReceiptSHA256) != 1 {
		t.Fatal("receipt retry failed one old row/one sidecar")
	}
	finalODS, err := ExportODS(ctx, db)
	if err != nil || !bytes.HasPrefix(finalODS, oldODS) ||
		!bytes.Equal(objectAt(newURLPath(t, g3.ManifestURL)), oldManifest) ||
		!bytes.Equal(objectAt(newURLPath(t, s1.ReceiptURL)), oldSubjectReceipt) {
		t.Fatal("old ODS prefix or frozen manifest/receipt bytes changed")
	}
	if !strings.Contains(logBody.String(), `"warehouse.favorite.subjectref_v2.ods.started"`) ||
		!strings.Contains(logBody.String(), `"warehouse.favorite.subjectref_v2.receipt.finished"`) ||
		strings.Contains(logBody.String(), `"subject_id"`) {
		t.Fatal("candidate stages did not use bounded injected structured telemetry")
	}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
	 ALTER COLUMN subject_uid SET DEFAULT 1001`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSubjectRefV2StorageCandidate(ctx, checker, bundle); !errors.Is(err, ErrContract) {
		t.Fatalf("weakened same-name sidecar columns passed first-write gate: %v", err)
	}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.ods_event_subject_ref_v2
	 ALTER COLUMN subject_uid DROP DEFAULT`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `ALTER TABLE warehouse_favorite.ods_event
	 DROP CONSTRAINT ods_event_pkey`); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSubjectRefV2StorageCandidate(ctx, checker, bundle); !errors.Is(err, ErrContract) {
		t.Fatalf("missing original producer+offset PK passed first-write gate: %v", err)
	}
	t.Logf("old_ods_prefix_sha256=%s snapshot_sha256=%s old_manifest=%s old_receipt=%s",
		favoriteV2SHA(oldODS), preflight.SnapshotSHA256, g3.Ref.ManifestSHA256, s1.Ref.ReceiptSHA256)
}

// Test-only handoff keeps the same fresh PG16 and localhost original coverage
// server alive while a separate CH/dbt owner verifies exported sidecar rows.
// The ready file is written only after official locked Apply and full original
// S3 coverage verification; a fixture mapping must never stand in for it.
func handoffFavoriteV2PGToCH(t *testing.T, db *pgxpool.Pool, oldODS []byte,
	preflight SubjectRefV2Report, global GlobalPublication, subject SubjectPublication) {
	t.Helper()
	dir := os.Getenv("FAVORITE_V2_CH_HANDOFF_DIR")
	if dir == "" {
		return
	}
	stat, err := os.Stat(dir)
	if !filepath.IsAbs(dir) || err != nil || !stat.IsDir() || !preflight.Clear() ||
		len(preflight.VerifiedCoverageRoots) != 1 {
		t.Fatal("favorite v2 CH handoff requires an isolated directory and clear coverage preflight")
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT e.producer,e.source_offset,e.event_id,
	 e.authority_id,e.tenant_id,e.subject_id,v.issuer,v.subject_uid
	 FROM warehouse_favorite.ods_event e
	 JOIN warehouse_favorite.ods_event_subject_ref_v2 v
	  ON v.producer=e.producer AND v.source_offset=e.source_offset AND v.event_id=e.event_id
	  AND v.authority_id=e.authority_id AND v.tenant_id=e.tenant_id AND v.subject_id=e.subject_id
	 WHERE e.producer=$1 AND e.source_offset<=3 ORDER BY e.source_offset`, Producer)
	if err != nil {
		t.Fatal(err)
	}
	var mapping bytes.Buffer
	count := 0
	for rows.Next() {
		var producer, eventID, authority, tenant, subjectID, issuer string
		var offset, uid int64
		if err := rows.Scan(&producer, &offset, &eventID, &authority, &tenant,
			&subjectID, &issuer, &uid); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		encoded, err := json.Marshal(map[string]any{
			"producer": producer, "source_offset": offset, "event_id": eventID,
			"authority_id": authority, "tenant_id": tenant, "subject_id": subjectID,
			"issuer": issuer, "subject_uid": strconv.FormatInt(uid, 10)})
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		mapping.Write(encoded)
		mapping.WriteByte('\n')
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if count != 3 || preflight.ODSRows != count {
		t.Fatal("official PG sidecar is incomplete at frozen offset 3")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	objects := []map[string]string{
		{"url": global.ManifestURL, "sha256": global.Ref.ManifestSHA256},
		{"url": global.Ref.EventIndexURL, "sha256": global.Ref.EventIndexSHA256},
		{"url": global.Ref.BatchEvidenceURL, "sha256": global.Ref.BatchEvidenceSHA256},
		{"url": subject.ReceiptURL, "sha256": subject.Ref.ReceiptSHA256},
		{"url": subject.Ref.SparseIndexURL, "sha256": subject.Ref.SparseIndexSHA256},
	}
	ready, err := json.Marshal(map[string]any{
		"status": "official_apply_passed", "through_offset": count,
		"old_ods_sha256":            favoriteV2SHA(oldODS),
		"mapping_sha256":            favoriteV2SHA(mapping.Bytes()),
		"preflight_snapshot_sha256": preflight.SnapshotSHA256,
		"verified_coverage_roots":   preflight.VerifiedCoverageRoots,
		"objects":                   objects,
		"source_kind":               "isolated_DC_and_RTW_authority_HTTP_fixture_not_real_second_RTW_user",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name string
		body []byte
	}{
		{"ods.jsonl", oldODS}, {"mapping-pg.jsonl", mapping.Bytes()},
		{"ready.json", append(ready, '\n')},
	} {
		if err := os.WriteFile(filepath.Join(dir, item.name), item.body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("official PG CH handoff was not released before deadline")
		case <-ticker.C:
		}
	}
}

func newURLPath(t *testing.T, target string) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}
