package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type OntologyPublication struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	Version     int64  `json:"definition_version"`
	Hash        string `json:"definition_hash"`
	Replay      bool   `json:"replay"`
}

// PublishOntology validates and atomically activates the next immutable
// definition. An invalid revision or stale expected head leaves the old head
// active. PendingOntologySubjects is the durable, queryable recompute handoff.
func (s *Store) PublishOntology(ctx context.Context, input OntologyDefinition, expectedVersion int64) (publication OntologyPublication, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.ontology.publish", SubjectRef{AuthorityID: input.AuthorityID, TenantID: input.TenantID})
	if beginErr != nil {
		return OntologyPublication{}, beginErr
	}
	defer func() { finish(ctx, stage, err, publication.Replay, "") }()
	d, hash, body, err := canonicalOntologyDefinition(input)
	if err != nil {
		return OntologyPublication{}, err
	}
	if expectedVersion < 0 {
		return OntologyPublication{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OntologyPublication{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_ontology_definitions
		(authority_id,tenant_id,definition_version,definition_hash,definition_body)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, d.AuthorityID, d.TenantID, d.Version, hash, body)
	if err != nil {
		return OntologyPublication{}, err
	}
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT definition_hash FROM usermodel_ontology_definitions
		WHERE authority_id=$1 AND tenant_id=$2 AND definition_version=$3`,
		d.AuthorityID, d.TenantID, d.Version).Scan(&storedHash)
	if err != nil {
		return OntologyPublication{}, err
	}
	if storedHash != hash {
		return OntologyPublication{}, fmt.Errorf("%w: ontology version body changed", ErrConflict)
	}
	// The initial INSERT also serializes two first publishers for this scope.
	insertHead, err := tx.Exec(ctx, `INSERT INTO usermodel_ontology_heads(authority_id,tenant_id,definition_version)
		VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, d.AuthorityID, d.TenantID, d.Version)
	if err != nil {
		return OntologyPublication{}, err
	}
	var current int64
	err = tx.QueryRow(ctx, `SELECT definition_version FROM usermodel_ontology_heads
		WHERE authority_id=$1 AND tenant_id=$2 FOR UPDATE`, d.AuthorityID, d.TenantID).Scan(&current)
	if err != nil {
		return OntologyPublication{}, err
	}
	publication = OntologyPublication{AuthorityID: d.AuthorityID, TenantID: d.TenantID, Version: d.Version, Hash: hash}
	if insertHead.RowsAffected() == 1 {
		if expectedVersion != 0 || d.Version != 1 {
			return OntologyPublication{}, fmt.Errorf("%w: initial ontology revision must be 1 with empty expected head", ErrConflict)
		}
	} else if current == d.Version {
		publication.Replay = true
	} else {
		if current != expectedVersion || d.Version != current+1 {
			return OntologyPublication{}, fmt.Errorf("%w: stale or nonconsecutive ontology revision", ErrConflict)
		}
		_, err = tx.Exec(ctx, `UPDATE usermodel_ontology_heads SET definition_version=$3,activated_at=now()
			WHERE authority_id=$1 AND tenant_id=$2`, d.AuthorityID, d.TenantID, d.Version)
		if err != nil {
			return OntologyPublication{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OntologyPublication{}, err
	}
	return publication, nil
}

// PendingOntologySubjects pages subjects whose fact version, active definition
// or declared temporal boundary is ahead of the materialized projection. The
// list can be regenerated after worker crashes; it is not a delivery receipt.
func (s *Store) PendingOntologySubjects(ctx context.Context, authorityID, tenantID, afterSubjectID string, asOf time.Time, limit int) (subjects []SubjectRef, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.ontology.pending", SubjectRef{AuthorityID: authorityID, TenantID: tenantID})
	if beginErr != nil {
		return nil, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !token.MatchString(authorityID) || !token.MatchString(tenantID) ||
		(afterSubjectID != "" && !token.MatchString(afterSubjectID)) || asOf.IsZero() || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT st.subject_id FROM usermodel_subject_state st
		JOIN usermodel_ontology_heads h ON h.authority_id=st.authority_id AND h.tenant_id=st.tenant_id
		LEFT JOIN usermodel_ontology_projections p ON p.authority_id=st.authority_id AND p.tenant_id=st.tenant_id AND p.subject_id=st.subject_id
		WHERE st.authority_id=$1 AND st.tenant_id=$2 AND st.subject_id>$3
		AND (p.subject_id IS NULL OR p.definition_version<>h.definition_version OR p.state_version<>st.state_version
			OR (p.next_change_at IS NOT NULL AND p.next_change_at<=$4))
		ORDER BY st.subject_id LIMIT $5`, authorityID, tenantID, afterSubjectID, asOf.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SubjectRef{}
	for rows.Next() {
		ref := SubjectRef{AuthorityID: authorityID, TenantID: tenantID}
		if err := rows.Scan(&ref.SubjectID); err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

// RebuildOntology pins the active definition and subject state in one PG
// transaction, reads the fact ledger's repeatable snapshot, and replaces only
// the rebuildable projection. The returned impact is derived from the old and
// new evidence, so a withdrawal identifies removed objects as well as values.
func (s *Store) RebuildOntology(ctx context.Context, subject SubjectRef, asOf time.Time) (projection OntologyProjection, impact OntologyImpact, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.ontology.rebuild", subject)
	if beginErr != nil {
		return OntologyProjection{}, OntologyImpact{}, beginErr
	}
	defer func() { finish(ctx, stage, err, impact.Replay, "") }()
	if !subject.Valid() || asOf.IsZero() {
		return OntologyProjection{}, OntologyImpact{}, ErrInvalid
	}
	consistentAt := asOf.UTC()
	// Read before taking a writer lock so even a one-connection pool can run
	// this use case. A later version comparison rejects a concurrent append.
	facts, err := s.Current(ctx, subject)
	if err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	defer tx.Rollback(ctx)
	var body []byte
	err = tx.QueryRow(ctx, `SELECT d.definition_body FROM usermodel_ontology_heads h
		JOIN usermodel_ontology_definitions d USING(authority_id,tenant_id,definition_version)
		WHERE h.authority_id=$1 AND h.tenant_id=$2 FOR SHARE OF h`,
		subject.AuthorityID, subject.TenantID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return OntologyProjection{}, OntologyImpact{}, ErrNotFound
	}
	if err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	var definition OntologyDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	var stateVersion int64
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&stateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return OntologyProjection{}, OntologyImpact{}, ErrNotFound
	}
	if err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	if facts.StateVersion != stateVersion {
		return OntologyProjection{}, OntologyImpact{}, fmt.Errorf("%w: source version changed during ontology rebuild", ErrConflict)
	}
	projection, err = BuildOntologyProjection(definition, facts, consistentAt)
	if err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	before := OntologyProjection{Subject: subject, Edges: []OntologyEdge{}, Derived: []OntologyDerived{}}
	var previousBody []byte
	err = tx.QueryRow(ctx, `SELECT projection_body FROM usermodel_ontology_projections
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&previousBody)
	if err == nil {
		if err := json.Unmarshal(previousBody, &before); err != nil {
			return OntologyProjection{}, OntologyImpact{}, err
		}
		if consistentAt.Before(before.AsOf) {
			return OntologyProjection{}, OntologyImpact{}, fmt.Errorf("%w: ontology as-of time rewind", ErrConflict)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	replay := len(previousBody) != 0 && before.DefinitionVersion == projection.DefinitionVersion &&
		before.StateVersion == projection.StateVersion && before.AsOf.Equal(projection.AsOf)
	impact = ontologyImpact(before, projection, replay)
	if !replay {
		body, err := json.Marshal(projection)
		if err != nil {
			return OntologyProjection{}, OntologyImpact{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO usermodel_ontology_projections
			(authority_id,tenant_id,subject_id,definition_version,state_version,as_of,next_change_at,projection_body)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(authority_id,tenant_id,subject_id) DO UPDATE SET
			definition_version=EXCLUDED.definition_version,state_version=EXCLUDED.state_version,
			as_of=EXCLUDED.as_of,next_change_at=EXCLUDED.next_change_at,
			projection_body=EXCLUDED.projection_body,rebuilt_at=now()`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID, projection.DefinitionVersion,
			projection.StateVersion, projection.AsOf, projection.NextChangeAt, body)
		if err != nil {
			return OntologyProjection{}, OntologyImpact{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OntologyProjection{}, OntologyImpact{}, err
	}
	return projection, impact, nil
}

// StoredOntologyProjection exposes the persisted snapshot with explicit pinned
// versions. A caller must compare them with the current fact and schema heads.
func (s *Store) StoredOntologyProjection(ctx context.Context, subject SubjectRef) (projection OntologyProjection, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.ontology.read", subject)
	if beginErr != nil {
		return OntologyProjection{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() {
		return OntologyProjection{}, ErrInvalid
	}
	var body []byte
	err = s.db.QueryRow(ctx, `SELECT projection_body FROM usermodel_ontology_projections
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return OntologyProjection{}, ErrNotFound
	}
	if err != nil {
		return OntologyProjection{}, err
	}
	if err := json.Unmarshal(body, &projection); err != nil {
		return OntologyProjection{}, err
	}
	if projection.Subject != subject {
		return OntologyProjection{}, fmt.Errorf("%w: ontology projection subject scope", ErrConflict)
	}
	return projection, nil
}

// ReadyOntologyProjection fails closed when the snapshot is behind either
// authoritative version or a declared time boundary. WS08-C may consume only
// this result (or do its own equivalent pinned-version comparison).
func (s *Store) ReadyOntologyProjection(ctx context.Context, subject SubjectRef, asOf time.Time) (projection OntologyProjection, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.ontology.ready_read", subject)
	if beginErr != nil {
		return OntologyProjection{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || asOf.IsZero() {
		return OntologyProjection{}, ErrInvalid
	}
	var body []byte
	err = s.db.QueryRow(ctx, `SELECT p.projection_body FROM usermodel_subject_state st
		JOIN usermodel_ontology_heads h ON h.authority_id=st.authority_id AND h.tenant_id=st.tenant_id
		JOIN usermodel_ontology_projections p ON p.authority_id=st.authority_id AND p.tenant_id=st.tenant_id AND p.subject_id=st.subject_id
		WHERE st.authority_id=$1 AND st.tenant_id=$2 AND st.subject_id=$3
		AND p.definition_version=h.definition_version AND p.state_version=st.state_version
		AND p.as_of<=$4 AND (p.next_change_at IS NULL OR p.next_change_at>$4)`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, asOf.UTC()).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return OntologyProjection{}, ErrPending
	}
	if err != nil {
		return OntologyProjection{}, err
	}
	if err := json.Unmarshal(body, &projection); err != nil {
		return OntologyProjection{}, err
	}
	if projection.Subject != subject {
		return OntologyProjection{}, fmt.Errorf("%w: ontology projection subject scope", ErrConflict)
	}
	return projection, nil
}
