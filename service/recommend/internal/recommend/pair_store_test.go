package recommend

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	usermodel "github.com/Sea-Go/Sea-BreakTheWaves/service/common/usermodelcontract"
)

type fixedPairVerifier struct {
	modelErr, indexErr, rankerErr, approvalErr error
	wrongSpace                                 bool
}

type failAfterCommitVerifier struct {
	*fixedPairVerifier
	modelCalls atomic.Int32
	failFrom   atomic.Int32
}

func (v *failAfterCommitVerifier) VerifyModel(ctx context.Context, proposal PairProposal) (ModelProbe, error) {
	call := v.modelCalls.Add(1)
	if v.failFrom.Load() > 0 && call >= v.failFrom.Load() {
		return ModelProbe{}, ErrPairPending
	}
	return v.fixedPairVerifier.VerifyModel(ctx, proposal)
}

func (v *fixedPairVerifier) VerifyModel(_ context.Context, proposal PairProposal) (ModelProbe, error) {
	if v.modelErr != nil {
		return ModelProbe{}, v.modelErr
	}
	space := proposal.Pair.SpaceID
	if v.wrongSpace {
		space = "different-space"
	}
	return ModelProbe{PairID: proposal.Pair.PairID, SpaceID: space,
		Dimension: proposal.Pair.Dimension, Metric: proposal.Pair.Metric,
		UserWeightsHash:       proposal.UserEncoder.Weights.SHA256,
		ItemWeightsHash:       proposal.ItemEncoder.Weights.SHA256,
		CandidateManifestHash: proposal.CandidateManifest.SHA256,
		ModelCallID:           "fixture-model-call", ProbeRef: "fixture-model-probe", ObservedAt: time.Now().UTC()}, nil
}

func (v *fixedPairVerifier) VerifyIndex(_ context.Context, index ItemIndexGeneration) (IndexProbe, error) {
	if v.indexErr != nil {
		return IndexProbe{}, v.indexErr
	}
	return IndexProbe{GenerationID: index.ID, PoolReleaseID: index.PoolReleaseID,
		PairID: index.PairID, SpaceID: index.SpaceID, Dimension: index.Dimension, Metric: index.Metric,
		IndexHash: index.Index.SHA256, CoveredItems: len(index.Items),
		BuildManifestHash: index.BuildManifest.SHA256,
		ProbeRef:          "fixture-index-probe", ObservedAt: time.Now().UTC()}, nil
}

func (v *fixedPairVerifier) VerifyRanker(_ context.Context, proposal PairProposal) (RankerProbe, error) {
	if v.rankerErr != nil {
		return RankerProbe{}, v.rankerErr
	}
	return RankerProbe{PairID: proposal.Pair.PairID, Kind: proposal.RankerKind,
		PolicyVersion: proposal.PolicyVersion, WeightsHash: proposal.RankerWeights.SHA256,
		ProbeRef: "fixture-ranker-probe", ObservedAt: time.Now().UTC()}, nil
}

func (v *fixedPairVerifier) VerifyApproval(_ context.Context, proposal PairProposal,
	index ItemIndexGeneration, approval Approval) (ApprovalProof, error) {
	if v.approvalErr != nil {
		return ApprovalProof{}, v.approvalErr
	}
	return ApprovalProof{ProposalID: proposal.ID, IndexID: index.ID,
		PairID: proposal.Pair.PairID, Approval: approval,
		ProofRef: "fixture-approval-proof", ApprovedAt: time.Now().UTC()}, nil
}

type bundleFunc func(context.Context, usermodel.SubjectRef, usermodel.PairRef) (
	usermodel.ServingBundle, usermodel.ServingPointer, error)

func (f bundleFunc) ReadyServingBundle(ctx context.Context, subject usermodel.SubjectRef,
	pair usermodel.PairRef) (usermodel.ServingBundle, usermodel.ServingPointer, error) {
	return f(ctx, subject, pair)
}

