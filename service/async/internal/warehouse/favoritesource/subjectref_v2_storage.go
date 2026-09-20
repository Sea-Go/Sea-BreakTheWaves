package favoritesource

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectRefV2StorageCandidate is only attached explicitly to both warehouse
// writers. A nil field preserves the old ODS/receipt and artifact contracts.
// Initialize and InitializeCoverage do not load the candidate migration.
type SubjectRefV2StorageCandidate struct {
	db       *pgxpool.Pool
	observed *telemetry.Bundle
	ready    bool
}

var ErrSubjectRefV2ProjectionPending = errors.New("warehouse favorite v2 sidecar projection pending")

func NewSubjectRefV2StorageCandidate(ctx context.Context, preflight *SubjectRefV2Preflight,
	observed *telemetry.Bundle) (*SubjectRefV2StorageCandidate, error) {
	if preflight == nil || preflight.DB == nil || observed == nil || observed.Closed() {
		return nil, ErrContract
	}
	if !preflight.markedLocalTarget(ctx) {
		return nil, ErrContract
	}
	report, err := preflight.Run(ctx)
	if err != nil || !report.Clear() {
		return nil, ErrContract
	}
	db := preflight.DB
	if err := checkFavoriteV2StorageDDL(ctx, db); err != nil {
		return nil, err
	}
	return &SubjectRefV2StorageCandidate{db: db, observed: observed, ready: true}, nil
}

func (p *SubjectRefV2StorageCandidate) readyFor(db *pgxpool.Pool) bool {
	return p != nil && p.ready && p.db == db && p.observed != nil && !p.observed.Closed()
}

func validFavoriteV2Subject(authority, tenant, uid string) bool {
	_, strict, canonical := projectedUID(sourcecoverage.SubjectRef{
		AuthorityID: authority, TenantID: tenant, SubjectID: uid})
	return strict && canonical
}

func favoriteV2UID(authority, tenant, uid string) (int64, error) {
	if !validFavoriteV2Subject(authority, tenant, uid) {
		return 0, ErrContract
	}
	n, _ := strconv.ParseInt(uid, 10, 64) // canonical positive int64 checked above
	return n, nil
}

func favoriteV2ProjectionError(cause error) error {
	if cause == nil {
		return nil
	}
	return &projectionError{cause: cause}
}

type projectionError struct{ cause error }

func (*projectionError) Error() string   { return "warehouse favorite v2 storage projection rejected" }
func (e *projectionError) Unwrap() error { return errors.Join(ErrContract, e.cause) }

func (p *SubjectRefV2StorageCandidate) projectODS(ctx context.Context, tx pgx.Tx,
	producer string, offset int64, eventID, authority, tenant, subjectID string) error {
	uid, err := favoriteV2UID(authority, tenant, subjectID)
	if err != nil || producer != Producer || offset < 1 || eventID == "" {
		return ErrContract
	}
	_, err = tx.Exec(ctx, `INSERT INTO warehouse_favorite.ods_event_subject_ref_v2
	 (producer,source_offset,event_id,authority_id,tenant_id,subject_id,issuer,subject_uid)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
		producer, offset, eventID, authority, tenant, subjectID, authority, uid)
	if err != nil {
		return favoriteV2ProjectionError(err)
	}
	var gotProducer, gotEvent, gotAuthority, gotTenant, gotSubject, issuer string
	var gotOffset, gotUID int64
	err = tx.QueryRow(ctx, `SELECT producer,source_offset,event_id,authority_id,tenant_id,
	 subject_id,issuer,subject_uid FROM warehouse_favorite.ods_event_subject_ref_v2
	 WHERE producer=$1 AND source_offset=$2 FOR SHARE`, producer, offset).
		Scan(&gotProducer, &gotOffset, &gotEvent, &gotAuthority, &gotTenant, &gotSubject, &issuer, &gotUID)
	if err != nil {
		return favoriteV2ProjectionError(err)
	}
	if gotProducer != producer || gotOffset != offset || gotEvent != eventID ||
		gotAuthority != authority || gotTenant != tenant || gotSubject != subjectID || issuer != authority || gotUID != uid {
		return ErrContract
	}
	return nil
}

func (p *SubjectRefV2StorageCandidate) projectReceipt(ctx context.Context, tx pgx.Tx,
	receiptHash, manifestHash, authority, tenant, subjectID string) error {
	uid, err := favoriteV2UID(authority, tenant, subjectID)
	if err != nil || !hashPattern.MatchString(receiptHash) || !hashPattern.MatchString(manifestHash) {
		return ErrContract
	}
	_, err = tx.Exec(ctx, `INSERT INTO warehouse_favorite.coverage_subject_receipt_subject_ref_v2
	 (receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,issuer,subject_uid)
	 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
		receiptHash, manifestHash, authority, tenant, subjectID, authority, uid)
	if err != nil {
		return favoriteV2ProjectionError(err)
	}
	var gotReceipt, gotManifest, gotAuthority, gotTenant, gotSubject, issuer string
	var gotUID int64
	err = tx.QueryRow(ctx, `SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,
	 subject_id,issuer,subject_uid FROM warehouse_favorite.coverage_subject_receipt_subject_ref_v2
	 WHERE receipt_sha256=$1 FOR SHARE`, receiptHash).
		Scan(&gotReceipt, &gotManifest, &gotAuthority, &gotTenant, &gotSubject, &issuer, &gotUID)
	if err != nil {
		return favoriteV2ProjectionError(err)
	}
	if gotReceipt != receiptHash || gotManifest != manifestHash || gotAuthority != authority ||
		gotTenant != tenant || gotSubject != subjectID || issuer != authority || gotUID != uid {
		return ErrContract
	}
	return nil
}

