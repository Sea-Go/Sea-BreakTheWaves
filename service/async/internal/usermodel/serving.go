package usermodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	contract "github.com/Sea-Go/Sea-BreakTheWaves/service/common/usermodelcontract"
)

// PairRef is a frozen compatibility request from recommend. A candidate pair
// can be built here, but this value alone never authorizes activation.
type (
	PairRef           = contract.PairRef
	ServingBundle     = contract.ServingBundle
	PairAuthorization = contract.PairAuthorization
	ServingPointer    = contract.ServingPointer
	PairAuthorizer    = contract.PairAuthorizer
)

type EncoderOutput struct {
	PairID            string    `json:"pair_id"`
	SpaceID           string    `json:"space_id"`
	EncoderID         string    `json:"encoder_id"`
	FeatureSnapshotID string    `json:"feature_snapshot_id"`
	Source            string    `json:"source"`
	ModelCallID       string    `json:"model_call_id,omitempty"`
	Vector            []float64 `json:"vector"`
}

type UserEncoder interface {
	EncodeUser(context.Context, FeatureSnapshot, PairRef) (EncoderOutput, error)
}

func validateEncoderOutput(out EncoderOutput, p PairRef, snapshot FeatureSnapshot) error {
	if out.PairID != p.PairID || out.SpaceID != p.SpaceID || out.EncoderID != p.EncoderID ||
		out.FeatureSnapshotID != snapshot.ID || len(out.Vector) != p.Dimension {
		return fmt.Errorf("%w: encoder output identity, space or dimension", ErrConflict)
	}
	if out.Source != "fixed_baseline" && out.Source != "dc_prediction" {
		return fmt.Errorf("%w: encoder source", ErrInvalid)
	}
	if (p.Kind == "fixed_baseline" && out.Source != "fixed_baseline") ||
		(p.Kind == "model" && out.Source != "dc_prediction") {
		return fmt.Errorf("%w: pair and encoder source", ErrConflict)
	}
	if (out.Source == "dc_prediction" && !token.MatchString(out.ModelCallID)) ||
		(out.Source == "fixed_baseline" && out.ModelCallID != "") {
		return fmt.Errorf("%w: model-call provenance", ErrInvalid)
	}
	for _, n := range out.Vector {
		if math.IsNaN(n) || math.IsInf(n, 0) || (n == 0 && math.Signbit(n)) {
			return fmt.Errorf("%w: non-finite user vector", ErrInvalid)
		}
	}
	return nil
}

func freezeBundle(b *ServingBundle) error {
	if b == nil || b.ID != "" || !b.Subject.Valid() || !b.Pair.Valid() ||
		!digest.MatchString(b.FeatureSnapshotID) || len(b.Vector) != b.Pair.Dimension {
		return ErrInvalid
	}
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	b.ID = hex.EncodeToString(sum[:])
	return nil
}

func decodeBundle(body []byte, subject SubjectRef, id string) (ServingBundle, error) {
	var b ServingBundle
	if err := json.Unmarshal(body, &b); err != nil {
		return ServingBundle{}, err
	}
	if b.Subject != subject || b.ID != id || !b.Pair.Valid() || !digest.MatchString(b.FeatureSnapshotID) ||
		len(b.Vector) != b.Pair.Dimension {
		return ServingBundle{}, ErrConflict
	}
	for _, n := range b.Vector {
		if math.IsNaN(n) || math.IsInf(n, 0) || (n == 0 && math.Signbit(n)) {
			return ServingBundle{}, ErrConflict
		}
	}
	copy := b
	copy.ID = ""
	check, err := json.Marshal(copy)
	if err != nil {
		return ServingBundle{}, err
	}
	sum := sha256.Sum256(check)
	if hex.EncodeToString(sum[:]) != id {
		return ServingBundle{}, ErrConflict
	}
	return b, nil
}

