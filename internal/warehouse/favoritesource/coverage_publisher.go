package favoritesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/app"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const CoverageBindingPolicyID = "rtw.favorite.authority.v1"

var ErrCoveragePending = errors.New("warehouse global source prefix not complete")
var ErrCoverageConflict = errors.New("warehouse source coverage conflicts with immutable evidence")
var coverageGeneration = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// CoveragePublisher consumes the warehouse-owned ODS and batch evidence. It
// never writes usermodel facts, FeatureBaselines, recommendation labels, or
// the FactWorker's independent DC cursor.
type CoveragePublisher struct {
	DB          *pgxpool.Pool
	Source      Source
	Binder      *app.FavoriteAuthorityBinder
	S3Prefix    string
	HTTPClient  *http.Client
	V2Candidate *SubjectRefV2StorageCandidate
}

type GlobalPublication struct {
	Ref         sourcecoverage.GlobalPrefixRef `json:"global_prefix"`
	ManifestURL string                         `json:"manifest_url"`
}

type SubjectPublication struct {
	Ref        sourcecoverage.SubjectCoverageRef `json:"subject_coverage"`
	ReceiptURL string                            `json:"receipt_url"`
}

type storedCoverageEvent struct {
	offset  int64
	event   eventing.Event
	hash    string
	receipt eventing.Receipt
	subject sourcecoverage.SubjectRef
}

func coverageHash(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func coverageCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsoncanonicalizer.Transform(raw)
}

func (p *CoveragePublisher) check() error {
	if p == nil || p.DB == nil || p.Source == nil || p.Binder == nil {
		return ErrContract
	}
	u, err := url.Parse(p.S3Prefix)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("isolated coverage publisher requires localhost S3 prefix")
	}
	return nil
}
func (p *CoveragePublisher) client() *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if p.HTTPClient != nil {
		copy := *p.HTTPClient
		c = &copy
		if c.Timeout == 0 {
			c.Timeout = 30 * time.Second
		}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}
func (p *CoveragePublisher) objectURL(generation, kind, hash, extension string) string {
	return strings.TrimRight(p.S3Prefix, "/") + "/warehouse-coverage/" + generation + "/" + kind + "/" + hash + extension
}
func (p *CoveragePublisher) get(ctx context.Context, target string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(body) > 32<<20 {
		return nil, resp.StatusCode, ErrCoverageConflict
	}
	return body, resp.StatusCode, nil
}
func (p *CoveragePublisher) putFixed(ctx context.Context, target string, body []byte) error {
	old, status, err := p.get(ctx, target)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		if !bytes.Equal(old, body) {
			return ErrCoverageConflict
		}
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("S3 preflight status %d: %w", status, ErrCoveragePending)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("S3 PUT status %d: %w", resp.StatusCode, ErrCoveragePending)
	}
	old, status, err = p.get(ctx, target)
	if err != nil || status != http.StatusOK || !bytes.Equal(old, body) {
		return ErrCoverageConflict
	}
	return nil
}

