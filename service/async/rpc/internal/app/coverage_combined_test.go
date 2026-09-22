package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/favoritesource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/featurebaseline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const producer = favoritesource.Producer

type joinedGate struct {
	warehouse *pgxpool.Pool
	model     *pgxpool.Pool
	store     *usermodel.Store
	dc        *datacenter.Client
	binder    *app.FavoriteAuthorityBinder
	consumer  *favoritesource.CoverageConsumer
	publisher *favoritesource.CoveragePublisher
	verifier  *usermodel.CoverageVerifier
	worker    *app.FactWorker
	graph     *usermodel.FactGraphRuntime
	builder   featurebaseline.CoveredBuilder
	objects   featurebaseline.RunnerCoveredObjects
}

func joinedEnv(t *testing.T, warehouseDSN, modelDSN, dcURL, dcToken, authorityURL, authorityToken, s3 string) *joinedGate {
	t.Helper()
	ctx := context.Background()
	warehouse, err := pgxpool.New(ctx, warehouseDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(warehouse.Close)
	if err := favoritesource.Initialize(ctx, warehouse); err != nil {
		t.Fatal(err)
	}
	if err := favoritesource.InitializeCoverage(ctx, warehouse); err != nil {
		t.Fatal(err)
	}
	model, err := pgxpool.New(ctx, modelDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	if err := usermodel.MigratePool(ctx, model); err != nil {
		t.Fatal(err)
	}
	observed, err := telemetry.New(ctx, telemetry.Config{Service: "coverage-combined-acceptance", Environment: "test", Version: "v2",
		InstanceID: "isolated", Output: io.Discard, Level: slog.LevelInfo, TraceExporter: tracetest.NewInMemoryExporter(), SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observed.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	store := usermodel.NewStore(model, observed)
	graph, err := usermodel.NewFactGraphRuntime("coverage-combined", store, inmemory.NewSessionService(), observed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := graph.Close(); err != nil {
			t.Error(err)
		}
	})
	dc, err := datacenter.New(httpclient.Config{BaseURL: dcURL, Token: dcToken})
	if err != nil {
		t.Fatal(err)
	}
	binder, err := app.NewFavoriteAuthorityBinder(app.FavoriteAuthorityBinderConfig{BaseURL: authorityURL, Token: authorityToken})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := usermodel.NewFavoriteCoverageHTTPProof(dc, usermodel.FavoriteCoverageProofConfig{AuthorityURL: authorityURL, AuthorityToken: authorityToken})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := usermodel.NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := app.NewFactWorker(app.FactWorkerConfig{Consumer: "btw-coverage-combined-fact", Producer: producer, BatchLimit: 1,
		Bindings: []app.FactEventBinding{{EventType: "rtw.favorite.assert", SchemaVersion: 1, Action: usermodel.Assert, EvidenceBinder: binder},
			{EventType: "rtw.favorite.retract", SchemaVersion: 1, Action: usermodel.Retract, EvidenceBinder: binder}}}, dc, nil, graph, store, observed)
	if err != nil {
		t.Fatal(err)
	}
	objects := featurebaseline.RunnerCoveredObjects{Runner: featurebaseline.Runner{}}
	g := &joinedGate{warehouse: warehouse, model: model, store: store, dc: dc, binder: binder,
		consumer:  &favoritesource.CoverageConsumer{DB: warehouse, Source: dc, Binder: binder, Limit: 1},
		publisher: &favoritesource.CoveragePublisher{DB: warehouse, Source: dc, Binder: binder, S3Prefix: s3},
		verifier:  verifier, worker: worker, graph: graph, objects: objects,
		builder: featurebaseline.CoveredBuilder{State: featurebaseline.UsermodelCoveredReader{Store: store}, Objects: objects,
			ArtifactRoot: strings.TrimRight(s3, "/") + "/warehouse-coverage"}}
	g.assertNoServingHeads(t)
	return g
}

func (g *joinedGate) sourceBytes(t *testing.T, ref sourcecoverage.GlobalPrefixRef) ([]byte, []byte) {
	t.Helper()
	ctx := context.Background()
	index, err := g.objects.ReadFixed(ctx, ref.EventIndexURL, ref.EventIndexSHA256)
	if err != nil {
		t.Fatal(err)
	}
	batches, err := g.objects.ReadFixed(ctx, ref.BatchEvidenceURL, ref.BatchEvidenceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return index, batches
}

func (g *joinedGate) verifySubject(t *testing.T, ref sourcecoverage.SubjectCoverageRef) {
	t.Helper()
	ctx := context.Background()
	sparse, err := g.objects.ReadFixed(ctx, ref.SparseIndexURL, ref.SparseIndexSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.verifier.VerifySubject(ctx, ref, sparse); err != nil {
		t.Fatal(err)
	}
}

func (g *joinedGate) candidate(t *testing.T, subject sourcecoverage.SubjectRef, prefix sourcecoverage.GlobalPrefixRef,
	generation string, revision int64) featurebaseline.CoveredBuildResult {
	t.Helper()
	result, err := g.builder.BuildCovered(context.Background(), usermodel.SubjectRef(subject),
		featurebaseline.FavoriteCandidateSpec(), generation, revision, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if result.Candidate.Status != "candidate_default_off" || result.Candidate.Coverage.Prefix != prefix {
		t.Fatalf("candidate escaped default-off/ref: %+v", result)
	}
	return result
}

func (g *joinedGate) coveredSubmission(t *testing.T, built featurebaseline.CoveredBuildResult) usermodel.CoveredBaselineSubmission {
	t.Helper()
	raw, err := g.objects.ReadFixed(context.Background(), built.ArtifactURL, built.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	encoded, err := json.Marshal(built.Candidate)
	if err != nil || hex.EncodeToString(sum[:]) != built.ArtifactHash || !bytes.Equal(raw, encoded) {
		t.Fatalf("H10 S3 original candidate bytes/hash differ: %v", err)
	}
	return usermodel.CoveredBaselineSubmission{ArtifactURL: built.ArtifactURL,
		ArtifactSHA256: built.ArtifactHash, CandidateJSON: raw,
		FeatureSpec: featurebaseline.FavoriteCandidateSpec()}
}

func (g *joinedGate) acceptCovered(t *testing.T, built featurebaseline.CoveredBuildResult,
	wantReplay bool) usermodel.CoveredBaselineReceipt {
	t.Helper()
	input := g.coveredSubmission(t, built)
	receipt, err := g.store.AcceptCoveredBaseline(context.Background(), input)
	if err != nil || receipt.Status != "accepted_historical_default_off" || receipt.Replay != wantReplay ||
		receipt.ArtifactSHA256 != built.ArtifactHash || receipt.Coverage != built.Candidate.Coverage ||
		receipt.Subject != built.Candidate.Subject || receipt.Revision != built.Candidate.Revision {
		t.Fatalf("v2 historical receipt differs: %+v %v", receipt, err)
	}
	var stored []byte
	if err := g.model.QueryRow(context.Background(), `SELECT candidate_raw FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
		receipt.Subject.AuthorityID, receipt.Subject.TenantID, receipt.Subject.SubjectID, receipt.Revision).Scan(&stored); err != nil || !bytes.Equal(stored, input.CandidateJSON) {
		t.Fatalf("PG raw candidate differs from S3: %v", err)
	}
	g.assertNoServingHeads(t)
	return receipt
}

func (g *joinedGate) freezeCovered(t *testing.T, built featurebaseline.CoveredBuildResult,
	wantCurrent bool) (usermodel.CoveredHistoricalSnapshot, usermodel.CoveredBundleCandidateV2) {
	t.Helper()
	ctx := context.Background()
	subject := built.Candidate.Subject
	spec := featurebaseline.FavoriteCandidateSpec()
	snapshot, err := g.store.FreezeCoveredSnapshot(ctx, subject, built.Candidate.Revision, spec)
	if err != nil || snapshot.Status != "historical_default_off" || snapshot.BaselineArtifactSHA256 != built.ArtifactHash ||
		len(snapshot.Values) != 1 || snapshot.Values[0].Value != built.Candidate.Values[0].Value {
		t.Fatalf("same-run v2 snapshot differs from H10 candidate: %+v %v", snapshot, err)
	}
	stored, err := g.store.CoveredSnapshotV2ByID(ctx, subject, snapshot.ID)
	if err != nil || stored.ID != snapshot.ID || stored.Replay {
		t.Fatalf("v2 snapshot historical read differs: %+v %v", stored, err)
	}
	pair, err := usermodel.CoveredFixedCandidatePair(spec)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := g.store.BuildFixedCoveredBundleCandidate(ctx, subject, snapshot.ID, pair)
	if !wantCurrent {
		if !errors.Is(err, usermodel.ErrCoveredBundlePending) {
			t.Fatalf("W1 history with accepted tail became current: %+v %v", bundle, err)
		}
		g.assertNoServingHeads(t)
		return snapshot, usermodel.CoveredBundleCandidateV2{}
	}
	if err != nil || bundle.Status != "candidate_default_off" || bundle.CoveredSnapshotID != snapshot.ID ||
		len(bundle.Vector) != 1 || bundle.Vector[0] != float64(len(snapshot.Contributions)) {
		t.Fatalf("same-run v2 fixed candidate differs: %+v %v", bundle, err)
	}
	storedBundle, err := g.store.CoveredBundleCandidateByID(ctx, subject, bundle.ID)
	if err != nil || storedBundle.ID != bundle.ID || storedBundle.Replay {
		t.Fatalf("v2 bundle historical read differs: %+v %v", storedBundle, err)
	}
	g.assertNoServingHeads(t)
	return snapshot, bundle
}

func (g *joinedGate) assertNoServingHeads(t *testing.T) {
	t.Helper()
	for _, table := range []string{"usermodel_feature_baselines", "usermodel_feature_heads",
		"usermodel_feature_snapshots", "usermodel_feature_snapshot_versions",
		"usermodel_serving_bundles", "usermodel_serving_pointers"} {
		var count int64
		if err := g.model.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("v2 acceptance moved v1/Serving table %s: %d %v", table, count, err)
		}
	}
}

func (g *joinedGate) graphAppendAt(t *testing.T, offset int64) {
	t.Helper()
	ctx := context.Background()
	batch, err := g.dc.ReadEvents(ctx, "btw-coverage-combined-fact", producer, 10)
	if err != nil {
		t.Fatal(err)
	}
	var selected *eventing.Item
	for i := range batch.Events {
		if batch.Events[i].Offset == offset {
			selected = &batch.Events[i]
			break
		}
	}
	if selected == nil {
		t.Fatalf("DC event at offset %d unavailable", offset)
	}
	item := *selected
	receipt, err := g.dc.EventReceipt(ctx, producer, item.Event.EventID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := g.binder.BindFactWithEvidence(ctx, app.FactSourceEvidence{Event: item.Event, InputHash: item.InputHash, Offset: item.Offset, Receipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	occurred, err1 := time.Parse(time.RFC3339Nano, item.Event.OccurredAt)
	observed, err2 := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	if err1 != nil || err2 != nil {
		t.Fatal("source time invalid")
	}
	bound.EventKey = usermodel.EventKey{Producer: producer, EventID: item.Event.EventID}
	bound.EvidenceRef = "dc:event:" + producer + ":" + strconv.FormatInt(offset, 10)
	bound.EvidenceHash = item.InputHash
	bound.OccurredAt = occurred.UTC()
	bound.ObservedAt = observed.UTC()
	bound.SourcePartition = "dc:" + producer
	bound.SourceSequence = offset
	runID := fmt.Sprintf("coverage-direct-graph:%d", offset)
	if got, err := g.graph.Append(ctx, usermodel.FactGraphRequest{Event: bound, SessionID: runID, RunID: runID}); err != nil || got.Status != "accepted" {
		t.Fatalf("real Graph/Store/Outbox offset %d: %+v %v", offset, got, err)
	}
	if got, err := g.store.CurrentReceipt(ctx, bound.Subject, bound.EventKey); err != nil || got.Status != "accepted" {
		t.Fatalf("source lacks accepted Outbox: %+v %v", got, err)
	}
}

func sourceSubject(id string) sourcecoverage.SubjectRef {
	return sourcecoverage.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: id}
}

func writeJoinedReport(t *testing.T, path string, value any) {
	t.Helper()
	if path == "" {
		return
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedRealRTWPublisherVerifierH10(t *testing.T) {
	keys := []string{"COVERAGE_JOIN_REAL_WAREHOUSE_DSN", "COVERAGE_JOIN_REAL_MODEL_DSN", "COVERAGE_JOIN_REAL_DC_URL", "COVERAGE_JOIN_REAL_DC_TOKEN",
		"COVERAGE_JOIN_REAL_AUTHORITY_URL", "COVERAGE_JOIN_REAL_AUTHORITY_TOKEN", "COVERAGE_JOIN_S3_PREFIX"}
	for _, key := range keys {
		if os.Getenv(key) == "" {
			t.Skip("run coverage/combined_acceptance.sh with true RTW/DC")
		}
	}
	g := joinedEnv(t, os.Getenv(keys[0]), os.Getenv(keys[1]), os.Getenv(keys[2]), os.Getenv(keys[3]), os.Getenv(keys[4]), os.Getenv(keys[5]), os.Getenv(keys[6]))
	ctx := context.Background()
	u1 := sourceSubject("1001")
	u2 := sourceSubject("1002")
	if got, err := g.consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 1 {
		t.Fatalf("publisher W1 ingest: %+v %v", got, err)
	}
	p1, err := g.publisher.PublishPrefix(ctx, 1, "combined_real_w1")
	if err != nil {
		t.Fatal(err)
	}
	s1, err := g.publisher.PublishSubject(ctx, p1.Ref, u1)
	if err != nil {
		t.Fatal(err)
	}
	se, err := g.publisher.PublishSubject(ctx, p1.Ref, u2)
	if err != nil || se.Ref.EventCount != 0 {
		t.Fatalf("real empty slice: %+v %v", se, err)
	}
	index, batches := g.sourceBytes(t, p1.Ref)
	if _, err := g.verifier.VerifyPrefix(ctx, p1.Ref, index, batches); !errors.Is(err, usermodel.ErrCoveragePending) {
		t.Fatalf("unaccepted fact produced coverage: %v", err)
	}
	if got, err := g.worker.RunOnce(ctx); err != nil || got.AckedOffset != 1 {
		t.Fatalf("Graph assert: %+v %v", got, err)
	}
	if _, err := g.verifier.VerifyPrefix(ctx, p1.Ref, index, batches); err != nil {
		t.Fatal(err)
	}
	g.verifySubject(t, s1.Ref)
	g.verifySubject(t, se.Ref)
	g.assertNoServingHeads(t)
	b1 := g.candidate(t, u1, p1.Ref, "combined_real_candidate_w1", 1)
	if len(b1.Candidate.Values) != 1 || b1.Candidate.Values[0].Value != "1" || !b1.Candidate.CurrentComplete {
		t.Fatalf("real W1 count: %+v", b1.Candidate)
	}
	if got, err := g.consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 2 {
		t.Fatalf("publisher W2 ingest: %+v %v", got, err)
	}
	if got, err := g.worker.RunOnce(ctx); err != nil || got.AckedOffset != 2 {
		t.Fatalf("Graph retract: %+v %v", got, err)
	}
	late := g.candidate(t, u1, p1.Ref, "combined_real_candidate_w1_after_retract", 1)
	if late.Candidate.Values[0].Value != "1" || len(late.Candidate.Tail) != 1 || late.Candidate.Tail[0].Offset != 2 || late.Candidate.CurrentComplete {
		t.Fatalf("W1 history lost after retract: %+v", late.Candidate)
	}
	acceptedW1 := g.acceptCovered(t, late, false)
	g.acceptCovered(t, late, true)
	snapshotW1, _ := g.freezeCovered(t, late, false)
	if _, err := g.store.AcceptCoveredBaseline(ctx, g.coveredSubmission(t, b1)); !errors.Is(err, usermodel.ErrCoveredBaselineConflict) {
		t.Fatalf("different W1 hash reused accepted revision: %v", err)
	}
	wrongBytes := g.coveredSubmission(t, late)
	wrongBytes.CandidateJSON = append(bytes.Clone(wrongBytes.CandidateJSON), 'x')
	if _, err := g.store.AcceptCoveredBaseline(ctx, wrongBytes); !errors.Is(err, usermodel.ErrCoveredBaselineInvalid) {
		t.Fatalf("changed S3 candidate bytes retained old hash: %v", err)
	}
	g.assertNoServingHeads(t)
	p2, err := g.publisher.PublishPrefix(ctx, 2, "combined_real_w2")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := g.publisher.PublishSubject(ctx, p2.Ref, u1)
	if err != nil {
		t.Fatal(err)
	}
	i2, b2 := g.sourceBytes(t, p2.Ref)
	if _, err := g.verifier.VerifyPrefix(ctx, p2.Ref, i2, b2); err != nil {
		t.Fatal(err)
	}
	g.verifySubject(t, s2.Ref)
	final := g.candidate(t, u1, p2.Ref, "combined_real_candidate_w2", 2)
	if final.Candidate.Values[0].Value != "0" || !final.Candidate.CurrentComplete || final.ArtifactHash == b1.ArtifactHash {
		t.Fatalf("real W2 count: %+v", final.Candidate)
	}
	acceptedW2 := g.acceptCovered(t, final, false)
	g.acceptCovered(t, final, true)
	snapshotW2, bundleW2 := g.freezeCovered(t, final, true)
	if got, err := g.dc.ReadEvents(ctx, "btw-coverage-combined-fact", producer, 10); err != nil || got.FromOffset != 3 {
		t.Fatalf("Graph ACK did not advance separately: %+v %v", got, err)
	}
	if got, err := g.dc.ReadEvents(ctx, favoritesource.DefaultConsumer, producer, 10); err != nil || got.FromOffset != 3 {
		t.Fatalf("warehouse ACK did not advance separately: %+v %v", got, err)
	}
	if path := os.Getenv("COVERAGE_JOIN_REAL_ODS_OUTPUT"); path != "" {
		body, err := favoritesource.ExportODS(ctx, g.warehouse)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJoinedReport(t, os.Getenv("COVERAGE_JOIN_REAL_REPORT"), map[string]any{"G1": p1.Ref, "G2": p2.Ref, "U1W1": s1.Ref, "U2W1": se.Ref, "U1W2": s2.Ref,
		"candidate_w1_sha256": b1.ArtifactHash, "candidate_w1_after_retract_sha256": late.ArtifactHash, "candidate_w2_sha256": final.ArtifactHash,
		"accepted_v2": map[string]any{"w1_after_retract": acceptedW1, "w2": acceptedW2, "v1_and_serving_heads": 0},
		"snapshot_v2": map[string]any{"w1_after_retract": snapshotW1, "w2": snapshotW2},
		"bundle_v2":   map[string]any{"w1_after_retract": "pending_tail", "w2": bundleW2}})
	t.Logf("true RTW/DC Publisher→Verifier→H10 W1=%s W2=%s candidate1=%s candidate2=%s", p1.Ref.EventIndexSHA256, p2.Ref.EventIndexSHA256, b1.ArtifactHash, final.ArtifactHash)
}

type fakeAuthorityRecord struct {
	Event       eventing.Event
	Receipt     eventing.Receipt
	Subject     sourcecoverage.SubjectRef
	Predecessor string
}
type fakeAuthority struct {
	mu           sync.Mutex
	records      map[string]fakeAuthorityRecord
	wrongSubject bool
}

func (f *fakeAuthority) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer coverage-combined-fixture-authority-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	id := parts[len(parts)-1]
	f.mu.Lock()
	record, ok := f.records[id]
	wrong := f.wrongSubject
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if wrong && strings.Contains(id, "9007199254742993") {
		record.Subject.SubjectID = "1001"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"event": record.Event, "subject_ref": record.Subject,
		"predecessor_event_id": record.Predecessor, "technical_receipt": record.Receipt, "source_event_hash": record.Receipt.InputHash})
}

func fixtureEvent(id, folder, user, target, operation string, at time.Time) eventing.Event {
	version := int64(1)
	if operation == "retract" {
		version = 2
	}
	eventID := fmt.Sprintf("favorite.%s.v%d", id, version)
	payload, _ := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID, "subject_ref": sourceSubject(user),
		"target_type": "article", "target_id": target, "target_revision": target + ":r1", "operation": operation,
		"source_ref": "rtw.favorite/" + id, "event_time": at.Format(time.RFC3339Nano), "available_at": at.Format(time.RFC3339Nano),
		"favorite_id": id, "folder_id": folder})
	return eventing.Event{EventID: eventID, EventType: "rtw.favorite." + operation, SchemaVersion: 1, Producer: producer,
		AggregateID: id, AggregateVersion: version, OperationID: eventID, OccurredAt: at.Format(time.RFC3339Nano), Payload: payload}
}

func TestCombinedTwoSubjectsPublisherVerifierH10(t *testing.T) {
	keys := []string{"COVERAGE_JOIN_TWO_WAREHOUSE_DSN", "COVERAGE_JOIN_TWO_MODEL_DSN", "COVERAGE_JOIN_TWO_DC_URL", "COVERAGE_JOIN_TWO_DC_TOKEN", "COVERAGE_JOIN_S3_PREFIX"}
	for _, key := range keys {
		if os.Getenv(key) == "" {
			t.Skip("run coverage/combined_acceptance.sh with isolated two-subject DC")
		}
	}
	ctx := context.Background()
	realRTW := os.Getenv("COVERAGE_JOIN_TWO_REAL_RTW") == "1"
	var authority *fakeAuthority
	authorityURL, authorityToken := os.Getenv("COVERAGE_JOIN_TWO_AUTHORITY_URL"), os.Getenv("COVERAGE_JOIN_TWO_AUTHORITY_TOKEN")
	dc, err := datacenter.New(httpclient.Config{BaseURL: os.Getenv(keys[2]), Token: os.Getenv(keys[3])})
	if err != nil {
		t.Fatal(err)
	}
	if realRTW {
		if authorityURL == "" || authorityToken == "" {
			t.Fatal("real RTW two-user source requires its authority URL and token")
		}
		batch, err := dc.ReadEvents(ctx, "btw-coverage-combined-fact", producer, 10)
		if err != nil || len(batch.Events) != 3 || batch.FromOffset != 1 {
			t.Fatalf("real RTW two-user DC source differs: %+v %v", batch, err)
		}
		for i, want := range []string{"favorite.9007199254741993.v1", "favorite.9007199254742993.v1", "favorite.9007199254741993.v2"} {
			if batch.Events[i].Offset != int64(i+1) || batch.Events[i].Event.EventID != want {
				t.Fatalf("real RTW two-user order differs at %d: %+v", i, batch.Events[i])
			}
		}
	} else {
		authority = &fakeAuthority{records: map[string]fakeAuthorityRecord{}}
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
		for i, spec := range []struct{ id, folder, user, target, operation string }{
			{"9007199254741993", "9007199254741991", "1001", "article-u1", "assert"},
			{"9007199254742993", "9007199254742991", "1002", "article-u2", "assert"},
			{"9007199254741993", "9007199254741991", "1001", "article-u1", "retract"},
		} {
			event := fixtureEvent(spec.id, spec.folder, spec.user, spec.target, spec.operation, at.Add(time.Duration(i)*time.Second))
			receipt, err := dc.PublishEvent(ctx, event)
			if err != nil || receipt.Offset != int64(i+1) {
				t.Fatalf("real DC fixture publish: %+v %v", receipt, err)
			}
			predecessor := ""
			if spec.operation == "retract" {
				predecessor = "favorite." + spec.id + ".v1"
			}
			authority.records[event.EventID] = fakeAuthorityRecord{Event: event, Receipt: receipt, Subject: sourceSubject(spec.user), Predecessor: predecessor}
		}
		server := httptest.NewServer(http.HandlerFunc(authority.handler))
		defer server.Close()
		authorityURL, authorityToken = server.URL, "coverage-combined-fixture-authority-token"
	}
	g := joinedEnv(t, os.Getenv(keys[0]), os.Getenv(keys[1]), os.Getenv(keys[2]), os.Getenv(keys[3]), authorityURL,
		authorityToken, os.Getenv(keys[4]))
	u1, u2, u3 := sourceSubject("1001"), sourceSubject("1002"), sourceSubject("1003")
	if got, err := g.consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != 1 {
		t.Fatalf("publisher W1: %+v %v", got, err)
	}
	p1, err := g.publisher.PublishPrefix(ctx, 1, "combined_two_w1")
	if err != nil {
		t.Fatal(err)
	}
	s1, err := g.publisher.PublishSubject(ctx, p1.Ref, u1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := g.worker.RunOnce(ctx); err != nil || got.AckedOffset != 1 {
		t.Fatalf("Graph u1 assert: %+v %v", got, err)
	}
	i1, b1 := g.sourceBytes(t, p1.Ref)
	if _, err := g.verifier.VerifyPrefix(ctx, p1.Ref, i1, b1); err != nil {
		t.Fatal(err)
	}
	g.verifySubject(t, s1.Ref)
	first := g.candidate(t, u1, p1.Ref, "combined_two_candidate_w1", 1)
	if first.Candidate.Values[0].Value != "1" || !first.Candidate.CurrentComplete {
		t.Fatalf("u1 W1: %+v", first.Candidate)
	}
	for _, want := range []int64{2, 3} {
		if got, err := g.consumer.RunOnce(ctx); err != nil || got.AcknowledgedOffset != want {
			t.Fatalf("publisher offset %d: %+v %v", want, got, err)
		}
	}
	p3, err := g.publisher.PublishPrefix(ctx, 3, "combined_two_w3")
	if err != nil {
		t.Fatal(err)
	}
	s3u1, err := g.publisher.PublishSubject(ctx, p3.Ref, u1)
	if err != nil || s3u1.Ref.EventCount != 2 {
		t.Fatalf("u1 sparse: %+v %v", s3u1, err)
	}
	s3u2, err := g.publisher.PublishSubject(ctx, p3.Ref, u2)
	if err != nil || s3u2.Ref.EventCount != 1 {
		t.Fatalf("u2 sparse: %+v %v", s3u2, err)
	}
	s3u3, err := g.publisher.PublishSubject(ctx, p3.Ref, u3)
	if err != nil || s3u3.Ref.EventCount != 0 {
		t.Fatalf("u3 empty: %+v %v", s3u3, err)
	}
	if got, err := g.dc.ReadEvents(ctx, favoritesource.DefaultConsumer, producer, 10); err != nil || got.FromOffset != 4 {
		t.Fatalf("publisher ACK W3: %+v %v", got, err)
	}
	if got, err := g.dc.ReadEvents(ctx, "btw-coverage-combined-fact", producer, 10); err != nil || got.FromOffset != 2 {
		t.Fatalf("publisher stole Graph ACK: %+v %v", got, err)
	}
	g.graphAppendAt(t, 3)
	late := g.candidate(t, u1, p1.Ref, "combined_two_candidate_w1_after_tail", 1)
	if late.Candidate.Values[0].Value != "1" || len(late.Candidate.Tail) != 1 || late.Candidate.Tail[0].Offset != 3 || late.Candidate.CurrentComplete {
		t.Fatalf("W1 assert lost to later retract: %+v", late.Candidate)
	}
	acceptedW1 := g.acceptCovered(t, late, false)
	g.acceptCovered(t, late, true)
	snapshotW1, _ := g.freezeCovered(t, late, false)
	if acceptedW1.Status != "accepted_historical_default_off" || acceptedW1.InputStateVersion != late.Candidate.InputStateVersion ||
		late.Candidate.CurrentComplete {
		t.Fatalf("tail W1 was promoted to serving state: %+v", acceptedW1)
	}
	if _, err := g.store.AcceptCoveredBaseline(ctx, g.coveredSubmission(t, first)); !errors.Is(err, usermodel.ErrCoveredBaselineConflict) {
		t.Fatalf("different W1 candidate hash reused revision: %v", err)
	}
	g.assertNoServingHeads(t)
	i3, b3 := g.sourceBytes(t, p3.Ref)
	parts := bytes.Split(bytes.TrimSuffix(i3, []byte{'\n'}), []byte{'\n'})
	if len(parts) != 3 {
		t.Fatalf("expected three global index rows, got %d", len(parts))
	}
	dropped := append(append(bytes.Clone(parts[0]), '\n'), parts[2]...)
	dropped = append(dropped, '\n')
	if _, err := g.verifier.VerifyPrefix(ctx, p3.Ref, dropped, b3); !errors.Is(err, usermodel.ErrCoverageConflict) {
		t.Fatalf("missing index row accepted: %v", err)
	}
	if authority != nil {
		authority.mu.Lock()
		authority.wrongSubject = true
		authority.mu.Unlock()
		if _, err := g.verifier.VerifyPrefix(ctx, p3.Ref, i3, b3); !errors.Is(err, usermodel.ErrCoverageConflict) {
			t.Fatalf("wrong RTW subject accepted: %v", err)
		}
		authority.mu.Lock()
		authority.wrongSubject = false
		authority.mu.Unlock()
	}
	if _, err := g.verifier.VerifyPrefix(ctx, p3.Ref, i3, b3); !errors.Is(err, usermodel.ErrCoveragePending) {
		t.Fatalf("offset2 absent PG fact accepted: %v", err)
	}
	if got, err := g.worker.RunOnce(ctx); err != nil || got.AckedOffset != 2 {
		t.Fatalf("Graph u2 assert: %+v %v", got, err)
	}
	if got, err := g.worker.RunOnce(ctx); err != nil || got.AckedOffset != 3 {
		t.Fatalf("Graph u1 retract replay: %+v %v", got, err)
	}
	if _, err := g.verifier.VerifyPrefix(ctx, p3.Ref, i3, b3); err != nil {
		t.Fatalf("verified W3: %v", err)
	}
	sparseU2, err := g.objects.ReadFixed(ctx, s3u2.Ref.SparseIndexURL, s3u2.Ref.SparseIndexSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.verifier.VerifySubject(ctx, s3u2.Ref, append(bytes.Clone(sparseU2), 'x')); !errors.Is(err, usermodel.ErrCoverageConflict) {
		t.Fatalf("tampered sparse bytes accepted: %v", err)
	}
	for _, ref := range []sourcecoverage.SubjectCoverageRef{s3u1.Ref, s3u2.Ref, s3u3.Ref} {
		g.verifySubject(t, ref)
	}
	c1 := g.candidate(t, u1, p3.Ref, "combined_two_candidate_u1_w3", 2)
	c2 := g.candidate(t, u2, p3.Ref, "combined_two_candidate_u2_w3", 1)
	c3 := g.candidate(t, u3, p3.Ref, "combined_two_candidate_u3_w3", 1)
	if c1.Candidate.Values[0].Value != "0" || c2.Candidate.Values[0].Value != "1" || c3.Candidate.Values[0].Value != "0" ||
		!c1.Candidate.CurrentComplete || !c2.Candidate.CurrentComplete || !c3.Candidate.CurrentComplete {
		t.Fatalf("W3 two-user counts: u1=%+v u2=%+v u3=%+v", c1.Candidate, c2.Candidate, c3.Candidate)
	}
	acceptedU1 := g.acceptCovered(t, c1, false)
	acceptedU2 := g.acceptCovered(t, c2, false)
	acceptedU3 := g.acceptCovered(t, c3, false)
	snapshotU1, bundleU1 := g.freezeCovered(t, c1, true)
	snapshotU2, bundleU2 := g.freezeCovered(t, c2, true)
	snapshotU3, bundleU3 := g.freezeCovered(t, c3, true)
	for _, candidate := range []featurebaseline.CoveredBuildResult{c1, c2, c3} {
		g.acceptCovered(t, candidate, true)
	}
	if acceptedU1.Revision != 2 || acceptedU2.Revision != 1 || acceptedU3.Revision != 1 ||
		acceptedU3.Coverage.EventCount != 0 || acceptedU3.Status != "accepted_historical_default_off" {
		t.Fatalf("multi-subject v2 historical receipts differ: u1=%+v u2=%+v empty=%+v", acceptedU1, acceptedU2, acceptedU3)
	}
	g.assertNoServingHeads(t)
	old, err := g.objects.ReadFixed(ctx, first.ArtifactURL, first.ArtifactHash)
	if err != nil || len(old) == 0 {
		t.Fatalf("W1 artifact changed: %v", err)
	}
	if oldIndex, oldBatch := g.sourceBytes(t, p1.Ref); !bytes.Equal(oldIndex, i1) || !bytes.Equal(oldBatch, b1) {
		t.Fatal("W1 global root bytes changed after W3")
	}
	originalSparse, err := g.objects.ReadFixed(ctx, s3u1.Ref.SparseIndexURL, s3u1.Ref.SparseIndexSHA256)
	if err != nil {
		t.Fatal(err)
	}
	putSparse := func(body []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, s3u1.Ref.SparseIndexURL, bytes.NewReader(body))
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
	putSparse([]byte("tampered"))
	if _, err := g.builder.BuildCovered(ctx, usermodel.SubjectRef(u1), featurebaseline.FavoriteCandidateSpec(), "combined_two_tampered", 4, p3.Ref); !errors.Is(err, featurebaseline.ErrCoveredContract) {
		t.Fatalf("tampered S3 sparse object reached H10: %v", err)
	}
	putSparse(originalSparse)
	if got, err := g.dc.ReadEvents(ctx, favoritesource.DefaultConsumer, producer, 10); err != nil || got.FromOffset != 4 {
		t.Fatalf("publisher cursor: %+v %v", got, err)
	}
	if got, err := g.dc.ReadEvents(ctx, "btw-coverage-combined-fact", producer, 10); err != nil || got.FromOffset != 4 {
		t.Fatalf("Graph cursor: %+v %v", got, err)
	}
	if path := os.Getenv("COVERAGE_JOIN_TWO_ODS_OUTPUT"); path != "" {
		body, err := favoritesource.ExportODS(ctx, g.warehouse)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJoinedReport(t, os.Getenv("COVERAGE_JOIN_TWO_REPORT"), map[string]any{"G1": p1.Ref, "G3": p3.Ref, "U1W1": s1.Ref, "U1W3": s3u1.Ref, "U2W3": s3u2.Ref, "U3W3": s3u3.Ref,
		"candidate_w1_sha256": first.ArtifactHash, "candidate_w1_after_tail_sha256": late.ArtifactHash, "candidate_u1_w3_sha256": c1.ArtifactHash,
		"candidate_u2_w3_sha256": c2.ArtifactHash, "candidate_u3_w3_sha256": c3.ArtifactHash,
		"accepted_v2": map[string]any{"u1_w1_after_tail": acceptedW1, "u1_w3": acceptedU1,
			"u2_w3": acceptedU2, "u3_empty_w3": acceptedU3, "v1_and_serving_heads": 0},
		"snapshot_v2": map[string]any{"u1_w1_after_tail": snapshotW1, "u1_w3": snapshotU1,
			"u2_w3": snapshotU2, "u3_empty_w3": snapshotU3},
		"bundle_v2": map[string]any{"u1_w1_after_tail": "pending_tail", "u1_w3": bundleU1,
			"u2_w3": bundleU2, "u3_empty_w3": bundleU3}})
	t.Logf("two-subject real DC/PG Publisher→Verifier→H10 W1=%s W3=%s u1=%s u2=%s empty=%s", p1.Ref.EventIndexSHA256, p3.Ref.EventIndexSHA256, s3u1.Ref.SparseIndexSHA256, s3u2.Ref.SparseIndexSHA256, s3u3.Ref.SparseIndexSHA256)
}
