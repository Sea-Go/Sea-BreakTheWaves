package usermodel_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/featurebaseline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type coveredFixedProof struct{ row sourcecoverage.EventIndexRow }

func (p coveredFixedProof) VerifyEvent(_ context.Context, row sourcecoverage.EventIndexRow) error {
	if row.Offset != p.row.Offset || row.EventID != p.row.EventID || row.InputHash != p.row.InputHash ||
		row.Subject != p.row.Subject {
		return usermodel.ErrCoverageConflict
	}
	return nil
}

type coveredListProof map[string]sourcecoverage.EventIndexRow

func (p coveredListProof) VerifyEvent(_ context.Context, row sourcecoverage.EventIndexRow) error {
	if !reflect.DeepEqual(p[row.Offset], row) {
		return usermodel.ErrCoverageConflict
	}
	return nil
}

type coveredMemoryObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (o *coveredMemoryObjects) ReadFixed(_ context.Context, target, expectedHash string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	body, ok := o.data[target]
	if !ok || coveredBytesHash(body) != expectedHash {
		return nil, errors.New("fixed object missing or changed")
	}
	return bytes.Clone(body), nil
}

func (o *coveredMemoryObjects) PutFixed(_ context.Context, target string, body []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if previous, ok := o.data[target]; ok && !bytes.Equal(previous, body) {
		return errors.New("fixed object mutation")
	}
	o.data[target] = bytes.Clone(body)
	return nil
}

func coveredBytesHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func coveredBaselineStore(t *testing.T) (*usermodel.Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("USERMODEL_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("run internal/usermodel/test-postgres.sh with isolated PG16")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "covered_accept_" + hex.EncodeToString(nonce[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	for _, migration := range []string{usermodelmigration.SQL, usermodelmigration.CoverageSQL} {
		if _, err := pool.Exec(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"../../migrations/usermodel/003_features.sql", "../../migrations/usermodel/004_serving.sql"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, usermodelmigration.CoveredBaselineSQL); err != nil {
		t.Fatal(err)
	}
	return usermodel.NewStore(pool, nil), pool
}

func coveredBaselineRow(t *testing.T, subject usermodel.SubjectRef, favoriteID string, offset int64,
	operation string, predecessor *usermodel.EventKey) (sourcecoverage.EventIndexRow, usermodel.Event) {
	t.Helper()
	version := int64(1)
	action := usermodel.Assert
	if operation == "retract" {
		version, action = 2, usermodel.Retract
	}
	eventID := fmt.Sprintf("favorite.%s.v%d", favoriteID, version)
	at := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	received := at.Add(time.Second)
	payload, err := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID,
		"subject_ref": subject, "target_type": "article", "target_id": "article-1",
		"target_revision": "article-1:r1", "operation": operation,
		"source_ref": "rtw.favorite/" + favoriteID, "event_time": at.Format(time.RFC3339Nano),
		"available_at": at.Format(time.RFC3339Nano), "favorite_id": favoriteID, "folder_id": "41"})
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
	hash := coveredBytesHash(canonical)
	row := sourcecoverage.EventIndexRow{Producer: source.Producer, Offset: fmt.Sprint(offset), EventID: eventID,
		InputHash: hash, RTWSourceHash: hash, ReceiptID: fmt.Sprintf("dc-receipt-%d", offset),
		ReceivedAt: received.Format(time.RFC3339Nano), Subject: sourcecoverage.SubjectRef(subject), EventSpec: raw}
	fact := usermodel.Event{Subject: subject, EventKey: usermodel.EventKey{Producer: source.Producer, EventID: eventID},
		Action: action, Kind: usermodel.ProductAction, Predicate: "favorite",
		ValueRef: "article/article-1/revision/article-1:r1", ItemID: "article-1",
		EvidenceRef: "dc:event:" + source.Producer + ":" + row.Offset, EvidenceHash: hash,
		OccurredAt: at, ObservedAt: received, SourcePartition: "dc:" + source.Producer,
		SourceSequence: offset, Supersedes: predecessor}
	return row, fact
}

