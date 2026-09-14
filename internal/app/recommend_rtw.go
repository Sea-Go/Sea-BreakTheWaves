package app

import (
	"context"
	"errors"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/recommend"
)

var ErrRTWItemSource = errors.New("RTW item source unavailable or malformed")

type ItemPublicationClient interface {
	GetCurrentSearchSnapshot(context.Context, string) (ridethewind.SearchSnapshot, error)
	GetRevision(context.Context, string) (ridethewind.Revision, error)
}

// RTWItemSource consumes current, manually published RTW Worker refs. It does
// not accept item IDs, index refs or revision metadata from model/client JSON.
type RTWItemSource struct{ client ItemPublicationClient }

func NewRTWItemSource(client ItemPublicationClient) (*RTWItemSource, error) {
	if nilDependency(client) {
		return nil, ErrRTWItemSource
	}
	return &RTWItemSource{client: client}, nil
}

func (s *RTWItemSource) Current(ctx context.Context, moduleID string) (recommend.Publication, error) {
	if s == nil || ctx == nil || moduleID == "" {
		return recommend.Publication{}, ErrRTWItemSource
	}
	wire, err := s.client.GetCurrentSearchSnapshot(ctx, moduleID)
	if err != nil {
		return recommend.Publication{}, err
	}
	indexes := make(map[string]corpus.Ref, len(wire.Indexes))
	for lane, ref := range wire.Indexes {
		indexes[lane] = corpus.Ref{Key: ref.Key, SHA256: ref.Sha256}
	}
	return recommend.Publication{ModuleID: wire.ModuleId, ReleaseID: wire.ReleaseId,
		Generation: wire.Generation, PublicationRevision: wire.PublicationRevision,
		Indexes: indexes, ValidRevisionIDs: append([]string{}, wire.ValidRevisionIds...)}, nil
}

func (s *RTWItemSource) Revision(ctx context.Context, revisionID string) (recommend.Revision, error) {
	if s == nil || ctx == nil || revisionID == "" {
		return recommend.Revision{}, ErrRTWItemSource
	}
	wire, err := s.client.GetRevision(ctx, revisionID)
	if err != nil {
		return recommend.Revision{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, wire.CreatedAt)
	if err != nil {
		return recommend.Revision{}, ErrRTWItemSource
	}
	return recommend.Revision{RevisionID: wire.RevisionId, ModuleID: wire.ModuleId,
		ItemID: wire.EntityId, Kind: wire.Kind, Title: wire.Title, MediaType: wire.MediaType,
		ObjectKey: wire.ObjectKey, ContentHash: wire.ContentHash, CreatedAt: created.UTC(),
		Withdrawn: wire.Withdrawn}, nil
}
