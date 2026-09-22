package usermodel

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ontologyFixture(tenant string, version int64) OntologyDefinition {
	return OntologyDefinition{AuthorityID: "rtw.identity", TenantID: tenant, Version: version,
		Objects: []OntologyObject{
			{Name: "subject", Attributes: []OntologyAttribute{{Name: "read_count", Type: "integer"}, {Name: "combined_count", Type: "integer"}}},
			{Name: "article"},
		},
		Relations: []OntologyRelation{{Name: "reads", ToObject: "article", Predicate: "read", Kind: Reading, Cardinality: "many", WindowSeconds: 3600}},
		Rules: []OntologyRule{
			{Name: "reads_count", OutputAttribute: "read_count", Inputs: []string{"relation.reads"}},
			{Name: "combined", OutputAttribute: "combined_count", Inputs: []string{"rule.reads_count", "relation.reads"}},
		},
	}
}

func ontologyTestStore(t *testing.T) *Store {
	t.Helper()
	return testStore(t, nil)
}

func ontologyValue(p OntologyProjection, name string) int64 {
	for _, v := range p.Derived {
		if v.Rule == name {
			return v.Value
		}
	}
	return -1
}

func TestOntologyDefinitionRejectsCyclesTypesAndUnboundInputs(t *testing.T) {
	d := ontologyFixture("tenant-a", 1)
	canonical, hash, _, err := canonicalOntologyDefinition(d)
	if err != nil || hash == "" || canonical.Version != 1 {
		t.Fatalf("valid definition: %+v %s %v", canonical, hash, err)
	}
	reordered := d
	reordered.Objects = slices.Clone(d.Objects)
	reordered.Objects[0], reordered.Objects[1] = reordered.Objects[1], reordered.Objects[0]
	reordered.Rules = slices.Clone(d.Rules)
	reordered.Rules[0], reordered.Rules[1] = reordered.Rules[1], reordered.Rules[0]
	_, sameHash, _, err := canonicalOntologyDefinition(reordered)
	if err != nil || sameHash != hash {
		t.Fatalf("declaration order changed hash: %s %s %v", hash, sameHash, err)
	}
	for name, mutate := range map[string]func(*OntologyDefinition){
		"wrong output type":  func(v *OntologyDefinition) { v.Objects[0].Attributes[0].Type = "string" },
		"unknown relation":   func(v *OntologyDefinition) { v.Rules[0].Inputs = []string{"relation.unknown"} },
		"dependency cycle":   func(v *OntologyDefinition) { v.Rules[0].Inputs = []string{"rule.combined"} },
		"unsupported action": func(v *OntologyDefinition) { v.Rules[0].Inputs = []string{"goal.create"} },
		"unknown target":     func(v *OntologyDefinition) { v.Relations[0].ToObject = "missing" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := ontologyFixture("tenant-a", 1)
			mutate(&bad)
			if _, _, _, err := canonicalOntologyDefinition(bad); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected validation failure, got %v", err)
			}
		})
	}
}