func (p *SubjectRefV2StorageCandidate) begin(ctx context.Context, event, key string,
	value any) (context.Context, *telemetry.Stage, error) {
	if p == nil || !p.ready || p.observed == nil {
		return ctx, nil, ErrContract
	}
	return p.observed.Begin(ctx, "warehouse", event, slog.Any(key, value))
}

func (p *SubjectRefV2StorageCandidate) end(ctx context.Context, stage *telemetry.Stage,
	err error, committed int64) {
	if stage == nil {
		return
	}
	if err == nil {
		stage.End(ctx, "succeeded", "", nil, slog.Int64("committed_offset", committed))
		return
	}
	outcome := "failed"
	if errors.Is(err, ErrContract) || errors.Is(err, ErrCoverageConflict) {
		outcome = "rejected"
	}
	if errors.Is(err, context.Canceled) {
		outcome = "cancelled"
	}
	// PG detail fields can include the old UID or event body. The structured
	// stage records only a bounded domain classification, not a raw SQL error.
	stage.End(ctx, outcome, "SUBJECTREF_V2_STORAGE_REJECTED", ErrContract)
}

type coverageReceiptWriter interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// The read-only startup gate pins the same PG16 sidecar graph as the operator
// migration. A same-name object on another relation or a weaker FK parent
// cannot authorize continuous writes.
func checkFavoriteV2StorageDDL(ctx context.Context, db *pgxpool.Pool) error {
	var ready bool
	err := db.QueryRow(ctx, `WITH expected(relation_name,column_shape) AS (VALUES
	 ('ods_event_subject_ref_v2','producer:text:t,source_offset:bigint:t,event_id:text:t,authority_id:text:t,tenant_id:text:t,subject_id:text:t,issuer:text:t,subject_uid:bigint:t'),
	 ('coverage_subject_receipt_subject_ref_v2','receipt_sha256:character(64):t,manifest_sha256:character(64):t,authority_id:text:t,tenant_id:text:t,subject_id:text:t,issuer:text:t,subject_uid:bigint:t')
	) SELECT count(*)=2 FROM expected x JOIN pg_class r
	 ON r.oid=to_regclass('warehouse_favorite.'||x.relation_name)
	 AND r.relkind='r' AND r.relpersistence='p'
	 JOIN LATERAL (SELECT string_agg(a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||
	  CASE WHEN a.attnotnull THEN 't' ELSE 'f' END,',' ORDER BY a.attnum) shape,
	  bool_and(NOT a.atthasdef AND a.attgenerated='' AND a.attidentity='') no_derived
	  FROM pg_attribute a WHERE a.attrelid=r.oid AND a.attnum>0 AND NOT a.attisdropped) cols
	 ON cols.shape=x.column_shape AND cols.no_derived
	 WHERE (SELECT count(*) FROM pg_constraint c WHERE c.conrelid=r.oid)=4
	 AND (SELECT count(*) FROM pg_index i WHERE i.indrelid=r.oid)=2`).Scan(&ready)
	if err != nil || !ready {
		return ErrContract
	}
	err = db.QueryRow(ctx, `WITH expected(relation_name,constraint_name,definition) AS (VALUES
	 ('ods_event','ods_event_pkey','PRIMARY KEY (producer, source_offset)'),
	 ('ods_event','ods_event_producer_event_id_key','UNIQUE (producer, event_id)'),
	 ('coverage_subject_receipt','coverage_subject_receipt_pkey','PRIMARY KEY (receipt_sha256)'),
	 ('coverage_subject_receipt','coverage_subject_receipt_manifest_sha256_authority_id_tenan_key',
	  'UNIQUE (manifest_sha256, authority_id, tenant_id, subject_id)'),
	 ('ods_event','ods_event_v2_anchor_key','UNIQUE (producer, source_offset, event_id, authority_id, tenant_id, subject_id)'),
	 ('coverage_subject_receipt','coverage_subject_receipt_v2_anchor_key','UNIQUE (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id)'),
	 ('ods_event_subject_ref_v2','ods_event_subject_ref_v2_pkey','PRIMARY KEY (producer, source_offset)'),
	 ('ods_event_subject_ref_v2','ods_event_subject_ref_v2_producer_event_id_key','UNIQUE (producer, event_id)'),
	 ('ods_event_subject_ref_v2','ods_event_subject_ref_v2_projection_check',
	  $def$CHECK (((issuer = 'rtw.identity'::text) AND (authority_id = issuer) AND (tenant_id = 'platform'::text) AND (subject_uid > 0) AND (subject_id = (subject_uid)::text)))$def$),
	 ('ods_event_subject_ref_v2','ods_event_subject_ref_v2_source_fkey',
	  'FOREIGN KEY (producer, source_offset, event_id, authority_id, tenant_id, subject_id) REFERENCES warehouse_favorite.ods_event(producer, source_offset, event_id, authority_id, tenant_id, subject_id)'),
	 ('coverage_subject_receipt_subject_ref_v2','coverage_subject_receipt_subject_ref_v2_pkey','PRIMARY KEY (receipt_sha256)'),
	 ('coverage_subject_receipt_subject_ref_v2','coverage_subject_receipt_subject_ref_v2_manifest_issuer_uid_key','UNIQUE (manifest_sha256, issuer, subject_uid)'),
	 ('coverage_subject_receipt_subject_ref_v2','coverage_subject_receipt_subject_ref_v2_projection_check',
	  $def$CHECK (((issuer = 'rtw.identity'::text) AND (authority_id = issuer) AND (tenant_id = 'platform'::text) AND (subject_uid > 0) AND (subject_id = (subject_uid)::text)))$def$),
	 ('coverage_subject_receipt_subject_ref_v2','coverage_subject_receipt_subject_ref_v2_source_fkey',
	  'FOREIGN KEY (receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id) REFERENCES warehouse_favorite.coverage_subject_receipt(receipt_sha256, manifest_sha256, authority_id, tenant_id, subject_id)')
	) SELECT count(*)=14 FROM expected x JOIN pg_constraint c
	 ON c.conrelid=to_regclass('warehouse_favorite.'||x.relation_name)
	 AND c.conname=x.constraint_name AND c.convalidated AND NOT c.condeferrable
	 AND pg_get_constraintdef(c.oid)=x.definition
	 WHERE (c.contype IN ('f','c') OR EXISTS (SELECT 1 FROM pg_index i
	  WHERE i.indexrelid=c.conindid AND i.indisvalid AND i.indisready AND i.indimmediate))`).Scan(&ready)
	if err != nil || !ready {
		return ErrContract
	}
	return nil
}

