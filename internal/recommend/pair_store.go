package recommend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	"github.com/jackc/pgx/v5"
)

func immutableBody[T any](raw []byte, out *T, valid func(T) bool) error {
	if err := json.Unmarshal(raw, out); err != nil || !valid(*out) {
		return ErrConflict
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrConflict
	}
	return nil
}

func (s *PairStore) readProposal(ctx context.Context, id string) (PairProposal, error) {
	var p PairProposal
	if s == nil || !artifacts.ValidHash(id) {
		return p, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT proposal_body FROM recommend_pair_proposals WHERE proposal_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrPairPending
	}
	if err != nil {
		return p, err
	}
	if err := immutableBody(raw, &p, validProposal); err != nil || p.ID != id {
		return PairProposal{}, ErrConflict
	}
	return p, nil
}

func (s *PairStore) readIndex(ctx context.Context, id string, proposal PairProposal, pool PoolRelease) (ItemIndexGeneration, error) {
	var index ItemIndexGeneration
	if s == nil || !artifacts.ValidHash(id) {
		return index, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT index_body FROM recommend_item_index_generations WHERE item_index_generation_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return index, ErrPairPending
	}
	if err != nil {
		return index, err
	}
	if err := immutableBody(raw, &index, func(x ItemIndexGeneration) bool { return validItemIndex(x, proposal, pool) }); err != nil || index.ID != id {
		return ItemIndexGeneration{}, ErrConflict
	}
	return index, nil
}

func (s *PairStore) readRelease(ctx context.Context, id string) (EncoderPairRelease, PoolRelease, error) {
	var release EncoderPairRelease
	if s == nil || !artifacts.ValidHash(id) {
		return release, PoolRelease{}, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT release_body FROM recommend_pair_releases WHERE encoder_pair_release_id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return release, PoolRelease{}, ErrPairPending
	}
	if err != nil {
		return release, PoolRelease{}, err
	}
	if err := json.Unmarshal(raw, &release); err != nil || release.ID != id {
		return EncoderPairRelease{}, PoolRelease{}, ErrConflict
	}
	pool, err := s.pool.Release(ctx, release.Proposal.PoolReleaseID)
	if err != nil {
		return EncoderPairRelease{}, PoolRelease{}, err
	}
	if !validPairRelease(release, pool) {
		return EncoderPairRelease{}, PoolRelease{}, ErrConflict
	}
	proposal, err := s.readProposal(ctx, release.Proposal.ID)
	if err != nil || !reflect.DeepEqual(proposal, release.Proposal) {
		return EncoderPairRelease{}, PoolRelease{}, ErrConflict
	}
	index, err := s.readIndex(ctx, release.Index.ID, proposal, pool)
	if err != nil || !reflect.DeepEqual(index, release.Index) {
		return EncoderPairRelease{}, PoolRelease{}, ErrConflict
	}
	canonical, err := json.Marshal(release)
	if err != nil || !bytes.Equal(canonical, raw) {
		return EncoderPairRelease{}, PoolRelease{}, ErrConflict
	}
	return release, pool, nil
}

func (s *PairStore) livePool(ctx context.Context, moduleID, poolID string) error {
	page, err := s.pool.Candidates(ctx, moduleID, 1)
	if err != nil {
		return err
	}
	if page.PoolReleaseID != poolID {
		return ErrStale
	}
	return nil
}

// RegisterProposal freezes an exported candidate and the currently published
// RTW content pool. It is not approval, DC loading or model activation.
func (s *PairStore) RegisterProposal(ctx context.Context, input PairProposal) (PairProposal, error) {
	if s == nil || ctx == nil || input.ID != "" {
		return PairProposal{}, ErrInvalid
	}
	pool, err := s.pool.Release(ctx, input.PoolReleaseID)
	if err != nil {
		return PairProposal{}, err
	}
	if input.ModuleID != pool.Publication.ModuleID || input.ItemFeatureHash != pool.FeatureHash {
		return PairProposal{}, ErrConflict
	}
	if err := s.livePool(ctx, input.ModuleID, pool.ID); err != nil {
		return PairProposal{}, err
	}
	input.ID, err = proposalID(input)
	if err != nil || !validProposal(input) {
		return PairProposal{}, ErrInvalid
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return PairProposal{}, err
	}
	if _, err = s.db.Exec(ctx, `INSERT INTO recommend_pair_proposals
 (proposal_id,module_id,pool_release_id,pair_id,proposal_body) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT (proposal_id) DO NOTHING`, input.ID, input.ModuleID, input.PoolReleaseID, input.Pair.PairID, raw); err != nil {
		return PairProposal{}, err
	}
	stored, err := s.readProposal(ctx, input.ID)
	if err != nil || !reflect.DeepEqual(stored, input) {
		return PairProposal{}, ErrConflict
	}
	return stored, nil
}

