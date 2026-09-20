package app

import (
	"context"
	"errors"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

var ErrRTWEffectiveRevision = errors.New("RTW effective search publication unavailable")

// RTWEffectiveRevisionChecker re-reads the authority's active publication
// before a candidate can become evidence. A moved pointer or withdrawn
// revision is never accepted against a previously signed snapshot.
type RTWEffectiveRevisionChecker struct{ current *RTWSearchSnapshotProvider }

func NewRTWEffectiveRevisionChecker(current *RTWSearchSnapshotProvider) (*RTWEffectiveRevisionChecker, error) {
	if current == nil {
		return nil, ErrRTWEffectiveRevision
	}
	return &RTWEffectiveRevisionChecker{current: current}, nil
}

func (c *RTWEffectiveRevisionChecker) Check(ctx context.Context, fixed searchdomain.Snapshot, chunk corpus.Chunk) (bool, error) {
	if c == nil || c.current == nil || ctx == nil || fixed.ModuleID == "" || chunk.RevisionID == "" {
		return false, ErrRTWEffectiveRevision
	}
	active, err := c.current.Current(ctx, fixed.ModuleID)
	if err != nil {
		return false, err
	}
	if active.ModuleID != fixed.ModuleID || active.ReleaseID != fixed.ReleaseID ||
		active.Generation != fixed.Generation || active.PublicationRevision != fixed.PublicationRevision ||
		!reflect.DeepEqual(active.Indexes, fixed.Indexes) {
		return false, nil
	}
	for _, revision := range active.ValidRevisionIDs {
		if revision == chunk.RevisionID {
			return true, nil
		}
	}
	return false, nil
}

var _ searchdomain.EffectiveRevisionChecker = (*RTWEffectiveRevisionChecker)(nil)
