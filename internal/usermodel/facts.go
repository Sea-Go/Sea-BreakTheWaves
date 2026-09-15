// Package usermodel owns accepted semantic user facts. Raw client observations
// remain in the DC/warehouse stream; a PG ACK here only proves domain commit.
package usermodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalid = errors.New("invalid user fact")
var ErrConflict = errors.New("user fact conflict")
var ErrNotFound = errors.New("user fact not found")
var ErrPending = errors.New("user fact dependency pending")

var token = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,191}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type SubjectRef struct {
	AuthorityID string `json:"authority_id"`
	TenantID    string `json:"tenant_id"`
	SubjectID   string `json:"subject_id"`
}

func (s SubjectRef) valid() bool {
	return token.MatchString(s.AuthorityID) && token.MatchString(s.TenantID) && token.MatchString(s.SubjectID)
}

type EventKey struct {
	Producer string `json:"producer"`
	EventID  string `json:"event_id"`
}

func (k EventKey) valid() bool { return token.MatchString(k.Producer) && token.MatchString(k.EventID) }

type Action string

const (
	Assert  Action = "assert"
	Correct Action = "correct"
	Retract Action = "retract"
)

type SemanticKind string

const (
	ProductAction SemanticKind = "product_action"
	Reading       SemanticKind = "reading"
	Impression    SemanticKind = "impression"
	LocalSemantic SemanticKind = "local_semantic"
	SelfReport    SemanticKind = "self_report"
)

// Event contains source evidence and an explicit semantic action. A reading
// without an impression remains a reading; no synthetic exposure is emitted.
// The producer is bound by the upstream adapter, never selected by a model.
type Event struct {
	Subject SubjectRef `json:"subject_ref"`
	EventKey
	Action                Action       `json:"action"`
	Kind                  SemanticKind `json:"kind"`
	Predicate             string       `json:"predicate"`
	ValueRef              string       `json:"value_ref,omitempty"`
	EvidenceRef           string       `json:"evidence_ref"`
	EvidenceHash          string       `json:"evidence_hash"`
	RuleVersion           string       `json:"rule_version,omitempty"`
	OccurredAt            time.Time    `json:"occurred_at"`
	ObservedAt            time.Time    `json:"observed_at"`
	SourcePartition       string       `json:"source_partition"`
	SourceSequence        int64        `json:"source_sequence,omitempty"`
	Supersedes            *EventKey    `json:"supersedes,omitempty"`
	ItemID                string       `json:"item_id,omitempty"`
	RequestID             string       `json:"request_id,omitempty"`
	SlateID               string       `json:"slate_id,omitempty"`
	ImpressionID          string       `json:"impression_id,omitempty"`
	VisibilityEvidenceRef string       `json:"visibility_evidence_ref,omitempty"`
}

type Receipt struct {
	Subject SubjectRef `json:"subject_ref"`
	EventKey
	NormalizedHash string `json:"normalized_hash"`
	Status         string `json:"status"`
	StateVersion   int64  `json:"state_version"`
	Replay         bool   `json:"replay"`
}

type UnmappedRef struct {
	AuthorityID       string `json:"authority_id"`
	TenantID          string `json:"tenant_id"`
	ExternalSubjectID string `json:"external_subject_id"`
}

func (r UnmappedRef) valid() bool {
	return token.MatchString(r.AuthorityID) && token.MatchString(r.TenantID) && token.MatchString(r.ExternalSubjectID)
}

type Store struct {
	db          *pgxpool.Pool
	telemetry   *telemetry.Bundle
	v2Candidate bool
}

type StoreOption func(*Store)

// WithSubjectRefV2Candidate enables the additive fact-ledger projection only
// for a deployment that has explicitly applied migration 007 and passed the
// old-row preflight. Existing constructors and callers remain v1 by default.
func WithSubjectRefV2Candidate() StoreOption {
	return func(s *Store) { s.v2Candidate = true }
}

