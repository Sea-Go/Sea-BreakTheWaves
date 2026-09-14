package app

import (
	"context"
	"errors"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
)

var ErrRTWSearchSnapshot = errors.New("RTW current search snapshot is invalid")

type CurrentSearchSnapshotClient interface {
	GetCurrentSearchSnapshot(context.Context, string) (ridethewind.SearchSnapshot, error)
}

// RTWSearchSnapshotProvider projects only the currently published RTW state
// into BTW's fixed search request. An HTTP scope resolver can combine it with
// authenticated identity and server-issued session/search/answer IDs.
type RTWSearchSnapshotProvider struct{ client CurrentSearchSnapshotClient }

func NewRTWSearchSnapshotProvider(client CurrentSearchSnapshotClient) (*RTWSearchSnapshotProvider, error) {
	if nilDependency(client) {
		return nil, ErrRTWSearchSnapshot
	}
	return &RTWSearchSnapshotProvider{client: client}, nil
}

func (p *RTWSearchSnapshotProvider) Current(ctx context.Context, moduleID string) (searchdomain.Snapshot, error) {
	if p == nil || ctx == nil || moduleID == "" {
		return searchdomain.Snapshot{}, ErrRTWSearchSnapshot
	}
	wire, err := p.client.GetCurrentSearchSnapshot(ctx, moduleID)
	if err != nil {
		return searchdomain.Snapshot{}, err
	}
	if wire.ModuleId != moduleID || wire.ReleaseId == "" || wire.Generation < 1 ||
		wire.PublicationRevision == "" || len(wire.Indexes) != 3 {
		return searchdomain.Snapshot{}, ErrRTWSearchSnapshot
	}
	pointer, err := strconv.ParseInt(wire.PublicationRevision, 10, 64)
	if err != nil || pointer < 1 || strconv.FormatInt(pointer, 10) != wire.PublicationRevision {
		return searchdomain.Snapshot{}, ErrRTWSearchSnapshot
	}
	indexes := make(map[searchdomain.Lane]corpus.Ref, 3)
	for _, lane := range []searchdomain.Lane{searchdomain.Dense, searchdomain.Sparse, searchdomain.MultiVector} {
		ref, ok := wire.Indexes[string(lane)]
		if !ok || !artifacts.ValidHash(ref.Sha256) || ref.Key != "sha256/"+ref.Sha256 {
			return searchdomain.Snapshot{}, ErrRTWSearchSnapshot
		}
		indexes[lane] = corpus.Ref{Key: ref.Key, SHA256: ref.Sha256}
	}
	seen := make(map[string]bool, len(wire.ValidRevisionIds))
	for _, id := range wire.ValidRevisionIds {
		if id == "" || seen[id] {
			return searchdomain.Snapshot{}, ErrRTWSearchSnapshot
		}
		seen[id] = true
	}
	return searchdomain.Snapshot{ModuleID: wire.ModuleId, ReleaseID: wire.ReleaseId,
		Generation: wire.Generation, PublicationRevision: wire.PublicationRevision,
		Indexes: indexes, ValidRevisionIDs: append([]string(nil), wire.ValidRevisionIds...)}, nil
}
