package content

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/jackc/pgx/v5"
)

// IndexDispatch is the durable, leased handoff of one immutable local READY
// result. ClaimEpoch fences a stale scanner; it is not an authentication token.
type IndexDispatch struct {
	BuildID     string
	JobID       string
	WorkerID    string
	Fence       Fence // RTW build-wide fence, never the DC job-local epoch.
	DC          TechnicalFence
	Result      corpus.Ref
	ClaimEpoch  int64
	ClaimUntil  time.Time
	RTWAccepted bool
}

// TechnicalFence belongs to one DC job attempt. It is deliberately distinct
// from the RTW build-wide Fence even when their initial epoch numbers match.
type TechnicalFence struct {
	AttemptID     string
	LeaseEpoch    int64
	CancelVersion int64
	ExpiresAt     time.Time
}

// AttachIndexDispatch records the DC attempt before Graph can commit READY.
// A later DC attempt may replace an expired one; the READY outbox and result
// remain immutable. A repeated or lower epoch cannot change a fixed job.
func (s *Store) AttachIndexDispatch(ctx context.Context, buildID, jobID, workerID string,
	dc TechnicalFence, fence Fence) error {
	if buildID == "" || jobID == "" || workerID == "" || fence.BuildID != buildID ||
		fence.AttemptID == "" || fence.LeaseEpoch <= 0 || fence.CancelVersion < 0 || fence.ExpiresAt.IsZero() ||
		dc.AttemptID == "" || dc.LeaseEpoch <= 0 || dc.CancelVersion < 0 || dc.ExpiresAt.IsZero() {
		return ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	initial, err := s.Get(ctx, buildID)
	if err != nil {
		return err
	}
	if err := lockModule(ctx, tx, initial.ModuleID); err != nil {
		return err
	}
	build, err := readBuild(ctx, tx, buildID, true)
	if err != nil {
		return err
	}
	if build.State != "BUILDING" && build.State != "READY" || build.ModuleID != initial.ModuleID {
		return ErrConflict
	}
	var dcLive, rtwLive bool
	if err := tx.QueryRow(ctx, "SELECT $1::timestamptz > clock_timestamp(),$2::timestamptz > clock_timestamp()",
		dc.ExpiresAt, fence.ExpiresAt).Scan(&dcLive, &rtwLive); err != nil {
		return err
	}
	if !dcLive || (build.State == "BUILDING" && !rtwLive) {
		return ErrConflict
	}
	var priorJob, priorWorker, priorAttempt, priorDCAttempt string
	var priorEpoch, priorCancel, priorDCEpoch, priorDCCancel int64
	var priorExpiry, priorDCExpiry time.Time
	err = tx.QueryRow(ctx, `SELECT job_id,worker_id,attempt_id,lease_epoch,cancel_version,lease_expires_at,
		dc_attempt_id,dc_lease_epoch,dc_cancel_version,dc_lease_expires_at
		FROM content_index_dispatch WHERE build_id=$1 FOR UPDATE`, buildID).Scan(
		&priorJob, &priorWorker, &priorAttempt, &priorEpoch, &priorCancel, &priorExpiry,
		&priorDCAttempt, &priorDCEpoch, &priorDCCancel, &priorDCExpiry)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO content_index_dispatch
			(build_id,job_id,worker_id,attempt_id,lease_epoch,cancel_version,lease_expires_at,
			 dc_attempt_id,dc_lease_epoch,dc_cancel_version,dc_lease_expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, buildID, jobID, workerID,
			fence.AttemptID, fence.LeaseEpoch, fence.CancelVersion, fence.ExpiresAt,
			dc.AttemptID, dc.LeaseEpoch, dc.CancelVersion, dc.ExpiresAt)
	} else if fence.LeaseEpoch < priorEpoch || fence.CancelVersion != priorCancel ||
		(fence.LeaseEpoch == priorEpoch && (priorAttempt != fence.AttemptID || fence.ExpiresAt.Before(priorExpiry))) ||
		(priorJob == jobID && dc.LeaseEpoch == priorDCEpoch && dc.AttemptID == priorDCAttempt &&
			dc.ExpiresAt.Before(priorDCExpiry)) ||
		(priorJob == jobID && (dc.LeaseEpoch < priorDCEpoch ||
			(dc.LeaseEpoch == priorDCEpoch && (priorDCAttempt != dc.AttemptID ||
				priorDCCancel != dc.CancelVersion)))) {
		return ErrConflict
	} else if priorJob != jobID || priorWorker != workerID || priorDCAttempt != dc.AttemptID ||
		priorDCEpoch != dc.LeaseEpoch || priorDCCancel != dc.CancelVersion ||
		!priorDCExpiry.Equal(dc.ExpiresAt) || !priorExpiry.Equal(fence.ExpiresAt) || fence.LeaseEpoch > priorEpoch {
		newTechnicalAttempt := priorJob != jobID || priorWorker != workerID ||
			priorDCAttempt != dc.AttemptID || priorDCEpoch != dc.LeaseEpoch ||
			priorDCCancel != dc.CancelVersion
		if fence.LeaseEpoch == priorEpoch && build.State != "READY" && newTechnicalAttempt {
			return ErrConflict // A new DC attempt needs a fresh RTW grant until READY is accepted.
		}
		_, err = tx.Exec(ctx, `UPDATE content_index_dispatch SET job_id=$2,worker_id=$3,attempt_id=$4,
			lease_epoch=$5,lease_expires_at=$6,dc_attempt_id=$7,dc_lease_epoch=$8,
			dc_cancel_version=$9,dc_lease_expires_at=$10,
			rtw_accepted_at=CASE WHEN $5>lease_epoch THEN NULL ELSE rtw_accepted_at END,
			stage='pending',claim_epoch=claim_epoch+1,claim_until=NULL,last_error='',
			updated_at=clock_timestamp() WHERE build_id=$1`,
			buildID, jobID, workerID, fence.AttemptID, fence.LeaseEpoch, fence.ExpiresAt,
			dc.AttemptID, dc.LeaseEpoch, dc.CancelVersion, dc.ExpiresAt)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE content_outbox SET delivered_at=NULL WHERE build_id=$1`, buildID)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClaimIndexDispatch scans the READY outbox and leases a single pending result.
// Empty buildID scans all pending builds. Restart reclaims an expired lease.
func (s *Store) ClaimIndexDispatch(ctx context.Context, buildID string) (IndexDispatch, bool, error) {
	var claim IndexDispatch
	var expiry, dcExpiry time.Time
	err := s.db.QueryRow(ctx, `WITH candidate AS (
		SELECT d.build_id FROM content_index_dispatch d JOIN content_outbox o USING(build_id)
		WHERE d.stage='pending' AND o.delivered_at IS NULL
		  AND (d.claim_until IS NULL OR d.claim_until<clock_timestamp())
		  AND ($1='' OR d.build_id=$1)
		ORDER BY d.updated_at,d.build_id FOR UPDATE OF d SKIP LOCKED LIMIT 1
	) UPDATE content_index_dispatch d SET claim_epoch=d.claim_epoch+1,
		claim_until=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp()
	FROM candidate c WHERE d.build_id=c.build_id
	RETURNING d.build_id,d.job_id,d.worker_id,d.attempt_id,d.lease_epoch,
		d.cancel_version,d.lease_expires_at,d.dc_attempt_id,d.dc_lease_epoch,
		d.dc_cancel_version,d.dc_lease_expires_at,d.claim_epoch,d.claim_until,
		d.rtw_accepted_at IS NOT NULL`, buildID).Scan(
		&claim.BuildID, &claim.JobID, &claim.WorkerID, &claim.Fence.AttemptID,
		&claim.Fence.LeaseEpoch, &claim.Fence.CancelVersion, &expiry,
		&claim.DC.AttemptID, &claim.DC.LeaseEpoch, &claim.DC.CancelVersion, &dcExpiry,
		&claim.ClaimEpoch, &claim.ClaimUntil, &claim.RTWAccepted)
	if errors.Is(err, pgx.ErrNoRows) {
		return IndexDispatch{}, false, nil
	}
	if err != nil {
		return IndexDispatch{}, false, err
	}
	claim.Fence.BuildID, claim.Fence.ExpiresAt = claim.BuildID, expiry
	claim.DC.ExpiresAt = dcExpiry
	build, err := s.Get(ctx, claim.BuildID)
	if err != nil {
		return claim, true, err
	}
	if build.State != "READY" || build.Result == nil || !validRef(*build.Result) || len(build.Lanes) != 3 {
		return claim, true, fmt.Errorf("%w: outbox has no immutable READY", ErrConflict)
	}
	claim.Result = *build.Result
	return claim, true, nil
}

func (s *Store) indexDispatchUpdate(ctx context.Context, claim IndexDispatch, suffix string, args ...any) error {
	params := append([]any{claim.BuildID, claim.ClaimEpoch, claim.JobID, claim.Fence.AttemptID,
		claim.Fence.LeaseEpoch, claim.Fence.CancelVersion, claim.DC.AttemptID,
		claim.DC.LeaseEpoch, claim.DC.CancelVersion}, args...)
	tag, err := s.db.Exec(ctx, `UPDATE content_index_dispatch SET `+suffix+`
		WHERE build_id=$1 AND claim_epoch=$2 AND job_id=$3 AND attempt_id=$4
		AND lease_epoch=$5 AND cancel_version=$6 AND dc_attempt_id=$7
		AND dc_lease_epoch=$8 AND dc_cancel_version=$9 AND stage='pending'
		AND claim_until>clock_timestamp()`, params...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) NoteIndexRTWAccepted(ctx context.Context, claim IndexDispatch) error {
	return s.indexDispatchUpdate(ctx, claim, `rtw_accepted_at=COALESCE(rtw_accepted_at,clock_timestamp()),updated_at=clock_timestamp()`)
}

// DeferIndexDispatch stores only a stable error code, never a response body or
// credential. A new DC epoch explicitly reopens needs_new_attempt/manual.
func (s *Store) DeferIndexDispatch(ctx context.Context, claim IndexDispatch, stage, code string) error {
	if stage != "pending" && stage != "needs_new_attempt" && stage != "manual" {
		return ErrInvalid
	}
	if code == "" || len(code) > 80 {
		return ErrInvalid
	}
	return s.indexDispatchUpdate(ctx, claim, `stage=$10,last_error=$11,
		claim_until=CASE WHEN $10='pending' THEN clock_timestamp()+interval '5 seconds' ELSE NULL END,
		updated_at=clock_timestamp()`, stage, code)
}

// DeliverIndexDispatch makes DC ACK and accepted RTW receipt visible as one
// local terminal marker. Both remote effects must have been checked first.
func (s *Store) DeliverIndexDispatch(ctx context.Context, claim IndexDispatch) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `UPDATE content_index_dispatch SET stage='complete',claim_until=NULL,
		updated_at=clock_timestamp() WHERE build_id=$1 AND claim_epoch=$2 AND job_id=$3
		AND attempt_id=$4 AND lease_epoch=$5 AND cancel_version=$6 AND dc_attempt_id=$7
		AND dc_lease_epoch=$8 AND dc_cancel_version=$9 AND stage='pending'
		AND claim_until>clock_timestamp() AND rtw_accepted_at IS NOT NULL`,
		claim.BuildID, claim.ClaimEpoch, claim.JobID, claim.Fence.AttemptID,
		claim.Fence.LeaseEpoch, claim.Fence.CancelVersion, claim.DC.AttemptID,
		claim.DC.LeaseEpoch, claim.DC.CancelVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE content_outbox SET delivered_at=clock_timestamp()
		WHERE build_id=$1 AND delivered_at IS NULL`, claim.BuildID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
