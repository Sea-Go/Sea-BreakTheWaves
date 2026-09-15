package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	usermodelmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/usermodel"
)

func TestSubjectRefV2StrictJSON(t *testing.T) {
	var valid SubjectRefV2
	if err := json.Unmarshal([]byte(`{"issuer":"rtw.identity","subject_id":"9223372036854775807"}`), &valid); err != nil ||
		valid != (SubjectRefV2{Issuer: "rtw.identity", SubjectID: "9223372036854775807"}) {
		t.Fatalf("canonical UID rejected: %+v %v", valid, err)
	}
	for _, raw := range []string{
		`{"issuer":"rtw.identity","subject_id":"42","tenant_id":"platform"}`,
		`{"issuer":"rtw.identity","authority_id":"rtw.identity","subject_id":"42"}`,
		`{"issuer":"rtw.identity","issuer":"rtw.identity","subject_id":"42"}`,
		`{"issuer":"rtw.identity","subject_id":"42","subject_id":"42"}`,
		`{"issuer":"rtw.identity"}`,
		`{"issuer":"rtw.identity","subject_id":42}`,
		`{"issuer":"rtw.identity","subject_id":"0"}`,
		`{"issuer":"rtw.identity","subject_id":"01"}`,
		`{"issuer":"rtw.identity","subject_id":"9223372036854775808"}`,
		`{"issuer":"other.identity","subject_id":"42"}`,
	} {
		var parsed SubjectRefV2
		if err := json.Unmarshal([]byte(raw), &parsed); !errors.Is(err, ErrInvalid) {
			t.Errorf("strict v2 decoder accepted %s: %+v %v", raw, parsed, err)
		}
	}
	if err := json.Unmarshal([]byte(`{"issuer":"rtw.identity","subject_id":"42"} true`), &valid); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func v2TestStore(t *testing.T) (*Store, *Store) {
	t.Helper()
	old := testStore(t, nil)
	if _, err := old.db.Exec(context.Background(), usermodelmigration.SubjectRefV2SQL); err != nil {
		t.Fatal(err)
	}
	return old, NewStore(old.db, nil, WithSubjectRefV2Candidate())
}

func v2Fact(id string, sequence int64) Event {
	e := fixture(id, sequence)
	e.Subject = SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "9223372036854775807"}
	return e
}

func TestSubjectRefV2HistoricalProjectionAndRollback(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	e := v2Fact("v2-history", 0)
	first := requireAppend(t, old, e)
	ref := SubjectRefV2{Issuer: "rtw.identity", SubjectID: e.Subject.SubjectID}
	if _, err := old.CurrentV2(ctx, ref); !errors.Is(err, ErrSubjectRefV2Disabled) {
		t.Fatalf("default-on v2 read: %v", err)
	}
	if _, err := candidate.CurrentV2(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unprojected history was treated as a new empty user: %v", err)
	}
	var bodyBefore, hashBefore, outboxBefore string
	if err := old.db.QueryRow(ctx, `SELECT event_body::text,normalized_hash FROM usermodel_events
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		e.Subject.AuthorityID, e.Subject.TenantID, e.Subject.SubjectID).Scan(&bodyBefore, &hashBefore); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT payload::text FROM usermodel_outbox
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		e.Subject.AuthorityID, e.Subject.TenantID, e.Subject.SubjectID).Scan(&outboxBefore); err != nil {
		t.Fatal(err)
	}
	if err := candidate.ProjectExistingSubjectV2(ctx, e.Subject); err != nil {
		t.Fatal(err)
	}
	if err := candidate.ProjectExistingSubjectV2(ctx, e.Subject); err != nil {
		t.Fatalf("same projection must replay: %v", err)
	}
	oldProjection, err := old.Current(ctx, e.Subject)
	if err != nil {
		t.Fatal(err)
	}
	v2Projection, err := candidate.CurrentV2(ctx, ref)
	if err != nil || !reflect.DeepEqual(oldProjection, v2Projection) {
		t.Fatalf("v1/v2 current differs: %v old=%+v new=%+v", err, oldProjection, v2Projection)
	}
	oldHistory, err := old.History(ctx, e.Subject, 10)
	if err != nil {
		t.Fatal(err)
	}
	v2History, cursor, err := candidate.HistoryAfterV2(ctx, ref, HistoryCursor{}, 10)
	if err != nil || !reflect.DeepEqual(oldHistory, v2History) || cursor.EventID != e.EventID {
		t.Fatalf("v1/v2 history differs: %v %+v", err, cursor)
	}
	oldOutbox, err := old.OutboxAfter(ctx, e.Subject, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	v2Outbox, err := candidate.OutboxAfterV2(ctx, ref, 0, 10)
	if err != nil || !reflect.DeepEqual(oldOutbox, v2Outbox) || len(v2Outbox) != 1 {
		t.Fatalf("v1/v2 Outbox differs: %v", err)
	}
	var bodyAfter, hashAfter, outboxAfter string
	if err := old.db.QueryRow(ctx, `SELECT event_body::text,normalized_hash FROM usermodel_events_subjectref_v2
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&bodyAfter, &hashAfter); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT payload::text FROM usermodel_outbox_subjectref_v2
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&outboxAfter); err != nil {
		t.Fatal(err)
	}
	if bodyBefore != bodyAfter || hashBefore != hashAfter || outboxBefore != outboxAfter || first.NormalizedHash != hashAfter {
		t.Fatal("projection changed old event/outbox evidence")
	}
	var sequence *int64
	if err := old.db.QueryRow(ctx, `SELECT source_sequence FROM usermodel_events_subjectref_v2 WHERE issuer=$1 AND subject_id=$2`,
		ref.Issuer, ref.SubjectID).Scan(&sequence); err != nil || sequence != nil {
		t.Fatalf("logical zero should be stored as PG NULL: %v %+v", err, sequence)
	}
	var watermarks int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_watermarks
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		e.Subject.AuthorityID, e.Subject.TenantID, e.Subject.SubjectID).Scan(&watermarks); err != nil || watermarks != 0 {
		t.Fatalf("zero sequence made a watermark: %d %v", watermarks, err)
	}
	if _, err := old.db.Exec(ctx, `UPDATE usermodel_subjectref_v2_projection SET subject_id='42'
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID); err == nil {
		t.Fatal("identity projection was editable")
	}
	// Rollback is an application option change. It neither removes the v2
	// sidecar nor changes the original v1 fact/history/read contract.
	rolledBack := NewStore(old.db, nil)
	if p, err := rolledBack.Current(ctx, e.Subject); err != nil || !reflect.DeepEqual(p, oldProjection) {
		t.Fatalf("v1 rollback changed a fact: %v", err)
	}
	if _, err := rolledBack.AppendV2(ctx, ref, Event{}); !errors.Is(err, ErrSubjectRefV2Disabled) {
		t.Fatalf("rollback still accepted v2: %v", err)
	}
}

func TestSubjectRefV2MixedReplayConflictAndAtomicProjection(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	e := v2Fact("v2-mixed", 1)
	e.Subject.SubjectID = "42"
	ref := SubjectRefV2{Issuer: "rtw.identity", SubjectID: "42"}
	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var receipt Receipt
			var err error
			if i%2 == 0 {
				receipt, err = candidate.Append(ctx, e)
			} else {
				// A verified v2 producer supplies only the v2 subject; the fact
				// ledger derives its old compatibility slot internally.
				copy := e
				copy.Subject = SubjectRef{}
				receipt, err = candidate.AppendV2(ctx, ref, copy)
			}
			if err != nil || receipt.NormalizedHash == "" || receipt.StateVersion != 1 {
				t.Errorf("mixed replay receipt %+v %v", receipt, err)
			}
		}(i)
	}
	wg.Wait()
	var facts, mappings, outboxes, watermarks int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_events_subjectref_v2
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_subjectref_v2_projection
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_outbox_subjectref_v2
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&outboxes); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_watermarks
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND contiguous_sequence=1 AND max_seen_sequence=1`,
		e.Subject.AuthorityID, e.Subject.TenantID, e.Subject.SubjectID).Scan(&watermarks); err != nil {
		t.Fatal(err)
	}
	if facts != 1 || mappings != 1 || outboxes != 1 || watermarks != 1 {
		t.Fatalf("mixed replay multiplied ledger rows: %d %d %d %d", facts, mappings, outboxes, watermarks)
	}
	changed := e
	changed.ValueRef = "article/rev-2"
	if _, err := candidate.AppendV2(ctx, ref, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("different body same EventKey did not conflict: %v", err)
	}
	if _, err := old.Append(ctx, e); err != nil {
		t.Fatalf("disabled v2 candidate changed v1 replay: %v", err)
	}
	if err := candidate.ProjectExistingSubjectV2(ctx, SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "43"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nonexistent old user was fabricated: %v", err)
	}
}