func pairFixture(pool PoolRelease) PairProposal {
	pair := usermodel.PairRef{PairID: "pair-fixed-1", SpaceID: "space-fixed-1",
		EncoderID: "user-encoder-1", Kind: "fixed_baseline", FeatureSpecVersion: "user-features-v1",
		FeatureSpecHash: artifacts.Hash([]byte("user-feature-spec")), Dimension: 2, Metric: "dot"}
	return PairProposal{ModuleID: pool.Publication.ModuleID, PoolReleaseID: pool.ID,
		ItemFeatureHash: pool.FeatureHash, Pair: pair,
		UserEncoder: EncoderArtifact{EncoderID: pair.EncoderID, Weights: artifacts.Reference([]byte("user-weights")),
			InputSpecHash: pair.FeatureSpecHash, SpaceID: pair.SpaceID, Dimension: 2, Metric: "dot"},
		ItemEncoder: EncoderArtifact{EncoderID: "item-encoder-1", Weights: artifacts.Reference([]byte("item-weights")),
			InputSpecHash: pool.FeatureHash, SpaceID: pair.SpaceID, Dimension: 2, Metric: "dot"},
		CandidateManifest: artifacts.Reference([]byte("candidate-manifest")), RankerKind: "rule_freshness",
		PolicyVersion: "freshness-v1"}
}

func indexFixture(proposal PairProposal, pool PoolRelease) ItemIndexGeneration {
	return ItemIndexGeneration{ProposalID: proposal.ID, PoolReleaseID: pool.ID,
		PairID: proposal.Pair.PairID, SpaceID: proposal.Pair.SpaceID, Dimension: proposal.Pair.Dimension,
		Metric: proposal.Pair.Metric, ItemEncoder: proposal.ItemEncoder,
		Index:         artifacts.Reference([]byte("item-index-generation")),
		BuildManifest: artifacts.Reference([]byte("item-index-build-manifest")), Items: indexedItems(pool)}
}

