package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

type fixedCoverageProof struct {
	expected map[string]sourcecoverage.EventIndexRow
}

func (f fixedCoverageProof) VerifyEvent(_ context.Context, row sourcecoverage.EventIndexRow) error {
	want, ok := f.expected[row.Offset]
	if !ok {
		return ErrCoverageConflict
	}
	// EventSpec in JSONL is JCS-normalized; its transport hash was checked by
	// sourcecoverage.EventIndexJSONL before this proof Adapter is called.
	want.EventSpec, row.EventSpec = nil, nil
	if !reflect.DeepEqual(want, row) {
		return ErrCoverageConflict
	}
	return nil
}

func coverageTestRow(t *testing.T, offset int64, subject SubjectRef, favoriteID, operation string) (sourcecoverage.EventIndexRow, Event) {
	t.Helper()
	version := int64(1)
	if operation == "retract" {
		version = 2
	}
	eventID := "favorite." + favoriteID + ".v" + strconv.FormatInt(version, 10)
	at := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	received := at.Add(time.Second)
	payload, err := json.Marshal(map[string]any{"schema_version": 1, "event_id": eventID,
		"subject_ref": subject, "target_type": "article", "target_id": "article-1",
		"target_revision": nil, "operation": operation, "source_ref": "rtw.favorite/" + favoriteID,
		"event_time": at.Format(time.RFC3339Nano), "available_at": at.Format(time.RFC3339Nano),
		"favorite_id": favoriteID, "folder_id": "41"})
	if err != nil {
		t.Fatal(err)
	}
	source := eventing.Event{EventID: eventID, EventType: "rtw.favorite." + operation,
		SchemaVersion: 1, Producer: favoriteCoverageProducer, AggregateID: favoriteID,
		AggregateVersion: version, OperationID: eventID, OccurredAt: at.Format(time.RFC3339Nano), Payload: payload}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	hash := coverageHash(canonical)
	row := sourcecoverage.EventIndexRow{Producer: favoriteCoverageProducer, Offset: strconv.FormatInt(offset, 10),
		EventID: eventID, InputHash: hash, RTWSourceHash: hash, ReceiptID: "dc-receipt-" + strconv.FormatInt(offset, 10),
		ReceivedAt: received.Format(time.RFC3339Nano), Subject: sourcecoverage.SubjectRef(subject), EventSpec: raw}
	fact := Event{Subject: subject, EventKey: EventKey{Producer: favoriteCoverageProducer, EventID: eventID},
		Action: Assert, Kind: ProductAction, Predicate: "favorite", ValueRef: "article/article-1", ItemID: "article-1",
		EvidenceRef: "dc:event:" + favoriteCoverageProducer + ":" + row.Offset, EvidenceHash: hash,
		OccurredAt: at, ObservedAt: received, SourcePartition: "dc:" + favoriteCoverageProducer, SourceSequence: offset}
	if operation == "retract" {
		fact.Action = Retract
		fact.Supersedes = &EventKey{Producer: favoriteCoverageProducer, EventID: "favorite." + favoriteID + ".v1"}
	}
	return row, fact
}

func coverageTestPrefix(t *testing.T, rows []sourcecoverage.EventIndexRow, generation string) (sourcecoverage.GlobalPrefixRef, []byte, []byte) {
	t.Helper()
	indexBody, indexHash, err := sourcecoverage.EventIndexJSONL(favoriteCoverageProducer, int64(len(rows)), rows)
	if err != nil {
		t.Fatal(err)
	}
	batchHash, err := sourcecoverage.BatchHash(rows)
	if err != nil {
		t.Fatal(err)
	}
	batchBody, batchDigest, err := sourcecoverage.BatchEvidenceJSONL([]sourcecoverage.BatchEvidence{{
		FromOffset: "1", ToOffset: strconv.Itoa(len(rows)), BatchHash: batchHash}}, int64(len(rows)))
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion, Producer: favoriteCoverageProducer,
		Origin: "1", ThroughOffset: strconv.Itoa(len(rows)), BindingPolicyID: FavoriteCoveragePolicyID,
		WarehouseConsumer: "warehouse-favorite", WarehouseGeneration: generation,
		EventIndexURL: "memory://event-index/" + indexHash, EventIndexSHA256: indexHash,
		BatchEvidenceURL: "memory://batch-evidence/" + batchDigest, BatchEvidenceSHA256: batchDigest}
	ref.ManifestSHA256, err = sourcecoverage.GlobalManifestHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ref, indexBody, batchBody
}

