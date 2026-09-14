package usermodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/sourcecoverage"
	"github.com/jackc/pgx/v5"
)

const coveredSnapshotStatus = "historical_default_off"

var ErrCoveredSnapshotInvalid = errors.New("covered historical snapshot contract mismatch")
var ErrCoveredSnapshotConflict = errors.New("covered historical snapshot conflicts with immutable source")
var ErrCoveredSnapshotPending = errors.New("covered historical source is not yet verified")

// CoveredHistoricalSnapshot freezes the accepted W-state, not a current
// serving pointer. CurrentCompleteAtBuild records the original H10 candidate
// and must never be used as a fresh DC watermark or pair authorization.
type CoveredHistoricalSnapshot struct {
	ID                     string                            `json:"snapshot_id"`
	Status                 string                            `json:"status"`
	Subject                SubjectRef                        `json:"subject_ref"`
	BaselineRevision       int64                             `json:"baseline_revision"`
	Generation             string                            `json:"baseline_generation"`
	BaselineArtifactSHA256 string                            `json:"baseline_artifact_sha256"`
	Coverage               sourcecoverage.SubjectCoverageRef `json:"subject_coverage"`
	Spec                   FeatureSpec                       `json:"feature_spec"`
	SpecHash               string                            `json:"feature_spec_hash"`
	AsOf                   time.Time                         `json:"as_of"`
	AvailableAt            time.Time                         `json:"available_at"`
	InputStateVersion      int64                             `json:"input_state_version_at_build"`
	CurrentCompleteAtBuild bool                              `json:"current_complete_at_build"`
	Contributions          []Fact                            `json:"active_at_prefix"`
	TailAtBuild            []coveredBaselineTail             `json:"tail_at_build"`
	Values                 []FeatureValue                    `json:"historical_values"`
	Replay                 bool                              `json:"-"`
}

func coveredSnapshotHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func freezeCoveredSnapshot(snapshot *CoveredHistoricalSnapshot) ([]byte, string, error) {
	if snapshot == nil || snapshot.ID != "" || snapshot.Status != coveredSnapshotStatus || !snapshot.Subject.valid() ||
		snapshot.BaselineRevision < 1 || !digest.MatchString(snapshot.BaselineArtifactSHA256) ||
		snapshot.Coverage.Subject != sourcecoverage.SubjectRef(snapshot.Subject) {
		return nil, "", ErrCoveredSnapshotInvalid
	}
	without, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	snapshot.ID = coveredSnapshotHash(without)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	return raw, coveredSnapshotHash(raw), nil
}

