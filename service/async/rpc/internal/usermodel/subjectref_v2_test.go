package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
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
	if err := MigratePool(context.Background(), old.db); err != nil {
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
	var projectedAtBefore, projectedAtAfter string
	if err := old.db.QueryRow(ctx, `SELECT projected_at::text FROM usermodel_subjectref_v2_projection
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&projectedAtBefore); err != nil {
		t.Fatal(err)
	}
	if err := MigratePool(ctx, old.db); err != nil {
		t.Fatalf("additive migration could not be retried: %v", err)
	}
	if err := old.db.QueryRow(ctx, `SELECT projected_at::text FROM usermodel_subjectref_v2_projection
		WHERE issuer=$1 AND subject_id=$2`, ref.Issuer, ref.SubjectID).Scan(&projectedAtAfter); err != nil || projectedAtBefore != projectedAtAfter {
		t.Fatalf("migration retry rewrote the identity projection: %q %q %v", projectedAtBefore, projectedAtAfter, err)
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

func TestSubjectRefV2ProjectAndAppendConcurrentLockOrder(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	first := v2Fact("v2-lock-first", 1)
	first.Subject.SubjectID = "42"
	requireAppend(t, old, first)
	second := v2Fact("v2-lock-second", 2)
	second.Subject = first.Subject
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := candidate.ProjectExistingSubjectV2(ctx, first.Subject); err != nil {
					t.Errorf("concurrent projection: %v", err)
				}
			} else if receipt, err := candidate.Append(ctx, second); err != nil || receipt.StateVersion != 2 {
				t.Errorf("concurrent append %+v %v", receipt, err)
			}
		}(i)
	}
	wg.Wait()
	var mappings, facts, outboxes int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_subjectref_v2_projection`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_events`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_outbox`).Scan(&outboxes); err != nil {
		t.Fatal(err)
	}
	if mappings != 1 || facts != 2 || outboxes != 2 {
		t.Fatalf("concurrent projection/append split one subject: %d %d %d", mappings, facts, outboxes)
	}
}