// RegisterItemIndex freezes the claimed full-coverage generation. It checks
// content revisions and encoder space, but a real index reader/probe must still
// independently attest the object before approval.
func (s *PairStore) RegisterItemIndex(ctx context.Context, input ItemIndexGeneration) (ItemIndexGeneration, error) {
	if s == nil || ctx == nil || input.ID != "" {
		return ItemIndexGeneration{}, ErrInvalid
	}
	proposal, err := s.readProposal(ctx, input.ProposalID)
	if err != nil {
		return ItemIndexGeneration{}, err
	}
	pool, err := s.pool.Release(ctx, proposal.PoolReleaseID)
	if err != nil {
		return ItemIndexGeneration{}, err
	}
	if err := s.livePool(ctx, proposal.ModuleID, pool.ID); err != nil {
		return ItemIndexGeneration{}, err
	}
	if !sameIndexedSet(input.Items, indexedItems(pool)) {
		return ItemIndexGeneration{}, ErrConflict
	}
	input.Items = indexedItems(pool)
	input.ID, err = indexID(input)
	if err != nil || !validItemIndex(input, proposal, pool) {
		return ItemIndexGeneration{}, ErrInvalid
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ItemIndexGeneration{}, err
	}
	if _, err = s.db.Exec(ctx, `INSERT INTO recommend_item_index_generations
 (item_index_generation_id,proposal_id,pool_release_id,index_body) VALUES($1,$2,$3,$4)
 ON CONFLICT (item_index_generation_id) DO NOTHING`, input.ID, input.ProposalID, input.PoolReleaseID, raw); err != nil {
		return ItemIndexGeneration{}, err
	}
	stored, err := s.readIndex(ctx, input.ID, proposal, pool)
	if err != nil || !reflect.DeepEqual(stored, input) {
		return ItemIndexGeneration{}, ErrConflict
	}
	return stored, nil
}

type pairProofs struct {
	model    ModelProbe
	index    IndexProbe
	ranker   RankerProbe
	approval ApprovalProof
}

func (s *PairStore) verifiedProofs(ctx context.Context, proposal PairProposal,
	index ItemIndexGeneration, approval Approval) (pairProofs, error) {
	if s.verifier == nil || nilPairVerifier(s.verifier) {
		return pairProofs{}, ErrPairPending
	}
	model, err := s.verifier.VerifyModel(ctx, proposal)
	if err != nil {
		return pairProofs{}, err
	}
	item, err := s.verifier.VerifyIndex(ctx, index)
	if err != nil {
		return pairProofs{}, err
	}
	ranker, err := s.verifier.VerifyRanker(ctx, proposal)
	if err != nil {
		return pairProofs{}, err
	}
	grant, err := s.verifier.VerifyApproval(ctx, proposal, index, approval)
	if err != nil {
		return pairProofs{}, err
	}
	now := time.Now().UTC()
	if !validModelProbe(model, proposal) || !validIndexProbe(item, index) ||
		!validRankerProbe(ranker, proposal) ||
		!validApprovalProof(grant, proposal, index) || grant.Approval != approval ||
		!proofFresh(now, model.ObservedAt) || !proofFresh(now, item.ObservedAt) ||
		!proofFresh(now, ranker.ObservedAt) {
		return pairProofs{}, ErrPairPending
	}
	return pairProofs{model: model, index: item, ranker: ranker, approval: grant}, nil
}

