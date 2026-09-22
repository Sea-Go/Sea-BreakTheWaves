package usermodel

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

type fixedBaselineEncoder struct {
	after func(FeatureSnapshot)
	bad   string
}

func (e fixedBaselineEncoder) EncodeUser(_ context.Context, snapshot FeatureSnapshot, pair PairRef) (EncoderOutput, error) {
	if e.after != nil {
		e.after(snapshot)
	}
	count, err := strconv.ParseFloat(featureValue(snapshot, "reading_count").Value, 64)
	if err != nil {
		return EncoderOutput{}, err
	}
	recent := 0.0
	if featureValue(snapshot, "recent_article").Value == "article/known" {
		recent = 1
	}
	out := EncoderOutput{PairID: pair.PairID, SpaceID: pair.SpaceID, EncoderID: pair.EncoderID,
		FeatureSnapshotID: snapshot.ID, Source: "fixed_baseline", Vector: []float64{count, recent}}
	switch e.bad {
	case "space":
		out.SpaceID = "wrong-space"
	case "dimension":
		out.Vector = []float64{count}
	case "nonfinite":
		out.Vector[0] = math.NaN()
	case "dc_without_call":
		out.Source = "dc_prediction"
	}
	return out, nil
}

type fixedPairAuthorizer struct {
	grant PairAuthorization
}

func (a fixedPairAuthorizer) AuthorizePair(context.Context, PairRef) (PairAuthorization, error) {
	return a.grant, nil
}

func servingTestStore(t *testing.T) *Store {
	t.Helper()
	return featureTestStore(t)
}

func testPair(t *testing.T) PairRef {
	t.Helper()
	_, hash, err := canonicalFeatureSpec(featureSpecFixture())
	if err != nil {
		t.Fatal(err)
	}
	return PairRef{PairID: "fixed-baseline-pair-v1", SpaceID: "fixed-baseline-space-v1",
		EncoderID: "fixed-count-recency-v1", Kind: "fixed_baseline", FeatureSpecVersion: "feature-v1",
		FeatureSpecHash: hash, Dimension: 2, Metric: "dot"}
}

func testGrant(pair PairRef) fixedPairAuthorizer {
	return fixedPairAuthorizer{PairAuthorization{Pair: pair,
		ApprovalRef: "fixture-recommend-baseline-1", Revision: 1, Active: true}}
}