// These are internal dual-read results, not a new ODS or subject-coverage
// artifact version. The original v1 ExportODS/manifest/receipt writers stay
// authoritative until the separate coverage and dataset contract migration.
type V2ODSEvent struct {
	Producer, EventID, SourceEventHash string
	SourceOffset                       int64
	OriginalSubject                    sourcecoverage.SubjectRef
	Issuer, SubjectID                  string
	EventSpec, TechnicalReceipt        json.RawMessage
}

func (p *SubjectRefV2StorageCandidate) ReadODSEventV2(ctx context.Context,
	producer string, offset int64) (V2ODSEvent, error) {
	var out V2ODSEvent
	if p == nil || !p.readyFor(p.db) || producer != Producer || offset < 1 {
		return out, ErrContract
	}
	var uid int64
	err := p.db.QueryRow(ctx, `SELECT e.producer,e.source_offset,e.event_id,e.source_event_hash,
	 e.event_spec,e.technical_receipt,e.authority_id,e.tenant_id,e.subject_id,
	 v.issuer,v.subject_uid FROM warehouse_favorite.ods_event e
	 JOIN warehouse_favorite.ods_event_subject_ref_v2 v
	  ON v.producer=e.producer AND v.source_offset=e.source_offset AND v.event_id=e.event_id
	  AND v.authority_id=e.authority_id AND v.tenant_id=e.tenant_id AND v.subject_id=e.subject_id
	 WHERE e.producer=$1 AND e.source_offset=$2`, producer, offset).
		Scan(&out.Producer, &out.SourceOffset, &out.EventID, &out.SourceEventHash,
			&out.EventSpec, &out.TechnicalReceipt, &out.OriginalSubject.AuthorityID,
			&out.OriginalSubject.TenantID, &out.OriginalSubject.SubjectID, &out.Issuer, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrSubjectRefV2ProjectionPending
	}
	if err != nil {
		return out, favoriteV2ProjectionError(err)
	}
	out.SubjectID = strconv.FormatInt(uid, 10)
	if !validFavoriteV2Subject(out.OriginalSubject.AuthorityID, out.OriginalSubject.TenantID,
		out.OriginalSubject.SubjectID) || out.Issuer != out.OriginalSubject.AuthorityID ||
		out.SubjectID != out.OriginalSubject.SubjectID {
		return V2ODSEvent{}, ErrContract
	}
	return out, nil
}