// NewStore borrows the pool and process telemetry. Pass a nonnil Bundle from
// the tRPC Runner assembly for runtime observability; tests can omit it.
func NewStore(db *pgxpool.Pool, bundle *telemetry.Bundle, opts ...StoreOption) *Store {
	s := &Store{db: db, telemetry: bundle}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func normalized(e Event, requireSubject bool) (Event, string, []byte, error) {
	if requireSubject && !e.Subject.valid() {
		return Event{}, "", nil, fmt.Errorf("%w: complete SubjectRef required", ErrInvalid)
	}
	if !e.EventKey.valid() || !token.MatchString(e.SourcePartition) || !token.MatchString(e.Predicate) ||
		!token.MatchString(e.EvidenceRef) || !digest.MatchString(e.EvidenceHash) || e.OccurredAt.IsZero() || e.ObservedAt.IsZero() || e.SourceSequence < 0 {
		return Event{}, "", nil, fmt.Errorf("%w: event identity, evidence, time or position", ErrInvalid)
	}
	if e.ValueRef != "" && !token.MatchString(e.ValueRef) {
		return Event{}, "", nil, fmt.Errorf("%w: value reference", ErrInvalid)
	}
	if e.Action != Assert && e.Action != Correct && e.Action != Retract {
		return Event{}, "", nil, fmt.Errorf("%w: action", ErrInvalid)
	}
	if (e.Action == Assert) != (e.Supersedes == nil) || (e.Supersedes != nil && (!e.Supersedes.valid() || *e.Supersedes == e.EventKey)) {
		return Event{}, "", nil, fmt.Errorf("%w: predecessor", ErrInvalid)
	}
	if e.Action != Retract {
		switch e.Kind {
		case ProductAction, Reading, Impression, LocalSemantic, SelfReport:
		default:
			return Event{}, "", nil, fmt.Errorf("%w: semantic kind", ErrInvalid)
		}
		if e.ValueRef == "" {
			return Event{}, "", nil, fmt.Errorf("%w: semantic value reference", ErrInvalid)
		}
	}
	if e.Kind == Impression && e.Action != Retract {
		if !token.MatchString(e.ImpressionID) || !token.MatchString(e.ItemID) || !token.MatchString(e.VisibilityEvidenceRef) {
			return Event{}, "", nil, fmt.Errorf("%w: actual impression evidence required", ErrInvalid)
		}
	}
	if e.Kind != Impression && e.ImpressionID != "" {
		return Event{}, "", nil, fmt.Errorf("%w: impression ID requires actual impression fact", ErrInvalid)
	}
	if e.Kind == Reading && e.Action != Retract && !token.MatchString(e.ItemID) {
		return Event{}, "", nil, fmt.Errorf("%w: reading item", ErrInvalid)
	}
	if e.Kind == LocalSemantic && e.Action != Retract && !token.MatchString(e.RuleVersion) {
		return Event{}, "", nil, fmt.Errorf("%w: local semantic rule", ErrInvalid)
	}
	for _, optional := range []string{e.RuleVersion, e.ItemID, e.RequestID, e.SlateID, e.ImpressionID, e.VisibilityEvidenceRef} {
		if optional != "" && !token.MatchString(optional) {
			return Event{}, "", nil, fmt.Errorf("%w: event reference", ErrInvalid)
		}
	}
	e.OccurredAt = e.OccurredAt.UTC()
	e.ObservedAt = e.ObservedAt.UTC()
	// SubjectRef is the primary-key scope, not source payload. Clearing it makes
	// the parked pre-mapping payload hash identical after trusted identity bind.
	withoutSubject := e
	withoutSubject.Subject = SubjectRef{}
	body, err := json.Marshal(withoutSubject)
	if err != nil {
		return Event{}, "", nil, err
	}
	sum := sha256.Sum256(body)
	return e, hex.EncodeToString(sum[:]), body, nil
}

func dbErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: unique event or source position", ErrConflict)
	}
	return err
}

