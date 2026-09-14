package usermodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const coveredBundleStatus = "candidate_default_off"

var ErrCoveredBundleInvalid = errors.New("covered fixed bundle candidate contract mismatch")
var ErrCoveredBundleConflict = errors.New("covered fixed bundle candidate pair or source conflict")
var ErrCoveredBundlePending = errors.New("covered fixed bundle candidate is not current and complete")

const coveredPairID = "candidate-favorite-count-v2"
const coveredSpaceID = "candidate-favorite-count-space-v2"
const coveredEncoderID = "fixed-favorite-count-v2"

// CoveredFixedCandidatePair is a deterministic *unapproved* PairRef for a
// single reversible favorite count. It is not recommend's PairAuthorization.
func CoveredFixedCandidatePair(input FeatureSpec) (PairRef, error) {
	spec, specHash, err := PrepareFeatureSpec(input)
	if err != nil {
		return PairRef{}, err
	}
	if len(spec.Features) != 1 {
		return PairRef{}, ErrCoveredBundleInvalid
	}
	f := spec.Features[0]
	if f.Name != "favorite_active_count" || f.Source != "fact" || f.Kind != ProductAction || f.Predicate != "favorite" ||
		f.Mode != "count" || f.Default != "0" || f.WindowSeconds != 0 || len(f.Vocabulary) != 0 || f.OOV != "" {
		return PairRef{}, ErrCoveredBundleInvalid
	}
	return PairRef{PairID: coveredPairID, SpaceID: coveredSpaceID, EncoderID: coveredEncoderID,
		Kind: "fixed_baseline", FeatureSpecVersion: spec.Version, FeatureSpecHash: specHash, Dimension: 1, Metric: "dot"}, nil
}

type CoveredBundleCandidateV2 struct {
	ID                     string     `json:"bundle_id"`
	Status                 string     `json:"status"`
	Subject                SubjectRef `json:"subject_ref"`
	Pair                   PairRef    `json:"pair"`
	CoveredSnapshotID      string     `json:"covered_snapshot_id"`
	BaselineRevision       int64      `json:"baseline_revision"`
	BaselineArtifactSHA256 string     `json:"baseline_artifact_sha256"`
	CoverageReceiptSHA256  string     `json:"coverage_receipt_sha256"`
	InputStateVersion      int64      `json:"input_state_version"`
	EncodingSource         string     `json:"encoding_source"`
	Vector                 []float64  `json:"user_vector"`
	Replay                 bool       `json:"-"`
}

func freezeCoveredBundle(bundle *CoveredBundleCandidateV2) ([]byte, string, error) {
	if bundle == nil || bundle.ID != "" || bundle.Status != coveredBundleStatus || !bundle.Subject.valid() ||
		!bundle.Pair.valid() || !digest.MatchString(bundle.CoveredSnapshotID) || len(bundle.Vector) != 1 || bundle.Vector[0] < 0 {
		return nil, "", ErrCoveredBundleInvalid
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		return nil, "", err
	}
	bundle.ID = coveredSnapshotHash(body)
	raw, err := json.Marshal(bundle)
	if err != nil {
		return nil, "", err
	}
	return raw, coveredSnapshotHash(raw), nil
}

