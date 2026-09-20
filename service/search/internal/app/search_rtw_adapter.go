package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

var ErrSearchCitationSource = errors.New("RTW search source or citation receipt differs from fixed evidence")

type SearchCitationClient interface {
	ReadSearchSource(context.Context, ridethewind.ReadSearchSourceReq) (ridethewind.CitationChunk, error)
	AcceptSearchCitations(context.Context, ridethewind.AcceptSearchCitationsReq) (ridethewind.SearchCitationReceipt, error)
	GetSearchCitations(context.Context, string) (ridethewind.SearchCitationRecord, error)
}

// RTWSearchCitationAdapter borrows the RTW worker client. It is the only
// provider-side boundary used by search.Delivery for fixed original quotes and
// durable citation acceptance. It does not grant publication or user scope.
type RTWSearchCitationAdapter struct{ client SearchCitationClient }

func NewRTWSearchCitationAdapter(client SearchCitationClient) (*RTWSearchCitationAdapter, error) {
	if nilDependency(client) {
		return nil, ErrSearchCitationSource
	}
	return &RTWSearchCitationAdapter{client: client}, nil
}

func (a *RTWSearchCitationAdapter) Read(ctx context.Context, snapshot searchdomain.Snapshot,
	candidate searchdomain.VerifiedCandidate) (corpus.Chunk, error) {
	if a == nil || ctx == nil || snapshot.ModuleID == "" || snapshot.ReleaseID == "" ||
		snapshot.Generation <= 0 || snapshot.PublicationRevision == "" || candidate.Chunk.Text != "" ||
		candidate.Key != (searchdomain.Key{SourceKind: candidate.Chunk.SourceKind,
			ContentID: candidate.Chunk.ContentID, RevisionID: candidate.Chunk.RevisionID, ChunkID: candidate.Chunk.ID}) {
		return corpus.Chunk{}, ErrSearchCitationSource
	}
	q := ridethewind.ReadSearchSourceReq{ModuleId: snapshot.ModuleID, ReleaseId: snapshot.ReleaseID,
		Generation: snapshot.Generation, PublicationRevision: snapshot.PublicationRevision,
		RevisionId: candidate.Key.RevisionID, ChunkId: candidate.Key.ChunkID}
	got, err := a.client.ReadSearchSource(ctx, q)
	if err != nil {
		return corpus.Chunk{}, fmt.Errorf("read RTW same-revision chunk: %w", err)
	}
	chunk := corpus.Chunk{ID: got.ChunkId, RevisionID: got.RevisionId, ContentID: got.ContentId,
		SourceKind: got.SourceKind, Original: corpus.Ref{Key: got.Original.Key, SHA256: got.Original.Sha256},
		Location: corpus.Location{Locator: got.Location.Locator, OriginalByteStart: got.Location.OriginalByteStart,
			OriginalByteEnd: got.Location.OriginalByteEnd, NormalizedRuneStart: got.Location.NormalizedRuneStart,
			NormalizedRuneEnd: got.Location.NormalizedRuneEnd},
		Text: got.Text, TextHash: got.TextHash, EncodingKey: got.EncodingKey,
		DuplicateOf: got.DuplicateOf, PreviousID: got.PreviousId, NextID: got.NextId, Required: got.Required}
	indexed := chunk
	indexed.Text = ""
	if !reflect.DeepEqual(indexed, candidate.Chunk) || !artifacts.ValidHash(chunk.TextHash) ||
		artifacts.Hash([]byte(chunk.Text)) != chunk.TextHash || !artifacts.ValidHash(chunk.Original.SHA256) ||
		chunk.Original.Key != "sha256/"+chunk.Original.SHA256 {
		return corpus.Chunk{}, ErrSearchCitationSource
	}
	return chunk, nil
}

func (a *RTWSearchCitationAdapter) Accept(ctx context.Context, pack searchdomain.EvidencePack) (searchdomain.CitationReceipt, error) {
	if a == nil || ctx == nil || pack.SearchID == "" || len(pack.Evidence) == 0 {
		return searchdomain.CitationReceipt{}, ErrSearchCitationSource
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		return searchdomain.CitationReceipt{}, fmt.Errorf("encode fixed evidence: %w", err)
	}
	hash := artifacts.Hash(raw)
	receipt, err := a.client.AcceptSearchCitations(ctx, ridethewind.AcceptSearchCitationsReq{
		SearchId: pack.SearchID, PackJson: string(raw), PackHash: hash})
	if err != nil {
		return searchdomain.CitationReceipt{}, fmt.Errorf("commit RTW citations: %w", err)
	}
	if receipt.SearchId != pack.SearchID || receipt.PackHash != hash || receipt.DurableRef == "" {
		return searchdomain.CitationReceipt{}, ErrSearchCitationSource
	}
	return searchdomain.CitationReceipt{SearchID: receipt.SearchId, PackHash: receipt.PackHash,
		DurableRef: receipt.DurableRef}, nil
}

// Recover only returns the committed metadata receipt for the caller's fixed
// pack hash. It never treats a transport acknowledgement or a new quote read
// as proof that a cancelled search's old citation mapping was accepted.
func (a *RTWSearchCitationAdapter) Recover(ctx context.Context, searchID, expectedHash string) (searchdomain.CitationReceipt, error) {
	if a == nil || ctx == nil || searchID == "" || !artifacts.ValidHash(expectedHash) {
		return searchdomain.CitationReceipt{}, ErrSearchCitationSource
	}
	record, err := a.client.GetSearchCitations(ctx, searchID)
	if err != nil {
		return searchdomain.CitationReceipt{}, fmt.Errorf("recover RTW citation receipt: %w", err)
	}
	if record.SearchId != searchID || record.PackHash != expectedHash || record.DurableRef == "" {
		return searchdomain.CitationReceipt{}, ErrSearchCitationSource
	}
	return searchdomain.CitationReceipt{SearchID: record.SearchId, PackHash: record.PackHash,
		DurableRef: record.DurableRef}, nil
}

var _ searchdomain.SourceReader = (*RTWSearchCitationAdapter)(nil)
var _ searchdomain.CitationAcceptor = (*RTWSearchCitationAdapter)(nil)
