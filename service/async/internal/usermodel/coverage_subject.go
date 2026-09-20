package usermodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/jackc/pgx/v5"
)

type SubjectCoverageReceipt struct {
	Ref    sourcecoverage.SubjectCoverageRef
	Replay bool
}

// VerifySubject derives the entire sparse slice from a previously verified
// *global* index. An empty byte slice is a valid zero-event proof only here.
func (v *CoverageVerifier) VerifySubject(ctx context.Context, ref sourcecoverage.SubjectCoverageRef,
	sparseJSONL []byte) (SubjectCoverageReceipt, error) {
	if v == nil || v.store == nil || ref.SchemaVersion != sourcecoverage.SchemaVersion ||
		!SubjectRef(ref.Subject).Valid() {
		return SubjectCoverageReceipt{}, ErrCoverageUnverified
	}
	receiptHash, err := sourcecoverage.SubjectReceiptHash(ref)
	if err != nil || receiptHash != ref.ReceiptSHA256 || len(sparseJSONL) > 64<<20 {
		return SubjectCoverageReceipt{}, ErrCoverageConflict
	}
	tx, err := v.store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return SubjectCoverageReceipt{}, err
	}
	defer tx.Rollback(ctx)
	var prefixBody []byte
	err = tx.QueryRow(ctx, `SELECT ref_body FROM usermodel_coverage_prefix WHERE manifest_sha256=$1`,
		ref.Prefix.ManifestSHA256).Scan(&prefixBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return SubjectCoverageReceipt{}, ErrCoverageUnverified
	}
	if err != nil {
		return SubjectCoverageReceipt{}, err
	}
	var cachedPrefix sourcecoverage.GlobalPrefixRef
	if json.Unmarshal(prefixBody, &cachedPrefix) != nil || !reflect.DeepEqual(cachedPrefix, ref.Prefix) {
		return SubjectCoverageReceipt{}, ErrCoverageConflict
	}
	rows, err := tx.Query(ctx, `SELECT row_body FROM usermodel_coverage_event
		WHERE manifest_sha256=$1 ORDER BY source_offset`, ref.Prefix.ManifestSHA256)
	if err != nil {
		return SubjectCoverageReceipt{}, err
	}
	full := make([]sourcecoverage.EventIndexRow, 0)
	for rows.Next() {
		var body []byte
		var row sourcecoverage.EventIndexRow
		if err := rows.Scan(&body); err != nil || json.Unmarshal(body, &row) != nil {
			rows.Close()
			return SubjectCoverageReceipt{}, ErrCoverageConflict
		}
		full = append(full, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SubjectCoverageReceipt{}, err
	}
	rows.Close()
	derived, digest, count, err := sourcecoverage.SubjectIndexJSONL(ref.Subject, full)
	if err != nil || !bytes.Equal(derived, sparseJSONL) || digest != ref.SparseIndexSHA256 || count != ref.EventCount {
		return SubjectCoverageReceipt{}, ErrCoverageConflict
	}
	var oldBody []byte
	err = tx.QueryRow(ctx, `SELECT ref_body FROM usermodel_coverage_subject
		WHERE manifest_sha256=$1 AND authority_id=$2 AND tenant_id=$3 AND subject_id=$4`,
		ref.Prefix.ManifestSHA256, ref.Subject.AuthorityID, ref.Subject.TenantID, ref.Subject.SubjectID).Scan(&oldBody)
	if err == nil {
		var old sourcecoverage.SubjectCoverageRef
		if json.Unmarshal(oldBody, &old) != nil || !reflect.DeepEqual(old, ref) {
			return SubjectCoverageReceipt{}, ErrCoverageConflict
		}
		return SubjectCoverageReceipt{Ref: ref, Replay: true}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SubjectCoverageReceipt{}, err
	}
	refBody, err := json.Marshal(ref)
	if err != nil {
		return SubjectCoverageReceipt{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_coverage_subject
		(manifest_sha256,authority_id,tenant_id,subject_id,receipt_sha256,sparse_index_sha256,event_count,ref_body)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ref.Prefix.ManifestSHA256,
		ref.Subject.AuthorityID, ref.Subject.TenantID, ref.Subject.SubjectID,
		ref.ReceiptSHA256, ref.SparseIndexSHA256, ref.EventCount, refBody)
	if err != nil {
		return SubjectCoverageReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SubjectCoverageReceipt{}, err
	}
	return SubjectCoverageReceipt{Ref: ref}, nil
}
