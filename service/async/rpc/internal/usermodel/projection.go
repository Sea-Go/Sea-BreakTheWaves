package usermodel

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type Fact struct {
	Event
	NormalizedHash   string `json:"normalized_hash"`
	Status           string `json:"status"`
	AcceptedVersion  int64  `json:"accepted_version"`
	ImpressionLinked bool   `json:"impression_linked"`
	LinkedImpression string `json:"linked_impression_id,omitempty"`
}

type Projection struct {
	Subject           SubjectRef  `json:"subject_ref"`
	StateVersion      int64       `json:"state_version"`
	ProjectionVersion int64       `json:"projection_version"`
	Active            []Fact      `json:"active_facts"`
	Watermarks        []Watermark `json:"watermarks"`
	Pending           int64       `json:"pending_dependency_count"`
}

type OutboxRecord struct {
	ID           int64      `json:"outbox_id"`
	Subject      SubjectRef `json:"subject_ref"`
	StateVersion int64      `json:"state_version"`
	EventType    string     `json:"event_type"`
	EventKey
	Payload json.RawMessage `json:"payload"`
}

type HistoryCursor struct {
	OccurredAt time.Time `json:"occurred_at"`
	Producer   string    `json:"producer"`
	EventID    string    `json:"event_id"`
}

// advanceWatermark records the highest accepted contiguous source sequence.
// Position origin is 1; until the source declares a compatible origin, a gap
// stays visible and downstream must not claim a complete baseline.
func advanceWatermark(ctx context.Context, tx pgx.Tx, e Event) error {
	if err := recordPosition(ctx, tx, e); err != nil {
		return err
	}
	if e.SourceSequence == 0 {
		return nil
	}
	s := e.Subject
	var contiguous, maxSeen int64
	err := tx.QueryRow(ctx, `SELECT contiguous_sequence,max_seen_sequence FROM usermodel_watermarks
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND source_partition=$5 FOR UPDATE`,
		s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.SourcePartition).Scan(&contiguous, &maxSeen)
	if err != nil {
		return err
	}
	for contiguous < maxSeen {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usermodel_events
			WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND source_partition=$5
			AND source_sequence=$6 AND status='accepted')`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.SourcePartition, contiguous+1).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			break
		}
		contiguous++
	}
	_, err = tx.Exec(ctx, `UPDATE usermodel_watermarks SET contiguous_sequence=$6
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND source_partition=$5`,
		s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.SourcePartition, contiguous)
	return err
}

func recordPosition(ctx context.Context, tx pgx.Tx, e Event) error {
	if e.SourceSequence == 0 {
		return nil
	}
	s := e.Subject
	_, err := tx.Exec(ctx, `INSERT INTO usermodel_watermarks
		(authority_id,tenant_id,subject_id,producer,source_partition,max_seen_sequence)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(authority_id,tenant_id,subject_id,producer,source_partition)
		DO UPDATE SET max_seen_sequence=GREATEST(usermodel_watermarks.max_seen_sequence,EXCLUDED.max_seen_sequence)`,
		s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.SourcePartition, e.SourceSequence)
	if err != nil {
		return err
	}
	return nil
}

// Current returns a repeatable-read snapshot. Consumers B/C must pin its
// state version and each source watermark, rather than using arrival order.
func (s *Store) Current(ctx context.Context, subject SubjectRef) (projection Projection, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.projection.read", subject)
	if beginErr != nil {
		return Projection{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() {
		return Projection{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Projection{}, err
	}
	defer tx.Rollback(ctx)
	p := Projection{Subject: subject, Active: []Fact{}, Watermarks: []Watermark{}}
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&p.StateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Projection{}, ErrNotFound
	}
	if err != nil {
		return Projection{}, err
	}
	p.ProjectionVersion = p.StateVersion
	rows, err := tx.Query(ctx, `SELECT e.event_body,e.normalized_hash,e.status,e.accepted_version,
		COALESCE(a.impression_id,'') FROM usermodel_active_facts af
		JOIN usermodel_events e USING(authority_id,tenant_id,subject_id,producer,event_id)
		LEFT JOIN usermodel_attributions a ON a.authority_id=e.authority_id AND a.tenant_id=e.tenant_id AND a.subject_id=e.subject_id
		AND a.producer=e.producer AND a.event_id=e.event_id AND a.revoked_version IS NULL
		WHERE af.authority_id=$1 AND af.tenant_id=$2 AND af.subject_id=$3
		ORDER BY e.occurred_at,e.producer,e.event_id`, subject.AuthorityID, subject.TenantID, subject.SubjectID)
	if err != nil {
		return Projection{}, err
	}
	for rows.Next() {
		var f Fact
		var body []byte
		if err := rows.Scan(&body, &f.NormalizedHash, &f.Status, &f.AcceptedVersion, &f.LinkedImpression); err != nil {
			rows.Close()
			return Projection{}, err
		}
		if err := json.Unmarshal(body, &f.Event); err != nil {
			rows.Close()
			return Projection{}, err
		}
		f.Subject = subject
		f.ImpressionLinked = f.LinkedImpression != ""
		p.Active = append(p.Active, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Projection{}, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT producer,source_partition,contiguous_sequence,max_seen_sequence
		FROM usermodel_watermarks WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 ORDER BY producer,source_partition`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID)
	if err != nil {
		return Projection{}, err
	}
	for rows.Next() {
		var w Watermark
		if err := rows.Scan(&w.Producer, &w.SourcePartition, &w.ContiguousSequence, &w.MaxSeenSequence); err != nil {
			rows.Close()
			return Projection{}, err
		}
		w.Complete = w.ContiguousSequence == w.MaxSeenSequence
		p.Watermarks = append(p.Watermarks, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Projection{}, err
	}
	rows.Close()
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND status='pending_dependency'`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&p.Pending); err != nil {
		return Projection{}, err
	}
	return p, tx.Commit(ctx)
}

// History returns immutable source envelopes plus their current acceptance
// status. A correction does not erase its predecessor from this read.
func (s *Store) History(ctx context.Context, subject SubjectRef, limit int) ([]Fact, error) {
	facts, _, err := s.HistoryAfter(ctx, subject, HistoryCursor{}, limit)
	return facts, err
}

// HistoryAfter uses a stable keyset cursor so WS07 can traverse all history
// without replacing immutable prior rows with a current-state shortcut.
func (s *Store) HistoryAfter(ctx context.Context, subject SubjectRef, after HistoryCursor, limit int) (facts []Fact, next HistoryCursor, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.history.read", subject)
	if beginErr != nil {
		return nil, HistoryCursor{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || limit < 1 || limit > 1000 ||
		(!after.OccurredAt.IsZero() && (!token.MatchString(after.Producer) || !token.MatchString(after.EventID))) {
		return nil, HistoryCursor{}, ErrInvalid
	}
	if after.OccurredAt.IsZero() && (after.Producer != "" || after.EventID != "") {
		return nil, HistoryCursor{}, ErrInvalid
	}
	if after.OccurredAt.IsZero() {
		after.OccurredAt = time.Time{}
	}
	rows, err := s.db.Query(ctx, `SELECT event_body,normalized_hash,status,COALESCE(accepted_version,0)
		FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3
		AND (occurred_at,producer,event_id)>($4,$5,$6)
		ORDER BY occurred_at,producer,event_id LIMIT $7`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, after.OccurredAt, after.Producer, after.EventID, limit)
	if err != nil {
		return nil, HistoryCursor{}, err
	}
	defer rows.Close()
	result := []Fact{}
	for rows.Next() {
		var f Fact
		var body []byte
		if err := rows.Scan(&body, &f.NormalizedHash, &f.Status, &f.AcceptedVersion); err != nil {
			return nil, HistoryCursor{}, err
		}
		if err := json.Unmarshal(body, &f.Event); err != nil {
			return nil, HistoryCursor{}, err
		}
		f.Subject = subject
		result = append(result, f)
	}
	if err := rows.Err(); err != nil {
		return nil, HistoryCursor{}, err
	}
	if len(result) == 0 {
		return result, HistoryCursor{}, nil
	}
	last := result[len(result)-1]
	return result, HistoryCursor{OccurredAt: last.OccurredAt, Producer: last.Producer, EventID: last.EventID}, nil
}

// OutboxAfter is the B/C and warehouse handoff. Each record carries a fixed
// subject state version and evidence reference; delivery has its own receipt.
func (s *Store) OutboxAfter(ctx context.Context, subject SubjectRef, afterVersion int64, limit int) (records []OutboxRecord, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.outbox.read", subject)
	if beginErr != nil {
		return nil, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || afterVersion < 0 || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT outbox_id,state_version,event_type,producer,event_id,payload
		FROM usermodel_outbox WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND state_version>$4
		ORDER BY state_version LIMIT $5`, subject.AuthorityID, subject.TenantID, subject.SubjectID, afterVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OutboxRecord{}
	for rows.Next() {
		v := OutboxRecord{Subject: subject}
		if err := rows.Scan(&v.ID, &v.StateVersion, &v.EventType, &v.Producer, &v.EventID, &v.Payload); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// ReconcilePending is an idempotent recovery entry after a missing predecessor
// arrives or an interrupted worker restarts. It does not infer a predecessor.
func (s *Store) ReconcilePending(ctx context.Context, subject SubjectRef) (countResult int, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.pending.reconcile", subject)
	if beginErr != nil {
		return 0, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() {
		return 0, ErrInvalid
	}
	var v2 SubjectRefV2
	if s.v2Candidate {
		var err error
		v2, err = subjectV2Projection(subject)
		if err != nil {
			return 0, err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var version int64
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if s.v2Candidate {
		if err := insertV2Projection(ctx, tx, subject, v2); err != nil {
			return 0, dbErr(err)
		}
	}
	rows, err := tx.Query(ctx, `SELECT e.event_body,e.normalized_hash FROM usermodel_events e
		JOIN usermodel_events p ON p.authority_id=e.authority_id AND p.tenant_id=e.tenant_id AND p.subject_id=e.subject_id
		AND p.producer=e.supersedes_producer AND p.event_id=e.supersedes_event_id AND p.status='accepted'
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.status='pending_dependency'
		ORDER BY e.occurred_at,e.producer,e.event_id LIMIT 128`, subject.AuthorityID, subject.TenantID, subject.SubjectID)
	if err != nil {
		return 0, err
	}
	type pending struct {
		body []byte
		hash string
	}
	var ready []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.body, &p.hash); err != nil {
			rows.Close()
			return 0, err
		}
		ready = append(ready, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	count := 0
	for _, p := range ready {
		var e Event
		if err := json.Unmarshal(p.body, &e); err != nil {
			return 0, err
		}
		e.Subject = subject
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID, e.Producer, e.EventID).Scan(&status); err != nil {
			return 0, err
		}
		if status == "accepted" {
			continue
		}
		version++
		if err := applyAccepted(ctx, tx, e, p.hash, version, true); err != nil {
			return 0, err
		}
		count++
		if err := resolveDependents(ctx, tx, subject, e.EventKey, version); err != nil {
			return 0, err
		}
		// resolveDependents may advance the state beyond our local counter.
		if err := tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&version); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

// ReplayOrder respects each source's sequence first. It then chooses the
// earliest available source head by event time and stable key. Cross-source
// clock skew can still change business interpretation; consumers pin a cutoff.
func ReplayOrder(facts []Fact) []Fact {
	groups := make(map[string][]Fact)
	keys := []string{}
	for _, fact := range facts {
		key := fact.Producer + "\x00" + fact.SourcePartition
		if _, exists := groups[key]; !exists {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], fact)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		sort.Slice(group, func(i, j int) bool {
			a, b := group[i], group[j]
			if a.SourceSequence > 0 && b.SourceSequence > 0 && a.SourceSequence != b.SourceSequence {
				return a.SourceSequence < b.SourceSequence
			}
			if (a.SourceSequence > 0) != (b.SourceSequence > 0) {
				return a.SourceSequence > 0
			}
			if !a.OccurredAt.Equal(b.OccurredAt) {
				return a.OccurredAt.Before(b.OccurredAt)
			}
			return a.EventID < b.EventID
		})
		groups[key] = group
	}
	result := make([]Fact, 0, len(facts))
	positions := make(map[string]int, len(keys))
	for len(result) < len(facts) {
		best := ""
		for _, key := range keys {
			if positions[key] >= len(groups[key]) {
				continue
			}
			if best == "" {
				best = key
				continue
			}
			a, b := groups[key][positions[key]], groups[best][positions[best]]
			if a.OccurredAt.Before(b.OccurredAt) || (a.OccurredAt.Equal(b.OccurredAt) && key < best) {
				best = key
			}
		}
		result = append(result, groups[best][positions[best]])
		positions[best]++
	}
	return result
}

func (s *Store) CountEvidenceGaps(ctx context.Context, subject SubjectRef) (gaps map[string]int64, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.evidence_gaps.read", subject)
	if beginErr != nil {
		return nil, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() {
		return nil, ErrInvalid
	}
	result := map[string]int64{}
	var count int64
	err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events e
		JOIN usermodel_active_facts af USING(authority_id,tenant_id,subject_id,producer,event_id)
		LEFT JOIN usermodel_attributions a ON a.authority_id=e.authority_id AND a.tenant_id=e.tenant_id AND a.subject_id=e.subject_id
		AND a.producer=e.producer AND a.event_id=e.event_id AND a.revoked_version IS NULL
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.status='accepted'
		AND e.semantic_kind IN ('reading','product_action') AND a.event_id IS NULL`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&count)
	if err != nil {
		return nil, err
	}
	result["unattributed_reading_or_action"] = count
	err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND status='pending_dependency'`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&count)
	if err != nil {
		return nil, err
	}
	result["pending_dependency"] = count
	return result, nil
}