func coverageTestSubject(t *testing.T, prefix sourcecoverage.GlobalPrefixRef,
	subject SubjectRef, rows []sourcecoverage.EventIndexRow) (sourcecoverage.SubjectCoverageRef, []byte) {
	t.Helper()
	body, digest, count, err := sourcecoverage.SubjectIndexJSONL(sourcecoverage.SubjectRef(subject), rows)
	if err != nil {
		t.Fatal(err)
	}
	ref := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion,
		Subject: sourcecoverage.SubjectRef(subject), Prefix: prefix,
		SparseIndexURL: "memory://subject-index/" + digest, SparseIndexSHA256: digest, EventCount: count}
	ref.ReceiptSHA256, err = sourcecoverage.SubjectReceiptHash(ref)
	if err != nil {
		t.Fatal(err)
	}
	return ref, body
}

func coverageTestStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t, nil)
	if err := MigratePool(context.Background(), store.db); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCoverageVerifierInterleavedSubjectsAndEmptyProof(t *testing.T) {
	store := coverageTestStore(t)
	ctx := context.Background()
	u1 := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	u3 := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1003"}
	var rows []sourcecoverage.EventIndexRow
	proof := fixedCoverageProof{expected: map[string]sourcecoverage.EventIndexRow{}}
	for _, input := range []struct {
		subject SubjectRef
		id      string
	}{{u1, "11"}, {u2, "22"}, {u1, "33"}} {
		row, fact := coverageTestRow(t, int64(len(rows)+1), input.subject, input.id, "assert")
		if receipt, err := store.Append(ctx, fact); err != nil || receipt.Status != "accepted" {
			t.Fatalf("source fact: %+v %v", receipt, err)
		}
		rows = append(rows, row)
		proof.expected[row.Offset] = row
	}
	ref, index, batches := coverageTestPrefix(t, rows, "favorite-interleaved-g1")
	verifier, err := NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CoveredStateAt(ctx, u1, ref, time.Now()); !errors.Is(err, ErrCoverageUnverified) {
		t.Fatalf("legacy PG facts silently gained v2 proof: %v", err)
	}
	accepted, err := verifier.VerifyPrefix(ctx, ref, index, batches)
	if err != nil || accepted.Replay {
		t.Fatalf("verify complete source: %+v %v", accepted, err)
	}
	if replay, err := verifier.VerifyPrefix(ctx, ref, index, batches); err != nil || !replay.Replay {
		t.Fatalf("same immutable root not idempotent: %+v %v", replay, err)
	}
	alteredIndex := append([]byte(nil), index...)
	alteredIndex[0] ^= 1
	if _, err := verifier.VerifyPrefix(ctx, ref, alteredIndex, batches); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("cached root accepted altered event-index bytes: %v", err)
	}
	alteredBatch := append([]byte(nil), batches...)
	alteredBatch[0] ^= 1
	if _, err := verifier.VerifyPrefix(ctx, ref, index, alteredBatch); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("cached root accepted altered batch-evidence bytes: %v", err)
	}
	for _, check := range []struct {
		subject SubjectRef
		offsets []int64
	}{{u1, []int64{1, 3}}, {u2, []int64{2}}, {u3, nil}} {
		subRef, sparse := coverageTestSubject(t, ref, check.subject, rows)
		if result, err := verifier.VerifySubject(ctx, subRef, sparse); err != nil || result.Replay {
			t.Fatalf("verify sparse subject %+v: %+v %v", check.subject, result, err)
		}
		state, err := store.CoveredStateAt(ctx, check.subject, ref, time.Now())
		if err != nil || !state.CurrentComplete || len(state.Tail) != 0 || len(state.ActiveAtPrefix) != len(check.offsets) ||
			state.Coverage.ReceiptSHA256 != subRef.ReceiptSHA256 {
			t.Fatalf("covered state %+v: %+v %v", check.subject, state, err)
		}
		for i, fact := range state.ActiveAtPrefix {
			if fact.SourceSequence != check.offsets[i] {
				t.Fatalf("sparse offsets %+v: %+v", check.subject, state.ActiveAtPrefix)
			}
		}
		if replay, err := verifier.VerifySubject(ctx, subRef, sparse); err != nil || !replay.Replay {
			t.Fatalf("subject receipt replay: %+v %v", replay, err)
		}
	}
	wrong, _ := coverageTestSubject(t, ref, u1, rows[:1])
	if _, err := verifier.VerifySubject(ctx, wrong, nil); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("missing subject offset 3 accepted: %v", err)
	}
	missing := []sourcecoverage.EventIndexRow{rows[0], rows[2]}
	if _, _, err := sourcecoverage.EventIndexJSONL(favoriteCoverageProducer, 3, missing); err == nil {
		t.Fatal("shared contract allowed missing global offset")
	}
	if _, err := store.db.Exec(ctx, `UPDATE usermodel_coverage_event SET input_hash=$1 WHERE manifest_sha256=$2 AND source_offset=1`,
		rows[0].InputHash, ref.ManifestSHA256); err == nil {
		t.Fatal("verified cache was mutable")
	}
	if _, err := store.db.Exec(ctx, `UPDATE usermodel_events SET event_body=jsonb_set(event_body,'{value_ref}', '"article/forged"')
		WHERE producer=$1 AND event_id=$2`, favoriteCoverageProducer, rows[0].EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CoveredStateAt(ctx, u1, ref, time.Now()); !errors.Is(err, ErrCoverageConflict) {
		t.Fatalf("cached root hid changed PG fact body: %v", err)
	}
}

