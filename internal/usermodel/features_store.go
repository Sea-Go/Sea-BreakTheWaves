package usermodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type FeatureReceipt struct {
	Subject      SubjectRef `json:"subject_ref"`
	Revision     int64      `json:"baseline_revision"`
	Generation   string     `json:"baseline_generation"`
	SpecHash     string     `json:"feature_spec_hash"`
	BaselineHash string     `json:"baseline_hash"`
	StateVersion int64      `json:"user_state_version"`
	Replay       bool       `json:"replay"`
}

type featureSourcePositions struct {
	producer, partition string
	positions           map[int64]bool
	max                 int64
}

// featureStateAt reads the fact ledger and its same-transaction accepted
// Outbox record. Outbox created_at is a recorded transaction time, including
// late promotion of a previously pending dependency; it is not a PostgreSQL
// commit timestamp. A retained request snapshot is the exact historical input.
// Accepted-version order reconstructs corrections and withdrawals at cutoff.
func (s *Store) featureStateAt(ctx context.Context, subject SubjectRef, asOf, availableAt time.Time) ([]Fact, []Watermark, int64, error) {
	if s == nil || s.db == nil || !subject.valid() || asOf.IsZero() || availableAt.IsZero() || asOf.After(availableAt) {
		return nil, nil, 0, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback(ctx)
	var stateVersion int64
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&stateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT e.event_body,e.normalized_hash,e.accepted_version
		FROM usermodel_events e JOIN usermodel_outbox o ON
			o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id AND o.subject_id=e.subject_id
			AND o.producer=e.producer AND o.event_id=e.event_id AND o.state_version=e.accepted_version
			AND o.event_type='usermodel.fact.accepted'
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.status='accepted'
			AND e.occurred_at<=$4 AND e.observed_at<=$5 AND o.created_at<=$5
		ORDER BY e.accepted_version,e.producer,e.event_id LIMIT 100001`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, asOf.UTC(), availableAt.UTC())
	if err != nil {
		return nil, nil, 0, err
	}
	active := map[string]Fact{}
	sources := map[string]*featureSourcePositions{}
	count := 0
	for rows.Next() {
		count++
		if count > 100000 {
			rows.Close()
			return nil, nil, 0, fmt.Errorf("%w: feature source limit", ErrPending)
		}
		var body []byte
		var f Fact
		if err := rows.Scan(&body, &f.NormalizedHash, &f.AcceptedVersion); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		if err := json.Unmarshal(body, &f.Event); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		f.Subject, f.Status = subject, "accepted"
		key := featureEventKey(f.EventKey)
		if f.Supersedes != nil {
			delete(active, featureEventKey(*f.Supersedes))
		}
		if f.Action != Retract {
			active[key] = f
		}
		if f.SourceSequence > 0 {
			sourceKey := featureSourceKey(f.Producer, f.SourcePartition)
			src := sources[sourceKey]
			if src == nil {
				src = &featureSourcePositions{producer: f.Producer, partition: f.SourcePartition, positions: map[int64]bool{}}
				sources[sourceKey] = src
			}
			src.positions[f.SourceSequence] = true
			if f.SourceSequence > src.max {
				src.max = f.SourceSequence
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, 0, err
	}
	rows.Close()
	facts := make([]Fact, 0, len(active))
	for _, f := range active {
		facts = append(facts, f)
	}
	sort.Slice(facts, func(i, j int) bool { return featureEventKey(facts[i].EventKey) < featureEventKey(facts[j].EventKey) })
	watermarks := make([]Watermark, 0, len(sources))
	for _, src := range sources {
		var contiguous int64
		for src.positions[contiguous+1] {
			contiguous++
		}
		watermarks = append(watermarks, Watermark{Producer: src.producer, SourcePartition: src.partition,
			ContiguousSequence: contiguous, MaxSeenSequence: src.max, Complete: contiguous == src.max})
	}
	sort.Slice(watermarks, func(i, j int) bool {
		return featureSourceKey(watermarks[i].Producer, watermarks[i].SourcePartition) < featureSourceKey(watermarks[j].Producer, watermarks[j].SourcePartition)
	})
	return facts, watermarks, stateVersion, tx.Commit(ctx)
}

func (s *Store) featureOntology(ctx context.Context, subject SubjectRef, spec FeatureSpec, stateVersion int64, asOf time.Time) (*OntologyProjection, error) {
	for _, def := range spec.Features {
		if def.Source != "ontology_rule" {
			continue
		}
		projection, err := s.ReadyOntologyProjection(ctx, subject, asOf)
		if err != nil {
			return nil, err
		}
		if projection.StateVersion != stateVersion {
			return nil, ErrPending
		}
		return &projection, nil
	}
	return nil, nil
}

// futureAcceptedChangeAt invalidates a frozen feature snapshot when an
// already-accepted event first enters its business/availability window. This
// includes future corrections and withdrawals, not just currently active
// assertions. Outbox.created_at is only an eligibility field, not a commit
// timestamp; the state-version lock still guards concurrent publication.
func (s *Store) futureAcceptedChangeAt(ctx context.Context, subject SubjectRef, asOf, availableAt time.Time) (*time.Time, error) {
	var next *time.Time
	err := s.db.QueryRow(ctx, `SELECT min(GREATEST(e.occurred_at,e.observed_at,o.created_at))
		FROM usermodel_events e JOIN usermodel_outbox o ON
			o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id AND o.subject_id=e.subject_id
			AND o.producer=e.producer AND o.event_id=e.event_id AND o.state_version=e.accepted_version
			AND o.event_type='usermodel.fact.accepted'
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.status='accepted'
			AND (e.occurred_at>$4 OR e.observed_at>$5 OR o.created_at>$5)`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, asOf.UTC(), availableAt.UTC()).Scan(&next)
	if err != nil {
		return nil, err
	}
	if next != nil {
		at := next.UTC()
		return &at, nil
	}
	return nil, nil
}

func (s *Store) setFutureFeatureChange(ctx context.Context, snapshot *FeatureSnapshot) error {
	next, err := s.futureAcceptedChangeAt(ctx, snapshot.Subject, snapshot.AsOf, snapshot.AvailableAt)
	if err != nil {
		return err
	}
	if next != nil && (snapshot.NextChangeAt == nil || next.Before(*snapshot.NextChangeAt)) {
		snapshot.NextChangeAt = next
	}
	return nil
}

func (s *Store) activeFeatureBaseline(ctx context.Context, subject SubjectRef) (*FeatureBaseline, error) {
	var body []byte
	err := s.db.QueryRow(ctx, `SELECT b.baseline_body FROM usermodel_feature_heads h
		JOIN usermodel_feature_baselines b USING(authority_id,tenant_id,subject_id,revision)
		WHERE h.authority_id=$1 AND h.tenant_id=$2 AND h.subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var baseline FeatureBaseline
	if err := json.Unmarshal(body, &baseline); err != nil {
		return nil, err
	}
	if baseline.Subject != subject {
		return nil, ErrConflict
	}
	return &baseline, nil
}

func validateFeatureCoverage(b FeatureBaseline, active []Fact, watermarks []Watermark) error {
	positions := map[string]int64{}
	for _, w := range watermarks {
		positions[featureSourceKey(w.Producer, w.SourcePartition)] = w.ContiguousSequence
	}
	covered := map[string]int64{}
	for _, w := range b.Watermarks {
		key := featureSourceKey(w.Producer, w.SourcePartition)
		if w.ContiguousSequence > positions[key] {
			return fmt.Errorf("%w: warehouse watermark ahead of accepted source", ErrPending)
		}
		covered[key] = w.ContiguousSequence
	}
	expected := map[string]Fact{}
	for _, f := range active {
		if f.SourceSequence > 0 && f.SourceSequence <= covered[featureSourceKey(f.Producer, f.SourcePartition)] {
			expected[featureEventKey(f.EventKey)] = f
		}
	}
	if len(expected) != len(b.Contributions) {
		return fmt.Errorf("%w: incomplete reversible baseline contributions", ErrPending)
	}
	for _, f := range b.Contributions {
		actual, ok := expected[featureEventKey(f.EventKey)]
		if !ok || actual.NormalizedHash != f.NormalizedHash || actual.AcceptedVersion != f.AcceptedVersion || !reflect.DeepEqual(actual.Event, f.Event) {
			return fmt.Errorf("%w: baseline contribution differs from accepted fact", ErrPending)
		}
	}
	return nil
}

func featureHeadVersion(ctx context.Context, tx pgx.Tx, subject SubjectRef) (int64, string, error) {
	var revision int64
	var hash string
	err := tx.QueryRow(ctx, `SELECT h.revision,b.baseline_hash FROM usermodel_feature_heads h
		JOIN usermodel_feature_baselines b USING(authority_id,tenant_id,subject_id,revision)
		WHERE h.authority_id=$1 AND h.tenant_id=$2 AND h.subject_id=$3 FOR UPDATE OF h`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&revision, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	return revision, hash, err
}

func lockFeatureState(ctx context.Context, tx pgx.Tx, subject SubjectRef, expected int64) error {
	var actual int64
	err := tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&actual)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: user facts changed during feature build", ErrConflict)
	}
	return nil
}