// currentFeatureTx reads the same snapshot and head in one transaction. Build
// and pointer mutation first lock subject state, serializing fact/feature CAS.
func currentFeatureTx(ctx context.Context, tx pgx.Tx, subject SubjectRef) (FeatureSnapshot, error) {
	var body []byte
	var stateVersion int64
	var headRevision, ontologyVersion *int64
	err := tx.QueryRow(ctx, `SELECT f.snapshot_body,st.state_version,h.revision,oh.definition_version
		FROM usermodel_feature_snapshots f JOIN usermodel_subject_state st USING(authority_id,tenant_id,subject_id)
		LEFT JOIN usermodel_feature_heads h USING(authority_id,tenant_id,subject_id)
		LEFT JOIN usermodel_ontology_heads oh ON oh.authority_id=f.authority_id AND oh.tenant_id=f.tenant_id
		WHERE f.authority_id=$1 AND f.tenant_id=$2 AND f.subject_id=$3`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&body, &stateVersion, &headRevision, &ontologyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return FeatureSnapshot{}, ErrPending
	}
	if err != nil {
		return FeatureSnapshot{}, err
	}
	var snap FeatureSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return FeatureSnapshot{}, err
	}
	if snap.Subject != subject || !digest.MatchString(snap.ID) || snap.StateVersion != stateVersion ||
		(headRevision == nil && snap.BaselineState != "absent") ||
		(headRevision != nil && (snap.BaselineState != "accepted" || snap.Revision != *headRevision)) ||
		(snap.OntologyVersion > 0 && (ontologyVersion == nil || snap.OntologyVersion != *ontologyVersion)) ||
		(snap.NextChangeAt != nil && !time.Now().UTC().Before(*snap.NextChangeAt)) {
		return FeatureSnapshot{}, ErrPending
	}
	check := snap
	check.ID = ""
	canonical, err := json.Marshal(check)
	if err != nil {
		return FeatureSnapshot{}, err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != snap.ID {
		return FeatureSnapshot{}, ErrConflict
	}
	return snap, nil
}