func TestCoverageVerifierPrefixBeforeTailWithdrawal(t *testing.T) {
	store := coverageTestStore(t)
	ctx := context.Background()
	u1 := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1001"}
	u2 := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "1002"}
	one, assert := coverageTestRow(t, 1, u1, "11", "assert")
	two, pending := coverageTestRow(t, 2, u2, "22", "retract")
	three, retract := coverageTestRow(t, 3, u1, "11", "retract")
	for _, fact := range []Event{assert, pending, retract} {
		if _, err := store.Append(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Current(ctx, u1)
	if err != nil || len(current.Active) != 0 {
		t.Fatalf("latest projection should be withdrawn: %+v %v", current, err)
	}
	proof := fixedCoverageProof{expected: map[string]sourcecoverage.EventIndexRow{
		"1": one, "2": two, "3": three}}
	verifier, err := NewCoverageVerifier(store, proof)
	if err != nil {
		t.Fatal(err)
	}
	full, index3, batches3 := coverageTestPrefix(t, []sourcecoverage.EventIndexRow{one, two, three}, "pending-g3")
	if _, err := verifier.VerifyPrefix(ctx, full, index3, batches3); !errors.Is(err, ErrCoveragePending) {
		t.Fatalf("pending offset 2 advanced W3: %v", err)
	}
	first, index1, batches1 := coverageTestPrefix(t, []sourcecoverage.EventIndexRow{one}, "covered-g1")
	if _, err := verifier.VerifyPrefix(ctx, first, index1, batches1); err != nil {
		t.Fatal(err)
	}
	subRef, sparse := coverageTestSubject(t, first, u1, []sourcecoverage.EventIndexRow{one})
	if _, err := verifier.VerifySubject(ctx, subRef, sparse); err != nil {
		t.Fatal(err)
	}
	state, err := store.CoveredStateAt(ctx, u1, first, time.Now())
	if err != nil || len(state.ActiveAtPrefix) != 1 || state.ActiveAtPrefix[0].EventID != one.EventID ||
		len(state.Tail) != 1 || state.Tail[0].Offset != 3 || state.Tail[0].Status != "accepted" || state.CurrentComplete {
		t.Fatalf("W1 assert was erased by accepted tail retract: %+v %v", state, err)
	}
	u2Ref, u2Sparse := coverageTestSubject(t, first, u2, []sourcecoverage.EventIndexRow{one})
	if _, err := verifier.VerifySubject(ctx, u2Ref, u2Sparse); err != nil {
		t.Fatal(err)
	}
	u2State, err := store.CoveredStateAt(ctx, u2, first, time.Now())
	if err != nil || len(u2State.ActiveAtPrefix) != 0 || len(u2State.Tail) != 1 ||
		u2State.Tail[0].Status != "pending_dependency" || u2State.CurrentComplete {
		t.Fatalf("pending user tail was marked complete: %+v %v", u2State, err)
	}
}