func TestRealPostgresPairPrebuildApprovalCASAndBundleGate(t *testing.T) {
	db := isolatedPool(t)
	source := itemFixture()
	poolStore, err := NewStore(db, source)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := poolStore.Rebuild(context.Background(), "module-1")
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fixedPairVerifier{}
	pairs, err := NewPairStore(poolStore, verifier)
	if err != nil {
		t.Fatal(err)
	}
	pendingPairs, err := NewPairStore(poolStore, nil)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := pendingPairs.RegisterProposal(context.Background(), pairFixture(pool))
	if err != nil || proposal.ID == "" {
		t.Fatalf("register candidate proposal: %+v %v", proposal, err)
	}
	if _, err := pairs.AuthorizePair(context.Background(), proposal.Pair); !errors.Is(err, ErrPairPending) {
		t.Fatalf("unapproved candidate authorized: %v", err)
	}
	badIndex := indexFixture(proposal, pool)
	badIndex.Items[0].RevisionID = "other-revision"
	if _, err := pendingPairs.RegisterItemIndex(context.Background(), badIndex); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong item revision accepted: %v", err)
	}
	index, err := pendingPairs.RegisterItemIndex(context.Background(), indexFixture(proposal, pool))
	if err != nil || index.ID == "" {
		t.Fatalf("register prebuilt index: %+v %v", index, err)
	}
	if _, err := pendingPairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1}); !errors.Is(err, ErrPairPending) {
		t.Fatalf("missing DC/index verifier approved candidate: %v", err)
	}
	pending, err := pendingPairs.Plan(context.Background(), "module-1",
		usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"}, 1, nil)
	if err != nil || pending.Status != "pending" || len(pending.Items) != 0 {
		t.Fatalf("missing verifier did not produce explicit Pending: %+v %v", pending, err)
	}
	verifier.wrongSpace = true
	if _, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1}); !errors.Is(err, ErrPairPending) {
		t.Fatalf("same-dimension wrong-space DC probe approved: %v", err)
	}
	verifier.wrongSpace = false
	verifier.rankerErr = ErrPairPending
	if _, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1}); !errors.Is(err, ErrPairPending) {
		t.Fatalf("unprobed ranker approved: %v", err)
	}
	verifier.rankerErr = nil
	approved, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1})
	if err != nil || approved.ID == "" || approved.Index.ID != index.ID {
		t.Fatalf("approved immutable pair release: %+v %v", approved, err)
	}
	replayedApproval, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1})
	if err != nil || replayedApproval.ID != approved.ID {
		t.Fatalf("approval retry created a new release: %+v %v", replayedApproval, err)
	}
	verifier.approvalErr = ErrPairPending
	if _, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1}); !errors.Is(err, ErrPairPending) {
		t.Fatalf("revoked approval replay returned prepared release as live: %v", err)
	}
	verifier.approvalErr = nil
	if _, err := pairs.AuthorizePair(context.Background(), proposal.Pair); !errors.Is(err, ErrPairPending) {
		t.Fatalf("prepared release became active without CAS: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, runErr := pairs.Activate(context.Background(), approved.ID, 0)
			results <- runErr
		}()
	}
	wg.Wait()
	close(results)
	var passed, conflicted int
	for err := range results {
		switch {
		case err == nil:
			passed++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected pair activation outcome: %v", err)
		}
	}
	if passed != 1 || conflicted != 1 {
		t.Fatalf("pair CAS outcomes passed=%d conflict=%d", passed, conflicted)
	}
	grant, err := pairs.AuthorizePair(context.Background(), proposal.Pair)
	if err != nil || !grant.Active || grant.Pair != proposal.Pair || grant.Revision != 1 {
		t.Fatalf("active pair not authorized: %+v %v", grant, err)
	}
	wrong := proposal.Pair
	wrong.SpaceID = "same-dimension-other-space"
	if _, err := pairs.AuthorizePair(context.Background(), wrong); !errors.Is(err, ErrPairPending) {
		t.Fatalf("wrong-space user bundle authorized: %v", err)
	}
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"}
	bundleID := artifacts.Hash([]byte("fixture-bundle"))
	featureID := artifacts.Hash([]byte("fixture-feature"))
	reader := bundleFunc(func(_ context.Context, actual usermodel.SubjectRef, pair usermodel.PairRef) (
		usermodel.ServingBundle, usermodel.ServingPointer, error) {
		if actual != subject || pair != proposal.Pair {
			t.Fatal("Plan requested wrong subject or pair")
		}
		return usermodel.ServingBundle{ID: bundleID, Subject: subject, Pair: pair,
				FeatureSnapshotID: featureID, EncodingSource: "fixed_baseline", Vector: []float64{1, 2}},
			usermodel.ServingPointer{PairID: pair.PairID, BundleID: bundleID, State: "active"}, nil
	})
	plan, err := pairs.Plan(context.Background(), "module-1", subject, 1, reader)
	if err != nil || plan.Status != "rule_baseline" || plan.RankingState != "freshness_rule" ||
		len(plan.Items) != 1 || plan.Items[0].ItemID != "article-2" || plan.PairReleaseID != approved.ID ||
		plan.BundleID != bundleID {
		t.Fatalf("fixed approved pair baseline plan: %+v %v", plan, err)
	}
	wrongReader := bundleFunc(func(_ context.Context, actual usermodel.SubjectRef, pair usermodel.PairRef) (
		usermodel.ServingBundle, usermodel.ServingPointer, error) {
		bundle, pointer, _ := reader.ReadyServingBundle(context.Background(), actual, pair)
		bundle.Pair.SpaceID = "wrong-space"
		return bundle, pointer, nil
	})
	plan, err = pairs.Plan(context.Background(), "module-1", subject, 1, wrongReader)
	if err != nil || plan.Status != "pending" || len(plan.Items) != 0 || plan.BundleID != "" {
		t.Fatalf("wrong-space bundle selected: %+v %v", plan, err)
	}
	approvedNext, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval-next", Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	var switched sync.Once
	switchingReader := bundleFunc(func(ctx context.Context, actual usermodel.SubjectRef, pair usermodel.PairRef) (
		usermodel.ServingBundle, usermodel.ServingPointer, error) {
		bundle, userPointer, readErr := reader.ReadyServingBundle(ctx, actual, pair)
		switched.Do(func() {
			if _, activateErr := pairs.Activate(ctx, approvedNext.ID, 1); activateErr != nil {
				t.Errorf("concurrent pair switch: %v", activateErr)
			}
		})
		return bundle, userPointer, readErr
	})
	plan, err = pairs.Plan(context.Background(), "module-1", subject, 1, switchingReader)
	if err != nil || plan.Status != "pending" || plan.Reason != "pair_changed" || len(plan.Items) != 0 {
		t.Fatalf("old pair plan escaped after pointer switch: %+v %v", plan, err)
	}
	verifier.modelErr = ErrPairPending
	if _, err := pairs.AuthorizePair(context.Background(), proposal.Pair); !errors.Is(err, ErrPairPending) {
		t.Fatalf("lost DC model proof still authorized pair: %v", err)
	}
	verifier.modelErr = nil
	source.mu.Lock()
	source.current.PublicationRevision = "2"
	source.current.ReleaseID = "release-2"
	source.current.Generation = 2
	source.mu.Unlock()
	plan, err = pairs.Plan(context.Background(), "module-1", subject, 1, reader)
	if err != nil || plan.Status != "pending" || len(plan.Items) != 0 {
		t.Fatalf("RTW content changed but old pair served: %+v %v", plan, err)
	}
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM recommend_pair_releases WHERE encoder_pair_release_id=$1`, approved.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("immutable pair release lost after withdrawal: %d %v", count, err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE recommend_pair_releases SET approval_ref='forged' WHERE encoder_pair_release_id=$1`, approved.ID); err == nil {
		t.Fatal("immutable pair release updated")
	}
}