func decodeCoveredBundle(raw []byte, rawHash, id string, subject SubjectRef) (CoveredBundleCandidateV2, error) {
	var bundle CoveredBundleCandidateV2
	if len(raw) == 0 || len(raw) > 1<<20 || coveredSnapshotHash(raw) != rawHash || !digest.MatchString(id) {
		return bundle, ErrCoveredBundleConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&bundle) != nil || decoder.Decode(new(any)) != io.EOF {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	if bundle.ID != id || bundle.Status != coveredBundleStatus || bundle.Subject != subject || !bundle.Pair.valid() ||
		bundle.EncodingSource != "fixed_baseline_candidate" || !digest.MatchString(bundle.CoveredSnapshotID) || len(bundle.Vector) != 1 || bundle.Vector[0] < 0 {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	check := bundle
	check.ID = ""
	check.Replay = false
	body, err := json.Marshal(check)
	if err != nil || coveredSnapshotHash(body) != id {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	return bundle, nil
}

func coveredSnapshotByIDTx(ctx context.Context, tx pgx.Tx, subject SubjectRef, id string) (CoveredHistoricalSnapshot, error) {
	var raw []byte
	var rawHash string
	err := tx.QueryRow(ctx, `SELECT snapshot_raw,snapshot_raw_sha256 FROM usermodel_covered_snapshots_v2
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

// BuildFixedCoveredBundleCandidate locks current subject state in one PG
// repeatable-read transaction. An accepted W1 history may still be read by ID,
// but a later withdraw or pending tail cannot produce a current candidate.
// No active head/pointer or PairAuthorization is created here.
func (s *Store) BuildFixedCoveredBundleCandidate(ctx context.Context, subject SubjectRef, snapshotID string,
	pair PairRef) (CoveredBundleCandidateV2, error) {
	if s == nil || s.db == nil || !subject.valid() || !digest.MatchString(snapshotID) {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_subject_state(authority_id,tenant_id,subject_id)
		VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, subject.AuthorityID, subject.TenantID, subject.SubjectID)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	var lockedVersion int64
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&lockedVersion)
	if err != nil {
		return CoveredBundleCandidateV2{}, coveredBundleDBError(err)
	}
	snapshot, err := coveredSnapshotByIDTx(ctx, tx, subject, snapshotID)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	expectedPair, err := CoveredFixedCandidatePair(snapshot.Spec)
	if err != nil || pair != expectedPair {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	var latestRevision int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`, subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&latestRevision)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	if snapshot.BaselineRevision != latestRevision || snapshot.InputStateVersion != lockedVersion || snapshot.AsOf.After(time.Now().UTC()) {
		return CoveredBundleCandidateV2{}, ErrCoveredBundlePending
	}
	var baselineRaw []byte
	var baselineHash, prefixHash, subjectHash, status string
	err = tx.QueryRow(ctx, `SELECT candidate_raw,artifact_sha256,prefix_manifest_sha256,subject_receipt_sha256,status
		FROM usermodel_covered_baselines_v2 WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, snapshot.BaselineRevision).
		Scan(&baselineRaw, &baselineHash, &prefixHash, &subjectHash, &status)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	if status != coveredBaselineStoredStatus || baselineHash != snapshot.BaselineArtifactSHA256 ||
		coveredSnapshotHash(baselineRaw) != baselineHash || prefixHash != snapshot.Coverage.Prefix.ManifestSHA256 ||
		subjectHash != snapshot.Coverage.ReceiptSHA256 {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	state, err := s.coveredStateAtTx(ctx, tx, subject, snapshot.Coverage.Prefix, now)
	if err != nil {
		return CoveredBundleCandidateV2{}, coveredBundleDBError(err)
	}
	if state.Coverage != snapshot.Coverage || state.StateVersion != lockedVersion || !state.CurrentComplete || len(state.Tail) != 0 ||
		!coveredBaselineContributionsEqual(state.ActiveAtPrefix, snapshot.Contributions) {
		return CoveredBundleCandidateV2{}, ErrCoveredBundlePending
	}
	values, err := featureValues(snapshot.Spec, state.ActiveAtPrefix, nil, now, now)
	if err != nil || !reflect.DeepEqual(values, snapshot.Values) || len(values) != 1 {
		return CoveredBundleCandidateV2{}, ErrCoveredBundlePending
	}
	count, err := strconv.ParseInt(values[0].Value, 10, 64)
	if err != nil || count < 0 || count > 9007199254740991 {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	bundle := CoveredBundleCandidateV2{Status: coveredBundleStatus, Subject: subject, Pair: pair, CoveredSnapshotID: snapshot.ID,
		BaselineRevision: snapshot.BaselineRevision, BaselineArtifactSHA256: baselineHash,
		CoverageReceiptSHA256: snapshot.Coverage.ReceiptSHA256, InputStateVersion: lockedVersion,
		EncodingSource: "fixed_baseline_candidate", Vector: []float64{float64(count)}}
	raw, rawHash, err := freezeCoveredBundle(&bundle)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	insert, err := tx.Exec(ctx, `INSERT INTO usermodel_covered_bundle_candidates_v2
		(authority_id,tenant_id,subject_id,bundle_id,snapshot_id,pair_id,space_id,encoder_id,pair_feature_spec_hash,
		 bundle_raw_sha256,bundle_raw,bundle_body,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, bundle.ID, snapshot.ID, pair.PairID, pair.SpaceID, pair.EncoderID,
		pair.FeatureSpecHash, rawHash, raw, raw, coveredBundleStatus)
	if err != nil {
		return CoveredBundleCandidateV2{}, coveredBundleDBError(err)
	}
	var savedID, savedRawHash string
	var savedRaw []byte
	err = tx.QueryRow(ctx, `SELECT bundle_id,bundle_raw_sha256,bundle_raw FROM usermodel_covered_bundle_candidates_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND snapshot_id=$4 AND pair_id=$5`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, snapshot.ID, pair.PairID).Scan(&savedID, &savedRawHash, &savedRaw)
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	if savedID != bundle.ID || savedRawHash != rawHash || !bytes.Equal(savedRaw, raw) {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleConflict
	}
	bundle.Replay = insert.RowsAffected() == 0
	if err := tx.Commit(ctx); err != nil {
		return CoveredBundleCandidateV2{}, coveredBundleDBError(err)
	}
	return bundle, nil
}

// CoveredBundleCandidateByID is historical only. ReadyServingBundle continues
// to use the legacy v1 pointer and cannot see this v2 candidate table.
func (s *Store) CoveredBundleCandidateByID(ctx context.Context, subject SubjectRef, id string) (CoveredBundleCandidateV2, error) {
	if s == nil || s.db == nil || !subject.valid() || !digest.MatchString(id) {
		return CoveredBundleCandidateV2{}, ErrCoveredBundleInvalid
	}
	var raw []byte
	var rawHash string
	err := s.db.QueryRow(ctx, `SELECT bundle_raw,bundle_raw_sha256 FROM usermodel_covered_bundle_candidates_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND bundle_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, id).Scan(&raw, &rawHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoveredBundleCandidateV2{}, ErrNotFound
	}
	if err != nil {
		return CoveredBundleCandidateV2{}, err
	}
	return decodeCoveredBundle(raw, rawHash, id, subject)
}

func coveredBundleDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40001" {
		return fmt.Errorf("%w: concurrent subject fact changed", ErrCoveredBundlePending)
	}
	return err
}
