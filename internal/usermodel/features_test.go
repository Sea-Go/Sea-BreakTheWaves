package usermodel

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func featureTestStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t, nil)
	ontologySQL, err := os.ReadFile("../../migrations/usermodel/002_ontology.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(context.Background(), string(ontologySQL)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../migrations/usermodel/003_features.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(context.Background(), string(body)); err != nil {
		t.Fatal(err)
	}
	return s
}

func featureFixture(id string, sequence int64, at time.Time) Event {
	e := fixture(id, sequence)
	e.OccurredAt, e.ObservedAt = at.UTC(), at.UTC()
	e.ValueRef = "article/known"
	return e
}

func featureSpecFixture() FeatureSpec {
	return FeatureSpec{Version: "feature-v1", Features: []FeatureDefinition{
		{Name: "reading_count", Source: "fact", Kind: Reading, Predicate: "read", Mode: "count", WindowSeconds: 3600, Default: "0"},
		{Name: "recent_article", Source: "fact", Kind: Reading, Predicate: "read", Mode: "latest", WindowSeconds: 3600,
			Default: "unknown", Vocabulary: []string{"article/known"}, OOV: "other"},
	}}
}

func featureBaselineFixture(t *testing.T, s *Store, spec FeatureSpec, revision int64, generation string,
	asOf, availableAt time.Time, coveredSequence int64) FeatureBaseline {
	t.Helper()
	canonical, hash, err := canonicalFeatureSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	active, _, _, err := s.featureStateAt(context.Background(), fixture("identity", 1).Subject, asOf, availableAt)
	if err != nil {
		t.Fatal(err)
	}
	contributions := []Fact{}
	for _, f := range active {
		if f.SourceSequence <= coveredSequence {
			contributions = append(contributions, f)
		}
	}
	values, err := featureValues(canonical, contributions, nil, asOf, availableAt)
	if err != nil {
		t.Fatal(err)
	}
	return FeatureBaseline{Subject: fixture("identity", 1).Subject, Revision: revision, Generation: generation,
		SpecVersion: canonical.Version, SpecHash: hash, AsOf: asOf, AvailableAt: availableAt,
		Watermarks: []Watermark{{Producer: "rtw.product", SourcePartition: "product-1", ContiguousSequence: coveredSequence,
			MaxSeenSequence: coveredSequence, Complete: true}}, Contributions: contributions, Values: values}
}

func featureValue(s FeatureSnapshot, name string) FeatureValue {
	for _, v := range s.Values {
		if v.Name == name {
			return v
		}
	}
	return FeatureValue{}
}