func TestPostgresOntologyPublicationRebuildWithdrawalLateAndRestart(t *testing.T) {
	s := ontologyTestStore(t)
	ctx := context.Background()
	d := ontologyFixture("tenant-a", 1)
	first, err := s.PublishOntology(ctx, d, 0)
	if err != nil || first.Replay || first.Version != 1 {
		t.Fatalf("first publication %+v %v", first, err)
	}
	second, err := s.PublishOntology(ctx, d, 0)
	if err != nil || !second.Replay || second.Hash != first.Hash {
		t.Fatalf("same revision replay %+v %v", second, err)
	}
	reordered := ontologyFixture("tenant-a", 1)
	reordered.Objects[0], reordered.Objects[1] = reordered.Objects[1], reordered.Objects[0]
	reordered.Rules[0], reordered.Rules[1] = reordered.Rules[1], reordered.Rules[0]
	third, err := s.PublishOntology(ctx, reordered, 0)
	if err != nil || !third.Replay || third.Hash != first.Hash {
		t.Fatalf("reordered revision replay %+v %v", third, err)
	}
	changed := ontologyFixture("tenant-a", 1)
	changed.Relations[0].Predicate = "other"
	if _, err := s.PublishOntology(ctx, changed, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("same version changed body: %v", err)
	}
	bad := ontologyFixture("tenant-a", 2)
	bad.Rules[0].Inputs = []string{"rule.combined"}
	if _, err := s.PublishOntology(ctx, bad, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle activated: %v", err)
	}
	e1, e2 := fixture("ontology-1", 1), fixture("ontology-2", 2)
	e2.ValueRef, e2.ItemID = "article/rev-2", "article-2"
	requireAppend(t, s, e1)
	requireAppend(t, s, e2)
	asOf := e2.OccurredAt.Add(time.Minute)
	pending, err := s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", asOf, 20)
	if err != nil || !reflect.DeepEqual(pending, []SubjectRef{e1.Subject}) {
		t.Fatalf("initial pending %+v %v", pending, err)
	}
	p1, impact, err := s.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || len(p1.Edges) != 2 || ontologyValue(p1, "reads_count") != 2 || ontologyValue(p1, "combined") != 4 ||
		len(p1.Edges[0].Evidence) != 1 || p1.Edges[0].Evidence[0].EvidenceRef != e1.EvidenceRef ||
		p1.Edges[0].Evidence[0].EventKey != e1.EventKey ||
		!reflect.DeepEqual(impact.RecomputeTargets, []string{"user_state", "user_features"}) {
		t.Fatalf("first projection %+v impact %+v err %v", p1, impact, err)
	}
	ready, err := s.ReadyOntologyProjection(ctx, e1.Subject, asOf)
	if err != nil || !reflect.DeepEqual(ready, p1) {
		t.Fatalf("pinned ontology read %+v %v", ready, err)
	}
	pending, err = s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", asOf, 20)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rebuilt still pending %+v %v", pending, err)
	}
	_, replay, err := s.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || !replay.Replay || len(replay.AffectedObjects) != 0 {
		t.Fatalf("same snapshot replay %+v %v", replay, err)
	}
	withdraw := fixture("ontology-withdraw", 3)
	withdraw.Action, withdraw.Supersedes = Retract, &e1.EventKey
	requireAppend(t, s, withdraw)
	if _, err := s.ReadyOntologyProjection(ctx, e1.Subject, asOf); !errors.Is(err, ErrPending) {
		t.Fatalf("stale graph was exposed: %v", err)
	}
	pending, err = s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", asOf, 20)
	if err != nil || len(pending) != 1 {
		t.Fatalf("withdrawal not pending %+v %v", pending, err)
	}
	p2, removed, err := s.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || len(p2.Edges) != 1 || ontologyValue(p2, "reads_count") != 1 || !slices.Contains(removed.AffectedObjects,
		OntologyObjectRef{Type: "article", ID: "article/rev-1"}) || !slices.Contains(removed.AffectedRules, "reads_count") {
		t.Fatalf("withdrawal %+v impact %+v err %v", p2, removed, err)
	}
	late := fixture("ontology-late", 4)
	late.OccurredAt = e1.OccurredAt.Add(-time.Minute)
	late.ObservedAt = e2.ObservedAt.Add(3 * time.Minute)
	late.ValueRef, late.ItemID = "article/rev-late", "article-late"
	requireAppend(t, s, late)
	p3, added, err := s.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || len(p3.Edges) != 2 || !slices.Contains(added.AffectedObjects,
		OntologyObjectRef{Type: "article", ID: "article/rev-late"}) {
		t.Fatalf("late event %+v %+v %v", p3, added, err)
	}
	other := fixture("ontology-other", 1)
	other.Subject.SubjectID = "user-2"
	requireAppend(t, s, other)
	separate := fixture("ontology-foreign", 1)
	separate.Subject.TenantID = "tenant-b"
	requireAppend(t, s, separate)
	pending, err = s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", asOf, 20)
	if err != nil || !reflect.DeepEqual(pending, []SubjectRef{other.Subject}) {
		t.Fatalf("subject/tenant leakage %+v %v", pending, err)
	}
	restarted := NewStore(s.db, nil)
	stored, err := restarted.StoredOntologyProjection(ctx, e1.Subject)
	if err != nil || !reflect.DeepEqual(stored, p3) {
		t.Fatalf("restart load %+v %v", stored, err)
	}
	_, replay, err = restarted.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || !replay.Replay {
		t.Fatalf("restart rebuild %+v %v", replay, err)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM usermodel_ontology_projections WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		e1.Subject.AuthorityID, e1.Subject.TenantID, e1.Subject.SubjectID); err != nil {
		t.Fatal(err)
	}
	rebuilt, _, err := restarted.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || !reflect.DeepEqual(rebuilt, p3) {
		t.Fatalf("rebuild from facts %+v %v", rebuilt, err)
	}
	d2 := ontologyFixture("tenant-a", 2)
	d2.Relations[0].Cardinality = "one"
	if _, err := s.PublishOntology(ctx, d2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadyOntologyProjection(ctx, e1.Subject, asOf); !errors.Is(err, ErrPending) {
		t.Fatalf("old definition exposed as ready: %v", err)
	}
	pending, err = s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", asOf, 20)
	if err != nil || !reflect.DeepEqual(pending, []SubjectRef{e1.Subject, other.Subject}) {
		t.Fatalf("schema change impact %+v %v", pending, err)
	}
	one, changedImpact, err := s.RebuildOntology(ctx, e1.Subject, asOf)
	if err != nil || one.DefinitionVersion != 2 || len(one.Edges) != 1 || one.Edges[0].ToID != e2.ValueRef || len(changedImpact.RecomputeTargets) != 2 {
		t.Fatalf("schema rebuild %+v %+v %v", one, changedImpact, err)
	}
	if _, err := s.PublishOntology(ctx, ontologyFixture("tenant-a", 3), 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale head: %v", err)
	}
}

