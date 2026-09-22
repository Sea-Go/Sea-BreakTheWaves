package usermodel

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CurrentReceipt reads the authoritative current status for one fixed event.
// Append replays its original receipt, which can remain pending after a late
// predecessor promotes the event. A current accepted receipt is returned only
// when the same committed snapshot also contains its accepted Outbox record.
// Consumers must compare NormalizedHash with their immutable input before ACK.
func (s *Store) CurrentReceipt(ctx context.Context, subject SubjectRef, key EventKey) (receipt Receipt, err error) {
	if ctx == nil || s == nil || s.db == nil || !subject.Valid() || !key.Valid() {
		return Receipt{}, ErrInvalid
	}
	ctx, stage, beginErr := s.begin(ctx, "usermodel.fact.receipt.read", key, subject)
	if beginErr != nil {
		return Receipt{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, receipt.Status) }()
	var status string
	var initialVersion int64
	var acceptedVersion *int64
	var outboxPresent bool
	err = s.db.QueryRow(ctx, `SELECT e.normalized_hash,e.status,e.initial_version,e.accepted_version,
		EXISTS(SELECT 1 FROM usermodel_outbox o
			WHERE o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id AND o.subject_id=e.subject_id
			AND o.producer=e.producer AND o.event_id=e.event_id
			AND o.state_version=e.accepted_version AND o.event_type='usermodel.fact.accepted')
		FROM usermodel_events e
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.producer=$4 AND e.event_id=$5`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, key.Producer, key.EventID).
		Scan(&receipt.NormalizedHash, &status, &initialVersion, &acceptedVersion, &outboxPresent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	receipt.Subject, receipt.EventKey, receipt.Status = subject, key, status
	switch status {
	case "accepted":
		if acceptedVersion == nil || *acceptedVersion <= 0 || !outboxPresent {
			return Receipt{}, ErrConflict
		}
		receipt.StateVersion = *acceptedVersion
	case "pending_dependency":
		if acceptedVersion != nil || outboxPresent {
			return Receipt{}, ErrConflict
		}
		receipt.StateVersion = initialVersion
	default:
		return Receipt{}, ErrConflict
	}
	return receipt, nil
}