func decodeCoveredSnapshot(raw []byte, rawHash, id string, subject SubjectRef) (CoveredHistoricalSnapshot, error) {
	var snapshot CoveredHistoricalSnapshot
	if len(raw) == 0 || len(raw) > 32<<20 || coveredSnapshotHash(raw) != rawHash || !digest.MatchString(id) {
		return snapshot, ErrCoveredSnapshotConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&snapshot) != nil || decoder.Decode(new(any)) != io.EOF {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	if snapshot.ID != id || snapshot.Status != coveredSnapshotStatus || snapshot.Subject != subject ||
		snapshot.Coverage.Subject != sourcecoverage.SubjectRef(subject) || snapshot.BaselineRevision < 1 {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	check := snapshot
	check.ID = ""
	check.Replay = false
	body, err := json.Marshal(check)
	if err != nil || coveredSnapshotHash(body) != id {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	spec, specHash, err := PrepareFeatureSpec(snapshot.Spec)
	if err != nil || specHash != snapshot.SpecHash || !reflect.DeepEqual(spec, snapshot.Spec) {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	if receiptHash, err := sourcecoverage.SubjectReceiptHash(snapshot.Coverage); err != nil || receiptHash != snapshot.Coverage.ReceiptSHA256 {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	return snapshot, nil
}

// FreezeCoveredSnapshot consumes exact accepted candidate bytea and receipt.
// It rechecks W-history in one repeatable-read transaction but deliberately
// ignores later tail changes: an old W1 assert remains a historical value 1.
func (s *Store) FreezeCoveredSnapshot(ctx context.Context, subject SubjectRef, revision int64, inputSpec FeatureSpec) (CoveredHistoricalSnapshot, error) {
	if s == nil || s.db == nil || !subject.valid() || revision < 1 {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotInvalid
	}
	spec, specHash, err := PrepareFeatureSpec(inputSpec)
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	var artifactURL, artifactHash, status, generation, rowSpecVersion, rowSpecHash, prefixHash, subjectHash string
	var candidateRaw []byte
	var stateVersion int64
	err = tx.QueryRow(ctx, `SELECT artifact_url,artifact_sha256,candidate_raw,status,generation,spec_version,spec_hash,
		prefix_manifest_sha256,subject_receipt_sha256,input_state_version FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, revision).
		Scan(&artifactURL, &artifactHash, &candidateRaw, &status, &generation, &rowSpecVersion, &rowSpecHash, &prefixHash, &subjectHash, &stateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoveredHistoricalSnapshot{}, ErrNotFound
	}
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	if status != coveredBaselineStoredStatus || rowSpecVersion != spec.Version || rowSpecHash != specHash ||
		!digest.MatchString(artifactHash) || coveredSnapshotHash(candidateRaw) != artifactHash {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	candidate, _, err := decodeCoveredBaseline(CoveredBaselineSubmission{ArtifactURL: artifactURL, ArtifactSHA256: artifactHash,
		CandidateJSON: candidateRaw, FeatureSpec: spec})
	if err != nil {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	if candidate.Subject != subject || candidate.Revision != revision || candidate.Generation != generation ||
		candidate.InputStateVersion != stateVersion || candidate.Coverage.Prefix.ManifestSHA256 != prefixHash ||
		candidate.Coverage.ReceiptSHA256 != subjectHash {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	state, err := s.coveredStateAtTx(ctx, tx, subject, candidate.Coverage.Prefix, candidate.AsOf)
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	if state.Coverage != candidate.Coverage || !coveredBaselineContributionsEqual(state.ActiveAtPrefix, candidate.Contributions) {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotPending
	}
	snapshot := CoveredHistoricalSnapshot{Status: coveredSnapshotStatus, Subject: subject, BaselineRevision: revision,
		Generation: generation, BaselineArtifactSHA256: artifactHash, Coverage: candidate.Coverage, Spec: spec, SpecHash: specHash,
		AsOf: candidate.AsOf, AvailableAt: candidate.AvailableAt, InputStateVersion: candidate.InputStateVersion,
		CurrentCompleteAtBuild: candidate.CurrentComplete, Contributions: candidate.Contributions, TailAtBuild: candidate.Tail, Values: candidate.Values}
	raw, rawHash, err := freezeCoveredSnapshot(&snapshot)
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	insert, err := tx.Exec(ctx, `INSERT INTO usermodel_covered_snapshots_v2
		(authority_id,tenant_id,subject_id,snapshot_id,baseline_revision,baseline_artifact_sha256,
		 prefix_manifest_sha256,subject_receipt_sha256,spec_version,spec_hash,input_state_version,
		 snapshot_raw_sha256,snapshot_raw,snapshot_body,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, snapshot.ID, revision, artifactHash, prefixHash, subjectHash,
		spec.Version, specHash, stateVersion, rawHash, raw, raw, coveredSnapshotStatus)
	if err != nil {
		return CoveredHistoricalSnapshot{}, coveredBaselineDBError(err)
	}
	var savedID, savedRawHash string
	var savedRaw []byte
	err = tx.QueryRow(ctx, `SELECT snapshot_id,snapshot_raw_sha256,snapshot_raw FROM usermodel_covered_snapshots_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND baseline_revision=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, revision).Scan(&savedID, &savedRawHash, &savedRaw)
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	if savedID != snapshot.ID || savedRawHash != rawHash || !bytes.Equal(savedRaw, raw) {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotConflict
	}
	// The row is immutable. A repeated freeze returns the same ID and bytes.
	snapshot.Replay = insert.RowsAffected() == 0
	if err := tx.Commit(ctx); err != nil {
		return CoveredHistoricalSnapshot{}, coveredBaselineDBError(err)
	}
	return snapshot, nil
}

// CoveredSnapshotV2ByID is historical by design and never implies current
// eligibility or a product-approved pair.
func (s *Store) CoveredSnapshotV2ByID(ctx context.Context, subject SubjectRef, id string) (CoveredHistoricalSnapshot, error) {
	if s == nil || s.db == nil || !subject.valid() || !digest.MatchString(id) {
		return CoveredHistoricalSnapshot{}, ErrCoveredSnapshotInvalid
	}
	var raw []byte
	var rawHash string
	err := s.db.QueryRow(ctx, `SELECT snapshot_raw,snapshot_raw_sha256 FROM usermodel_covered_snapshots_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND snapshot_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, id).Scan(&raw, &rawHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoveredHistoricalSnapshot{}, ErrNotFound
	}
	if err != nil {
		return CoveredHistoricalSnapshot{}, err
	}
	return decodeCoveredSnapshot(raw, rawHash, id, subject)
}