// BuildServingBundle freezes only a candidate. A changed fact, baseline or
// feature snapshot during the external encode rejects publication.
func (s *Store) BuildServingBundle(ctx context.Context, subject SubjectRef, pair PairRef,
	encoder UserEncoder) (bundle ServingBundle, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.serving.build", subject)
	if beginErr != nil {
		return ServingBundle{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || !pair.Valid() || encoder == nil {
		return ServingBundle{}, ErrInvalid
	}
	snapshot, err := s.ReadyFeatureSnapshot(ctx, subject)
	if err != nil {
		return ServingBundle{}, err
	}
	if snapshot.SpecVersion != pair.FeatureSpecVersion || snapshot.SpecHash != pair.FeatureSpecHash {
		return ServingBundle{}, fmt.Errorf("%w: pair feature contract", ErrConflict)
	}
	out, err := encoder.EncodeUser(ctx, snapshot, pair)
	if err != nil {
		return ServingBundle{}, err
	}
	if err := validateEncoderOutput(out, pair, snapshot); err != nil {
		return ServingBundle{}, err
	}
	bundle = ServingBundle{
		Subject: subject, Pair: pair, FeatureSnapshotID: snapshot.ID,
		StateVersion: snapshot.StateVersion, OntologyVersion: snapshot.OntologyVersion,
		BaselineState: snapshot.BaselineState, Generation: snapshot.Generation, Revision: snapshot.Revision,
		Watermarks:     append([]Watermark(nil), snapshot.Watermarks...),
		Tail:           append([]EventKey(nil), snapshot.Tail...),
		EncodingSource: out.Source, ModelCallID: out.ModelCallID,
		Vector: append([]float64(nil), out.Vector...),
	}
	if err := freezeBundle(&bundle); err != nil {
		return ServingBundle{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ServingBundle{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockFeatureState(ctx, tx, subject, snapshot.StateVersion); err != nil {
		return ServingBundle{}, err
	}
	current, err := currentFeatureTx(ctx, tx, subject)
	if err != nil {
		return ServingBundle{}, err
	}
	if current.ID != snapshot.ID {
		return ServingBundle{}, fmt.Errorf("%w: feature snapshot changed during encoding", ErrConflict)
	}
	body, err := json.Marshal(bundle)
	if err != nil {
		return ServingBundle{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO usermodel_serving_bundles
		(authority_id,tenant_id,subject_id,bundle_id,pair_id,space_id,feature_snapshot_id,bundle_body)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, bundle.ID, pair.PairID, pair.SpaceID,
		snapshot.ID, body)
	if err != nil {
		return ServingBundle{}, dbErr(err)
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `SELECT bundle_body FROM usermodel_serving_bundles
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND bundle_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, bundle.ID).Scan(&stored); err != nil {
		return ServingBundle{}, err
	}
	saved, err := decodeBundle(stored, subject, bundle.ID)
	if err != nil || !reflect.DeepEqual(saved, bundle) {
		return ServingBundle{}, ErrConflict
	}
	return bundle, tx.Commit(ctx)
}

// ServingBundleByID returns an immutable historical candidate or active
// record only to the exact SubjectRef. It does not imply current eligibility.
func (s *Store) ServingBundleByID(ctx context.Context, subject SubjectRef, id string) (bundle ServingBundle, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.serving.version_read", subject)
	if beginErr != nil {
		return ServingBundle{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || !digest.MatchString(id) {
		return ServingBundle{}, ErrInvalid
	}
	var body []byte
	err = s.db.QueryRow(ctx, `SELECT bundle_body FROM usermodel_serving_bundles
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND bundle_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, id).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServingBundle{}, ErrNotFound
	}
	if err != nil {
		return ServingBundle{}, err
	}
	return decodeBundle(body, subject, id)
}

func pointerTx(ctx context.Context, tx pgx.Tx, subject SubjectRef, pairID string) (ServingPointer, error) {
	var p ServingPointer
	p.Subject, p.PairID = subject, pairID
	err := tx.QueryRow(ctx, `SELECT bundle_id,state,approval_ref,approval_revision,pointer_version
		FROM usermodel_serving_pointers WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND pair_id=$4
		FOR UPDATE`, subject.AuthorityID, subject.TenantID, subject.SubjectID, pairID).
		Scan(&p.BundleID, &p.State, &p.ApprovalRef, &p.ApprovalRevision, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServingPointer{}, ErrNotFound
	}
	return p, err
}

// ActivateServingBundle CAS-switches the pointer after recommend authorizes
// this exact pair. expectedVersion=0 means that no pointer may yet exist.
func (s *Store) ActivateServingBundle(ctx context.Context, subject SubjectRef, pair PairRef, bundleID string,
	expectedVersion int64, authorizer PairAuthorizer) (pointer ServingPointer, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.serving.activate", subject)
	if beginErr != nil {
		return ServingPointer{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || !pair.Valid() || !digest.MatchString(bundleID) ||
		expectedVersion < 0 || authorizer == nil {
		return ServingPointer{}, ErrInvalid
	}
	grant, err := authorizer.AuthorizePair(ctx, pair)
	if err != nil {
		return ServingPointer{}, err
	}
	if !grant.Active || grant.Pair != pair ||
		!token.MatchString(grant.ApprovalRef) || grant.Revision <= 0 {
		return ServingPointer{}, fmt.Errorf("%w: pair not authorized", ErrConflict)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ServingPointer{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockSubjectForServing(ctx, tx, subject); err != nil {
		return ServingPointer{}, err
	}
	snap, err := currentFeatureTx(ctx, tx, subject)
	if err != nil {
		return ServingPointer{}, err
	}
	var body []byte
	err = tx.QueryRow(ctx, `SELECT bundle_body FROM usermodel_serving_bundles
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND bundle_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, bundleID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServingPointer{}, ErrNotFound
	}
	if err != nil {
		return ServingPointer{}, err
	}
	bundle, err := decodeBundle(body, subject, bundleID)
	if err != nil {
		return ServingPointer{}, err
	}
	if bundle.Pair != pair || bundle.FeatureSnapshotID != snap.ID {
		return ServingPointer{}, fmt.Errorf("%w: bundle pair or feature version", ErrConflict)
	}
	old, err := pointerTx(ctx, tx, subject, pair.PairID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ServingPointer{}, err
	}
	if errors.Is(err, ErrNotFound) && expectedVersion != 0 ||
		err == nil && (old.Version != expectedVersion || grant.Revision < old.ApprovalRevision) {
		return ServingPointer{}, fmt.Errorf("%w: serving pointer CAS or approval revision", ErrConflict)
	}
	pointer = ServingPointer{Subject: subject, PairID: pair.PairID, BundleID: bundleID,
		State: "active", ApprovalRef: grant.ApprovalRef, ApprovalRevision: grant.Revision,
		Version: expectedVersion + 1}
	if expectedVersion == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO usermodel_serving_pointers
			(authority_id,tenant_id,subject_id,pair_id,bundle_id,state,approval_ref,approval_revision,pointer_version)
			VALUES($1,$2,$3,$4,$5,'active',$6,$7,1)`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID, pair.PairID, bundleID,
			grant.ApprovalRef, grant.Revision)
	} else {
		_, err = tx.Exec(ctx, `UPDATE usermodel_serving_pointers
			SET bundle_id=$5,state='active',approval_ref=$6,approval_revision=$7,pointer_version=$8,changed_at=now()
			WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND pair_id=$4`,
			subject.AuthorityID, subject.TenantID, subject.SubjectID, pair.PairID, bundleID,
			grant.ApprovalRef, grant.Revision, pointer.Version)
	}
	if err != nil {
		return ServingPointer{}, dbErr(err)
	}
	return pointer, tx.Commit(ctx)
}

func lockSubjectForServing(ctx context.Context, tx pgx.Tx, subject SubjectRef) error {
	var version int64
	err := tx.QueryRow(ctx, `SELECT state_version FROM usermodel_subject_state
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 FOR UPDATE`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// DisableServingBundle is a local CAS. The immutable bundle remains available
// by ID for an actual request that retained it.
func (s *Store) DisableServingBundle(ctx context.Context, subject SubjectRef, pairID string,
	expectedVersion int64) (pointer ServingPointer, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.serving.disable", subject)
	if beginErr != nil {
		return ServingPointer{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || !token.MatchString(pairID) || expectedVersion <= 0 {
		return ServingPointer{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ServingPointer{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockSubjectForServing(ctx, tx, subject); err != nil {
		return ServingPointer{}, err
	}
	pointer, err = pointerTx(ctx, tx, subject, pairID)
	if err != nil {
		return ServingPointer{}, err
	}
	if pointer.Version != expectedVersion || pointer.State != "active" {
		return ServingPointer{}, ErrConflict
	}
	pointer.Version++
	pointer.State = "disabled"
	_, err = tx.Exec(ctx, `UPDATE usermodel_serving_pointers
		SET state='disabled',pointer_version=$5,changed_at=now()
		WHERE authority_id=$1 AND tenant_id=$2 AND subject_id=$3 AND pair_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, pairID, pointer.Version)
	if err != nil {
		return ServingPointer{}, err
	}
	return pointer, tx.Commit(ctx)
}

// ReadyServingBundle requires the pair selected by recommend. It never
// searches other pairs or returns a disabled/stale representation as current.
func (s *Store) ReadyServingBundle(ctx context.Context, subject SubjectRef, pair PairRef) (bundle ServingBundle, pointer ServingPointer, err error) {
	ctx, stage, beginErr := s.beginSubject(ctx, "usermodel.serving.ready_read", subject)
	if beginErr != nil {
		return ServingBundle{}, ServingPointer{}, beginErr
	}
	defer func() { finish(ctx, stage, err, false, "") }()
	if !subject.Valid() || !pair.Valid() {
		return ServingBundle{}, ServingPointer{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ServingBundle{}, ServingPointer{}, err
	}
	defer tx.Rollback(ctx)
	snap, err := currentFeatureTx(ctx, tx, subject)
	if err != nil {
		return ServingBundle{}, ServingPointer{}, err
	}
	var body []byte
	err = tx.QueryRow(ctx, `SELECT p.bundle_id,p.state,p.approval_ref,p.approval_revision,p.pointer_version,b.bundle_body
		FROM usermodel_serving_pointers p JOIN usermodel_serving_bundles b USING(authority_id,tenant_id,subject_id,bundle_id)
		WHERE p.authority_id=$1 AND p.tenant_id=$2 AND p.subject_id=$3 AND p.pair_id=$4`,
		subject.AuthorityID, subject.TenantID, subject.SubjectID, pair.PairID).
		Scan(&pointer.BundleID, &pointer.State, &pointer.ApprovalRef, &pointer.ApprovalRevision, &pointer.Version, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServingBundle{}, ServingPointer{}, ErrNotFound
	}
	if err != nil {
		return ServingBundle{}, ServingPointer{}, err
	}
	pointer.Subject, pointer.PairID = subject, pair.PairID
	if pointer.State != "active" {
		return ServingBundle{}, ServingPointer{}, ErrPending
	}
	bundle, err = decodeBundle(body, subject, pointer.BundleID)
	if err != nil {
		return ServingBundle{}, ServingPointer{}, err
	}
	if bundle.Pair != pair || bundle.FeatureSnapshotID != snap.ID {
		return ServingBundle{}, ServingPointer{}, ErrPending
	}
	return bundle, pointer, tx.Commit(ctx)
}
