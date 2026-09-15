package usermodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const subjectRefV2Issuer = "rtw.identity"
const subjectRefV1Slot = "platform"

var ErrSubjectRefV2Disabled = errors.New("SubjectRef v2 candidate disabled")

// SubjectRefV2 is the candidate storage identity. RTW UserCenter remains its
// only issuer; the historical platform slot is not part of this type.
type SubjectRefV2 struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

// UnmarshalJSON is strict even when a caller decodes this type outside the
// signed RTW audience boundary. Unknown, duplicated or missing fields cannot
// silently turn an old three-part identity into a v2 identity.
func (v *SubjectRefV2) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return fmt.Errorf("%w: SubjectRef v2 must be an object", ErrInvalid)
	}
	seen := map[string]bool{}
	var candidate SubjectRefV2
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%w: invalid SubjectRef v2 field", ErrInvalid)
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return fmt.Errorf("%w: repeated SubjectRef v2 field", ErrInvalid)
		}
		seen[name] = true
		switch name {
		case "issuer":
			if err := decoder.Decode(&candidate.Issuer); err != nil {
				return fmt.Errorf("%w: issuer must be a string", ErrInvalid)
			}
		case "subject_id":
			if err := decoder.Decode(&candidate.SubjectID); err != nil {
				return fmt.Errorf("%w: subject_id must be a string", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: unknown SubjectRef v2 field", ErrInvalid)
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return fmt.Errorf("%w: incomplete SubjectRef v2", ErrInvalid)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing SubjectRef v2 data", ErrInvalid)
	}
	if !seen["issuer"] || !seen["subject_id"] {
		return fmt.Errorf("%w: missing SubjectRef v2 field", ErrInvalid)
	}
	if _, err := candidate.legacy(); err != nil {
		return err
	}
	*v = candidate
	return nil
}

func canonicalUID(uid string) bool {
	n, err := strconv.ParseInt(uid, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == uid
}

func (v SubjectRefV2) legacy() (SubjectRef, error) {
	if v.Issuer != subjectRefV2Issuer || !canonicalUID(v.SubjectID) {
		return SubjectRef{}, fmt.Errorf("%w: noncanonical SubjectRef v2", ErrInvalid)
	}
	return SubjectRef{AuthorityID: v.Issuer, TenantID: subjectRefV1Slot, SubjectID: v.SubjectID}, nil
}

func (s SubjectRef) v2Projection() (SubjectRefV2, error) {
	if s.AuthorityID != subjectRefV2Issuer || s.TenantID != subjectRefV1Slot || !canonicalUID(s.SubjectID) {
		return SubjectRefV2{}, fmt.Errorf("%w: incompatible legacy SubjectRef", ErrInvalid)
	}
	return SubjectRefV2{Issuer: s.AuthorityID, SubjectID: s.SubjectID}, nil
}

func (s *Store) beginV2(ctx context.Context, event string, v2 SubjectRefV2) (context.Context, *telemetry.Stage, error) {
	if s.telemetry == nil {
		return ctx, nil, nil
	}
	return s.telemetry.Begin(ctx, "usermodel", event,
		slog.String("issuer", v2.Issuer),
		slog.String("subject_id", v2.SubjectID),
		slog.String("subject_ref_version", "v2"))
}

func insertV2Projection(ctx context.Context, tx pgx.Tx, old SubjectRef, v2 SubjectRefV2) error {
	// Append, binding, recovery and explicit backfill all acquire the parent
	// subject lock before the sidecar uniqueness/FK locks. A reversed order
	// can deadlock against an Append already holding the parent FOR UPDATE.
	var stateVersion int64
	err := tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		old.AuthorityID, old.TenantID, old.SubjectID).Scan(&stateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO usermodel_subjectref_v2_projection
		(legacy_authority_id,legacy_tenant_id,legacy_subject_id,issuer,subject_id)
		SELECT $1,$2,$3,$4,$5 FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3
		ON CONFLICT(legacy_authority_id,legacy_tenant_id,legacy_subject_id) DO NOTHING`,
		old.AuthorityID, old.TenantID, old.SubjectID, v2.Issuer, v2.SubjectID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: v2 issuer/UID has a different old owner", ErrConflict)
		}
		return err
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var issuer, uid string
	err = tx.QueryRow(ctx, `SELECT issuer,subject_id FROM usermodel_subjectref_v2_projection
		WHERE legacy_authority_id=$1 AND legacy_tenant_id=$2 AND legacy_subject_id=$3`,
		old.AuthorityID, old.TenantID, old.SubjectID).Scan(&issuer, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if issuer != v2.Issuer || uid != v2.SubjectID {
		return fmt.Errorf("%w: existing v2 projection differs", ErrConflict)
	}
	return nil
}

// ProjectExistingSubjectV2 adds only an identity mapping for an existing v1
// subject. It does not copy, normalize or mutate an old event or artifact.
func (s *Store) ProjectExistingSubjectV2(ctx context.Context, old SubjectRef) (err error) {
	if !s.v2Candidate {
		return ErrSubjectRefV2Disabled
	}
	v2, err := old.v2Projection()
	if err != nil {
		return err
	}
	ctx, stage, err := s.beginV2(ctx, "usermodel.subjectref.v2.project", v2)
	if err != nil {
		return err
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := insertV2Projection(ctx, tx, old, v2); err != nil {
		return dbErr(err)
	}
	return tx.Commit(ctx)
}

// AppendV2 uses a server-verified v2 subject and reuses the one v1 fact
// ledger transaction. During this candidate window the original v1 event
// JSON/hash, EventKey and Outbox receipt remain the sole business write.
func (s *Store) AppendV2(ctx context.Context, subject SubjectRefV2, input Event) (receipt Receipt, err error) {
	if !s.v2Candidate {
		return Receipt{}, ErrSubjectRefV2Disabled
	}
	ctx, stage, err := s.beginV2(ctx, "usermodel.subjectref.v2.append", subject)
	if err != nil {
		return Receipt{}, err
	}
	defer func() { finish(ctx, stage, err, receipt.Replay, receipt.Status) }()
	old, err := subject.legacy()
	if err != nil {
		return Receipt{}, err
	}
	if input.Subject != (SubjectRef{}) && input.Subject != old {
		return Receipt{}, fmt.Errorf("%w: v2 subject conflicts with event owner", ErrConflict)
	}
	input.Subject = old
	return s.Append(ctx, input)
}

// ResolveV2Legacy reads the immutable projection. Its result is an internal
// v1 storage key, never a SubjectRef issued to a client or a model.
func (s *Store) ResolveV2Legacy(ctx context.Context, subject SubjectRefV2) (old SubjectRef, err error) {
	if !s.v2Candidate {
		return SubjectRef{}, ErrSubjectRefV2Disabled
	}
	ctx, stage, err := s.beginV2(ctx, "usermodel.subjectref.v2.resolve", subject)
	if err != nil {
		return SubjectRef{}, err
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if _, err := subject.legacy(); err != nil {
		return SubjectRef{}, err
	}
	err = s.db.QueryRow(ctx, `SELECT legacy_authority_id,legacy_tenant_id,legacy_subject_id
		FROM usermodel_subjectref_v2_projection WHERE issuer=$1 AND subject_id=$2`,
		subject.Issuer, subject.SubjectID).Scan(&old.AuthorityID, &old.TenantID, &old.SubjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SubjectRef{}, ErrNotFound
	}
	return old, err
}

func (s *Store) CurrentV2(ctx context.Context, subject SubjectRefV2) (Projection, error) {
	old, err := s.ResolveV2Legacy(ctx, subject)
	if err != nil {
		return Projection{}, err
	}
	return s.Current(ctx, old)
}

func (s *Store) HistoryAfterV2(ctx context.Context, subject SubjectRefV2, after HistoryCursor, limit int) ([]Fact, HistoryCursor, error) {
	old, err := s.ResolveV2Legacy(ctx, subject)
	if err != nil {
		return nil, HistoryCursor{}, err
	}
	return s.HistoryAfter(ctx, old, after, limit)
}

func (s *Store) OutboxAfterV2(ctx context.Context, subject SubjectRefV2, afterVersion int64, limit int) ([]OutboxRecord, error) {
	old, err := s.ResolveV2Legacy(ctx, subject)
	if err != nil {
		return nil, err
	}
	return s.OutboxAfter(ctx, old, afterVersion, limit)
}