func TestSubjectRefV2RejectsOldSlotAndNoncanonicalUIDWithoutMerge(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	e := v2Fact("v2-invalid", 1)
	e.Subject.SubjectID = "42"
	bad := e
	bad.Subject.TenantID = "other"
	if _, err := old.Append(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.Append(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-platform old key collapsed into v2: %v", err)
	}
	if err := candidate.ProjectExistingSubjectV2(ctx, bad.Subject); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-platform historical key was projected: %v", err)
	}
	for _, uid := range []string{"", "0", "-1", "01", "+1", "9223372036854775808", "x42"} {
		if _, err := candidate.AppendV2(ctx, SubjectRefV2{Issuer: "rtw.identity", SubjectID: uid}, e); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad UID %q accepted: %v", uid, err)
		}
	}
	if _, err := candidate.AppendV2(ctx, SubjectRefV2{Issuer: "other.identity", SubjectID: "42"}, e); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong issuer accepted: %v", err)
	}
	if _, err := candidate.AppendV2(ctx, SubjectRefV2{Issuer: "rtw.identity", SubjectID: "42"}, bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("v2/event old-owner conflict was ignored: %v", err)
	}
	var rows int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_subjectref_v2_projection`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("bad identity created a v2 sidecar: %d %v", rows, err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_events WHERE subject_id='42'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("bad identity changed old event count: %d %v", rows, err)
	}
	if _, err := old.db.Exec(ctx, `INSERT INTO usermodel_subjectref_v2_projection
		(legacy_authority_id,legacy_tenant_id,legacy_subject_id,issuer,subject_id)
		VALUES('rtw.identity','other','42','rtw.identity','42')`); err == nil {
		t.Fatal("database accepted a non-platform identity projection")
	}
	if _, err := old.db.Exec(ctx, `INSERT INTO usermodel_subjectref_v2_projection
		(legacy_authority_id,legacy_tenant_id,legacy_subject_id,issuer,subject_id)
		VALUES('rtw.identity','platform','43','rtw.identity','43')`); err == nil {
		t.Fatal("database accepted a projection without an old subject")
	}
	// Existing v1 anomalous data remains distinct for the Phase-1 blocker
	// report. Neither Go admission nor SQL projects it by dropping the slot.
	if p, err := old.Current(ctx, bad.Subject); err != nil || p.StateVersion != 1 {
		t.Fatalf("old anomalous identity was silently merged: %+v %v", p, err)
	}
}
