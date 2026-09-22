package featurebaseline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type coveredFixedProof struct{ expected sourcecoverage.EventIndexRow }

func (p coveredFixedProof) VerifyEvent(_ context.Context, row sourcecoverage.EventIndexRow) error {
	if row.Offset != p.expected.Offset || row.EventID != p.expected.EventID || row.InputHash != p.expected.InputHash ||
		row.Subject != p.expected.Subject {
		return usermodel.ErrCoverageConflict
	}
	return nil
}

func coveredPGStore(t *testing.T) *usermodel.Store {
	t.Helper()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("run covered_test_postgres.sh with isolated PostgreSQL 16")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "h10_covered_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	if err := usermodel.MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := usermodel.MigratePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return usermodel.NewStore(pool, nil)
}

func coveredSourceEvent(t *testing.T, subject usermodel.SubjectRef, favoriteID string, offset int64,
	action usermodel.Action, predecessor *usermodel.EventKey) (sourcecoverage.EventIndexRow, usermodel.Event) {
	t.Helper()
	version, operation := int64(1), "assert"
	if action == usermodel.Retract {
		version, operation = 2, "retract"
	}
	eventID := "favorite." + favoriteID + ".v" + strconv.FormatInt(version, 10)
	at := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Second)
	received := at.Add(100 * time.Millisecond)
	payload, err := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID,
		"subject_ref": subject, "target_type": "article", "target_id": "article-1",
		"target_revision": nil, "operation": operation, "source_ref": "rtw.favorite/" + favoriteID,
		"event_time": at.Format(time.RFC3339Nano), "available_at": at.Format(time.RFC3339Nano),
		"favorite_id": favoriteID, "folder_id": "41"})
	if err != nil {
		t.Fatal(err)
	}
	source := eventing.Event{EventID: eventID, EventType: "rtw.favorite." + operation,
		SchemaVersion: 1, Producer: "rtw.community.favorite", AggregateID: favoriteID,
		AggregateVersion: version, OperationID: eventID, OccurredAt: at.Format(time.RFC3339Nano), Payload: payload}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	eventHash := coveredHash(canonical)
	row := sourcecoverage.EventIndexRow{Producer: source.Producer, Offset: strconv.FormatInt(offset, 10),
		EventID: eventID, InputHash: eventHash, RTWSourceHash: eventHash,
		ReceiptID: "dc-receipt-" + strconv.FormatInt(offset, 10), ReceivedAt: received.Format(time.RFC3339Nano),
		Subject: sourcecoverage.SubjectRef{AuthorityID: subject.AuthorityID, TenantID: subject.TenantID, SubjectID: subject.SubjectID}, EventSpec: raw}
	fact := usermodel.Event{Subject: subject, EventKey: usermodel.EventKey{Producer: source.Producer, EventID: eventID},
		Action: action, Kind: usermodel.ProductAction, Predicate: "favorite", ValueRef: "article/article-1", ItemID: "article-1",
		EvidenceRef: "dc:event:" + source.Producer + ":" + row.Offset, EvidenceHash: eventHash,
		OccurredAt: at, ObservedAt: received, SourcePartition: "dc:" + source.Producer,
		SourceSequence: offset, Supersedes: predecessor}
	return row, fact
}