func TestPairProposalCarriesExactEncoderAndFeatureSpaces(t *testing.T) {
	pool, err := BuildPool(context.Background(), itemFixture(), "module-1")
	if err != nil {
		t.Fatal(err)
	}
	p := pairFixture(pool)
	p.ID, _ = proposalID(p)
	if !validProposal(p) {
		t.Fatal("fixed pair proposal fixture is invalid")
	}
	for _, change := range []func(*PairProposal){
		func(p *PairProposal) { p.ItemEncoder.SpaceID = "other-space" },
		func(p *PairProposal) { p.ItemEncoder.InputSpecHash = artifacts.Hash([]byte("other-item-spec")) },
		func(p *PairProposal) { p.UserEncoder.InputSpecHash = artifacts.Hash([]byte("other-user-spec")) },
		func(p *PairProposal) { p.ItemEncoder.Dimension++ },
		func(p *PairProposal) { p.RankerKind = "model_logit" },
	} {
		bad := p
		change(&bad)
		bad.ID, _ = proposalID(bad)
		if validProposal(bad) || reflect.DeepEqual(bad, p) {
			t.Fatalf("incompatible encoder pair accepted: %+v", bad)
		}
	}
}

func TestRealPostgresModelPairWithoutServingRankerReturnsPending(t *testing.T) {
	db := isolatedPool(t)
	source := itemFixture()
	poolStore, err := NewStore(db, source)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := poolStore.Rebuild(context.Background(), "module-1")
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := NewPairStore(poolStore, &fixedPairVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	input := pairFixture(pool)
	input.Pair.Kind = "model"
	input.RankerKind = "model_logit"
	input.RankerWeights = artifacts.Reference([]byte("ranker-weights"))
	proposal, err := pairs.RegisterProposal(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	index, err := pairs.RegisterItemIndex(context.Background(), indexFixture(proposal, pool))
	if err != nil {
		t.Fatal(err)
	}
	release, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-model-approval", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairs.Activate(context.Background(), release.ID, 0); err != nil {
		t.Fatal(err)
	}
	subject := usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "model-user"}
	bundleID := artifacts.Hash([]byte("fixture-model-bundle"))
	featureID := artifacts.Hash([]byte("fixture-model-feature"))
	reader := bundleFunc(func(_ context.Context, _ usermodel.SubjectRef, pair usermodel.PairRef) (
		usermodel.ServingBundle, usermodel.ServingPointer, error) {
		return usermodel.ServingBundle{ID: bundleID, Subject: subject, Pair: pair,
				FeatureSnapshotID: featureID, EncodingSource: "dc_prediction",
				ModelCallID: "fixture-prediction", Vector: []float64{1, 2}},
			usermodel.ServingPointer{PairID: pair.PairID, BundleID: bundleID, State: "active"}, nil
	})
	plan, err := pairs.Plan(context.Background(), "module-1", subject, 10, reader)
	if err != nil || plan.Status != "pending" || plan.Reason != "ranker_unavailable" ||
		plan.RankingState != "not_run" || len(plan.Items) != 0 || plan.PairReleaseID != release.ID {
		t.Fatalf("model pair without verified serving scorer emitted a slate: %+v %v", plan, err)
	}
}

