package favoritesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectRefV2Preflight only reads the existing favorite ODS and coverage
// receipts. It does not rewrite an EventSpec, move an old slot, or publish a
// replacement coverage artifact. Findings contain counts and digests, never
// the old SubjectRef, UID, EventSpec, or technical receipt.
type SubjectRefV2Preflight struct {
	DB       *pgxpool.Pool
	Objects  CoverageObjectReader
	S3Prefix string
	// LocalTestNonce is provisioned by the separate disposable-DB test owner.
	// Run is read-only without it; Apply/continuous writes require the marker.
	LocalTestNonce string
}

type CoverageObjectReader interface {
	Get(context.Context, string) ([]byte, error)
}

type SubjectRefV2Finding struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type SubjectRefV2Report struct {
	ODSRows               int                   `json:"ods_rows"`
	BatchWindows          int                   `json:"batch_windows"`
	Publications          int                   `json:"publications"`
	SubjectReceipts       int                   `json:"subject_receipts"`
	SnapshotSHA256        string                `json:"snapshot_sha256"`
	VerifiedCoverageRoots []string              `json:"verified_coverage_roots,omitempty"`
	Findings              []SubjectRefV2Finding `json:"findings"`
}

func (r SubjectRefV2Report) Clear() bool { return len(r.Findings) == 0 }

type preflightODS struct {
	producer, eventID, favoriteID, folderID, targetType, targetID, operation, predecessor string
	eventType, aggregateID                                                                string
	offset, aggregateVersion                                                              int64
	eventTime, availableAt, receivedAt                                                    time.Time
	revision                                                                              *string
	subject                                                                               sourcecoverage.SubjectRef
	eventSpec, receipt                                                                    []byte
	sourceHash                                                                            string
}

type preflightPublication struct {
	manifest, generation, producer, indexHash, batchHash string
	through                                              int64
}

type preflightBatch struct {
	consumer, producer, hash string
	from, to                 int64
}

type preflightSubjectReceipt struct {
	receiptHash, manifest, sparseHash string
	subject                           sourcecoverage.SubjectRef
	eventCount                        int64
}

type preflightSnapshot struct {
	cursor       int64
	ods          []preflightODS
	batches      []preflightBatch
	publications []preflightPublication
	receipts     []preflightSubjectReceipt
}

const preflightMaxRows = 10000

func (p *SubjectRefV2Preflight) Run(ctx context.Context) (SubjectRefV2Report, error) {
	if p == nil || p.DB == nil {
		return SubjectRefV2Report{}, ErrContract
	}
	snapshot, err := p.readSnapshot(ctx)
	if err != nil {
		return SubjectRefV2Report{}, err
	}
	return p.assessSnapshot(ctx, snapshot), nil
}

func (p *SubjectRefV2Preflight) assessSnapshot(ctx context.Context,
	snapshot preflightSnapshot) SubjectRefV2Report {
	report := assessSubjectRefV2(snapshot)
	if len(snapshot.publications) > 0 {
		if p.Objects == nil || p.S3Prefix == "" {
			addFinding(&report, "coverage_objects_unverified", len(snapshot.publications))
		} else {
			p.verifyCoverage(ctx, snapshot, &report)
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool { return report.Findings[i].Code < report.Findings[j].Code })
	sort.Strings(report.VerifiedCoverageRoots)
	return report
}

func (p *SubjectRefV2Preflight) readSnapshot(ctx context.Context) (preflightSnapshot, error) {
	var snapshot preflightSnapshot
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return snapshot, fmt.Errorf("begin read-only favorite preflight: %w", err)
	}
	defer tx.Rollback(context.Background())
	snapshot, err = p.readSnapshotInTx(ctx, tx)
	if err != nil {
		return snapshot, err
	}
	if err = tx.Commit(ctx); err != nil {
		return snapshot, fmt.Errorf("close read-only favorite snapshot: %w", err)
	}
	return snapshot, nil
}