func verifyFixedSQLBaseline(t *testing.T, s *Store, b FeatureBaseline) {
	t.Helper()
	query, err := os.ReadFile("testdata/features_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(context.Background(), string(query), b.Subject.AuthorityID, b.Subject.TenantID,
		b.Subject.SubjectID, b.Watermarks[0].ContiguousSequence, b.AsOf, b.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var producer, eventID, hash, latest string
		var sequence, count int64
		if err := rows.Scan(&producer, &eventID, &hash, &sequence, &count, &latest); err != nil {
			t.Fatal(err)
		}
		if count != int64(len(b.Contributions)) || latest != "article/known" {
			t.Fatalf("fixed SQL values differ: count=%d latest=%s", count, latest)
		}
		for _, f := range b.Contributions {
			if f.Producer == producer && f.EventID == eventID && f.NormalizedHash == hash && f.SourceSequence == sequence {
				seen[featureEventKey(f.EventKey)] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(b.Contributions) {
		t.Fatalf("fixed SQL contribution mismatch: matched=%d baseline=%d", len(seen), len(b.Contributions))
	}
}

func TestFeatureSpecMissingOOVAndWindow(t *testing.T) {
	spec, _, err := canonicalFeatureSpec(featureSpecFixture())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	values, err := featureValues(spec, nil, nil, now, now)
	if err != nil || !reflect.DeepEqual(values, []FeatureValue{{Name: "reading_count", Value: "0", Missing: true},
		{Name: "recent_article", Value: "unknown", Missing: true}}) {
		t.Fatalf("missing: %+v %v", values, err)
	}
	f := Fact{Event: featureFixture("oov", 1, now.Add(-30*time.Minute)), Status: "accepted"}
	f.ValueRef = "article/unseen"
	values, err = featureValues(spec, []Fact{f}, nil, now, now)
	if err != nil || values[0].Value != "1" || values[1].Value != "other" || !values[1].OOV || values[1].Missing {
		t.Fatalf("OOV: %+v %v", values, err)
	}
	values, err = featureValues(spec, []Fact{f}, nil, now.Add(2*time.Hour), now.Add(2*time.Hour))
	if err != nil || !values[0].Missing || values[0].Value != "0" || !values[1].Missing {
		t.Fatalf("window expiry: %+v %v", values, err)
	}
	bad := featureSpecFixture()
	bad.Features[0].Default = "1"
	if _, _, err := canonicalFeatureSpec(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("count default accepted: %v", err)
	}
}

func TestPostgresFeatureBaselineTailWithdrawalAdvanceAndRestart(t *testing.T) {
	s := featureTestStore(t)
	ctx := context.Background()
	spec := featureSpecFixture()
	baseTime := time.Now().UTC().Add(-35 * time.Minute)
	e1 := featureFixture("feat-1", 1, baseTime)
	e2 := featureFixture("feat-2", 2, baseTime.Add(5*time.Minute))
	requireAppend(t, s, e1)
	requireAppend(t, s, e2)
	firstAvailable := time.Now().UTC()
	b1 := featureBaselineFixture(t, s, spec, 1, "sql-g1", firstAvailable, firstAvailable, 2)
	verifyFixedSQLBaseline(t, s, b1)
	r1, snap, err := s.AcceptFeatureBaseline(ctx, b1, spec, firstAvailable, firstAvailable)
	if err != nil || r1.Replay || snap.BaselineState != "accepted" || len(snap.Tail) != 0 || featureValue(snap, "reading_count").Value != "2" {
		t.Fatalf("baseline 1: receipt=%+v snapshot=%+v err=%v", r1, snap, err)
	}
	firstSnapshotID := snap.ID
	if !digest.MatchString(firstSnapshotID) {
		t.Fatalf("missing immutable snapshot ID: %q", firstSnapshotID)
	}

	// A late event has an older event time but an uncovered source position.
	e3 := featureFixture("feat-3", 3, baseTime.Add(2*time.Minute))
	requireAppend(t, s, e3)
	visibleBeforeArrival, _, _, err := s.featureStateAt(ctx, e1.Subject, firstAvailable, firstAvailable)
	if err != nil || len(visibleBeforeArrival) != 2 {
		t.Fatalf("late arrival leaked into older availability point: %d %v", len(visibleBeforeArrival), err)
	}
	tailAt := time.Now().UTC()
	snap, err = s.RefreshFeatureSnapshot(ctx, e1.Subject, spec, tailAt, tailAt)
	if err != nil || len(snap.Tail) != 1 || snap.Tail[0].EventID != e3.EventID ||
		featureValue(snap, "reading_count").Value != "3" || featureValue(snap, "recent_article").Value != "article/known" {
		t.Fatalf("late tail: %+v %v", snap, err)
	}

	// Withdrawal of a fact already counted by the warehouse baseline must
	// remove that contribution rather than decrementing an opaque aggregate.
	withdraw := featureFixture("feat-4-withdraw", 4, baseTime.Add(7*time.Minute))
	withdraw.Action, withdraw.Kind, withdraw.ValueRef = Retract, "", ""
	withdraw.Supersedes = &e1.EventKey
	requireAppend(t, s, withdraw)
	withdrawAt := time.Now().UTC()
	snap, err = s.RefreshFeatureSnapshot(ctx, e1.Subject, spec, withdrawAt, withdrawAt)
	if err != nil || featureValue(snap, "reading_count").Value != "2" || len(snap.Tail) != 1 {
		t.Fatalf("withdraw covered fact: %+v %v", snap, err)
	}

	b2 := featureBaselineFixture(t, s, spec, 2, "sql-g2", withdrawAt, withdrawAt, 4)
	verifyFixedSQLBaseline(t, s, b2)
	r2, snap, err := s.AcceptFeatureBaseline(ctx, b2, spec, withdrawAt, withdrawAt)
	if err != nil || r2.Replay || len(snap.Tail) != 0 || featureValue(snap, "reading_count").Value != "2" {
		t.Fatalf("baseline advance: %+v %+v %v", r2, snap, err)
	}
	replayed, _, err := s.AcceptFeatureBaseline(ctx, b2, spec, withdrawAt, withdrawAt)
	if err != nil || !replayed.Replay || replayed.BaselineHash != r2.BaselineHash {
		t.Fatalf("same immutable generation replay: %+v %v", replayed, err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			receipt, same, err := s.AcceptFeatureBaseline(ctx, b2, spec, withdrawAt, withdrawAt)
			if err != nil || !receipt.Replay || same.ID != snap.ID {
				t.Errorf("concurrent baseline replay: %+v %+v %v", receipt, same, err)
			}
		}()
	}
	group.Wait()
	changed := b2
	changed.Values = append([]FeatureValue(nil), b2.Values...)
	changed.Values[0].Value = "999"
	if _, _, err := s.AcceptFeatureBaseline(ctx, changed, spec, withdrawAt, withdrawAt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutated SQL value accepted: %v", err)
	}
	if got, err := NewStore(s.db, nil).ReadyFeatureSnapshot(ctx, e1.Subject); err != nil || got.Revision != 2 || featureValue(got, "reading_count").Value != "2" {
		t.Fatalf("restart read: %+v %v", got, err)
	}
	if old, err := NewStore(s.db, nil).FeatureSnapshotByID(ctx, e1.Subject, firstSnapshotID); err != nil ||
		old.ID != firstSnapshotID || old.Revision != 1 || featureValue(old, "reading_count").Value != "2" {
		t.Fatalf("old request snapshot lost after head advance: %+v %v", old, err)
	}
	otherSubject := e1.Subject
	otherSubject.SubjectID = "different-user"
	if _, err := s.FeatureSnapshotByID(ctx, otherSubject, firstSnapshotID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-subject snapshot read: %v", err)
	}
	var baselineCount int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_feature_baselines WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		e1.Subject.AuthorityID, e1.Subject.TenantID, e1.Subject.SubjectID).Scan(&baselineCount); err != nil || baselineCount != 2 {
		t.Fatalf("immutable generations: %d %v", baselineCount, err)
	}
}

func TestPostgresFeaturesAvailabilityMissingContributionAndCAS(t *testing.T) {
	s := featureTestStore(t)
	ctx := context.Background()
	spec := featureSpecFixture()
	at := time.Now().UTC().Add(-20 * time.Minute)
	e1 := featureFixture("cutoff-1", 1, at)
	requireAppend(t, s, e1)
	available := time.Now().UTC()
	snap, err := s.RefreshFeatureSnapshot(ctx, e1.Subject, spec, available, available)
	if err != nil || snap.BaselineState != "absent" || featureValue(snap, "reading_count").Value != "1" {
		t.Fatalf("unbaselined downgrade: %+v %v", snap, err)
	}
	b := featureBaselineFixture(t, s, spec, 1, "sql-cutoff-g1", available, available, 1)
	b.Contributions = nil
	b.Values = []FeatureValue{{Name: "reading_count", Value: "0", Missing: true},
		{Name: "recent_article", Value: "unknown", Missing: true}}
	if _, _, err := s.AcceptFeatureBaseline(ctx, b, spec, available, available); !errors.Is(err, ErrPending) {
		t.Fatalf("missing covered contribution accepted: %v", err)
	}
	b = featureBaselineFixture(t, s, spec, 1, "sql-cutoff-g1", available, available, 1)
	if _, _, err := s.AcceptFeatureBaseline(ctx, b, spec, available, available); err != nil {
		t.Fatal(err)
	}
	oldVersion := snap.StateVersion
	future := featureFixture("cutoff-future", 2, available.Add(time.Hour))
	requireAppend(t, s, future)
	cutoff := time.Now().UTC()
	snap, err = s.RefreshFeatureSnapshot(ctx, e1.Subject, spec, cutoff, cutoff)
	if err != nil || featureValue(snap, "reading_count").Value != "1" || len(snap.Tail) != 0 {
		t.Fatalf("future fact leaked into snapshot: %+v %v", snap, err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockFeatureState(ctx, tx, e1.Subject, oldVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale fact version passed CAS: %v", err)
	}
}

func TestPostgresFeaturesFutureWithdrawalAndOntologyHead(t *testing.T) {
	s := featureTestStore(t)
	ctx := context.Background()
	before := time.Now().UTC().Add(-15 * time.Minute)
	e := featureFixture("ontology-feature", 1, before)
	requireAppend(t, s, e)
	spec := featureSpecFixture()
	spec.Features = append(spec.Features, FeatureDefinition{Name: "ontology_reads", Source: "ontology_rule",
		Rule: "reads_count", Mode: "count", Default: "0"})
	definition := ontologyFixture(e.Subject.TenantID, 1)
	if _, err := s.PublishOntology(ctx, definition, 0); err != nil {
		t.Fatal(err)
	}
	buildAt := time.Now().UTC()
	if _, _, err := s.RebuildOntology(ctx, e.Subject, buildAt); err != nil {
		t.Fatal(err)
	}
	snap, err := s.RefreshFeatureSnapshot(ctx, e.Subject, spec, buildAt, buildAt)
	if err != nil || featureValue(snap, "ontology_reads").Value != "1" || snap.OntologyVersion != 1 {
		t.Fatalf("ready ontology feature: %+v %v", snap, err)
	}
	definition2 := ontologyFixture(e.Subject.TenantID, 2)
	if _, err := s.PublishOntology(ctx, definition2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadyFeatureSnapshot(ctx, e.Subject); !errors.Is(err, ErrPending) {
		t.Fatalf("old ontology feature remained ready: %v", err)
	}
	// The source ledger accepts the later withdrawal, but its information
	// cannot enter a request whose event/availability cutoff is earlier.
	future := featureFixture("future-withdraw", 2, buildAt.Add(time.Hour))
	future.Action, future.Kind, future.ValueRef = Retract, "", ""
	future.Supersedes = &e.EventKey
	requireAppend(t, s, future)
	visible, _, _, err := s.featureStateAt(ctx, e.Subject, buildAt, buildAt)
	if err != nil || len(visible) != 1 || visible[0].EventID != e.EventID {
		t.Fatalf("future withdrawal changed earlier fact state: %+v %v", visible, err)
	}
}

func TestPostgresFeaturesOutOfOrderWithdrawalRecomputedFromAcceptedOutbox(t *testing.T) {
	s := featureTestStore(t)
	ctx := context.Background()
	spec := featureSpecFixture()
	at := time.Now().UTC().Add(-20 * time.Minute)
	first := featureFixture("ordered-1", 1, at)
	requireAppend(t, s, first)
	second := featureFixture("ordered-2", 2, at.Add(time.Minute))
	withdrawal := featureFixture("ordered-3", 3, at.Add(2*time.Minute))
	withdrawal.Action, withdrawal.Kind, withdrawal.ValueRef = Retract, "", ""
	withdrawal.Supersedes = &second.EventKey
	pending := requireAppend(t, s, withdrawal)
	if pending.Status != "pending_dependency" {
		t.Fatalf("withdrawal did not wait for predecessor: %+v", pending)
	}
	before := time.Now().UTC()
	snap, err := s.RefreshFeatureSnapshot(ctx, first.Subject, spec, before, before)
	if err != nil || featureValue(snap, "reading_count").Value != "1" || snap.Watermarks[0].ContiguousSequence != 1 {
		t.Fatalf("pending withdrawal polluted feature: %+v %v", snap, err)
	}
	requireAppend(t, s, second)
	after := time.Now().UTC()
	snap, err = s.RefreshFeatureSnapshot(ctx, first.Subject, spec, after, after)
	if err != nil || featureValue(snap, "reading_count").Value != "1" || snap.Watermarks[0].ContiguousSequence != 3 {
		t.Fatalf("promoted withdrawal not recomputed: %+v %v", snap, err)
	}
	baseline := featureBaselineFixture(t, s, spec, 1, "sql-ordered-g1", after, after, 3)
	if len(baseline.Contributions) != 1 || baseline.Contributions[0].EventID != first.EventID {
		t.Fatalf("reversible baseline retained withdrawn predecessor: %+v", baseline.Contributions)
	}
	if _, snap, err := s.AcceptFeatureBaseline(ctx, baseline, spec, after, after); err != nil ||
		featureValue(snap, "reading_count").Value != "1" || len(snap.Tail) != 0 {
		t.Fatalf("out-of-order accepted baseline: %+v %v", snap, err)
	}
}