func TestRealPostgresPairActivationUnknownAfterCommittedPointer(t *testing.T) {
	db := isolatedPool(t)
	source := itemFixture()
	poolStore, err := NewStore(db, source)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := poolStore.Rebuild(context.Background(), "module-1")
	if err != nil {
		t.Fatal(err)
	}
	verifier := &failAfterCommitVerifier{fixedPairVerifier: &fixedPairVerifier{}}
	pairs, err := NewPairStore(poolStore, verifier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := pairs.RegisterProposal(context.Background(), pairFixture(pool))
	if err != nil {
		t.Fatal(err)
	}
	index, err := pairs.RegisterItemIndex(context.Background(), indexFixture(proposal, pool))
	if err != nil {
		t.Fatal(err)
	}
	release, err := pairs.Approve(context.Background(), proposal.ID, index.ID,
		Approval{Ref: "fixture-approval", Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	verifier.modelCalls.Store(0)
	verifier.failFrom.Store(3) // pre-CAS and in-transaction pass; post-commit probe fails.
	committed, err := pairs.Activate(context.Background(), release.ID, 0)
	if !errors.Is(err, ErrActivationUnknown) || committed.ReleaseID != release.ID || committed.Version != 1 {
		t.Fatalf("committed pointer hidden behind post-check failure: %+v %v", committed, err)
	}
	raw, err := pairs.CurrentPointer(context.Background(), "module-1")
	if err != nil || raw != committed {
		t.Fatalf("uncertain activation cannot reconcile committed pointer: %+v %v", raw, err)
	}
	if _, _, err := pairs.Active(context.Background(), "module-1"); !errors.Is(err, ErrPairPending) {
		t.Fatalf("failed post-commit probe still served pair: %v", err)
	}
	pending, err := pairs.Plan(context.Background(), "module-1",
		usermodel.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"}, 1, nil)
	if err != nil || pending.Status != "pending" || len(pending.Items) != 0 {
		t.Fatalf("uncertain committed pointer emitted ranked items: %+v %v", pending, err)
	}
	verifier.failFrom.Store(0)
	if active, pointer, err := pairs.Active(context.Background(), "module-1"); err != nil ||
		active.ID != release.ID || pointer != committed {
		t.Fatalf("reconciled pair cannot resume after probe recovery: %+v %+v %v", active, pointer, err)
	}
}
