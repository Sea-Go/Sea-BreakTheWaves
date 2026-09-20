package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
	"go.opentelemetry.io/otel"
)

func TestRTWSearchCitationAdapterHTTPReceiptRecovery(t *testing.T) {
	_ = testWorkerObservation(t) // install the same process tracer/propagator as a real BTW worker
	original := artifacts.Reference([]byte("original bytes"))
	quote := "citation text"
	chunkID := artifacts.Hash([]byte("chunk identity"))
	indexed := corpus.Chunk{ID: chunkID, RevisionID: "rev-1", ContentID: "doc-1", SourceKind: "wiki",
		Original: original, Location: corpus.Location{Locator: "paragraph:1", OriginalByteStart: 0,
			OriginalByteEnd: len(quote), NormalizedRuneStart: 0, NormalizedRuneEnd: len([]rune(quote))},
		TextHash: artifacts.Hash([]byte(quote)), EncodingKey: "bge-m3-v1", Required: true}
	key := searchdomain.Key{SourceKind: indexed.SourceKind, ContentID: indexed.ContentID,
		RevisionID: indexed.RevisionID, ChunkID: indexed.ID}
	snapshot := searchdomain.Snapshot{ModuleID: "module-1", ReleaseID: "release-1", Generation: 3,
		PublicationRevision: "7", ValidRevisionIDs: []string{indexed.RevisionID},
		Indexes: map[searchdomain.Lane]corpus.Ref{
			searchdomain.Dense:       artifacts.Reference([]byte("dense")),
			searchdomain.Sparse:      artifacts.Reference([]byte("sparse")),
			searchdomain.MultiVector: artifacts.Reference([]byte("multi")),
		}}
	hit := searchdomain.LaneHit{Lane: searchdomain.Dense, Rank: 1, RawScore: 0.8, Index: snapshot.Indexes[searchdomain.Dense]}
	var mu sync.Mutex
	stored := map[string]ridethewind.SearchCitationRecord{}
	var readTrace, acceptTrace string
	var corruptSource bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/knowledge/search-sources/read":
			mu.Lock()
			readTrace = r.Header.Get("traceparent")
			bad := corruptSource
			mu.Unlock()
			var q ridethewind.ReadSearchSourceReq
			if json.NewDecoder(r.Body).Decode(&q) != nil || q.ModuleId != snapshot.ModuleID || q.ReleaseId != snapshot.ReleaseID ||
				q.Generation != snapshot.Generation || q.PublicationRevision != snapshot.PublicationRevision ||
				q.RevisionId != indexed.RevisionID || q.ChunkId != indexed.ID {
				http.Error(w, "wrong source", http.StatusConflict)
				return
			}
			body := quote
			if bad {
				body = "forged source"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": ridethewind.CitationChunk{
				ChunkId: indexed.ID, RevisionId: indexed.RevisionID, ContentId: indexed.ContentID,
				SourceKind: indexed.SourceKind, Original: ridethewind.CitationObject{Key: original.Key, Sha256: original.SHA256},
				Location: ridethewind.CitationLocation{Locator: indexed.Location.Locator,
					OriginalByteStart: indexed.Location.OriginalByteStart, OriginalByteEnd: indexed.Location.OriginalByteEnd,
					NormalizedRuneStart: indexed.Location.NormalizedRuneStart, NormalizedRuneEnd: indexed.Location.NormalizedRuneEnd},
				Text: body, TextHash: indexed.TextHash, EncodingKey: indexed.EncodingKey, Required: true}})
		case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/knowledge/search-citations":
			mu.Lock()
			acceptTrace = r.Header.Get("traceparent")
			mu.Unlock()
			var q ridethewind.AcceptSearchCitationsReq
			if json.NewDecoder(r.Body).Decode(&q) != nil || q.SearchId == "" ||
				artifacts.Hash([]byte(q.PackJson)) != q.PackHash {
				http.Error(w, "wrong pack", http.StatusConflict)
				return
			}
			mu.Lock()
			stored[q.SearchId] = ridethewind.SearchCitationRecord{SearchId: q.SearchId, PackHash: q.PackHash,
				DurableRef: "search-citations/sha256/" + artifacts.Hash([]byte(q.SearchId))}
			mu.Unlock()
			if q.SearchId == "search-uncertain" {
				http.Error(w, "receipt lost after commit", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": ridethewind.SearchCitationReceipt{
				SearchId: q.SearchId, PackHash: q.PackHash,
				DurableRef: "search-citations/sha256/" + artifacts.Hash([]byte(q.SearchId))}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/v1/knowledge/search-citations/"):
			id := strings.TrimPrefix(r.URL.Path, "/internal/v1/knowledge/search-citations/")
			mu.Lock()
			record, ok := stored[id]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": record})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := ridethewind.New(httpclient.Config{BaseURL: server.URL, Token: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewRTWSearchCitationAdapter(client)
	if err != nil {
		t.Fatal(err)
	}
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, q searchdomain.Request) (searchdomain.Result, error) {
		return searchdomain.Result{Status: "complete", StopReason: "batch_complete", Snapshot: q.Snapshot,
			Profile: searchdomain.Profile{RequestedDepth: q.Depth, EffectiveDepth: q.Depth,
				RequestedIntelligence: q.Intelligence, EffectiveIntelligence: q.Intelligence, PolicyVersion: "fixture-p1"},
			Candidates: []searchdomain.Candidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61, Sources: []searchdomain.LaneHit{hit}}},
			Verified: []searchdomain.VerifiedCandidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
				Sources: []searchdomain.LaneHit{hit}}}, UsedSubqueries: 1}, nil
	})
	delivery, err := searchdomain.NewDelivery(searcher, searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
		return true, nil
	}), adapter, adapter, searchdomain.EvidenceLimits{MaxReads: 2, MaxQuoteRunes: 100})
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := otel.Tracer("rtw-citation-consumer-test").Start(context.Background(), "search-parent")
	request := searchdomain.Request{Query: "why", Depth: searchdomain.Fast, Intelligence: searchdomain.Low, Snapshot: snapshot}
	uncertain, err := delivery.Search(ctx, "search-uncertain", request)
	if err == nil || len(uncertain.Pack.Evidence) != 0 {
		t.Fatalf("uncertain RTW commit became public citation: %+v %v", uncertain, err)
	}
	mu.Lock()
	prior := stored["search-uncertain"]
	mu.Unlock()
	if prior.PackHash == "" {
		t.Fatal("fixture did not commit before losing response")
	}
	recovered, err := adapter.Recover(ctx, "search-uncertain", prior.PackHash)
	if err != nil || recovered.PackHash != prior.PackHash || recovered.DurableRef != prior.DurableRef {
		t.Fatalf("failed to recover RTW committed receipt: %+v %v", recovered, err)
	}
	if _, err := adapter.Recover(ctx, "search-uncertain", strings.Repeat("f", 64)); !errors.Is(err, ErrSearchCitationSource) {
		t.Fatalf("wrong fixed hash recovered citation: %v", err)
	}
	accepted, err := delivery.Search(ctx, "search-accepted", request)
	span.End()
	if err != nil || len(accepted.Pack.Evidence) != 1 || accepted.Pack.Evidence[0].Quote != quote ||
		accepted.Receipt.DurableRef == "" {
		t.Fatalf("same-version source/RTW receipt not delivered: %+v %v", accepted, err)
	}
	mu.Lock()
	corruptSource = true
	mu.Unlock()
	corrupt, err := delivery.Search(ctx, "search-corrupt", request)
	mu.Lock()
	_, committedCorrupt := stored["search-corrupt"]
	mu.Unlock()
	if !errors.Is(err, ErrSearchCitationSource) || len(corrupt.Pack.Evidence) != 0 || committedCorrupt {
		t.Fatalf("corrupt RTW source reached citation commit: %+v %v committed=%v", corrupt, err, committedCorrupt)
	}
	mu.Lock()
	readParent, acceptParent := readTrace, acceptTrace
	mu.Unlock()
	if len(readParent) < 35 || len(acceptParent) < 35 || readParent[:35] != acceptParent[:35] {
		t.Fatalf("RTW read and accept lost common W3C parent: read=%q accept=%q", readParent, acceptParent)
	}
}
