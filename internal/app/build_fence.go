package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/content"
)

type buildFenceClient interface {
	GetBuild(context.Context, string) (ridethewind.Build, error)
	ClaimBuild(context.Context, ridethewind.ClaimBuildReq) (ridethewind.Build, error)
}

// claimAllocatedRTWBuildFence bridges a DC job-local lease to RTW's
// build-wide authority. A lost HTTP reply is recovered by reading that exact
// grant; no BTW calculation is ever sent as the requested RTW epoch.
func claimAllocatedRTWBuildFence(ctx context.Context, builds buildFenceClient,
	prior ridethewind.Build, dc content.Fence) (content.Fence, error) {
	if builds == nil || prior.BuildId == "" || prior.BuildId != dc.BuildID || prior.State != "BUILDING" ||
		prior.Generation < 1 || prior.ManifestHash == "" || prior.CancelVersion != dc.CancelVersion ||
		dc.AttemptID == "" || dc.LeaseEpoch < 1 || !dc.ExpiresAt.After(time.Now()) {
		return content.Fence{}, fmt.Errorf("%w: fixed RTW build and active DC attempt required", content.ErrConflict)
	}
	expiry := dc.ExpiresAt.UTC().Format(time.RFC3339Nano)
	request := ridethewind.ClaimBuildReq{BuildId: prior.BuildId, Generation: prior.Generation,
		ManifestHash: prior.ManifestHash, CancelVersion: prior.CancelVersion,
		AttemptId: dc.AttemptID, LeaseEpoch: 0, LeaseExpiresAt: expiry}
	claimed, claimErr := builds.ClaimBuild(ctx, request)
	if claimErr != nil {
		// The authority may have committed before the transport failed.
		var readErr error
		claimed, readErr = builds.GetBuild(ctx, prior.BuildId)
		if readErr != nil {
			return content.Fence{}, fmt.Errorf("RTW build grant reply uncertain: %w; read: %v", claimErr, readErr)
		}
	}
	expectedEpoch := prior.LeaseEpoch + 1
	if prior.AttemptId == dc.AttemptID {
		expectedEpoch = prior.LeaseEpoch // Same live attempt replay or renewal.
	}
	if expectedEpoch < 1 || claimed.BuildId != prior.BuildId || claimed.ModuleId != prior.ModuleId ||
		claimed.ReleaseId != prior.ReleaseId || claimed.Generation != prior.Generation ||
		claimed.ManifestHash != prior.ManifestHash || claimed.CancelVersion != prior.CancelVersion ||
		claimed.State != "BUILDING" || claimed.AttemptId != dc.AttemptID ||
		claimed.LeaseEpoch != expectedEpoch || claimed.LeaseExpiresAt != expiry {
		return content.Fence{}, fmt.Errorf("%w: RTW grant differs from current build authority", content.ErrConflict)
	}
	return content.Fence{BuildID: prior.BuildId, AttemptID: claimed.AttemptId,
		LeaseEpoch: claimed.LeaseEpoch, CancelVersion: claimed.CancelVersion,
		ExpiresAt: dc.ExpiresAt}, nil
}