func TestBuildCoveredUsesVerifiedPostgresHistoricalState(t *testing.T) {
	store := coveredPGStore(t)
	ctx := context.Background()
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	row, assert := coveredSourceEvent(t, subject, "100", 1, usermodel.Assert, nil)
	if receipt, err := store.Append(ctx, assert); err != nil || receipt.Status != "accepted" {
		t.Fatalf("assert fact: %+v %v", receipt, err)
	}
	_, pending := coveredSourceEvent(t, subject, "200", 2, usermodel.Retract,
		&usermodel.EventKey{Producer: assert.Producer, EventID: "favorite.200.v1"})
	if receipt, err := store.Append(ctx, pending); err != nil || receipt.Status != "pending_dependency" {
		t.Fatalf("pending fact: %+v %v", receipt, err)
	}
	_, retract := coveredSourceEvent(t, subject, "100", 3, usermodel.Retract, &assert.EventKey)
	if receipt, err := store.Append(ctx, retract); err != nil || receipt.Status != "accepted" {
		t.Fatalf("retract fact: %+v %v", receipt, err)
	}
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			body, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case http.MethodPut:
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read", http.StatusInternalServerError)
				return
			}
			objects[r.URL.Path] = body
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	root := server.URL + "/fixed"
	full, indexHash, err := sourcecoverage.EventIndexJSONL(row.Producer, 1, []sourcecoverage.EventIndexRow{row})
	if err != nil {
		t.Fatal(err)
	}
	batchHash, err := sourcecoverage.BatchHash([]sourcecoverage.EventIndexRow{row})
	if err != nil {
		t.Fatal(err)
	}
	batches, batchDigest, err := sourcecoverage.BatchEvidenceJSONL([]sourcecoverage.BatchEvidence{{
		FromOffset: "1", ToOffset: "1", BatchHash: batchHash}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	prefix := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Producer: row.Producer, Origin: "1", ThroughOffset: "1", BindingPolicyID: usermodel.FavoriteCoveragePolicyID,
		WarehouseConsumer: "btw-warehouse-favorite", WarehouseGeneration: "h10_pg_001",
		EventIndexURL: root + "/coverage/full.jsonl", EventIndexSHA256: indexHash,
		BatchEvidenceURL: root + "/coverage/batches.jsonl", BatchEvidenceSHA256: batchDigest}
	prefix.ManifestSHA256, err = sourcecoverage.GlobalManifestHash(prefix)
	if err != nil {
		t.Fatal(err)
	}
	sparse, sparseHash, count, err := sourcecoverage.SubjectIndexJSONL(row.Subject, []sourcecoverage.EventIndexRow{row})
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Subject: row.Subject, Prefix: prefix, SparseIndexURL: root + "/coverage/1001.jsonl",
		SparseIndexSHA256: sparseHash, EventCount: count}
	ref.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	objects["/fixed/coverage/1001.jsonl"] = bytes.Clone(sparse)
	mu.Unlock()
	builder := CoveredBuilder{State: UsermodelCoveredReader{Store: store},
		Objects:      RunnerCoveredObjects{Runner: Runner{HTTPClient: server.Client()}},
		ArtifactRoot: root, Now: func() time.Time { return time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC) }}
	if _, err := builder.BuildCovered(ctx, subject, FavoriteCandidateSpec(), "covered_pg", 1, prefix); !errors.Is(err, usermodel.ErrCoverageUnverified) {
		t.Fatalf("unverified global root produced candidate: %v", err)
	}
	verifier, err := usermodel.NewCoverageVerifier(store, coveredFixedProof{expected: row})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPrefix(ctx, prefix, full, batches); err != nil {
		t.Fatalf("fixed source proof and PG verify: %v", err)
	}
	if _, err := verifier.VerifySubject(ctx, ref, sparse); err != nil {
		t.Fatalf("verified subject slice: %v", err)
	}
	state, err := store.CoveredStateAt(ctx, subject, prefix, time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC))
	if err != nil || len(state.ActiveAtPrefix) != 1 || len(state.Tail) != 2 || state.CurrentComplete {
		t.Fatalf("PG W1 historical fold: %+v %v", state, err)
	}
	result, err := builder.BuildCovered(ctx, subject, FavoriteCandidateSpec(), "covered_pg", 1, prefix)
	if err != nil || result.Candidate.Status != "candidate_default_off" ||
		len(result.Candidate.Values) != 1 || result.Candidate.Values[0].Value != "1" ||
		len(result.Candidate.Tail) != 2 || result.Candidate.Tail[0].Offset != 2 || result.Candidate.Tail[1].Offset != 3 ||
		result.Candidate.CurrentComplete {
		t.Fatalf("PG W1 candidate used latest empty Active: %+v %v", result.Candidate, err)
	}
	mu.Lock()
	objects["/fixed/coverage/1001.jsonl"] = []byte("tampered")
	mu.Unlock()
	if _, err := builder.BuildCovered(ctx, subject, FavoriteCandidateSpec(), "covered_pg", 1, prefix); !errors.Is(err, ErrCoveredContract) {
		t.Fatalf("tampered sparse object remained acceptable: %v", err)
	}
}