func TestPostgresOntologyRebuildWithOneConnection(t *testing.T) {
	s := ontologyTestStore(t)
	ctx := context.Background()
	if _, err := s.PublishOntology(ctx, ontologyFixture("tenant-a", 1), 0); err != nil {
		t.Fatal(err)
	}
	e := fixture("one-connection", 1)
	requireAppend(t, s, e)
	config := s.db.Config()
	config.MaxConns = 1
	onePool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(onePool.Close)
	one := NewStore(onePool, nil)
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p, _, err := one.RebuildOntology(deadline, e.Subject, e.OccurredAt.Add(time.Minute))
	if err != nil || len(p.Edges) != 1 {
		t.Fatalf("one connection rebuild %+v %v", p, err)
	}
}

func TestPostgresOntologyEvidenceChangeWithStableCount(t *testing.T) {
	s := ontologyTestStore(t)
	ctx := context.Background()
	if _, err := s.PublishOntology(ctx, ontologyFixture("tenant-a", 1), 0); err != nil {
		t.Fatal(err)
	}
	e := fixture("same-target-original", 1)
	requireAppend(t, s, e)
	asOf := e.OccurredAt.Add(3 * time.Minute)
	first, _, err := s.RebuildOntology(ctx, e.Subject, asOf)
	if err != nil {
		t.Fatal(err)
	}
	corrected := fixture("same-target-correction", 2)
	corrected.Action, corrected.Supersedes = Correct, &e.EventKey
	requireAppend(t, s, corrected)
	second, impact, err := s.RebuildOntology(ctx, e.Subject, asOf)
	if err != nil || ontologyValue(first, "reads_count") != ontologyValue(second, "reads_count") ||
		!slices.Contains(impact.AffectedObjects, OntologyObjectRef{Type: "article", ID: e.ValueRef}) ||
		!slices.Contains(impact.AffectedRules, "reads_count") || second.Edges[0].Evidence[0].EventKey != corrected.EventKey {
		t.Fatalf("stable count lost evidence impact: before %+v after %+v impact %+v err %v", first, second, impact, err)
	}
}

func TestPostgresOntologyConcurrentFirstPublication(t *testing.T) {
	s := ontologyTestStore(t)
	d := ontologyFixture("tenant-a", 1)
	const callers = 8
	var wg sync.WaitGroup
	receipts := make(chan OntologyPublication, callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.PublishOntology(context.Background(), d, 0)
			if err != nil {
				errorsSeen <- err
				return
			}
			receipts <- r
		}()
	}
	wg.Wait()
	close(receipts)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent publication: %v", err)
	}
	firsts, replays := 0, 0
	for r := range receipts {
		if r.Replay {
			replays++
		} else {
			firsts++
		}
	}
	if firsts != 1 || replays != callers-1 {
		t.Fatalf("first=%d replay=%d", firsts, replays)
	}
}

func TestPostgresOntologyTemporalChangeAndScope(t *testing.T) {
	s := ontologyTestStore(t)
	ctx := context.Background()
	if _, err := s.PublishOntology(ctx, ontologyFixture("tenant-a", 1), 0); err != nil {
		t.Fatal(err)
	}
	e := fixture("window-1", 1)
	requireAppend(t, s, e)
	beforeExpiry := e.OccurredAt.Add(30 * time.Minute)
	p, _, err := s.RebuildOntology(ctx, e.Subject, beforeExpiry)
	if err != nil || len(p.Edges) != 1 || p.NextChangeAt == nil || !p.NextChangeAt.Equal(e.OccurredAt.Add(time.Hour)) {
		t.Fatalf("next change %+v %v", p, err)
	}
	pending, err := s.PendingOntologySubjects(ctx, "rtw.identity", "tenant-a", "", e.OccurredAt.Add(time.Hour), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expiry not pending %+v %v", pending, err)
	}
	expired, changed, err := s.RebuildOntology(ctx, e.Subject, e.OccurredAt.Add(time.Hour))
	if err != nil || len(expired.Edges) != 0 || !slices.Contains(changed.AffectedObjects,
		OntologyObjectRef{Type: "article", ID: e.ValueRef}) {
		t.Fatalf("expiry %+v %+v %v", expired, changed, err)
	}
	if _, _, err := s.RebuildOntology(ctx, e.Subject, beforeExpiry); !errors.Is(err, ErrConflict) {
		t.Fatalf("time rewind: %v", err)
	}
	foreign := e.Subject
	foreign.TenantID = "tenant-b"
	if _, _, err := s.RebuildOntology(ctx, foreign, beforeExpiry); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign scope: %v", err)
	}
}