// readSnapshotInTx is shared by the ordinary read-only report and the
// locked apply transaction. Every database finding then refers to the exact
// old rows that the additive DDL will project before that transaction commits.
func (p *SubjectRefV2Preflight) readSnapshotInTx(ctx context.Context,
	tx pgx.Tx) (preflightSnapshot, error) {
	var snapshot preflightSnapshot
	var err error
	err = tx.QueryRow(ctx, `SELECT committed_offset FROM warehouse_favorite.consumer_cursor WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer).Scan(&snapshot.cursor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return snapshot, fmt.Errorf("read favorite cursor: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT producer,source_offset,event_id,event_type,aggregate_id,aggregate_version,event_spec,source_event_hash,technical_receipt,
		authority_id,tenant_id,subject_id,favorite_id,folder_id,target_type,target_id,target_revision,operation,coalesce(predecessor_event_id,''),
		event_time,available_at,dc_received_at
		FROM warehouse_favorite.ods_event ORDER BY producer,source_offset LIMIT 10001`)
	if err != nil {
		return snapshot, fmt.Errorf("read favorite ODS: %w", err)
	}
	for rows.Next() {
		var r preflightODS
		if err := rows.Scan(&r.producer, &r.offset, &r.eventID, &r.eventType, &r.aggregateID, &r.aggregateVersion, &r.eventSpec, &r.sourceHash, &r.receipt,
			&r.subject.AuthorityID, &r.subject.TenantID, &r.subject.SubjectID, &r.favoriteID, &r.folderID, &r.targetType, &r.targetID, &r.revision, &r.operation, &r.predecessor,
			&r.eventTime, &r.availableAt, &r.receivedAt); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan favorite ODS: %w", err)
		}
		snapshot.ods = append(snapshot.ods, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate favorite ODS: %w", err)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT consumer,producer,from_offset,to_offset,batch_hash
		FROM warehouse_favorite.coverage_batch_evidence ORDER BY consumer,producer,from_offset LIMIT 10001`)
	if err != nil {
		return snapshot, fmt.Errorf("read favorite batch evidence: %w", err)
	}
	for rows.Next() {
		var r preflightBatch
		if err := rows.Scan(&r.consumer, &r.producer, &r.from, &r.to, &r.hash); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan favorite batch evidence: %w", err)
		}
		snapshot.batches = append(snapshot.batches, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate favorite batch evidence: %w", err)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT manifest_sha256,generation,producer,through_offset,event_index_sha256,batch_evidence_sha256
		FROM warehouse_favorite.coverage_publication ORDER BY generation LIMIT 10001`)
	if err != nil {
		return snapshot, fmt.Errorf("read favorite publications: %w", err)
	}
	for rows.Next() {
		var r preflightPublication
		if err := rows.Scan(&r.manifest, &r.generation, &r.producer, &r.through, &r.indexHash, &r.batchHash); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan favorite publication: %w", err)
		}
		snapshot.publications = append(snapshot.publications, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate favorite publications: %w", err)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,sparse_index_sha256,event_count
		FROM warehouse_favorite.coverage_subject_receipt ORDER BY manifest_sha256,receipt_sha256 LIMIT 10001`)
	if err != nil {
		return snapshot, fmt.Errorf("read favorite subject receipts: %w", err)
	}
	for rows.Next() {
		var r preflightSubjectReceipt
		if err := rows.Scan(&r.receiptHash, &r.manifest, &r.subject.AuthorityID, &r.subject.TenantID, &r.subject.SubjectID, &r.sparseHash, &r.eventCount); err != nil {
			rows.Close()
			return snapshot, fmt.Errorf("scan favorite subject receipt: %w", err)
		}
		snapshot.receipts = append(snapshot.receipts, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, fmt.Errorf("iterate favorite subject receipts: %w", err)
	}
	rows.Close()
	return snapshot, nil
}

func projectedUID(subject sourcecoverage.SubjectRef) (string, bool, bool) {
	n, err := strconv.ParseInt(subject.SubjectID, 10, 64)
	validUID := err == nil && n > 0 && strconv.FormatInt(n, 10) == subject.SubjectID
	if !validUID {
		return "", false, false
	}
	return subject.SubjectID, subject.AuthorityID == "rtw.identity" && subject.TenantID == "platform", true
}

func pgTimestampMatches(stored time.Time, source string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, source)
	if err != nil {
		return false
	}
	// pgx/v5 v5.8.0 encodes timestamptz as whole microseconds, truncating
	// sub-microsecond input. Accepting either adjacent microsecond would hide
	// a materialized ODS change while the source EventSpec stays fixed.
	return stored.Equal(parsed.UTC().Truncate(time.Microsecond))
}

func fingerprintField(h hash.Hash, value []byte) {
	h.Write([]byte(strconv.Itoa(len(value))))
	h.Write([]byte{':'})
	h.Write(value)
}

func addFinding(report *SubjectRefV2Report, code string, count int) {
	if count == 0 {
		return
	}
	for i := range report.Findings {
		if report.Findings[i].Code == code {
			report.Findings[i].Count += count
			return
		}
	}
	report.Findings = append(report.Findings, SubjectRefV2Finding{Code: code, Count: count})
}

func assessSubjectRefV2(s preflightSnapshot) SubjectRefV2Report {
	report := SubjectRefV2Report{ODSRows: len(s.ods), BatchWindows: len(s.batches), Publications: len(s.publications), SubjectReceipts: len(s.receipts)}
	h := sha256.New()
	fingerprintField(h, []byte("cursor"))
	fingerprintField(h, []byte(strconv.FormatInt(s.cursor, 10)))
	odsFavorite := map[string]sourcecoverage.SubjectRef{}
	odsFolderTarget := map[string]sourcecoverage.SubjectRef{}
	receiptProjection := map[string]sourcecoverage.SubjectRef{}
	manifest := map[string]preflightPublication{}
	asserts := map[string]preflightODS{}
	last := int64(0)
	for _, r := range s.ods {
		// Hash the exact stored evidence and selected keys in sorted query order.
		// The digest is a snapshot fingerprint, not a replacement source hash.
		for _, value := range []string{r.producer, strconv.FormatInt(r.offset, 10), r.eventID, r.eventType, r.aggregateID,
			strconv.FormatInt(r.aggregateVersion, 10), r.sourceHash, r.subject.AuthorityID, r.subject.TenantID,
			r.subject.SubjectID, r.favoriteID, r.folderID, r.targetType, r.targetID, r.operation, r.predecessor,
			r.eventTime.UTC().Format(time.RFC3339Nano), r.availableAt.UTC().Format(time.RFC3339Nano), r.receivedAt.UTC().Format(time.RFC3339Nano)} {
			fingerprintField(h, []byte(value))
		}
		if r.revision == nil {
			fingerprintField(h, []byte("revision:null"))
		} else {
			fingerprintField(h, []byte("revision:value"))
			fingerprintField(h, []byte(*r.revision))
		}
		fingerprintField(h, r.eventSpec)
		fingerprintField(h, r.receipt)
		if r.producer != Producer || r.offset != last+1 || r.offset > preflightMaxRows {
			addFinding(&report, "ods_prefix_or_producer", 1)
		}
		if r.producer == Producer {
			last = r.offset
		}
		canonical, err := jsoncanonicalizer.Transform(r.eventSpec)
		sum := sha256.Sum256(canonical)
		var event eventing.Event
		var receipt eventing.Receipt
		eventErr := json.Unmarshal(r.eventSpec, &event)
		receiptErr := json.Unmarshal(r.receipt, &receipt)
		var body struct {
			Subject        sourcecoverage.SubjectRef `json:"subject_ref"`
			FavoriteID     json.RawMessage           `json:"favorite_id"`
			FolderID       json.RawMessage           `json:"folder_id"`
			TargetType     string                    `json:"target_type"`
			TargetID       string                    `json:"target_id"`
			TargetRevision *string                   `json:"target_revision"`
			Operation      string                    `json:"operation"`
			EventTime      string                    `json:"event_time"`
			AvailableAt    string                    `json:"available_at"`
		}
		bodyErr := json.Unmarshal(event.Payload, &body)
		favoriteID, favoriteErr := positiveDecimal(body.FavoriteID)
		folderID, folderErr := positiveDecimal(body.FolderID)
		if err != nil || !hashPattern.MatchString(r.sourceHash) || hex.EncodeToString(sum[:]) != r.sourceHash ||
			eventErr != nil || receiptErr != nil || bodyErr != nil ||
			event.Producer != r.producer || event.EventID != r.eventID || event.EventType != r.eventType ||
			event.AggregateID != r.aggregateID || event.AggregateVersion != r.aggregateVersion ||
			event.AggregateID != r.favoriteID || favoriteErr != nil || favoriteID != r.favoriteID ||
			folderErr != nil || folderID != r.folderID || body.Subject != r.subject || body.TargetType != r.targetType ||
			body.TargetID != r.targetID || !sameRevision(body.TargetRevision, r.revision) || body.Operation != r.operation ||
			!pgTimestampMatches(r.eventTime, body.EventTime) || !pgTimestampMatches(r.availableAt, body.AvailableAt) ||
			!pgTimestampMatches(r.receivedAt, receipt.ReceivedAt) ||
			receipt.Producer != r.producer || receipt.EventID != r.eventID || receipt.Offset != r.offset ||
			receipt.InputHash != r.sourceHash || receipt.TechnicalStatus != "accepted" || receipt.ReceiptID == "" {
			addFinding(&report, "immutable_event_or_receipt_mismatch", 1)
		}
		if r.operation == "assert" {
			if r.predecessor != "" || r.eventID != "favorite."+r.favoriteID+".v1" || r.aggregateVersion != 1 || r.eventType != "rtw.favorite.assert" {
				addFinding(&report, "favorite_version_chain_mismatch", 1)
			}
			asserts[r.eventID] = r
		} else if r.operation != "retract" || r.eventID != "favorite."+r.favoriteID+".v2" || r.aggregateVersion != 2 ||
			r.eventType != "rtw.favorite.retract" || r.predecessor != "favorite."+r.favoriteID+".v1" {
			addFinding(&report, "favorite_version_chain_mismatch", 1)
		}
		uid, strict, candidate := projectedUID(r.subject)
		if !candidate {
			addFinding(&report, "invalid_positive_int64_uid", 1)
			continue
		}
		if !strict {
			addFinding(&report, "exceptional_legacy_slot_requires_review", 1)
		}
		favoriteKey := uid + "\x00" + r.favoriteID
		folderTargetKey := uid + "\x00" + r.folderID + "\x00" + r.targetType + "\x00" + r.targetID
		if old, ok := odsFavorite[favoriteKey]; ok && old != r.subject {
			addFinding(&report, "projected_favorite_key_collision", 1)
		} else {
			odsFavorite[favoriteKey] = r.subject
		}
		if old, ok := odsFolderTarget[folderTargetKey]; ok && old != r.subject {
			addFinding(&report, "projected_folder_target_collision", 1)
		} else {
			odsFolderTarget[folderTargetKey] = r.subject
		}
	}
	for _, r := range s.ods {
		if r.operation != "retract" {
			continue
		}
		prior, ok := asserts[r.predecessor]
		if !ok || prior.producer != r.producer || prior.offset >= r.offset || prior.subject != r.subject ||
			prior.favoriteID != r.favoriteID || prior.folderID != r.folderID || prior.targetType != r.targetType ||
			prior.targetID != r.targetID || !sameRevision(prior.revision, r.revision) {
			addFinding(&report, "favorite_predecessor_mismatch", 1)
		}
	}
	if len(s.ods) > preflightMaxRows || s.cursor != last {
		addFinding(&report, "cursor_or_scan_limit", 1)
	}
	for _, b := range s.batches {
		for _, value := range []string{b.consumer, b.producer, strconv.FormatInt(b.from, 10), strconv.FormatInt(b.to, 10), b.hash} {
			fingerprintField(h, []byte(value))
		}
		if b.consumer != DefaultConsumer || b.producer != Producer || b.from < 1 || b.to < b.from || b.to > last || !hashPattern.MatchString(b.hash) {
			addFinding(&report, "batch_evidence_reference_mismatch", 1)
		}
	}
	if len(s.batches) > preflightMaxRows {
		addFinding(&report, "batch_evidence_scan_limit", 1)
	}
	for _, p := range s.publications {
		manifest[p.manifest] = p
		for _, value := range []string{p.manifest, p.generation, p.producer, strconv.FormatInt(p.through, 10), p.indexHash, p.batchHash} {
			fingerprintField(h, []byte(value))
		}
		if p.producer != Producer || p.through < 1 || p.through > last || !coverageGeneration.MatchString(p.generation) || !hashPattern.MatchString(p.manifest) ||
			!hashPattern.MatchString(p.indexHash) || !hashPattern.MatchString(p.batchHash) {
			addFinding(&report, "published_prefix_reference_mismatch", 1)
		}
	}
	if len(s.publications) > preflightMaxRows {
		addFinding(&report, "publication_scan_limit", 1)
	}
	for _, r := range s.receipts {
		for _, value := range []string{r.receiptHash, r.manifest, r.subject.AuthorityID, r.subject.TenantID, r.subject.SubjectID, r.sparseHash, strconv.FormatInt(r.eventCount, 10)} {
			fingerprintField(h, []byte(value))
		}
		if _, ok := manifest[r.manifest]; !ok || !hashPattern.MatchString(r.receiptHash) || !hashPattern.MatchString(r.sparseHash) || r.eventCount < 0 {
			addFinding(&report, "subject_receipt_reference_mismatch", 1)
		}
		uid, strict, candidate := projectedUID(r.subject)
		if !candidate {
			addFinding(&report, "invalid_positive_int64_uid", 1)
			continue
		}
		if !strict {
			addFinding(&report, "exceptional_legacy_slot_requires_review", 1)
		}
		key := r.manifest + "\x00" + uid
		if old, ok := receiptProjection[key]; ok && old != r.subject {
			addFinding(&report, "projected_subject_receipt_collision", 1)
		} else {
			receiptProjection[key] = r.subject
		}
	}
	if len(s.receipts) > preflightMaxRows {
		addFinding(&report, "subject_receipt_scan_limit", 1)
	}
	report.SnapshotSHA256 = hex.EncodeToString(h.Sum(nil))
	return report
}