func saveFeatureSnapshot(ctx context.Context, tx pgx.Tx, snap FeatureSnapshot) error {
	if !digest.MatchString(snap.ID) {
		return ErrInvalid
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	var revision any
	if snap.BaselineState == "accepted" {
		revision = snap.Revision
	}
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_feature_snapshot_versions
		(authority_id,tenant_id,subject_id,snapshot_id,snapshot_body)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		snap.Subject.AuthorityID, snap.Subject.TenantID, snap.Subject.SubjectID, snap.ID, body)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO usermodel_feature_snapshots
		(authority_id,tenant_id,subject_id,state_version,spec_version,spec_hash,baseline_revision,as_of,available_at,snapshot_body)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(authority_id,tenant_id,subject_id) DO UPDATE SET
		state_version=EXCLUDED.state_version,spec_version=EXCLUDED.spec_version,spec_hash=EXCLUDED.spec_hash,
		baseline_revision=EXCLUDED.baseline_revision,as_of=EXCLUDED.as_of,
		available_at=EXCLUDED.available_at,snapshot_body=EXCLUDED.snapshot_body,built_at=now()
		WHERE usermodel_feature_snapshots.available_at<=EXCLUDED.available_at`,
		snap.Subject.AuthorityID, snap.Subject.TenantID, snap.Subject.SubjectID, snap.StateVersion,
		snap.SpecVersion, snap.SpecHash, revision, snap.AsOf, snap.AvailableAt, body)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: feature snapshot availability rewind", ErrConflict)
	}
	return nil
}

func freezeFeatureSnapshot(snapshot *FeatureSnapshot) error {
	if snapshot == nil || !snapshot.Subject.valid() || snapshot.ID != "" {
		return ErrInvalid
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	snapshot.ID = hex.EncodeToString(sum[:])
	return nil
}

// AcceptFeatureBaseline validates the frozen warehouse generation against the
// accepted fact ledger at its own cutoff, then atomically advances the head and
// publishes a recomputed current snapshot. A missing contribution is Pending.
func (s *Store) AcceptFeatureBaseline(ctx context.Context, input FeatureBaseline, inputSpec FeatureSpec,
	asOf, availableAt time.Time) (receipt FeatureReceipt, snapshot FeatureSnapshot, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.features.baseline.accept", input.Subject)
	if beginErr != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, beginErr
	}
	defer func() { finish(ctx, stage, err, receipt.Replay, "") }()
	spec, specHash, err := canonicalFeatureSpec(inputSpec)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	b, baselineHash, err := canonicalFeatureBaseline(input, spec, specHash)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if b.AvailableAt.After(time.Now().UTC()) || availableAt.After(time.Now().UTC()) {
		return FeatureReceipt{}, FeatureSnapshot{}, ErrInvalid
	}
	coveredFacts, coveredWM, _, err := s.featureStateAt(ctx, b.Subject, b.AsOf, b.AvailableAt)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if err := validateFeatureCoverage(b, coveredFacts, coveredWM); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	active, watermarks, stateVersion, err := s.featureStateAt(ctx, b.Subject, asOf, availableAt)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	ontology, err := s.featureOntology(ctx, b.Subject, spec, stateVersion, asOf)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	snapshot, err = mergeFeatureSnapshot(spec, specHash, &b, active, watermarks, stateVersion, ontology, asOf, availableAt)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if err := s.setFutureFeatureChange(ctx, &snapshot); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if err := freezeFeatureSnapshot(&snapshot); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockFeatureState(ctx, tx, b.Subject, stateVersion); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	headRevision, headHash, err := featureHeadVersion(ctx, tx, b.Subject)
	if err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if b.Revision == headRevision && baselineHash == headHash {
		receipt.Replay = true
	} else if b.Revision != headRevision+1 {
		return FeatureReceipt{}, FeatureSnapshot{}, fmt.Errorf("%w: stale or nonconsecutive baseline", ErrConflict)
	}
	if !receipt.Replay {
		if headRevision > 0 {
			var oldBody []byte
			if err := tx.QueryRow(ctx, `SELECT baseline_body FROM usermodel_feature_baselines
				WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
				b.Subject.AuthorityID, b.Subject.TenantID, b.Subject.SubjectID, headRevision).Scan(&oldBody); err != nil {
				return FeatureReceipt{}, FeatureSnapshot{}, err
			}
			var old FeatureBaseline
			if err := json.Unmarshal(oldBody, &old); err != nil {
				return FeatureReceipt{}, FeatureSnapshot{}, err
			}
			oldWM := map[string]int64{}
			for _, w := range old.Watermarks {
				oldWM[featureSourceKey(w.Producer, w.SourcePartition)] = w.ContiguousSequence
			}
			newWM := map[string]int64{}
			for _, w := range b.Watermarks {
				newWM[featureSourceKey(w.Producer, w.SourcePartition)] = w.ContiguousSequence
			}
			for key, seq := range oldWM {
				if newWM[key] < seq {
					return FeatureReceipt{}, FeatureSnapshot{}, fmt.Errorf("%w: baseline source watermark rewind", ErrConflict)
				}
			}
			if b.AvailableAt.Before(old.AvailableAt) || b.AsOf.Before(old.AsOf) {
				return FeatureReceipt{}, FeatureSnapshot{}, fmt.Errorf("%w: baseline time rewind", ErrConflict)
			}
		}
		body, err := json.Marshal(b)
		if err != nil {
			return FeatureReceipt{}, FeatureSnapshot{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO usermodel_feature_baselines
			(authority_id,tenant_id,subject_id,revision,generation,spec_version,spec_hash,baseline_hash,baseline_body)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, b.Subject.AuthorityID, b.Subject.TenantID,
			b.Subject.SubjectID, b.Revision, b.Generation, b.SpecVersion, b.SpecHash, baselineHash, body)
		if err != nil {
			return FeatureReceipt{}, FeatureSnapshot{}, dbErr(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO usermodel_feature_heads(authority_id,tenant_id,subject_id,revision)
			VALUES($1,$2,$3,$4) ON CONFLICT(authority_id,tenant_id,subject_id) DO UPDATE SET
			revision=EXCLUDED.revision,changed_at=now()`, b.Subject.AuthorityID, b.Subject.TenantID, b.Subject.SubjectID, b.Revision)
		if err != nil {
			return FeatureReceipt{}, FeatureSnapshot{}, err
		}
	}
	if err := saveFeatureSnapshot(ctx, tx, snapshot); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeatureReceipt{}, FeatureSnapshot{}, err
	}
	receipt.Subject, receipt.Revision, receipt.Generation, receipt.SpecHash, receipt.BaselineHash, receipt.StateVersion =
		b.Subject, b.Revision, b.Generation, specHash, baselineHash, stateVersion
	return receipt, snapshot, nil
}

// RefreshFeatureSnapshot is an idempotent restart/retraction recovery entry.
// With no baseline it publishes a visible absent-baseline downgrade.
func (s *Store) RefreshFeatureSnapshot(ctx context.Context, subject SubjectRef, inputSpec FeatureSpec,
	asOf, availableAt time.Time) (snapshot FeatureSnapshot, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.features.refresh", subject)
	if beginErr != nil {
		return FeatureSnapshot{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.valid() || availableAt.After(time.Now().UTC()) {
		return FeatureSnapshot{}, ErrInvalid
	}
	spec, specHash, err := canonicalFeatureSpec(inputSpec)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	baseline, err := s.activeFeatureBaseline(ctx, subject)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	active, watermarks, stateVersion, err := s.featureStateAt(ctx, subject, asOf, availableAt)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	ontology, err := s.featureOntology(ctx, subject, spec, stateVersion, asOf)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	snapshot, err = mergeFeatureSnapshot(spec, specHash, baseline, active, watermarks, stateVersion, ontology, asOf, availableAt)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	snapshot.Subject = subject
	if err := s.setFutureFeatureChange(ctx, &snapshot); err != nil {
		return FeatureSnapshot{}, err
	}
	if err := freezeFeatureSnapshot(&snapshot); err != nil {
		return FeatureSnapshot{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockFeatureState(ctx, tx, subject, stateVersion); err != nil {
		return FeatureSnapshot{}, err
	}
	headRevision, _, err := featureHeadVersion(ctx, tx, subject)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if (baseline == nil && headRevision != 0) || (baseline != nil && headRevision != baseline.Revision) {
		return FeatureSnapshot{}, fmt.Errorf("%w: baseline changed during feature build", ErrConflict)
	}
	if err := saveFeatureSnapshot(ctx, tx, snapshot); err != nil {
		return FeatureSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FeatureSnapshot{}, err
	}
	return snapshot, nil
}

// ReadyFeatureSnapshot is for consumers D/F. A stale source state or baseline
// head returns Pending, so a previously built value is never silently current.
func (s *Store) ReadyFeatureSnapshot(ctx context.Context, subject SubjectRef) (snapshot FeatureSnapshot, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.features.ready_read", subject)
	if beginErr != nil {
		return FeatureSnapshot{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.valid() {
		return FeatureSnapshot{}, ErrInvalid
	}
	var body []byte
	var stateVersion int64
	var headRevision *int64
	var ontologyVersion *int64
	err = s.db.QueryRow(ctx, `SELECT f.snapshot_body,st.state_version,h.revision,oh.definition_version
		FROM usermodel_feature_snapshots f JOIN usermodel_subject_state st USING(authority_id,tenant_id,subject_id)
		LEFT JOIN usermodel_feature_heads h USING(authority_id,tenant_id,subject_id)
		LEFT JOIN usermodel_ontology_heads oh ON oh.authority_id=f.authority_id AND oh.tenant_id=f.tenant_id
		WHERE f.authority_id=$1 AND f.tenant_id=$2 AND f.subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&body, &stateVersion, &headRevision, &ontologyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeatureSnapshot{}, ErrPending
	}
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return FeatureSnapshot{}, err
	}
	if snapshot.Subject != subject || snapshot.StateVersion != stateVersion ||
		(headRevision == nil && snapshot.BaselineState != "absent") ||
		(headRevision != nil && (snapshot.BaselineState != "accepted" || snapshot.Revision != *headRevision)) ||
		(snapshot.OntologyVersion > 0 && (ontologyVersion == nil || snapshot.OntologyVersion != *ontologyVersion)) ||
		(snapshot.NextChangeAt != nil && !time.Now().UTC().Before(*snapshot.NextChangeAt)) {
		return FeatureSnapshot{}, ErrPending
	}
	return snapshot, nil
}

// FeatureSnapshotByID reads an immutable input retained by an actual request
// or training sample. It does not imply that this version remains current.
func (s *Store) FeatureSnapshotByID(ctx context.Context, subject SubjectRef, id string) (snapshot FeatureSnapshot, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.features.version_read", subject)
	if beginErr != nil {
		return FeatureSnapshot{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.valid() || !digest.MatchString(id) {
		return FeatureSnapshot{}, ErrInvalid
	}
	var body []byte
	err = s.db.QueryRow(ctx, `SELECT snapshot_body FROM usermodel_feature_snapshot_versions
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND snapshot_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, id).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeatureSnapshot{}, ErrNotFound
	}
	if err != nil {
		return FeatureSnapshot{}, err
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return FeatureSnapshot{}, err
	}
	if snapshot.Subject != subject || snapshot.ID != id {
		return FeatureSnapshot{}, ErrConflict
	}
	copy := snapshot
	copy.ID = ""
	check, err := json.Marshal(copy)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	sum := sha256.Sum256(check)
	if hex.EncodeToString(sum[:]) != id {
		return FeatureSnapshot{}, ErrConflict
	}
	return snapshot, nil
}