func TestPostgresServingBundleBaselineActivationRefreshAndDisable(t *testing.T) {
	s := servingTestStore(t)
	ctx := context.Background()
	pair := testPair(t)
	spec := featureSpecFixture()
	at := time.Now().UTC().Add(-30 * time.Minute)
	first := featureFixture("serving-1", 1, at)
	second := featureFixture("serving-2", 2, at.Add(time.Minute))
	requireAppend(t, s, first)
	requireAppend(t, s, second)
	cutoff := time.Now().UTC()
	b1 := featureBaselineFixture(t, s, spec, 1, "serving-sql-g1", cutoff, cutoff, 2)
	if _, snap, err := s.AcceptFeatureBaseline(ctx, b1, spec, cutoff, cutoff); err != nil ||
		snap.Revision != 1 || featureValue(snap, "reading_count").Value != "2" {
		t.Fatalf("real feature baseline: %+v %v", snap, err)
	}
	bundle, err := s.BuildServingBundle(ctx, first.Subject, pair, fixedBaselineEncoder{})
	if err != nil || bundle.FeatureSnapshotID == "" || bundle.Revision != 1 ||
		bundle.Generation != "serving-sql-g1" || len(bundle.Watermarks) != 1 ||
		bundle.Watermarks[0].ContiguousSequence != 2 || len(bundle.Tail) != 0 ||
		len(bundle.Vector) != 2 || bundle.Vector[0] != 2 || bundle.Vector[1] != 1 {
		t.Fatalf("PG feature to candidate bundle: %+v %v", bundle, err)
	}
	if duplicate, err := s.BuildServingBundle(ctx, first.Subject, pair, fixedBaselineEncoder{}); err != nil ||
		duplicate.ID != bundle.ID {
		t.Fatalf("idempotent immutable candidate: %+v %v", duplicate, err)
	}
	if _, _, err := s.ReadyServingBundle(ctx, first.Subject, pair); !errors.Is(err, ErrNotFound) {
		t.Fatalf("candidate became active: %v", err)
	}
	if _, err := s.ActivateServingBundle(ctx, first.Subject, pair, bundle.ID, 0,
		fixedPairAuthorizer{PairAuthorization{Pair: pair,
			ApprovalRef: "candidate-only", Revision: 1}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unapproved candidate activated: %v", err)
	}
	wrongGrant := testGrant(pair)
	wrongGrant.grant.Pair.EncoderID = "another-encoder"
	if _, err := s.ActivateServingBundle(ctx, first.Subject, pair, bundle.ID, 0,
		wrongGrant); !errors.Is(err, ErrConflict) {
		t.Fatalf("different encoder approval activated pair: %v", err)
	}
	ptr, err := s.ActivateServingBundle(ctx, first.Subject, pair, bundle.ID, 0, testGrant(pair))
	if err != nil || ptr.Version != 1 || ptr.State != "active" {
		t.Fatalf("baseline activation: %+v %v", ptr, err)
	}
	if got, active, err := s.ReadyServingBundle(ctx, first.Subject, pair); err != nil ||
		got.ID != bundle.ID || active.BundleID != bundle.ID || active.ApprovalRevision != 1 {
		t.Fatalf("current read: %+v %+v %v", got, active, err)
	}
	other := first.Subject
	other.TenantID = "another-tenant"
	if _, err := s.ServingBundleByID(ctx, other, bundle.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant immutable read: %v", err)
	}
	other = first.Subject
	other.SubjectID = "another-user"
	if _, _, err := s.ReadyServingBundle(ctx, other, pair); !errors.Is(err, ErrPending) {
		t.Fatalf("cross-subject active read: %v", err)
	}
	wrongSpace := pair
	wrongSpace.SpaceID = "same-dimension-other-space"
	if _, _, err := s.ReadyServingBundle(ctx, first.Subject, wrongSpace); !errors.Is(err, ErrPending) {
		t.Fatalf("same-dimension other space accepted: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE usermodel_serving_bundles SET bundle_body='{}'::jsonb
		WHERE bundle_id=$1`, bundle.ID); err == nil {
		t.Fatal("database allowed immutable bundle mutation")
	}

	// A new fact invalidates the old current representation even before the
	// near-line feature materialization catches up. Its historical ID survives.
	third := featureFixture("serving-3", 3, at.Add(2*time.Minute))
	requireAppend(t, s, third)
	if _, _, err := s.ReadyServingBundle(ctx, first.Subject, pair); !errors.Is(err, ErrPending) {
		t.Fatalf("fact change did not invalidate active bundle: %v", err)
	}
	if old, err := s.ServingBundleByID(ctx, first.Subject, bundle.ID); err != nil || old.ID != bundle.ID {
		t.Fatalf("historical bundle lost: %+v %v", old, err)
	}
	refreshedAt := time.Now().UTC()
	snapshot, err := s.RefreshFeatureSnapshot(ctx, first.Subject, spec, refreshedAt, refreshedAt)
	if err != nil || snapshot.ID == bundle.FeatureSnapshotID || len(snapshot.Tail) != 1 {
		t.Fatalf("near-line feature refresh: %+v %v", snapshot, err)
	}
	next, err := s.BuildServingBundle(ctx, first.Subject, pair, fixedBaselineEncoder{})
	if err != nil || next.ID == bundle.ID || next.Vector[0] != 3 || len(next.Tail) != 1 {
		t.Fatalf("new user vector: %+v %v", next, err)
	}
	if _, err := s.ActivateServingBundle(ctx, first.Subject, pair, next.ID, 0, testGrant(pair)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS replaced pointer: %v", err)
	}
	if _, err := s.ActivateServingBundle(ctx, first.Subject, pair, next.ID, ptr.Version,
		fixedPairAuthorizer{PairAuthorization{Pair: pair,
			ApprovalRef: "old-approval", Revision: 0, Active: true}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid approval revision accepted: %v", err)
	}
	ptr, err = s.ActivateServingBundle(ctx, first.Subject, pair, next.ID, ptr.Version, testGrant(pair))
	if err != nil || ptr.Version != 2 {
		t.Fatalf("new pointer: %+v %v", ptr, err)
	}

	// Advancing the warehouse generation is another input version change;
	// the old bundle remains historical, but the new feature ID requires a
	// fresh encode and CAS before it can be served.
	b2 := featureBaselineFixture(t, s, spec, 2, "serving-sql-g2", refreshedAt, refreshedAt, 3)
	if _, snap, err := s.AcceptFeatureBaseline(ctx, b2, spec, refreshedAt, refreshedAt); err != nil ||
		snap.Revision != 2 || snap.ID == next.FeatureSnapshotID {
		t.Fatalf("second PG generation: %+v %v", snap, err)
	}
	if _, _, err := s.ReadyServingBundle(ctx, first.Subject, pair); !errors.Is(err, ErrPending) {
		t.Fatalf("old generation served: %v", err)
	}
	latest, err := s.BuildServingBundle(ctx, first.Subject, pair, fixedBaselineEncoder{})
	if err != nil || latest.Generation != "serving-sql-g2" || len(latest.Tail) != 0 {
		t.Fatalf("second generation encode: %+v %v", latest, err)
	}
	ptr, err = s.ActivateServingBundle(ctx, first.Subject, pair, latest.ID, ptr.Version, testGrant(pair))
	if err != nil || ptr.Version != 3 {
		t.Fatalf("second generation activate: %+v %v", ptr, err)
	}
	if got, _, err := NewStore(s.db, nil).ReadyServingBundle(ctx, first.Subject, pair); err != nil ||
		got.ID != latest.ID {
		t.Fatalf("restart current read: %+v %v", got, err)
	}
	disabled, err := s.DisableServingBundle(ctx, first.Subject, pair.PairID, ptr.Version)
	if err != nil || disabled.State != "disabled" || disabled.Version != 4 {
		t.Fatalf("disable: %+v %v", disabled, err)
	}
	if _, _, err := s.ReadyServingBundle(ctx, first.Subject, pair); !errors.Is(err, ErrPending) {
		t.Fatalf("disabled bundle still served: %v", err)
	}
	if _, err := s.DisableServingBundle(ctx, first.Subject, pair.PairID, ptr.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale disable CAS: %v", err)
	}
	if old, err := s.ServingBundleByID(ctx, first.Subject, latest.ID); err != nil || old.ID != latest.ID {
		t.Fatalf("disabled historical bundle not readable: %+v %v", old, err)
	}
}

func TestPostgresServingBundleRejectsWrongVersionsAndConcurrentFact(t *testing.T) {
	s := servingTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-10 * time.Minute)
	first := featureFixture("serving-race-1", 1, at)
	requireAppend(t, s, first)
	spec := featureSpecFixture()
	now := time.Now().UTC()
	if _, err := s.RefreshFeatureSnapshot(ctx, first.Subject, spec, now, now); err != nil {
		t.Fatal(err)
	}
	pair := testPair(t)
	badSpec := pair
	badSpec.FeatureSpecVersion = "other-version"
	if _, err := s.BuildServingBundle(ctx, first.Subject, badSpec, fixedBaselineEncoder{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("feature spec mismatch: %v", err)
	}
	for _, reason := range []string{"space", "dimension", "nonfinite", "dc_without_call"} {
		if _, err := s.BuildServingBundle(ctx, first.Subject, pair, fixedBaselineEncoder{bad: reason}); err == nil {
			t.Fatalf("invalid encoder output %s accepted", reason)
		}
	}
	modelPair := pair
	modelPair.Kind = "model"
	if _, err := s.BuildServingBundle(ctx, first.Subject, modelPair, fixedBaselineEncoder{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("fixed baseline impersonated DC model pair: %v", err)
	}
	second := featureFixture("serving-race-2", 2, at.Add(time.Minute))
	encoder := fixedBaselineEncoder{after: func(FeatureSnapshot) { requireAppend(t, s, second) }}
	if _, err := s.BuildServingBundle(ctx, first.Subject, pair, encoder); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale inference published after fact changed: %v", err)
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM usermodel_serving_bundles`).Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("stale candidate left row: %d %v", count, err)
	}
}