func (s *Store) begin(ctx context.Context, event string, key EventKey, subject SubjectRef) (context.Context, *telemetry.Stage, error) {
	if s.telemetry == nil {
		return ctx, nil, nil
	}
	attrs := []slog.Attr{}
	if key.valid() {
		attrs = append(attrs, slog.String("event_id", key.EventID), slog.String("producer", key.Producer))
	}
	if subject.AuthorityID != "" {
		attrs = append(attrs, slog.String("authority_id", subject.AuthorityID))
	}
	if subject.TenantID != "" {
		attrs = append(attrs, slog.String("tenant_id", subject.TenantID))
	}
	if subject.SubjectID != "" {
		attrs = append(attrs, slog.String("subject_id", subject.SubjectID))
	}
	ctx, stage, err := s.telemetry.Begin(ctx, "usermodel", event, attrs...)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, stage, nil
}

func (s *Store) beginSubject(ctx context.Context, event string, subject SubjectRef) (context.Context, *telemetry.Stage, error) {
	return s.begin(ctx, event, EventKey{}, subject)
}

func finish(ctx context.Context, stage *telemetry.Stage, err error, replay bool, status string) {
	if stage == nil {
		return
	}
	outcome, code := "succeeded", ""
	if err == nil && (status == "pending_subject" || status == "pending_dependency") {
		outcome = "partial"
	}
	if err != nil {
		outcome, code = "failed", "STORE_ERROR"
		switch {
		case errors.Is(err, ErrInvalid):
			outcome, code = "rejected", "INVALID_FACT"
		case errors.Is(err, ErrConflict):
			outcome, code = "rejected", "FACT_CONFLICT"
		case errors.Is(err, ErrPending):
			outcome, code = "partial", "EVIDENCE_PENDING"
		case errors.Is(err, ErrNotFound):
			outcome, code = "rejected", "NOT_FOUND"
		case errors.Is(err, context.Canceled):
			outcome, code = "cancelled", "CANCELLED"
		case errors.Is(err, context.DeadlineExceeded):
			outcome, code = "timed_out", "DEADLINE_EXCEEDED"
		}
	}
	attrs := []slog.Attr{slog.Bool("replay", replay)}
	if status != "" {
		attrs = append(attrs, slog.String("receipt_status", status))
	}
	stage.End(ctx, outcome, code, err, attrs...)
}