// Approve prepares a full immutable release only after fresh independent
// model/index probes and an approval proof. It leaves the active pointer alone.
func (s *PairStore) Approve(ctx context.Context, proposalID, indexID string, approval Approval) (EncoderPairRelease, error) {
	if s == nil || ctx == nil || !validID(approval.Ref) || approval.Revision < 1 {
		return EncoderPairRelease{}, ErrInvalid
	}
	proposal, err := s.readProposal(ctx, proposalID)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	pool, err := s.pool.Release(ctx, proposal.PoolReleaseID)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	index, err := s.readIndex(ctx, indexID, proposal, pool)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	if err := s.livePool(ctx, proposal.ModuleID, pool.ID); err != nil {
		return EncoderPairRelease{}, err
	}
	var existingID string
	err = s.db.QueryRow(ctx, `SELECT encoder_pair_release_id FROM recommend_pair_releases
 WHERE proposal_id=$1 AND item_index_generation_id=$2 AND approval_ref=$3 AND approval_revision=$4`,
		proposal.ID, index.ID, approval.Ref, approval.Revision).Scan(&existingID)
	if err == nil {
		if _, err := s.verifiedProofs(ctx, proposal, index, approval); err != nil {
			return EncoderPairRelease{}, err
		}
		stored, _, err := s.readRelease(ctx, existingID)
		return stored, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return EncoderPairRelease{}, err
	}
	proofs, err := s.verifiedProofs(ctx, proposal, index, approval)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	release := EncoderPairRelease{Proposal: proposal, Index: index, ModelProbe: proofs.model,
		IndexProbe: proofs.index, RankerProbe: proofs.ranker, Approval: proofs.approval}
	release.ID, err = pairReleaseID(release)
	if err != nil || !validPairRelease(release, pool) {
		return EncoderPairRelease{}, ErrConflict
	}
	raw, err := json.Marshal(release)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	if _, err = s.db.Exec(ctx, `INSERT INTO recommend_pair_releases
 (encoder_pair_release_id,proposal_id,item_index_generation_id,module_id,pool_release_id,pair_id,
 approval_ref,approval_revision,release_body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
 ON CONFLICT DO NOTHING`, release.ID, proposal.ID, index.ID,
		proposal.ModuleID, pool.ID, proposal.Pair.PairID, approval.Ref, approval.Revision, raw); err != nil {
		return EncoderPairRelease{}, err
	}
	err = s.db.QueryRow(ctx, `SELECT encoder_pair_release_id FROM recommend_pair_releases
 WHERE proposal_id=$1 AND item_index_generation_id=$2 AND approval_ref=$3 AND approval_revision=$4`,
		proposal.ID, index.ID, approval.Ref, approval.Revision).Scan(&existingID)
	if err != nil {
		return EncoderPairRelease{}, err
	}
	stored, _, err := s.readRelease(ctx, existingID)
	if err != nil || stored.Proposal.ID != proposal.ID || stored.Index.ID != index.ID ||
		stored.Approval.Approval != approval {
		return EncoderPairRelease{}, ErrConflict
	}
	return stored, nil
}

func (s *PairStore) activePointer(ctx context.Context, moduleID string) (PairPointer, error) {
	var p PairPointer
	err := s.db.QueryRow(ctx, `SELECT module_id,encoder_pair_release_id,pair_id,pointer_version,
 approval_ref,approval_revision FROM recommend_pair_heads WHERE module_id=$1`, moduleID).
		Scan(&p.ModuleID, &p.ReleaseID, &p.PairID, &p.Version, &p.ApprovalRef, &p.ApprovalRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return PairPointer{}, ErrPairPending
	}
	return p, err
}

// CurrentPointer is a read-only reconciliation view. It does not assert that
// the pair is still eligible; Active performs the RTW/probe checks.
func (s *PairStore) CurrentPointer(ctx context.Context, moduleID string) (PairPointer, error) {
	if s == nil || ctx == nil || !validID(moduleID) {
		return PairPointer{}, ErrInvalid
	}
	return s.activePointer(ctx, moduleID)
}

func (s *PairStore) checkLive(ctx context.Context, release EncoderPairRelease) error {
	if err := s.livePool(ctx, release.Proposal.ModuleID, release.Proposal.PoolReleaseID); err != nil {
		return err
	}
	_, err := s.verifiedProofs(ctx, release.Proposal, release.Index, release.Approval.Approval)
	return err
}