func TestSubjectRefV2BindUnmappedProjectsAtomicFactAndRejectsBadSlot(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	e := v2Fact("v2-bound", 1)
	e.Subject.SubjectID = ""
	ref := UnmappedRef{AuthorityID: "rtw.identity", TenantID: "platform", ExternalSubjectID: "source-alias-42"}
	if parked, err := candidate.ParkUnmapped(ctx, ref, e); err != nil || parked.Status != "pending_subject" {
		t.Fatalf("canonical source was not parked: %+v %v", parked, err)
	}
	var mappings int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_subjectref_v2_projection`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("unbound source fabricated a user: %d %v", mappings, err)
	}
	subject := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "42"}
	bound, err := candidate.BindUnmapped(ctx, ref, e.EventKey, subject)
	if err != nil || bound.StateVersion != 1 || bound.Status != "accepted" {
		t.Fatalf("candidate binding failed %+v %v", bound, err)
	}
	refV2 := SubjectRefV2{Issuer: "rtw.identity", SubjectID: "42"}
	oldCurrent, err := old.Current(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	newCurrent, err := candidate.CurrentV2(ctx, refV2)
	if err != nil || !reflect.DeepEqual(oldCurrent, newCurrent) || len(newCurrent.Active) != 1 {
		t.Fatalf("bound v1/v2 current differs: %+v %v", newCurrent, err)
	}
	if replay, err := candidate.BindUnmapped(ctx, ref, e.EventKey, subject); err != nil || !replay.Replay {
		t.Fatalf("bound identity replay failed: %+v %v", replay, err)
	}
	oldPending := v2Fact("v2-old-bound", 2)
	oldPending.Subject.SubjectID = ""
	otherRef := UnmappedRef{AuthorityID: "rtw.identity", TenantID: "platform", ExternalSubjectID: "other-source-alias-44"}
	otherSubject := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "44"}
	if _, err := old.ParkUnmapped(ctx, otherRef, oldPending); err != nil {
		t.Fatal(err)
	}
	if _, err := old.BindUnmapped(ctx, otherRef, oldPending.EventKey, otherSubject); err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.CurrentV2(ctx, SubjectRefV2{Issuer: "rtw.identity", SubjectID: "44"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unprojected old binding was assumed empty: %v", err)
	}
	if replay, err := candidate.BindUnmapped(ctx, otherRef, oldPending.EventKey, otherSubject); err != nil || !replay.Replay {
		t.Fatalf("already bound v1 history was not projected: %+v %v", replay, err)
	}
	if p, err := candidate.CurrentV2(ctx, SubjectRefV2{Issuer: "rtw.identity", SubjectID: "44"}); err != nil || len(p.Active) != 1 {
		t.Fatalf("replayed old binding did not project its other subject: %+v %v", p, err)
	}
	badRef := UnmappedRef{AuthorityID: "rtw.identity", TenantID: "other", ExternalSubjectID: "bad-source-alias"}
	bad := v2Fact("v2-bad-bound", 1)
	bad.Subject = SubjectRef{AuthorityID: "rtw.identity", TenantID: "other"}
	if _, err := candidate.ParkUnmapped(ctx, badRef, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("candidate parked non-platform source: %v", err)
	}
	if _, err := old.ParkUnmapped(ctx, badRef, bad); err != nil {
		t.Fatal(err)
	}
	badSubject := SubjectRef{AuthorityID: "rtw.identity", TenantID: "other", SubjectID: "43"}
	if _, err := candidate.BindUnmapped(ctx, badRef, bad.EventKey, badSubject); !errors.Is(err, ErrInvalid) {
		t.Fatalf("candidate bound non-platform source: %v", err)
	}
	var boundSubject *string
	if err := old.db.QueryRow(ctx, `SELECT bound_subject_id FROM usermodel_unmapped_events
		WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3`,
		badRef.AuthorityID, badRef.TenantID, badRef.ExternalSubjectID).Scan(&boundSubject); err != nil || boundSubject != nil {
		t.Fatalf("failed candidate binding changed parked source: %+v %v", boundSubject, err)
	}
	badUIDRef := UnmappedRef{AuthorityID: "rtw.identity", TenantID: "platform", ExternalSubjectID: "bad-uid-alias"}
	badUIDEvent := v2Fact("v2-bad-uid-bound", 1)
	badUIDEvent.Subject = SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform"}
	if _, err := candidate.ParkUnmapped(ctx, badUIDRef, badUIDEvent); err != nil {
		t.Fatal(err)
	}
	badUIDSubject := SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "01"}
	if _, err := candidate.BindUnmapped(ctx, badUIDRef, badUIDEvent.EventKey, badUIDSubject); !errors.Is(err, ErrInvalid) {
		t.Fatalf("candidate bound noncanonical UID: %v", err)
	}
	if err := old.db.QueryRow(ctx, `SELECT bound_subject_id FROM usermodel_unmapped_events
		WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3`,
		badUIDRef.AuthorityID, badUIDRef.TenantID, badUIDRef.ExternalSubjectID).Scan(&boundSubject); err != nil || boundSubject != nil {
		t.Fatalf("bad UID candidate binding changed parked source: %+v %v", boundSubject, err)
	}
	var v2Facts, v2Outboxes int
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_events_subjectref_v2 WHERE issuer='rtw.identity' AND subject_id='42'`).Scan(&v2Facts); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_outbox_subjectref_v2 WHERE issuer='rtw.identity' AND subject_id='42'`).Scan(&v2Outboxes); err != nil {
		t.Fatal(err)
	}
	if v2Facts != 1 || v2Outboxes != 1 {
		t.Fatalf("binding created an unprojected or duplicate fact: %d %d", v2Facts, v2Outboxes)
	}
}

func TestSubjectRefV2AttributionAndRecoveryUseTheSameProjectionGuard(t *testing.T) {
	old, candidate := v2TestStore(t)
	ctx := context.Background()
	reading := v2Fact("v2-link-reading", 1)
	reading.Subject.SubjectID = "42"
	reading.RequestID = "request-42"
	requireAppend(t, old, reading)
	display := v2Fact("v2-link-display", 2)
	display.Subject = reading.Subject
	display.Kind, display.Predicate = Impression, "display"
	display.ImpressionID = "impression-42"
	display.VisibilityEvidenceRef = "visible/42"
	display.RequestID = reading.RequestID
	display.OccurredAt = reading.OccurredAt.Add(-time.Second)
	requireAppend(t, old, display)
	version, err := candidate.LinkImpression(ctx, reading.Subject, reading.EventKey,
		display.ImpressionID, display.VisibilityEvidenceRef)
	if err != nil || version != 3 {
		t.Fatalf("candidate attribution failed: %d %v", version, err)
	}
	ref := SubjectRefV2{Issuer: "rtw.identity", SubjectID: "42"}
	if p, err := candidate.CurrentV2(ctx, ref); err != nil || p.StateVersion != 3 {
		t.Fatalf("attribution left v2 state unprojected: %+v %v", p, err)
	}
	if outbox, err := candidate.OutboxAfterV2(ctx, ref, 0, 10); err != nil || len(outbox) != 3 {
		t.Fatalf("attribution left v2 Outbox unprojected: %+v %v", outbox, err)
	}
	other := v2Fact("v2-recovery-existing", 1)
	other.Subject.SubjectID = "44"
	requireAppend(t, old, other)
	if count, err := candidate.ReconcilePending(ctx, other.Subject); err != nil || count != 0 {
		t.Fatalf("candidate recovery failed: %d %v", count, err)
	}
	if p, err := candidate.CurrentV2(ctx, SubjectRefV2{Issuer: "rtw.identity", SubjectID: "44"}); err != nil || p.StateVersion != 1 {
		t.Fatalf("recovery did not project existing subject: %+v %v", p, err)
	}
	bad := v2Fact("v2-recovery-bad-slot", 1)
	bad.Subject = SubjectRef{AuthorityID: "rtw.identity", TenantID: "other", SubjectID: "43"}
	requireAppend(t, old, bad)
	if _, err := candidate.ReconcilePending(ctx, bad.Subject); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recovery admitted bad legacy slot: %v", err)
	}
	if _, err := candidate.LinkImpression(ctx, bad.Subject, bad.EventKey,
		display.ImpressionID, display.VisibilityEvidenceRef); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attribution admitted bad legacy slot: %v", err)
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