func coveredBaselinePrefix(t *testing.T, row sourcecoverage.EventIndexRow) (sourcecoverage.GlobalPrefixRef, []byte, []byte) {
	t.Helper()
	index, indexHash, err := sourcecoverage.EventIndexJSONL(row.Producer, 1, []sourcecoverage.EventIndexRow{row})
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
	root := "http://objects.test/warehouse-coverage"
	ref := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion, Producer: row.Producer,
		Origin: "1", ThroughOffset: "1", BindingPolicyID: usermodel.FavoriteCoveragePolicyID,
		WarehouseConsumer: "warehouse-favorite", WarehouseGeneration: "g1",
		EventIndexURL: root + "/g1/event-index/" + indexHash + ".jsonl", EventIndexSHA256: indexHash,
		BatchEvidenceURL: root + "/g1/batch-evidence/" + batchDigest + ".jsonl", BatchEvidenceSHA256: batchDigest}
	ref.ManifestSHA256, err = sourcecoverage.GlobalManifestHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ref, index, batches
}

func TestAcceptActualH10CoveredCandidateHistoricalAndReplay(t *testing.T) {
	store, pool := coveredBaselineStore(t)
	ctx := context.Background()
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	one, assert := coveredBaselineRow(t, subject, "11", 1, "assert", nil)
	_, pending := coveredBaselineRow(t, subject, "22", 2, "retract", &usermodel.EventKey{Producer: assert.Producer, EventID: "favorite.22.v1"})
	_, retract := coveredBaselineRow(t, subject, "11", 3, "retract", &assert.EventKey)
	for _, fact := range []usermodel.Event{assert, pending, retract} {
		if _, err := store.Append(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	prefix, index, batches := coveredBaselinePrefix(t, one)
	verifier, err := usermodel.NewCoverageVerifier(store, coveredFixedProof{row: one})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPrefix(ctx, prefix, index, batches); err != nil {
		t.Fatal(err)
	}
	sparse, sparseHash, count, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef(subject), []sourcecoverage.EventIndexRow{one})
	if err != nil {
		t.Fatal(err)
	}
	subRef := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Subject: sourcecoverage.SubjectRef(subject), Prefix: prefix,
		SparseIndexURL:    "http://objects.test/warehouse-coverage/g1/subject-index/" + sparseHash + ".jsonl",
		SparseIndexSHA256: sparseHash, EventCount: count}
	subRef.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(subRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifySubject(ctx, subRef, sparse); err != nil {
		t.Fatal(err)
	}
	objects := &coveredMemoryObjects{data: map[string][]byte{subRef.SparseIndexURL: sparse}}
	spec := featurebaseline.FavoriteCandidateSpec() // unapproved, default-off
	builder := featurebaseline.CoveredBuilder{State: featurebaseline.UsermodelCoveredReader{Store: store},
		Objects: objects, ArtifactRoot: "http://objects.test/warehouse-coverage", Now: time.Now}
	built, err := builder.BuildCovered(ctx, subject, spec, "baseline_accept_g1", 1, prefix)
	if err != nil || built.Candidate.CurrentComplete || len(built.Candidate.Contributions) != 1 ||
		len(built.Candidate.Tail) != 2 || built.Candidate.Values[0].Value != "1" {
		t.Fatalf("actual H10 W1 candidate: %+v %v", built, err)
	}
	raw, err := objects.ReadFixed(ctx, built.ArtifactURL, built.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	submission := usermodel.CoveredBaselineSubmission{ArtifactURL: built.ArtifactURL,
		ArtifactSHA256: built.ArtifactHash, CandidateJSON: raw, FeatureSpec: spec}
	accepted, err := store.AcceptCoveredBaseline(ctx, submission)
	if err != nil || accepted.Replay || accepted.Status != "accepted_historical_default_off" ||
		accepted.ArtifactSHA256 != built.ArtifactHash || accepted.Coverage.ReceiptSHA256 != subRef.ReceiptSHA256 {
		t.Fatalf("same-PG historical acceptance: %+v %v", accepted, err)
	}
	var savedRaw []byte
	if err := pool.QueryRow(ctx, `SELECT candidate_raw FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=1`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&savedRaw); err != nil || !bytes.Equal(savedRaw, raw) {
		t.Fatalf("PG jsonb lost H10 exact artifact bytes: equal=%v err=%v", bytes.Equal(savedRaw, raw), err)
	}
	for _, table := range []string{"usermodel_feature_baselines", "usermodel_feature_heads",
		"usermodel_serving_bundles", "usermodel_serving_pointers"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("v2 historical receipt activated legacy table %s: count=%d err=%v", table, count, err)
		}
	}
	if replay, err := store.AcceptCoveredBaseline(ctx, submission); err != nil || !replay.Replay ||
		replay.Status != "accepted_historical_default_off" {
		t.Fatalf("same revision/hash replay: %+v %v", replay, err)
	}
	emptySubject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1003"}
	emptySparse, emptyHash, emptyCount, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef(emptySubject),
		[]sourcecoverage.EventIndexRow{one})
	if err != nil || len(emptySparse) != 0 || emptyCount != 0 {
		t.Fatalf("globally verified empty subject bytes: count=%d err=%v", emptyCount, err)
	}
	emptyRef := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Subject: sourcecoverage.SubjectRef(emptySubject), Prefix: prefix,
		SparseIndexURL:    "http://objects.test/warehouse-coverage/g1/subject-index/" + emptyHash + ".jsonl",
		SparseIndexSHA256: emptyHash, EventCount: 0}
	emptyRef.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(emptyRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifySubject(ctx, emptyRef, emptySparse); err != nil {
		t.Fatal(err)
	}
	objects.data[emptyRef.SparseIndexURL] = emptySparse
	emptyBuilt, err := builder.BuildCovered(ctx, emptySubject, spec, "baseline_empty_g1", 1, prefix)
	if err != nil || !emptyBuilt.Candidate.CurrentComplete || emptyBuilt.Candidate.InputStateVersion != 0 {
		t.Fatalf("H10 globally empty candidate: %+v %v", emptyBuilt, err)
	}
	emptyRaw, err := objects.ReadFixed(ctx, emptyBuilt.ArtifactURL, emptyBuilt.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	if emptyReceipt, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
		ArtifactURL: emptyBuilt.ArtifactURL, ArtifactSHA256: emptyBuilt.ArtifactHash,
		CandidateJSON: emptyRaw, FeatureSpec: spec}); err != nil ||
		emptyReceipt.Status != "accepted_historical_default_off" {
		t.Fatalf("empty subject historical receipt: %+v %v", emptyReceipt, err)
	}
	for name, mutate := range map[string]func(*featurebaseline.CoveredCandidate){
		"wrong-values": func(c *featurebaseline.CoveredCandidate) { c.Values[0].Value = "99" },
		"missing-tail": func(c *featurebaseline.CoveredCandidate) { c.Tail = c.Tail[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			changed := built.Candidate
			changed.Generation = "invalid_" + name
			changed.Revision = 2
			changed.Values = append([]usermodel.FeatureValue(nil), built.Candidate.Values...)
			changed.Tail = append([]featurebaseline.CoveredTailEvent(nil), built.Candidate.Tail...)
			mutate(&changed)
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			hash := coveredBytesHash(raw)
			url := "http://objects.test/warehouse-coverage/" + changed.Generation + "/covered-baseline/" + hash + ".json"
			if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
				ArtifactURL: url, ArtifactSHA256: hash, CandidateJSON: raw, FeatureSpec: spec}); !errors.Is(err, usermodel.ErrCoveredBaselineConflict) && !errors.Is(err, usermodel.ErrCoveredBaselinePending) {
				t.Fatalf("changed H10 candidate accepted: %v", err)
			}
		})
	}
	future := built.Candidate
	future.Generation = "future_baseline"
	future.Revision = 2
	future.AsOf = time.Now().UTC().Add(time.Hour)
	future.AvailableAt = future.AsOf
	futureRaw, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	futureHash := coveredBytesHash(futureRaw)
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
		ArtifactURL:    "http://objects.test/warehouse-coverage/future_baseline/covered-baseline/" + futureHash + ".json",
		ArtifactSHA256: futureHash, CandidateJSON: futureRaw, FeatureSpec: spec}); !errors.Is(err, usermodel.ErrCoveredBaselineInvalid) {
		t.Fatalf("future available_at was stored as historical baseline: %v", err)
	}
	stale, err := builder.BuildCovered(ctx, subject, spec, "baseline_accept_g2", 2, prefix)
	if err != nil {
		t.Fatal(err)
	}
	staleRaw, err := objects.ReadFixed(ctx, stale.ArtifactURL, stale.ArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	_, newFact := coveredBaselineRow(t, subject, "44", 4, "assert", nil)
	if _, err := store.Append(ctx, newFact); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: stale.ArtifactURL,
		ArtifactSHA256: stale.ArtifactHash, CandidateJSON: staleRaw, FeatureSpec: spec}); !errors.Is(err, usermodel.ErrCoveredBaselinePending) {
		t.Fatalf("new PG fact did not stale candidate state version: %v", err)
	}
	if replay, err := store.AcceptCoveredBaseline(ctx, submission); err != nil || !replay.Replay ||
		replay.Status != "accepted_historical_default_off" {
		t.Fatalf("historical replay after new fact: %+v %v", replay, err)
	}
	changed := built.Candidate
	changed.Generation = "baseline_changed"
	changedRaw, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedHash := coveredBytesHash(changedRaw)
	changedURL := "http://objects.test/warehouse-coverage/baseline_changed/covered-baseline/" + changedHash + ".json"
	if _, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{ArtifactURL: changedURL,
		ArtifactSHA256: changedHash, CandidateJSON: changedRaw, FeatureSpec: spec}); !errors.Is(err, usermodel.ErrCoveredBaselineConflict) {
		t.Fatalf("same revision with different hash was accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE usermodel_covered_baselines_v2 SET status='accepted_historical_default_off'
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=1`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID); err == nil {
		t.Fatal("immutable v2 receipt accepted UPDATE")
	}
	if strings.Contains(string(raw), "accepted_historical_default_off") {
		t.Fatal("H10 candidate bytes were altered to look accepted")
	}
}

// This optional cross-slice test consumes the exact bytes archived by the
// WS07 Publisher acceptance. Its synthetic two-user source is not the single-
// subject real RTW run; the PG, Verifier, H10 builder and v2 Accept are real.
func TestPublishedCoverageArtifactInterop(t *testing.T) {
	artifactDir := os.Getenv("SEA_COVERAGE_PUBLISHER_ARTIFACT_DIR")
	if artifactDir == "" {
		t.Skip("set SEA_COVERAGE_PUBLISHER_ARTIFACT_DIR to WS07 cross-domain evidence")
	}
	store, pool := coveredBaselineStore(t)
	ctx := context.Background()
	var refs struct {
		G3 sourcecoverage.GlobalPrefixRef    `json:"G3"`
		U1 sourcecoverage.SubjectCoverageRef `json:"U1"`
		U2 sourcecoverage.SubjectCoverageRef `json:"U2"`
		U3 sourcecoverage.SubjectCoverageRef `json:"U3"`
	}
	refBody, err := os.ReadFile(filepath.Join(artifactDir, "two-ref.json"))
	if err != nil || json.Unmarshal(refBody, &refs) != nil {
		t.Fatalf("WS07 two-ref artifact unavailable: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/G3/event-index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	batches, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two/G3/batch-evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	proof := coveredListProof{}
	scanner := bufio.NewScanner(bytes.NewReader(index))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var row sourcecoverage.EventIndexRow
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		proof[row.Offset] = row
		var source eventing.Event
		var payload struct {
			TargetType     string  `json:"target_type"`
			TargetID       string  `json:"target_id"`
			TargetRevision *string `json:"target_revision"`
		}
		if json.Unmarshal(row.EventSpec, &source) != nil || json.Unmarshal(source.Payload, &payload) != nil {
			t.Fatal("WS07 EventSpec body malformed")
		}
		offset, err := strconv.ParseInt(row.Offset, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		occurred, err1 := time.Parse(time.RFC3339Nano, source.OccurredAt)
		received, err2 := time.Parse(time.RFC3339Nano, row.ReceivedAt)
		if err1 != nil || err2 != nil {
			t.Fatal("WS07 EventSpec time malformed")
		}
		valueRef := payload.TargetType + "/" + payload.TargetID
		if payload.TargetRevision != nil {
			valueRef += "/revision/" + *payload.TargetRevision
		}
		fact := usermodel.Event{Subject: usermodel.SubjectRef(row.Subject),
			EventKey: usermodel.EventKey{Producer: row.Producer, EventID: row.EventID},
			Action:   usermodel.Assert, Kind: usermodel.ProductAction, Predicate: "favorite",
			ValueRef: valueRef, ItemID: payload.TargetID,
			EvidenceRef: "dc:event:" + row.Producer + ":" + row.Offset, EvidenceHash: row.InputHash,
			OccurredAt: occurred, ObservedAt: received, SourcePartition: "dc:" + row.Producer,
			SourceSequence: offset}
		if source.EventType == "rtw.favorite.retract" {
			fact.Action = usermodel.Retract
			fact.Supersedes = &usermodel.EventKey{Producer: row.Producer,
				EventID: "favorite." + source.AggregateID + ".v1"}
		}
		if receipt, err := store.Append(ctx, fact); err != nil || receipt.Status != "accepted" {
			t.Fatalf("PG acceptance of published source %s: %+v %v", row.Offset, receipt, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	verifier, err := usermodel.NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyPrefix(ctx, refs.G3, index, batches); err != nil {
		t.Fatalf("WS07 exact global artifact rejected: %v", err)
	}
	objects := &coveredMemoryObjects{data: map[string][]byte{}}
	for name, ref := range map[string]sourcecoverage.SubjectCoverageRef{
		"U1": refs.U1, "U2": refs.U2, "U3": refs.U3} {
		sparse, err := os.ReadFile(filepath.Join(artifactDir, "artifacts/two", name, "sparse-index.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		objects.data[ref.SparseIndexURL] = sparse
		if _, err := verifier.VerifySubject(ctx, ref, sparse); err != nil {
			t.Fatalf("WS07 exact %s sparse artifact rejected: %v", name, err)
		}
	}
	cutoff := time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC)
	if cutoff.After(time.Now().UTC()) {
		t.Log("WS07 archived fixture event time is in the future; exact global/subject bytes verified, H10/Accept waits for its business cutoff")
		return
	}
	root := strings.Split(refs.G3.EventIndexURL, "/"+refs.G3.WarehouseGeneration+"/")[0]
	spec := featurebaseline.FavoriteCandidateSpec()
	builder := featurebaseline.CoveredBuilder{State: featurebaseline.UsermodelCoveredReader{Store: store},
		Objects: objects, ArtifactRoot: root, Now: func() time.Time { return cutoff }}
	for name, ref := range map[string]sourcecoverage.SubjectCoverageRef{
		"U1": refs.U1, "U2": refs.U2, "U3": refs.U3} {
		subject := usermodel.SubjectRef(ref.Subject)
		built, err := builder.BuildCovered(ctx, subject, spec, "publisher_accept_"+strings.ToLower(name), 1, refs.G3)
		if err != nil {
			t.Fatalf("H10 candidate from WS07 %s: %v", name, err)
		}
		raw, err := objects.ReadFixed(ctx, built.ArtifactURL, built.ArtifactHash)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := store.AcceptCoveredBaseline(ctx, usermodel.CoveredBaselineSubmission{
			ArtifactURL: built.ArtifactURL, ArtifactSHA256: built.ArtifactHash,
			CandidateJSON: raw, FeatureSpec: spec})
		if err != nil || receipt.Status != "accepted_historical_default_off" {
			t.Fatalf("WS07→Verifier→H10→v2 Accept %s: %+v %v", name, receipt, err)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_serving_pointers`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("WS07 %s caused Serving activation: count=%d err=%v", name, count, err)
		}
	}
}