// Activate rechecks all live dependencies and CAS-switches exactly one module.
// A training candidate, index registration or prepared release never switches
// this pointer automatically.
func (s *PairStore) Activate(ctx context.Context, releaseID string, expectedVersion int64) (PairPointer, error) {
	if s == nil || ctx == nil || expectedVersion < 0 {
		return PairPointer{}, ErrInvalid
	}
	release, _, err := s.readRelease(ctx, releaseID)
	if err != nil {
		return PairPointer{}, err
	}
	if err := s.checkLive(ctx, release); err != nil {
		return PairPointer{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PairPointer{}, err
	}
	defer tx.Rollback(context.Background())
	module := release.Proposal.ModuleID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "recommend-pair/"+module); err != nil {
		return PairPointer{}, err
	}
	var previous PairPointer
	err = tx.QueryRow(ctx, `SELECT module_id,encoder_pair_release_id,pair_id,pointer_version,
 approval_ref,approval_revision FROM recommend_pair_heads WHERE module_id=$1 FOR UPDATE`, module).
		Scan(&previous.ModuleID, &previous.ReleaseID, &previous.PairID, &previous.Version,
			&previous.ApprovalRef, &previous.ApprovalRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PairPointer{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) && expectedVersion != 0 ||
		err == nil && (previous.Version != expectedVersion ||
			release.Approval.Approval.Revision < previous.ApprovalRevision) {
		return PairPointer{}, ErrConflict
	}
	if err := s.checkLive(ctx, release); err != nil {
		return PairPointer{}, err
	}
	pointer := PairPointer{ModuleID: module, ReleaseID: release.ID, PairID: release.Proposal.Pair.PairID,
		Version: expectedVersion + 1, ApprovalRef: release.Approval.Approval.Ref,
		ApprovalRevision: release.Approval.Approval.Revision}
	if expectedVersion == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO recommend_pair_heads
 (module_id,encoder_pair_release_id,pair_id,pointer_version,approval_ref,approval_revision)
 VALUES($1,$2,$3,1,$4,$5)`, module, release.ID, pointer.PairID, pointer.ApprovalRef, pointer.ApprovalRevision)
	} else {
		_, err = tx.Exec(ctx, `UPDATE recommend_pair_heads SET encoder_pair_release_id=$2,pair_id=$3,
 pointer_version=$4,approval_ref=$5,approval_revision=$6,changed_at=clock_timestamp() WHERE module_id=$1`,
			module, release.ID, pointer.PairID, pointer.Version, pointer.ApprovalRef, pointer.ApprovalRevision)
	}
	if err != nil {
		return PairPointer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pointer, errors.Join(ErrActivationUnknown, err)
	}
	if err := s.checkLive(ctx, release); err != nil {
		return pointer, errors.Join(ErrActivationUnknown, err)
	}
	return pointer, nil
}

// Active returns the fully checked current pair for one module. It fails
// closed if RTW moved/withdrew content or probes no longer pass.
func (s *PairStore) Active(ctx context.Context, moduleID string) (EncoderPairRelease, PairPointer, error) {
	if s == nil || ctx == nil || !validID(moduleID) {
		return EncoderPairRelease{}, PairPointer{}, ErrInvalid
	}
	pointer, err := s.activePointer(ctx, moduleID)
	if err != nil {
		return EncoderPairRelease{}, PairPointer{}, err
	}
	release, _, err := s.readRelease(ctx, pointer.ReleaseID)
	if err != nil {
		return EncoderPairRelease{}, PairPointer{}, err
	}
	if release.Proposal.ModuleID != moduleID || release.Proposal.Pair.PairID != pointer.PairID ||
		release.Approval.Approval.Ref != pointer.ApprovalRef ||
		release.Approval.Approval.Revision != pointer.ApprovalRevision {
		return EncoderPairRelease{}, PairPointer{}, ErrConflict
	}
	if err := s.checkLive(ctx, release); err != nil {
		return EncoderPairRelease{}, PairPointer{}, err
	}
	current, err := s.activePointer(ctx, moduleID)
	if err != nil || current != pointer {
		return EncoderPairRelease{}, PairPointer{}, ErrStale
	}
	return release, pointer, nil
}

// AuthorizePair implements usermodel.PairAuthorizer. The user representation
// is global to a pair; at least one module must have that exact validated
// pair active. A module-specific request still calls Active(moduleID).
func (s *PairStore) AuthorizePair(ctx context.Context, pair usermodel.PairRef) (usermodel.PairAuthorization, error) {
	if s == nil || ctx == nil || !validPairRef(pair) {
		return usermodel.PairAuthorization{}, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT module_id FROM recommend_pair_heads WHERE pair_id=$1 ORDER BY module_id LIMIT 33`, pair.PairID)
	if err != nil {
		return usermodel.PairAuthorization{}, err
	}
	var modules []string
	for rows.Next() {
		var module string
		if err := rows.Scan(&module); err != nil {
			rows.Close()
			return usermodel.PairAuthorization{}, err
		}
		modules = append(modules, module)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return usermodel.PairAuthorization{}, err
	}
	if len(modules) == 0 || len(modules) > 32 {
		return usermodel.PairAuthorization{}, ErrPairPending
	}
	for _, module := range modules {
		release, pointer, err := s.Active(ctx, module)
		if err != nil {
			if !errors.Is(err, ErrPairPending) && !errors.Is(err, ErrStale) &&
				!errors.Is(err, ErrUnavailable) {
				return usermodel.PairAuthorization{}, err
			}
			continue
		}
		if release.Proposal.Pair == pair {
			return usermodel.PairAuthorization{Pair: pair, ApprovalRef: pointer.ApprovalRef,
				Revision: pointer.ApprovalRevision, Active: true}, nil
		}
	}
	return usermodel.PairAuthorization{}, ErrPairPending
}

var _ usermodel.PairAuthorizer = (*PairStore)(nil)
