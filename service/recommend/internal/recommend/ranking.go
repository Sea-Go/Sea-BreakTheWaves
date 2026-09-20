package recommend

import (
	"context"
	"errors"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	usermodel "github.com/Sea-Go/Sea-BreakTheWaves/service/common/usermodelcontract"
)

type BundleReader interface {
	ReadyServingBundle(context.Context, usermodel.SubjectRef, usermodel.PairRef) (
		usermodel.ServingBundle, usermodel.ServingPointer, error)
}

type RankedItem struct {
	ItemID     string   `json:"item_id"`
	RevisionID string   `json:"revision_id"`
	Position   int      `json:"position"`
	Sources    []string `json:"sources"`
}

// ServingPlan is a fixed input record, not a product Slate or impression.
// Pending plans never emit ranked items. The only current ready path is a
// clearly identified fixed-baseline freshness rule after full pair approval.
type ServingPlan struct {
	Status              string            `json:"status"` // pending or rule_baseline
	Reason              string            `json:"reason,omitempty"`
	ModuleID            string            `json:"module_id"`
	PairReleaseID       string            `json:"encoder_pair_release_id,omitempty"`
	Pair                usermodel.PairRef `json:"pair"`
	PairPointerVersion  int64             `json:"pair_pointer_version,omitempty"`
	PoolReleaseID       string            `json:"pool_release_id,omitempty"`
	ItemIndexGeneration string            `json:"item_index_generation_id,omitempty"`
	BundleID            string            `json:"bundle_id,omitempty"`
	FeatureSnapshotID   string            `json:"feature_snapshot_id,omitempty"`
	RankingState        string            `json:"ranking_state"`
	Items               []RankedItem      `json:"items"`
}

func pendingPlan(module, reason string) ServingPlan {
	return ServingPlan{Status: "pending", Reason: reason, ModuleID: module,
		RankingState: "not_run", Items: []RankedItem{}}
}

// Plan freezes the currently approved pair, target module's item index/pool
// and current user bundle before choosing a ranking path. A model ranker has
// no verified serving adapter yet, so it remains Pending even after a pair
// becomes active. The baseline path reports its rule and has no pseudo-score.
func (s *PairStore) Plan(ctx context.Context, moduleID string, subject usermodel.SubjectRef,
	limit int, bundles BundleReader) (ServingPlan, error) {
	if s == nil || ctx == nil || !validID(moduleID) || limit < 1 || limit > MaxCandidates {
		return ServingPlan{}, ErrInvalid
	}
	release, pointer, err := s.Active(ctx, moduleID)
	if err != nil {
		if errors.Is(err, ErrPairPending) || errors.Is(err, ErrStale) || errors.Is(err, ErrUnavailable) {
			return pendingPlan(moduleID, "pair_unavailable"), nil
		}
		return ServingPlan{}, err
	}
	plan := pendingPlan(moduleID, "user_bundle_unavailable")
	plan.PairReleaseID, plan.Pair = release.ID, release.Proposal.Pair
	plan.PairPointerVersion, plan.PoolReleaseID = pointer.Version, release.Proposal.PoolReleaseID
	plan.ItemIndexGeneration = release.Index.ID
	if bundles == nil || nilPairVerifier(bundles) {
		return plan, nil
	}
	bundle, bundlePointer, err := bundles.ReadyServingBundle(ctx, subject, release.Proposal.Pair)
	if err != nil {
		if errors.Is(err, usermodel.ErrPending) || errors.Is(err, usermodel.ErrNotFound) ||
			errors.Is(err, usermodel.ErrConflict) {
			return plan, nil
		}
		return ServingPlan{}, err
	}
	if !artifacts.ValidHash(bundle.ID) || !artifacts.ValidHash(bundle.FeatureSnapshotID) ||
		bundle.Pair != release.Proposal.Pair || bundlePointer.State != "active" ||
		bundlePointer.PairID != release.Proposal.Pair.PairID || bundlePointer.BundleID != bundle.ID ||
		bundle.Subject.AuthorityID != subject.AuthorityID ||
		bundle.Subject.TenantID != subject.TenantID || bundle.Subject.SubjectID != subject.SubjectID ||
		len(bundle.Vector) != release.Proposal.Pair.Dimension {
		return plan, nil
	}
	plan.BundleID, plan.FeatureSnapshotID = bundle.ID, bundle.FeatureSnapshotID
	page, err := s.pool.Candidates(ctx, moduleID, limit)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrStale) {
			plan.Reason = "content_unavailable"
			return plan, nil
		}
		return ServingPlan{}, err
	}
	if page.PoolReleaseID != release.Proposal.PoolReleaseID ||
		page.FeatureHash != release.Proposal.ItemFeatureHash {
		plan.Reason = "content_mismatch"
		return plan, nil
	}
	// No cross-service transaction spans recommend and usermodel. Revalidate
	// both pointers at the response boundary so a concurrent pair switch or
	// user refresh cannot publish a stale rule slate as current.
	latestRelease, latestPointer, err := s.Active(ctx, moduleID)
	if err != nil {
		if errors.Is(err, ErrPairPending) || errors.Is(err, ErrStale) || errors.Is(err, ErrUnavailable) {
			return pendingPlan(moduleID, "pair_changed"), nil
		}
		return ServingPlan{}, err
	}
	if latestRelease.ID != release.ID || latestPointer != pointer {
		return pendingPlan(moduleID, "pair_changed"), nil
	}
	latestBundle, latestBundlePointer, err := bundles.ReadyServingBundle(ctx, subject, release.Proposal.Pair)
	if err != nil {
		if errors.Is(err, usermodel.ErrPending) || errors.Is(err, usermodel.ErrNotFound) ||
			errors.Is(err, usermodel.ErrConflict) {
			return pendingPlan(moduleID, "user_bundle_changed"), nil
		}
		return ServingPlan{}, err
	}
	if latestBundle.ID != bundle.ID || latestBundle.FeatureSnapshotID != bundle.FeatureSnapshotID ||
		latestBundlePointer != bundlePointer {
		return pendingPlan(moduleID, "user_bundle_changed"), nil
	}
	if release.Proposal.RankerKind != "rule_freshness" ||
		release.Proposal.Pair.Kind != "fixed_baseline" || bundle.EncodingSource != "fixed_baseline" {
		plan.Reason = "ranker_unavailable"
		return plan, nil
	}
	plan.Status, plan.Reason, plan.RankingState = "rule_baseline", "", "freshness_rule"
	for i, candidate := range page.Candidates {
		plan.Items = append(plan.Items, RankedItem{ItemID: candidate.Item.ItemID,
			RevisionID: candidate.Item.RevisionID, Position: i + 1,
			Sources: append([]string{}, candidate.Sources...)})
	}
	return plan, nil
}