type V2SubjectReceipt struct {
	ReceiptSHA256, ManifestSHA256, SparseIndexSHA256 string
	EventCount                                       int64
	OriginalSubject                                  sourcecoverage.SubjectRef
	Issuer, SubjectID                                string
}

func (p *SubjectRefV2StorageCandidate) ReadSubjectReceiptV2(ctx context.Context,
	manifestHash, subjectID string) (V2SubjectReceipt, error) {
	var out V2SubjectReceipt
	uid, err := favoriteV2UID("rtw.identity", "platform", subjectID)
	if p == nil || !p.readyFor(p.db) || err != nil || !hashPattern.MatchString(manifestHash) {
		return out, ErrContract
	}
	err = p.db.QueryRow(ctx, `SELECT r.receipt_sha256,r.manifest_sha256,r.sparse_index_sha256,
	 r.event_count,r.authority_id,r.tenant_id,r.subject_id,v.issuer,v.subject_uid
	 FROM warehouse_favorite.coverage_subject_receipt r
	 JOIN warehouse_favorite.coverage_subject_receipt_subject_ref_v2 v
	  ON v.receipt_sha256=r.receipt_sha256 AND v.manifest_sha256=r.manifest_sha256
	  AND v.authority_id=r.authority_id AND v.tenant_id=r.tenant_id AND v.subject_id=r.subject_id
	 WHERE v.manifest_sha256=$1 AND v.issuer='rtw.identity' AND v.subject_uid=$2`, manifestHash, uid).
		Scan(&out.ReceiptSHA256, &out.ManifestSHA256, &out.SparseIndexSHA256, &out.EventCount,
			&out.OriginalSubject.AuthorityID, &out.OriginalSubject.TenantID,
			&out.OriginalSubject.SubjectID, &out.Issuer, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrSubjectRefV2ProjectionPending
	}
	if err != nil {
		return out, favoriteV2ProjectionError(err)
	}
	out.SubjectID = strconv.FormatInt(uid, 10)
	if out.Issuer != out.OriginalSubject.AuthorityID || out.SubjectID != out.OriginalSubject.SubjectID ||
		!validFavoriteV2Subject(out.OriginalSubject.AuthorityID, out.OriginalSubject.TenantID,
			out.OriginalSubject.SubjectID) || out.ManifestSHA256 != manifestHash {
		return V2SubjectReceipt{}, ErrContract
	}
	return out, nil
}