// PublishPrefix permits only a committed warehouse consumer prefix ending at
// one recorded DC read-window boundary. Every ODS row is rechecked against DC
// receipt and RTW authoritative EventSpec/SubjectRef before bytes are frozen.
func (p *CoveragePublisher) PublishPrefix(ctx context.Context, through int64, generation string) (GlobalPublication, error) {
	var output GlobalPublication
	if err := p.check(); err != nil {
		return output, err
	}
	if through < 1 || through > 10000 || !coverageGeneration.MatchString(generation) {
		return output, ErrContract
	}
	events, batches, err := p.readFrozen(ctx, through)
	if err != nil {
		return output, err
	}
	index := make([]sourcecoverage.EventIndexRow, 0, len(events))
	for _, stored := range events {
		dcReceipt, err := p.Source.EventReceipt(ctx, Producer, stored.event.EventID)
		if err != nil {
			return output, fmt.Errorf("DC receipt unavailable: %w: %w", ErrCoveragePending, err)
		}
		if !reflect.DeepEqual(dcReceipt, stored.receipt) || dcReceipt.Offset != stored.offset || dcReceipt.InputHash != stored.hash {
			return output, ErrCoverageConflict
		}
		fact, err := p.Binder.BindFactWithEvidence(ctx, app.FactSourceEvidence{Event: stored.event, InputHash: stored.hash, Offset: stored.offset, Receipt: dcReceipt})
		if err != nil {
			return output, fmt.Errorf("RTW source authority: %w: %w", ErrCoverageConflict, err)
		}
		if fact.Subject.AuthorityID != stored.subject.AuthorityID || fact.Subject.TenantID != stored.subject.TenantID || fact.Subject.SubjectID != stored.subject.SubjectID {
			return output, ErrCoverageConflict
		}
		spec, err := json.Marshal(stored.event)
		if err != nil {
			return output, err
		}
		index = append(index, sourcecoverage.EventIndexRow{Producer: Producer, Offset: strconv.FormatInt(stored.offset, 10),
			EventID: stored.event.EventID, InputHash: stored.hash, RTWSourceHash: stored.hash,
			ReceiptID: dcReceipt.ReceiptID, ReceivedAt: dcReceipt.ReceivedAt, Subject: stored.subject, EventSpec: spec})
	}
	indexBody, indexHash, err := sourcecoverage.EventIndexJSONL(Producer, through, index)
	if err != nil {
		return output, err
	}
	batchProof := make([]sourcecoverage.BatchEvidence, 0, len(batches))
	for _, b := range batches {
		batchProof = append(batchProof, sourcecoverage.BatchEvidence{FromOffset: strconv.FormatInt(b.from, 10), ToOffset: strconv.FormatInt(b.to, 10), BatchHash: b.hash})
	}
	if err := sourcecoverage.VerifyBatchEvidence(Producer, index, batchProof); err != nil {
		return output, ErrCoverageConflict
	}
	batchBody, batchHash, err := sourcecoverage.BatchEvidenceJSONL(batchProof, through)
	if err != nil {
		return output, err
	}
	ref := sourcecoverage.GlobalPrefixRef{SchemaVersion: sourcecoverage.SchemaVersion, Producer: Producer, Origin: "1",
		ThroughOffset: strconv.FormatInt(through, 10), BindingPolicyID: CoverageBindingPolicyID,
		WarehouseConsumer: DefaultConsumer, WarehouseGeneration: generation,
		EventIndexURL: p.objectURL(generation, "event-index", indexHash, ".jsonl"), EventIndexSHA256: indexHash,
		BatchEvidenceURL: p.objectURL(generation, "batch-evidence", batchHash, ".jsonl"), BatchEvidenceSHA256: batchHash}
	manifestHash, err := sourcecoverage.GlobalManifestHash(ref)
	if err != nil {
		return output, err
	}
	manifestBody, err := coverageCanonical(ref)
	if err != nil || coverageHash(manifestBody) != manifestHash {
		return output, ErrCoverageConflict
	}
	ref.ManifestSHA256 = manifestHash
	manifestURL := p.objectURL(generation, "manifest", manifestHash, ".json")
	for _, artifact := range []struct {
		url  string
		body []byte
	}{{ref.EventIndexURL, indexBody}, {ref.BatchEvidenceURL, batchBody}, {manifestURL, manifestBody}} {
		if err := p.putFixed(ctx, artifact.url, artifact.body); err != nil {
			return output, err
		}
	}
	if err := p.recordPublication(ctx, ref, through); err != nil {
		return output, err
	}
	return GlobalPublication{Ref: ref, ManifestURL: manifestURL}, nil
}

type storedCoverageBatch struct {
	from, to int64
	hash     string
}

