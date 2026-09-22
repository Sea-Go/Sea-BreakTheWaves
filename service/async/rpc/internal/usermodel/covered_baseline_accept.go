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
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const coveredBaselineSchema = "sea.user-feature-baseline.covered.v2"
const coveredBaselineStoredStatus = "accepted_historical_default_off"

var ErrCoveredBaselineInvalid = errors.New("covered baseline candidate contract mismatch")
var ErrCoveredBaselineConflict = errors.New("covered baseline revision or source conflict")
var ErrCoveredBaselinePending = errors.New("covered baseline source changed during acceptance")

// CoveredBaselineSubmission carries the exact H10 artifact bytes. The Store
// verifies their hash and PG history; H10's object Adapter owns S3 readback.
type CoveredBaselineSubmission struct {
	ArtifactURL    string
	ArtifactSHA256 string
	CandidateJSON  []byte
	FeatureSpec    FeatureSpec
}

type CoveredBaselineReceipt struct {
	Subject           SubjectRef                        `json:"subject_ref"`
	Revision          int64                             `json:"revision"`
	Generation        string                            `json:"generation"`
	ArtifactSHA256    string                            `json:"artifact_sha256"`
	Coverage          sourcecoverage.SubjectCoverageRef `json:"subject_coverage"`
	InputStateVersion int64                             `json:"input_state_version"`
	Status            string                            `json:"status"`
	Replay            bool                              `json:"replay"`
}

type coveredBaselineTail struct {
	EventKey
	Offset int64  `json:"offset"`
	Status string `json:"status"`
}

// coveredBaselineCandidate mirrors H10's frozen JSON wire without importing
// featurebaseline back into usermodel. Unknown fields and changed shape fail.
type coveredBaselineCandidate struct {
	SchemaVersion     string                            `json:"schema_version"`
	Status            string                            `json:"status"`
	Subject           SubjectRef                        `json:"subject_ref"`
	Generation        string                            `json:"generation"`
	Revision          int64                             `json:"revision"`
	SpecVersion       string                            `json:"feature_spec_version"`
	SpecHash          string                            `json:"feature_spec_hash"`
	Coverage          sourcecoverage.SubjectCoverageRef `json:"subject_coverage"`
	AsOf              time.Time                         `json:"as_of"`
	AvailableAt       time.Time                         `json:"available_at"`
	InputStateVersion int64                             `json:"input_state_version"`
	CurrentComplete   bool                              `json:"current_complete"`
	Contributions     []Fact                            `json:"contributions"`
	Tail              []coveredBaselineTail             `json:"tail"`
	Values            []FeatureValue                    `json:"values"`
}

