package usermodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/jackc/pgx/v5"
)

const FavoriteCoveragePolicyID = "rtw.favorite.authority.v1"
const favoriteCoverageProducer = "rtw.community.favorite"
const maxCoverageRows = 100000

var ErrCoverageUnverified = errors.New("source coverage has not been verified")
var ErrCoveragePending = errors.New("source coverage lacks accepted fact and Outbox")
var ErrCoverageConflict = errors.New("source coverage conflicts with immutable source or domain fact")

// CoverageProofSource is the remote-owned seam. A real adapter reads the DC
// immutable receipt and RTW frozen source; a fixed adapter tests Verifier's
// own global/sparse/PG rules without simulating the two protocols here.
type CoverageProofSource interface {
	VerifyEvent(context.Context, sourcecoverage.EventIndexRow) error
}

type CoverageVerifier struct {
	store *Store
	proof CoverageProofSource
}

type CoverageReceipt struct {
	Prefix sourcecoverage.GlobalPrefixRef
	Replay bool
}

func NewCoverageVerifier(store *Store, proof CoverageProofSource) (*CoverageVerifier, error) {
	if store == nil || store.db == nil || proof == nil {
		return nil, ErrInvalid
	}
	return &CoverageVerifier{store: store, proof: proof}, nil
}

func coverageLines[T any](body []byte, maxRows int) ([]T, error) {
	if len(body) == 0 || len(body) > 64<<20 || body[len(body)-1] != '\n' {
		return nil, ErrCoverageConflict
	}
	lines := bytes.Split(body[:len(body)-1], []byte{'\n'})
	if len(lines) == 0 || len(lines) > maxRows {
		return nil, ErrCoverageConflict
	}
	values := make([]T, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			return nil, ErrCoverageConflict
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var value T
		if err := decoder.Decode(&value); err != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, ErrCoverageConflict
		}
		values = append(values, value)
	}
	return values, nil
}

func coverageHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyPrefix is the first full [1,W] check. A cached manifest may replay
// only after its supplied immutable bytes and references still hash exactly.
// The cache never turns legacy per-subject watermarks into global coverage.
func (v *CoverageVerifier) VerifyPrefix(ctx context.Context, ref sourcecoverage.GlobalPrefixRef,
	indexJSONL, batchesJSONL []byte) (CoverageReceipt, error) {
	if v == nil || v.store == nil || v.proof == nil || ref.SchemaVersion != sourcecoverage.SchemaVersion ||
		ref.Producer != favoriteCoverageProducer || ref.BindingPolicyID != FavoriteCoveragePolicyID ||
		ref.Origin != "1" {
		return CoverageReceipt{}, ErrCoverageUnverified
	}
	through, err := strconv.ParseInt(ref.ThroughOffset, 10, 64)
	if err != nil || through < 1 || through > maxCoverageRows {
		return CoverageReceipt{}, ErrCoverageUnverified
	}
	manifestHash, err := sourcecoverage.GlobalManifestHash(ref)
	if err != nil || manifestHash != ref.ManifestSHA256 {
		return CoverageReceipt{}, ErrCoverageConflict
	}
	rows, err := coverageLines[sourcecoverage.EventIndexRow](indexJSONL, maxCoverageRows)
	if err != nil {
		return CoverageReceipt{}, err
	}
	canonicalIndex, indexHash, err := sourcecoverage.EventIndexJSONL(ref.Producer, through, rows)
	if err != nil || indexHash != ref.EventIndexSHA256 || !bytes.Equal(canonicalIndex, indexJSONL) {
		return CoverageReceipt{}, ErrCoverageConflict
	}
	batches, err := coverageLines[sourcecoverage.BatchEvidence](batchesJSONL, maxCoverageRows)
	if err != nil {
		return CoverageReceipt{}, err
	}
	canonicalBatches, batchHash, err := sourcecoverage.BatchEvidenceJSONL(batches, through)
	if err != nil || batchHash != ref.BatchEvidenceSHA256 || !bytes.Equal(canonicalBatches, batchesJSONL) ||
		sourcecoverage.VerifyBatchEvidence(ref.Producer, rows, batches) != nil {
		return CoverageReceipt{}, ErrCoverageConflict
	}
	replay, err := cachedCoveragePrefix(ctx, v.store.db, ref, through)
	if err != nil {
		return CoverageReceipt{}, err
	}
	if replay {
		return CoverageReceipt{Prefix: ref, Replay: true}, nil
	}
	// The remote immutable-source walk must not pin a PG transaction snapshot.
	// PG fact/Outbox checking and proof-cache inserts happen together below.
	for _, row := range rows {
		if err := v.proof.VerifyEvent(ctx, row); err != nil {
			return CoverageReceipt{}, fmt.Errorf("verify DC/RTW source offset %s: %w", row.Offset, err)
		}
	}
	tx, err := v.store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return CoverageReceipt{}, err
	}
	defer tx.Rollback(ctx)
	replay, err = cachedCoveragePrefix(ctx, tx, ref, through)
	if err != nil {
		return CoverageReceipt{}, err
	}
	if replay {
		return CoverageReceipt{Prefix: ref, Replay: true}, tx.Commit(ctx)
	}
	var oldHash string
	err = tx.QueryRow(ctx, `SELECT event_index_sha256 FROM usermodel_coverage_prefix
		WHERE producer=$1 AND binding_policy_id=$2 AND through_offset=$3 LIMIT 1`,
		ref.Producer, ref.BindingPolicyID, through).Scan(&oldHash)
	if err == nil && oldHash != ref.EventIndexSHA256 {
		return CoverageReceipt{}, ErrCoverageConflict
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CoverageReceipt{}, err
	}
	refBody, err := json.Marshal(ref)
	if err != nil {
		return CoverageReceipt{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usermodel_coverage_prefix
		(manifest_sha256,producer,binding_policy_id,through_offset,event_index_sha256,batch_evidence_sha256,ref_body)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, ref.ManifestSHA256, ref.Producer, ref.BindingPolicyID,
		through, ref.EventIndexSHA256, ref.BatchEvidenceSHA256, refBody); err != nil {
		return CoverageReceipt{}, err
	}
	for _, row := range rows {
		fact, err := verifyCoveredFact(ctx, tx, row)
		if err != nil {
			return CoverageReceipt{}, err
		}
		position, _ := strconv.ParseInt(row.Offset, 10, 64)
		rowBody, _ := json.Marshal(row)
		if _, err := tx.Exec(ctx, `INSERT INTO usermodel_coverage_event
			(manifest_sha256,source_offset,producer,event_id,input_hash,authority_id,tenant_id,subject_id,
			 normalized_hash,accepted_version,row_body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			ref.ManifestSHA256, position, row.Producer, row.EventID, row.InputHash,
			row.Subject.AuthorityID, row.Subject.TenantID, row.Subject.SubjectID,
			fact.NormalizedHash, fact.AcceptedVersion, rowBody); err != nil {
			return CoverageReceipt{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CoverageReceipt{}, err
	}
	return CoverageReceipt{Prefix: ref}, nil
}

type coveragePrefixQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func cachedCoveragePrefix(ctx context.Context, q coveragePrefixQueryer,
	ref sourcecoverage.GlobalPrefixRef, through int64) (bool, error) {
	var existingBody []byte
	var count int64
	err := q.QueryRow(ctx, `SELECT p.ref_body,
		(SELECT COUNT(*) FROM usermodel_coverage_event e WHERE e.manifest_sha256=p.manifest_sha256)
		FROM usermodel_coverage_prefix p WHERE p.manifest_sha256=$1`, ref.ManifestSHA256).
		Scan(&existingBody, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var existing sourcecoverage.GlobalPrefixRef
	if count != through || json.Unmarshal(existingBody, &existing) != nil || !reflect.DeepEqual(existing, ref) {
		return false, ErrCoverageConflict
	}
	return true, nil
}

func verifyCoveredFact(ctx context.Context, tx pgx.Tx, row sourcecoverage.EventIndexRow) (Fact, error) {
	var duplicates int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM usermodel_events WHERE producer=$1 AND event_id=$2`,
		row.Producer, row.EventID).Scan(&duplicates); err != nil {
		return Fact{}, err
	}
	if duplicates == 0 {
		return Fact{}, ErrCoveragePending
	}
	if duplicates != 1 {
		return Fact{}, ErrCoverageConflict
	}
	var body []byte
	var fact Fact
	var acceptedVersion *int64
	var outboxID *int64
	err := tx.QueryRow(ctx, `SELECT e.event_body,e.normalized_hash,e.status,e.accepted_version,o.outbox_id
		FROM usermodel_events e LEFT JOIN usermodel_outbox o ON
			o.authority_id=e.authority_id AND o.tenant_id=e.tenant_id AND o.subject_id=e.subject_id
			AND o.producer=e.producer AND o.event_id=e.event_id AND o.state_version=e.accepted_version
			AND o.event_type='usermodel.fact.accepted'
		WHERE e.authority_id=$1 AND e.tenant_id=$2 AND e.subject_id=$3 AND e.producer=$4 AND e.event_id=$5`,
		row.Subject.AuthorityID, row.Subject.TenantID, row.Subject.SubjectID, row.Producer, row.EventID).
		Scan(&body, &fact.NormalizedHash, &fact.Status, &acceptedVersion, &outboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Fact{}, ErrCoverageConflict
	}
	if err != nil {
		return Fact{}, err
	}
	if fact.Status != "accepted" || acceptedVersion == nil || *acceptedVersion <= 0 || outboxID == nil {
		return Fact{}, ErrCoveragePending
	}
	fact.AcceptedVersion = *acceptedVersion
	if err := json.Unmarshal(body, &fact.Event); err != nil {
		return Fact{}, ErrCoverageConflict
	}
	fact.Subject = SubjectRef(row.Subject)
	position, _ := strconv.ParseInt(row.Offset, 10, 64)
	if fact.EventKey != (EventKey{Producer: row.Producer, EventID: row.EventID}) ||
		fact.SourceSequence != position || fact.SourcePartition != "dc:"+row.Producer ||
		fact.EvidenceHash != row.InputHash || fact.EvidenceRef != "dc:event:"+row.Producer+":"+row.Offset {
		return Fact{}, ErrCoverageConflict
	}
	received, err := time.Parse(time.RFC3339Nano, row.ReceivedAt)
	if err != nil || !fact.ObservedAt.Equal(received) {
		return Fact{}, ErrCoverageConflict
	}
	_, normalizedHash, _, err := normalized(fact.Event, true)
	if err != nil || normalizedHash != fact.NormalizedHash || !coverageFavoriteSemantic(row.EventSpec, fact.Event) {
		return Fact{}, ErrCoverageConflict
	}
	return fact, nil
}

func coverageFavoriteSemantic(raw json.RawMessage, fact Event) bool {
	var source eventing.Event
	if json.Unmarshal(raw, &source) != nil || source.Producer != favoriteCoverageProducer ||
		source.EventID != fact.EventID || source.SchemaVersion != 1 || source.OccurredAt == "" {
		return false
	}
	occurred, err := time.Parse(time.RFC3339Nano, source.OccurredAt)
	if err != nil || !occurred.Equal(fact.OccurredAt) {
		return false
	}
	var payload struct {
		TargetType     string  `json:"target_type"`
		TargetID       string  `json:"target_id"`
		TargetRevision *string `json:"target_revision"`
		Operation      string  `json:"operation"`
	}
	if json.Unmarshal(source.Payload, &payload) != nil || payload.TargetType == "" || payload.TargetID == "" ||
		fact.Kind != ProductAction || fact.Predicate != "favorite" || fact.ItemID != payload.TargetID {
		return false
	}
	valueRef := payload.TargetType + "/" + payload.TargetID
	if payload.TargetRevision != nil {
		valueRef += "/revision/" + *payload.TargetRevision
	}
	if fact.ValueRef != valueRef {
		return false
	}
	switch source.EventType {
	case "rtw.favorite.assert":
		return payload.Operation == "assert" && fact.Action == Assert && fact.Supersedes == nil
	case "rtw.favorite.retract":
		return payload.Operation == "retract" && fact.Action == Retract && fact.Supersedes != nil &&
			fact.Supersedes.Producer == source.Producer &&
			fact.Supersedes.EventID == "favorite."+source.AggregateID+".v1"
	default:
		return false
	}
}