// Append atomically admits a semantic fact, state version and Outbox record.
// A replay returns the immutable original receipt, including pending status.
func (s *Store) Append(ctx context.Context, input Event) (receipt Receipt, err error) {
	ctx, stage, beginErr := s.begin(ctx, "usermodel.fact.append", input.EventKey, input.Subject)
	if beginErr != nil {
		return Receipt{}, beginErr
	}
	defer func() { finish(ctx, stage, err, receipt.Replay, receipt.Status) }()
	e, hash, body, err := normalized(input, true)
	if err != nil {
		return Receipt{}, err
	}
	var v2 SubjectRefV2
	if s.v2Candidate {
		v2, err = input.Subject.v2Projection()
		if err != nil {
			return Receipt{}, err
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback(ctx)
	receipt, err = appendTx(ctx, tx, e, hash, body)
	if err != nil {
		return Receipt{}, dbErr(err)
	}
	if s.v2Candidate {
		if err := insertV2Projection(ctx, tx, e.Subject, v2); err != nil {
			return Receipt{}, dbErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Receipt{}, dbErr(err)
	}
	return receipt, nil
}

func appendTx(ctx context.Context, tx pgx.Tx, e Event, hash string, body []byte) (Receipt, error) {
	s := e.Subject
	if _, err := tx.Exec(ctx, `INSERT INTO usermodel_subject_state(authority_id,tenant_id,subject_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, s.AuthorityID, s.TenantID, s.SubjectID); err != nil {
		return Receipt{}, err
	}
	var version int64
	if err := tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`, s.AuthorityID, s.TenantID, s.SubjectID).Scan(&version); err != nil {
		return Receipt{}, err
	}
	var oldHash, oldStatus string
	var oldVersion int64
	err := tx.QueryRow(ctx, `SELECT normalized_hash, initial_status, initial_version FROM usermodel_events
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
		s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.EventID).Scan(&oldHash, &oldStatus, &oldVersion)
	if err == nil {
		if oldHash != hash {
			return Receipt{}, fmt.Errorf("%w: same event key with different normalized payload", ErrConflict)
		}
		return Receipt{Subject: s, EventKey: e.EventKey, NormalizedHash: hash, Status: oldStatus, StateVersion: oldVersion, Replay: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, err
	}
	status := "accepted"
	var predecessorProducer, predecessorID any
	if e.Supersedes != nil {
		cycle, cycleErr := wouldCreateDependencyCycle(ctx, tx, e)
		if cycleErr != nil {
			return Receipt{}, cycleErr
		}
		if cycle {
			return Receipt{}, fmt.Errorf("%w: cyclic predecessor chain", ErrConflict)
		}
		predecessorProducer, predecessorID = e.Supersedes.Producer, e.Supersedes.EventID
		var targetStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Supersedes.Producer, e.Supersedes.EventID).Scan(&targetStatus)
		if errors.Is(err, pgx.ErrNoRows) || targetStatus == "pending_dependency" {
			status = "pending_dependency"
		} else if err != nil {
			return Receipt{}, err
		}
	}
	initialVersion := version
	if status == "accepted" {
		initialVersion++
	}
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_events
		(authority_id,tenant_id,subject_id,producer,event_id,normalized_hash,event_body,action,semantic_kind,
		 occurred_at,observed_at,source_partition,source_sequence,supersedes_producer,supersedes_event_id,
		 status,initial_status,initial_version,accepted_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,0),$14,$15,$16,$16,$17,$18)`,
		s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.EventID, hash, body, e.Action, e.Kind,
		e.OccurredAt, e.ObservedAt, e.SourcePartition, e.SourceSequence, predecessorProducer, predecessorID,
		status, initialVersion, nullableVersion(status, initialVersion))
	if err != nil {
		return Receipt{}, err
	}
	if status == "accepted" {
		if err := applyAccepted(ctx, tx, e, hash, initialVersion, false); err != nil {
			return Receipt{}, err
		}
		if err := resolveDependents(ctx, tx, s, e.EventKey, initialVersion); err != nil {
			return Receipt{}, err
		}
	} else if err := recordPosition(ctx, tx, e); err != nil {
		return Receipt{}, err
	}
	return Receipt{Subject: s, EventKey: e.EventKey, NormalizedHash: hash, Status: status, StateVersion: initialVersion}, nil
}

// A newly arriving predecessor can close a previously parked chain. UNION
// deduplicates visited rows, so even preexisting corrupt cycles terminate.
func wouldCreateDependencyCycle(ctx context.Context, tx pgx.Tx, e Event) (bool, error) {
	if e.Supersedes == nil {
		return false, nil
	}
	var cycle bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain(producer,event_id,supersedes_producer,supersedes_event_id) AS (
		SELECT producer,event_id,supersedes_producer,supersedes_event_id FROM usermodel_events
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5
		UNION
		SELECT p.producer,p.event_id,p.supersedes_producer,p.supersedes_event_id FROM usermodel_events p
		JOIN chain c ON p.producer=c.supersedes_producer AND p.event_id=c.supersedes_event_id
		WHERE p.authority_id=$1 AND p.tenant_id=$2 AND p.subject_id=$3
	)
	SELECT EXISTS(SELECT 1 FROM chain WHERE (producer=$6 AND event_id=$7)
		OR (supersedes_producer=$6 AND supersedes_event_id=$7))`,
		e.Subject.AuthorityID, e.Subject.TenantID, e.Subject.SubjectID,
		e.Supersedes.Producer, e.Supersedes.EventID, e.Producer, e.EventID).Scan(&cycle)
	return cycle, err
}

func nullableVersion(status string, version int64) any {
	if status != "accepted" {
		return nil
	}
	return version
}

func applyAccepted(ctx context.Context, tx pgx.Tx, e Event, hash string, version int64, promoted bool) error {
	s := e.Subject
	if e.Supersedes != nil {
		var predecessorBody []byte
		if err := tx.QueryRow(ctx, `SELECT event_body FROM usermodel_events WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Supersedes.Producer, e.Supersedes.EventID).Scan(&predecessorBody); err != nil {
			return err
		}
		var predecessor Event
		if err := json.Unmarshal(predecessorBody, &predecessor); err != nil {
			return err
		}
		if predecessor.Kind == Impression {
			if _, err := tx.Exec(ctx, `UPDATE usermodel_attributions SET revoked_version=$4
				WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND impression_id=$5 AND revoked_version IS NULL`,
				s.AuthorityID, s.TenantID, s.SubjectID, version, predecessor.ImpressionID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM usermodel_active_facts WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Supersedes.Producer, e.Supersedes.EventID); err != nil {
			return err
		}
	}
	if e.Action != Retract {
		var impressionID any
		if e.Kind == Impression {
			impressionID = e.ImpressionID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO usermodel_active_facts(authority_id,tenant_id,subject_id,producer,event_id,impression_id,activated_version) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.EventID, impressionID, version); err != nil {
			return err
		}
	}
	if promoted {
		if _, err := tx.Exec(ctx, `UPDATE usermodel_events SET status='accepted',accepted_version=$6 WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
			s.AuthorityID, s.TenantID, s.SubjectID, e.Producer, e.EventID, version); err != nil {
			return err
		}
	}
	if err := advanceWatermark(ctx, tx, e); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"normalized_hash": hash, "action": e.Action,
		"semantic_kind": e.Kind, "evidence_ref": e.EvidenceRef, "source_partition": e.SourcePartition,
		"source_sequence": e.SourceSequence, "accepted_version": version})
	if _, err := tx.Exec(ctx, `INSERT INTO usermodel_outbox(authority_id,tenant_id,subject_id,state_version,event_type,producer,event_id,payload)
		VALUES($1,$2,$3,$4,'usermodel.fact.accepted',$5,$6,$7)`, s.AuthorityID, s.TenantID, s.SubjectID, version, e.Producer, e.EventID, payload); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE usermodel_subject_state SET state_version=$4,updated_at=now() WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		s.AuthorityID, s.TenantID, s.SubjectID, version)
	return err
}

// Resolve at most 128 predecessor links per transaction. A bounded explicit
// Reconcile call can continue an unusually long chain without unbounded locks.
func resolveDependents(ctx context.Context, tx pgx.Tx, subject SubjectRef, predecessor EventKey, version int64) error {
	for i := 0; i < 128; i++ {
		var body []byte
		var hash string
		err := tx.QueryRow(ctx, `SELECT event_body,normalized_hash FROM usermodel_events
			WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND supersedes_producer=$4 AND supersedes_event_id=$5 AND status='pending_dependency'`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID, predecessor.Producer, predecessor.EventID).Scan(&body, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var e Event
		if err := json.Unmarshal(body, &e); err != nil {
			return err
		}
		e.Subject = subject
		version++
		if err := applyAccepted(ctx, tx, e, hash, version, true); err != nil {
			return err
		}
		predecessor = e.EventKey
	}
	return nil
}

// ParkUnmapped preserves a source-scoped envelope when RTW has not resolved
// the subject. It does not create a UserFact, state version or training sample.
func (s *Store) ParkUnmapped(ctx context.Context, ref UnmappedRef, input Event) (receipt Receipt, err error) {
	ctx, stage, beginErr := s.begin(ctx, "usermodel.fact.park", input.EventKey, input.Subject)
	if beginErr != nil {
		return Receipt{}, beginErr
	}
	defer func() { finish(ctx, stage, err, receipt.Replay, receipt.Status) }()
	if !ref.valid() || input.Subject.SubjectID != "" ||
		input.Subject.AuthorityID != ref.AuthorityID || input.Subject.TenantID != ref.TenantID {
		return Receipt{}, ErrInvalid
	}
	_, hash, body, err := normalized(input, false)
	if err != nil {
		return Receipt{}, err
	}
	var existingHash string
	var boundVersion *int64
	err = s.db.QueryRow(ctx, `INSERT INTO usermodel_unmapped_events
		(authority_id,tenant_id,external_subject_id,producer,event_id,normalized_hash,event_body)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING RETURNING normalized_hash,bound_version`,
		ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID, input.Producer, input.EventID, hash, body).
		Scan(&existingHash, &boundVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.db.QueryRow(ctx, `SELECT normalized_hash,bound_version FROM usermodel_unmapped_events
			WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3 AND producer=$4 AND event_id=$5`,
			ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID, input.Producer, input.EventID).Scan(&existingHash, &boundVersion)
		if err == nil {
			receipt.Replay = true
		}
	}
	if err != nil {
		return Receipt{}, dbErr(err)
	}
	if existingHash != hash {
		return Receipt{}, ErrConflict
	}
	receipt.EventKey, receipt.NormalizedHash = input.EventKey, hash
	receipt.Status = "pending_subject"
	return receipt, nil
}

// BindUnmapped consumes a verified RTW SubjectRef and admits the parked event
// in the same transaction that marks its source alias bound. A rival mapping
// for the same alias is rejected rather than merging two users.
func (s *Store) BindUnmapped(ctx context.Context, ref UnmappedRef, key EventKey, subject SubjectRef) (receipt Receipt, err error) {
	ctx, stage, beginErr := s.begin(ctx, "usermodel.fact.bind", key, subject)
	if beginErr != nil {
		return Receipt{}, beginErr
	}
	defer func() { finish(ctx, stage, err, receipt.Replay, receipt.Status) }()
	if !ref.valid() || !key.valid() || !subject.valid() || subject.AuthorityID != ref.AuthorityID || subject.TenantID != ref.TenantID {
		return Receipt{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback(ctx)
	var body []byte
	var hash string
	var boundSubject *string
	var boundVersion *int64
	var boundStatus *string
	err = tx.QueryRow(ctx, `SELECT event_body,normalized_hash,bound_subject_id,bound_version,bound_status FROM usermodel_unmapped_events
		WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3 AND producer=$4 AND event_id=$5 FOR UPDATE`,
		ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID, key.Producer, key.EventID).Scan(&body, &hash, &boundSubject, &boundVersion, &boundStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usermodel_subject_bindings(authority_id,tenant_id,external_subject_id,subject_id)
		VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID, subject.SubjectID); err != nil {
		return Receipt{}, err
	}
	var mappedSubject string
	if err := tx.QueryRow(ctx, `SELECT subject_id FROM usermodel_subject_bindings
		WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3 FOR UPDATE`,
		ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID).Scan(&mappedSubject); err != nil {
		return Receipt{}, err
	}
	if mappedSubject != subject.SubjectID {
		return Receipt{}, ErrConflict
	}
	if boundSubject != nil && *boundSubject != subject.SubjectID {
		return Receipt{}, ErrConflict
	}
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return Receipt{}, err
	}
	e.Subject = subject
	if boundSubject == nil {
		receipt, err = appendTx(ctx, tx, e, hash, body)
		if err != nil {
			return Receipt{}, dbErr(err)
		}
		_, err = tx.Exec(ctx, `UPDATE usermodel_unmapped_events SET bound_subject_id=$6,bound_version=$7,bound_status=$8
			WHERE authority_id=$1 AND tenant_id=$2 AND external_subject_id=$3 AND producer=$4 AND event_id=$5`,
			ref.AuthorityID, ref.TenantID, ref.ExternalSubjectID, key.Producer, key.EventID, subject.SubjectID, receipt.StateVersion, receipt.Status)
		if err != nil {
			return Receipt{}, err
		}
	} else {
		receipt = Receipt{Subject: subject, EventKey: key, NormalizedHash: hash, Status: *boundStatus, StateVersion: *boundVersion, Replay: true}
	}
	if err := tx.Commit(ctx); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// LinkImpression adds attribution only after the real impression has been
// verified by its producer. It never creates an impression fact.
func (s *Store) LinkImpression(ctx context.Context, subject SubjectRef, key EventKey, impressionID, evidenceRef string) (version int64, err error) {
	ctx, stage, beginErr := s.begin(ctx, "usermodel.fact.link_impression", key, subject)
	if beginErr != nil {
		return 0, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.valid() || !key.valid() || !token.MatchString(impressionID) || !token.MatchString(evidenceRef) {
		return 0, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	var kind, status, action string
	var behaviorBody []byte
	err = tx.QueryRow(ctx, `SELECT e.semantic_kind,e.status,e.action,e.event_body FROM usermodel_events e
		JOIN usermodel_active_facts af USING(authority_id,tenant_id,subject_id,producer,event_id)
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.producer=$4 AND e.event_id=$5`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, key.Producer, key.EventID).Scan(&kind, &status, &action, &behaviorBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != "accepted" || (kind != string(Reading) && kind != string(ProductAction)) || action == string(Retract) {
		return 0, ErrInvalid
	}
	var behavior Event
	if err := json.Unmarshal(behaviorBody, &behavior); err != nil {
		return 0, err
	}
	if !token.MatchString(behavior.ItemID) || !token.MatchString(behavior.RequestID) {
		return 0, ErrInvalid
	}
	var oldImpression, oldEvidence string
	var oldVersion int64
	var revokedVersion *int64
	err = tx.QueryRow(ctx, `SELECT impression_id,source_evidence_ref,linked_version,revoked_version FROM usermodel_attributions
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND producer=$4 AND event_id=$5`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, key.Producer, key.EventID).Scan(&oldImpression, &oldEvidence, &oldVersion, &revokedVersion)
	if err == nil {
		if oldImpression != impressionID || oldEvidence != evidenceRef {
			return 0, ErrConflict
		}
		if revokedVersion != nil {
			return 0, ErrPending
		}
		return oldVersion, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var actualCount int64
	err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events e
		JOIN usermodel_active_facts af USING(authority_id,tenant_id,subject_id,producer,event_id)
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.semantic_kind='impression'
		AND e.status='accepted' AND e.event_body->>'impression_id'=$4
		AND e.event_body->>'visibility_evidence_ref'=$5 AND e.event_body->>'item_id'=$6
		AND e.event_body->>'request_id'=$7 AND ($8='' OR e.event_body->>'slate_id'=$8)
		AND e.occurred_at <= $9`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, impressionID, evidenceRef,
		behavior.ItemID, behavior.RequestID, behavior.SlateID, behavior.OccurredAt).Scan(&actualCount)
	if err != nil {
		return 0, err
	}
	if actualCount == 0 {
		return 0, ErrPending
	}
	if actualCount != 1 {
		return 0, ErrConflict
	}
	version++
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_attributions(authority_id,tenant_id,subject_id,producer,event_id,impression_id,source_evidence_ref,linked_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, subject.AuthorityID, subject.TenantID, subject.SubjectID, key.Producer, key.EventID, impressionID, evidenceRef, version)
	if err != nil {
		return 0, dbErr(err)
	}
	payload, _ := json.Marshal(map[string]string{"impression_id": impressionID, "source_evidence_ref": evidenceRef})
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_outbox(authority_id,tenant_id,subject_id,state_version,event_type,producer,event_id,payload)
		VALUES($1,$2,$3,$4,'usermodel.attribution.linked',$5,$6,$7)`, subject.AuthorityID, subject.TenantID, subject.SubjectID, version, key.Producer, key.EventID, payload)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `UPDATE usermodel_subject_state SET state_version=$4,updated_at=now() WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, version)
	if err != nil {
		return 0, err
	}
	return version, tx.Commit(ctx)
}