func (p *CoveragePublisher) readFrozen(ctx context.Context, through int64) ([]storedCoverageEvent, []storedCoverageBatch, error) {
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	var committed int64
	if err := tx.QueryRow(ctx, `SELECT committed_offset FROM warehouse_favorite.consumer_cursor WHERE consumer=$1 AND producer=$2`, DefaultConsumer, Producer).Scan(&committed); err != nil || committed < through {
		return nil, nil, ErrCoveragePending
	}
	rows, err := tx.Query(ctx, `SELECT source_offset,event_spec,source_event_hash,technical_receipt,authority_id,tenant_id,subject_id
		FROM warehouse_favorite.ods_event WHERE producer=$1 AND source_offset BETWEEN 1 AND $2 ORDER BY source_offset`, Producer, through)
	if err != nil {
		return nil, nil, err
	}
	events := make([]storedCoverageEvent, 0, through)
	for rows.Next() {
		var e storedCoverageEvent
		var spec, receipt []byte
		if err := rows.Scan(&e.offset, &spec, &e.hash, &receipt, &e.subject.AuthorityID, &e.subject.TenantID, &e.subject.SubjectID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if json.Unmarshal(spec, &e.event) != nil || json.Unmarshal(receipt, &e.receipt) != nil {
			rows.Close()
			return nil, nil, ErrCoverageConflict
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	if int64(len(events)) != through {
		return nil, nil, ErrCoveragePending
	}
	for i, e := range events {
		if e.offset != int64(i+1) {
			return nil, nil, ErrCoveragePending
		}
	}
	batchRows, err := tx.Query(ctx, `SELECT from_offset,to_offset,batch_hash FROM warehouse_favorite.coverage_batch_evidence
		WHERE consumer=$1 AND producer=$2 AND from_offset<=$3 ORDER BY from_offset`, DefaultConsumer, Producer, through)
	if err != nil {
		return nil, nil, err
	}
	batches := []storedCoverageBatch{}
	for batchRows.Next() {
		var b storedCoverageBatch
		if err := batchRows.Scan(&b.from, &b.to, &b.hash); err != nil {
			batchRows.Close()
			return nil, nil, err
		}
		batches = append(batches, b)
	}
	if err := batchRows.Err(); err != nil {
		batchRows.Close()
		return nil, nil, err
	}
	batchRows.Close()
	if len(batches) == 0 || batches[len(batches)-1].to != through {
		return nil, nil, ErrCoveragePending
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return events, batches, nil
}

func (p *CoveragePublisher) recordPublication(ctx context.Context, ref sourcecoverage.GlobalPrefixRef, through int64) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO warehouse_favorite.coverage_publication(manifest_sha256,generation,producer,through_offset,event_index_sha256,batch_evidence_sha256)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, ref.ManifestSHA256, ref.WarehouseGeneration, Producer, through, ref.EventIndexSHA256, ref.BatchEvidenceSHA256)
	if err != nil {
		return err
	}
	var actual sourcecoverage.GlobalPrefixRef
	var gotThrough int64
	err = p.DB.QueryRow(ctx, `SELECT manifest_sha256,generation,producer,through_offset,event_index_sha256,batch_evidence_sha256
		FROM warehouse_favorite.coverage_publication WHERE generation=$1`, ref.WarehouseGeneration).
		Scan(&actual.ManifestSHA256, &actual.WarehouseGeneration, &actual.Producer, &gotThrough, &actual.EventIndexSHA256, &actual.BatchEvidenceSHA256)
	if err != nil || actual.ManifestSHA256 != ref.ManifestSHA256 || actual.Producer != Producer || gotThrough != through ||
		actual.EventIndexSHA256 != ref.EventIndexSHA256 || actual.BatchEvidenceSHA256 != ref.BatchEvidenceSHA256 {
		return ErrCoverageConflict
	}
	return nil
}

func (p *CoveragePublisher) PublishSubject(ctx context.Context, prefix sourcecoverage.GlobalPrefixRef,
	subject sourcecoverage.SubjectRef) (output SubjectPublication, err error) {
	if err := p.check(); err != nil {
		return output, err
	}
	if p.V2Candidate != nil {
		if !p.V2Candidate.readyFor(p.DB) || !validFavoriteV2Subject(subject.AuthorityID, subject.TenantID, subject.SubjectID) {
			return output, ErrContract
		}
	}
	if prefix.BindingPolicyID != CoverageBindingPolicyID || prefix.WarehouseConsumer != DefaultConsumer || prefix.Producer != Producer || prefix.Origin != "1" ||
		!coverageGeneration.MatchString(prefix.WarehouseGeneration) {
		return output, ErrContract
	}
	w, err := strconv.ParseInt(prefix.ThroughOffset, 10, 64)
	if err != nil || w < 1 || w > 10000 {
		return output, ErrContract
	}
	hash, err := sourcecoverage.GlobalManifestHash(prefix)
	if err != nil || hash != prefix.ManifestSHA256 {
		return output, ErrCoverageConflict
	}
	var actualHash string
	if err := p.DB.QueryRow(ctx, `SELECT manifest_sha256 FROM warehouse_favorite.coverage_publication WHERE generation=$1 AND through_offset=$2`, prefix.WarehouseGeneration, w).Scan(&actualHash); err != nil || actualHash != hash {
		return output, ErrCoveragePending
	}
	fullBody, status, err := p.get(ctx, prefix.EventIndexURL)
	if err != nil || status != http.StatusOK || coverageHash(fullBody) != prefix.EventIndexSHA256 {
		return output, ErrCoverageConflict
	}
	var rows []sourcecoverage.EventIndexRow
	for _, line := range bytes.Split(bytes.TrimSuffix(fullBody, []byte{'\n'}), []byte{'\n'}) {
		var row sourcecoverage.EventIndexRow
		if json.Unmarshal(line, &row) != nil {
			return output, ErrCoverageConflict
		}
		rows = append(rows, row)
	}
	check, checkHash, err := sourcecoverage.EventIndexJSONL(Producer, w, rows)
	if err != nil || checkHash != prefix.EventIndexSHA256 || !bytes.Equal(check, fullBody) {
		return output, ErrCoverageConflict
	}
	batchBody, status, err := p.get(ctx, prefix.BatchEvidenceURL)
	if err != nil || status != http.StatusOK || coverageHash(batchBody) != prefix.BatchEvidenceSHA256 {
		return output, ErrCoverageConflict
	}
	batches := []sourcecoverage.BatchEvidence{}
	for _, line := range bytes.Split(bytes.TrimSuffix(batchBody, []byte{'\n'}), []byte{'\n'}) {
		var item sourcecoverage.BatchEvidence
		if json.Unmarshal(line, &item) != nil {
			return output, ErrCoverageConflict
		}
		batches = append(batches, item)
	}
	canonicalBatches, batchHash, err := sourcecoverage.BatchEvidenceJSONL(batches, w)
	if err != nil || batchHash != prefix.BatchEvidenceSHA256 || !bytes.Equal(canonicalBatches, batchBody) ||
		sourcecoverage.VerifyBatchEvidence(Producer, rows, batches) != nil {
		return output, ErrCoverageConflict
	}
	manifestURL := p.objectURL(prefix.WarehouseGeneration, "manifest", prefix.ManifestSHA256, ".json")
	manifestBody, status, err := p.get(ctx, manifestURL)
	withoutHash := prefix
	withoutHash.ManifestSHA256 = ""
	canonicalManifest, canonErr := coverageCanonical(withoutHash)
	if err != nil || canonErr != nil || status != http.StatusOK || coverageHash(manifestBody) != prefix.ManifestSHA256 ||
		!bytes.Equal(manifestBody, canonicalManifest) {
		return output, ErrCoverageConflict
	}
	sparse, sparseHash, count, err := sourcecoverage.SubjectIndexJSONL(subject, rows)
	if err != nil {
		return output, err
	}
	subjectBody, err := coverageCanonical(subject)
	if err != nil {
		return output, err
	}
	subjectPath := coverageHash(subjectBody)
	sparseURL := p.objectURL(prefix.WarehouseGeneration, "subject-index/"+subjectPath, sparseHash, ".jsonl")
	ref := sourcecoverage.SubjectCoverageRef{SchemaVersion: sourcecoverage.SchemaVersion, Subject: subject, Prefix: prefix,
		SparseIndexURL: sparseURL, SparseIndexSHA256: sparseHash, EventCount: count}
	receiptHash, err := sourcecoverage.SubjectReceiptHash(ref)
	if err != nil {
		return output, err
	}
	receiptBody, err := coverageCanonical(ref)
	if err != nil || coverageHash(receiptBody) != receiptHash {
		return output, ErrCoverageConflict
	}
	ref.ReceiptSHA256 = receiptHash
	receiptURL := p.objectURL(prefix.WarehouseGeneration, "subject-receipt", receiptHash, ".json")
	if err := p.putFixed(ctx, sparseURL, sparse); err != nil {
		return output, err
	}
	if err := p.putFixed(ctx, receiptURL, receiptBody); err != nil {
		return output, err
	}
	if p.V2Candidate != nil {
		var stage *telemetry.Stage
		var beginErr error
		ctx, stage, beginErr = p.V2Candidate.begin(ctx, "warehouse.favorite.subjectref_v2.receipt",
			"manifest_sha256", prefix.ManifestSHA256)
		if beginErr != nil {
			return output, beginErr
		}
		defer func() { p.V2Candidate.end(ctx, stage, err, 0) }()
	}
	var receiptTx pgx.Tx
	writer := coverageReceiptWriter(p.DB)
	if p.V2Candidate != nil {
		receiptTx, err = p.DB.Begin(ctx)
		if err != nil {
			return output, err
		}
		defer receiptTx.Rollback(context.Background())
		writer = receiptTx
	}
	_, err = writer.Exec(ctx, `INSERT INTO warehouse_favorite.coverage_subject_receipt
		(receipt_sha256,manifest_sha256,authority_id,tenant_id,subject_id,sparse_index_sha256,event_count)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, ref.ReceiptSHA256, prefix.ManifestSHA256,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, sparseHash, count)
	if err != nil {
		return output, err
	}
	var oldHash, oldSparse string
	var oldCount int64
	lookup := `SELECT receipt_sha256,sparse_index_sha256,event_count FROM warehouse_favorite.coverage_subject_receipt
		WHERE manifest_sha256=$1 AND authority_id=$2 AND tenant_id=$3 AND subject_id=$4`
	if p.V2Candidate != nil {
		lookup += ` FOR SHARE`
	}
	err = writer.QueryRow(ctx, lookup, prefix.ManifestSHA256,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&oldHash, &oldSparse, &oldCount)
	if err != nil || oldHash != ref.ReceiptSHA256 || oldSparse != sparseHash || oldCount != count {
		return output, ErrCoverageConflict
	}
	if p.V2Candidate != nil {
		if err = p.V2Candidate.projectReceipt(ctx, receiptTx, ref.ReceiptSHA256, prefix.ManifestSHA256,
			subject.AuthorityID, subject.TenantID, subject.SubjectID); err != nil {
			return output, err
		}
		if err = receiptTx.Commit(ctx); err != nil {
			return output, err
		}
	}
	return SubjectPublication{Ref: ref, ReceiptURL: receiptURL}, nil
}