func decodeCoveredBaseline(input CoveredBaselineSubmission) (coveredBaselineCandidate, FeatureSpec, error) {
	var candidate coveredBaselineCandidate
	if len(input.CandidateJSON) == 0 || len(input.CandidateJSON) > 32<<20 || !digest.MatchString(input.ArtifactSHA256) {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	sum := sha256.Sum256(input.CandidateJSON)
	if hex.EncodeToString(sum[:]) != input.ArtifactSHA256 {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(input.CandidateJSON))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&candidate) != nil || decoder.Decode(new(any)) != io.EOF {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	if candidate.SchemaVersion != coveredBaselineSchema || candidate.Status != "candidate_default_off" ||
		!candidate.Subject.Valid() || !token.MatchString(candidate.Generation) || candidate.Revision < 1 ||
		candidate.InputStateVersion < 0 || candidate.AsOf.IsZero() || candidate.AvailableAt.IsZero() ||
		!candidate.AsOf.Equal(candidate.AvailableAt) || candidate.AvailableAt.After(time.Now().UTC()) ||
		candidate.Coverage.Subject != sourcecoverage.SubjectRef(candidate.Subject) {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	parsed, err := url.Parse(input.ArtifactURL)
	expectedSuffix := "/" + url.PathEscape(candidate.Generation) + "/covered-baseline/" + input.ArtifactSHA256 + ".json"
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		len(input.ArtifactURL) > 2048 || !strings.HasSuffix(parsed.EscapedPath(), expectedSuffix) {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	spec, specHash, err := PrepareFeatureSpec(input.FeatureSpec)
	if err != nil || candidate.SpecVersion != spec.Version || candidate.SpecHash != specHash {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	for _, def := range spec.Features {
		if def.Source != "fact" || def.Mode != "count" {
			return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
		}
	}
	if len(candidate.Contributions) > 100000 || len(candidate.Tail) > 100000 {
		return candidate, FeatureSpec{}, ErrCoveredBaselineInvalid
	}
	return candidate, spec, nil
}

// AcceptCoveredBaseline stores an immutable *historical*, default-off v2
// candidate. It never changes v1 heads, snapshots, or ServingBundle pointers.
func (s *Store) AcceptCoveredBaseline(ctx context.Context,
	input CoveredBaselineSubmission) (CoveredBaselineReceipt, error) {
	if s == nil || s.db == nil {
		return CoveredBaselineReceipt{}, ErrCoveredBaselineInvalid
	}
	candidate, spec, err := decodeCoveredBaseline(input)
	if err != nil {
		return CoveredBaselineReceipt{}, err
	}
	receipt := CoveredBaselineReceipt{Subject: candidate.Subject, Revision: candidate.Revision,
		Generation: candidate.Generation, ArtifactSHA256: input.ArtifactSHA256, Coverage: candidate.Coverage,
		InputStateVersion: candidate.InputStateVersion, Status: coveredBaselineStoredStatus}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return CoveredBaselineReceipt{}, err
	}
	defer tx.Rollback(ctx)
	var oldHash, oldURL string
	var oldRaw []byte
	err = tx.QueryRow(ctx, `SELECT artifact_sha256,artifact_url,candidate_raw FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND revision=$4`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID,
		candidate.Revision).Scan(&oldHash, &oldURL, &oldRaw)
	if err == nil {
		if oldHash != input.ArtifactSHA256 || oldURL != input.ArtifactURL || !bytes.Equal(oldRaw, input.CandidateJSON) {
			return CoveredBaselineReceipt{}, ErrCoveredBaselineConflict
		}
		receipt.Replay = true
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CoveredBaselineReceipt{}, err
	}
	var lockedVersion int64
	err = tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID).Scan(&lockedVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CoveredBaselineReceipt{}, coveredBaselineDBError(err)
	}
	if err == nil && lockedVersion != candidate.InputStateVersion {
		return CoveredBaselineReceipt{}, ErrCoveredBaselinePending
	}
	state, err := s.coveredStateAtTx(ctx, tx, candidate.Subject, candidate.Coverage.Prefix, candidate.AsOf)
	if err != nil {
		return CoveredBaselineReceipt{}, err
	}
	if state.StateVersion != candidate.InputStateVersion || state.Coverage != candidate.Coverage ||
		state.CurrentComplete != candidate.CurrentComplete ||
		!coveredBaselineContributionsEqual(state.ActiveAtPrefix, candidate.Contributions) ||
		!coveredBaselineTailEqual(state.Tail, candidate.Tail) {
		return CoveredBaselineReceipt{}, ErrCoveredBaselinePending
	}
	values, err := featureValues(spec, state.ActiveAtPrefix, nil, candidate.AsOf, candidate.AvailableAt)
	if err != nil || !reflect.DeepEqual(values, candidate.Values) {
		return CoveredBaselineReceipt{}, ErrCoveredBaselineConflict
	}
	var previous int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0) FROM usermodel_covered_baselines_v2
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID).Scan(&previous); err != nil {
		return CoveredBaselineReceipt{}, err
	}
	if candidate.Revision != previous+1 {
		return CoveredBaselineReceipt{}, ErrCoveredBaselineConflict
	}
	w, err := strconv.ParseInt(candidate.Coverage.Prefix.ThroughOffset, 10, 64)
	if err != nil || w < 1 {
		return CoveredBaselineReceipt{}, ErrCoveredBaselineInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_covered_baselines_v2
		(authority_id,tenant_id,subject_id,revision,generation,artifact_url,artifact_sha256,schema_version,status,
		 spec_version,spec_hash,prefix_manifest_sha256,subject_receipt_sha256,through_offset,input_state_version,
		 as_of,available_at,candidate_raw,candidate_body)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		candidate.Subject.AuthorityID, candidate.Subject.TenantID, candidate.Subject.SubjectID,
		candidate.Revision, candidate.Generation, input.ArtifactURL, input.ArtifactSHA256,
		candidate.SchemaVersion, coveredBaselineStoredStatus, candidate.SpecVersion, candidate.SpecHash,
		candidate.Coverage.Prefix.ManifestSHA256, candidate.Coverage.ReceiptSHA256, w,
		candidate.InputStateVersion, candidate.AsOf, candidate.AvailableAt, input.CandidateJSON,
		input.CandidateJSON)
	if err != nil {
		return CoveredBaselineReceipt{}, coveredBaselineDBError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CoveredBaselineReceipt{}, coveredBaselineDBError(err)
	}
	return receipt, nil
}

func coveredBaselineContributionsEqual(active, frozen []Fact) bool {
	expected := slices.Clone(active)
	for i := range expected {
		expected[i].ImpressionLinked, expected[i].LinkedImpression = false, ""
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].EventID < expected[j].EventID })
	return reflect.DeepEqual(expected, frozen)
}

func coveredBaselineTailEqual(active []CoverageTailEvent, frozen []coveredBaselineTail) bool {
	expected := make([]coveredBaselineTail, len(active))
	for i, event := range active {
		expected[i] = coveredBaselineTail{EventKey: event.EventKey, Offset: event.Offset, Status: event.Status}
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].Offset == expected[j].Offset {
			return expected[i].EventID < expected[j].EventID
		}
		return expected[i].Offset < expected[j].Offset
	})
	return reflect.DeepEqual(expected, frozen)
}

func coveredBaselineDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "40001" {
			return fmt.Errorf("%w: concurrent user fact changed", ErrCoveredBaselinePending)
		}
		if pgErr.Code == "23505" || pgErr.Code == "23503" {
			return fmt.Errorf("%w: duplicate revision or missing verified source", ErrCoveredBaselineConflict)
		}
	}
	return err
}
