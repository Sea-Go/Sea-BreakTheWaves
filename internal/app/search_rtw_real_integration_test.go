package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"go.opentelemetry.io/otel/trace"
)

// Invoked only by RTW's isolated real-HTTP/PG acceptance test. It consumes
// that process's fixed publication with the generated BTW provider client.
func TestRTWRealProviderCitationAdapter(t *testing.T) {
	path := os.Getenv("SEA_RTW_REAL_CITATION_FIXTURE")
	if path == "" {
		t.Skip("RTW cross-repository fixture path unset")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		BaseURL     string                `json:"base_url"`
		Token       string                `json:"token"`
		TraceIDPath string                `json:"trace_id_path"`
		Snapshot    searchdomain.Snapshot `json:"snapshot"`
		Chunk       corpus.Chunk          `json:"chunk"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	client, err := ridethewind.New(httpclient.Config{BaseURL: fixture.BaseURL, Token: fixture.Token})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewRTWSearchCitationAdapter(client)
	if err != nil {
		t.Fatal(err)
	}
	snapshotProvider, err := NewRTWSearchSnapshotProvider(client)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotProvider.Current(context.Background(), fixture.Snapshot.ModuleID)
	if err != nil || !reflect.DeepEqual(snapshot, fixture.Snapshot) {
		t.Fatalf("real RTW active publication differs from fixed test state: %+v %v", snapshot, err)
	}
	indexed := fixture.Chunk
	indexed.Text = ""
	key := searchdomain.Key{SourceKind: indexed.SourceKind, ContentID: indexed.ContentID,
		RevisionID: indexed.RevisionID, ChunkID: indexed.ID}
	hit := searchdomain.LaneHit{Lane: searchdomain.Dense, Index: snapshot.Indexes[searchdomain.Dense],
		Rank: 1, RawScore: 0.8}
	searcher := searchdomain.ExecuteFunc(func(_ context.Context, request searchdomain.Request) (searchdomain.Result, error) {
		return searchdomain.Result{Status: "complete", StopReason: "batch_complete", Snapshot: request.Snapshot,
			Profile: searchdomain.Profile{RequestedDepth: request.Depth, EffectiveDepth: request.Depth,
				RequestedIntelligence: request.Intelligence, EffectiveIntelligence: request.Intelligence,
				PolicyVersion: "cross-repo-fixture-v1"},
			Candidates: []searchdomain.Candidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
				Sources: []searchdomain.LaneHit{hit}}},
			Verified: []searchdomain.VerifiedCandidate{{Key: key, Chunk: indexed, RRFScore: 1.0 / 61,
				Sources: []searchdomain.LaneHit{hit}}}, UsedSubqueries: 1}, nil
	})
	delivery, err := searchdomain.NewDelivery(searcher, searchdomain.CheckFunc(func(context.Context, searchdomain.Snapshot, corpus.Chunk) (bool, error) {
		return true, nil
	}), adapter, adapter, searchdomain.EvidenceLimits{MaxReads: 2, MaxQuoteRunes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	bundle := testWorkerObservation(t)
	ctx, stage, err := bundle.Begin(context.Background(), "search", "search.cross_repo_citation")
	if err != nil {
		t.Fatal(err)
	}
	request := searchdomain.Request{Query: "fixed citation", Depth: searchdomain.Fast,
		Intelligence: searchdomain.Low, Snapshot: snapshot}
	result, runErr := delivery.Search(ctx, "search-btw-real-provider", request)
	if runErr != nil {
		stage.End(ctx, "failed", "CROSS_REPO_CITATION_FAILED", runErr)
	} else {
		stage.End(ctx, "succeeded", "", nil)
	}
	if runErr != nil || len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].Quote != fixture.Chunk.Text ||
		result.Receipt.DurableRef == "" {
		t.Fatalf("real RTW source/citation consumer failed: %+v %v", result, runErr)
	}
	record, err := client.GetSearchCitations(context.Background(), result.Pack.SearchID)
	if err != nil || record.PackHash != result.Receipt.PackHash || record.DurableRef != result.Receipt.DurableRef ||
		len(record.Evidence) != 1 || record.Evidence[0].QuoteHash != fixture.Chunk.TextHash ||
		record.Evidence[0].State != "available" {
		t.Fatalf("real RTW durable mapping differs: %+v %v", record, err)
	}
	if recovered, err := adapter.Recover(context.Background(), result.Pack.SearchID, result.Receipt.PackHash); err != nil || recovered != result.Receipt {
		t.Fatalf("real RTW receipt cannot be recovered: %+v %v", recovered, err)
	}
	// The same actual RTW publication and citation receipt must also admit a
	// validated product turn through BTW's generated answer-history client.
	history, err := NewRTWAcceptedRootHistory(client)
	if err != nil {
		t.Fatal(err)
	}
	subject := btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "9123"}
	turn := searchdomain.AcceptedRootTurn{
		Request: searchdomain.SummaryRequest{SearchID: result.Pack.SearchID,
			AnswerID: "answer-btw-real-provider", Subject: subject, SessionID: "cross-repo-learning-session",
			Search: request},
		Result: searchdomain.SummaryResult{Search: result, AnswerID: "answer-btw-real-provider",
			Answer:    "The published source contains the cited evidence.",
			Citations: []string{result.Pack.Evidence[0].ID}, SummaryStatus: "succeeded"},
	}
	if err := history.Commit(ctx, turn); err != nil {
		t.Fatalf("real RTW refused the BTW validated product turn: %v", err)
	}
	if err := history.Commit(ctx, turn); err != nil {
		t.Fatalf("real RTW answer idempotent replay failed: %v", err)
	}
	turns, err := history.List(ctx, subject, turn.Request.SessionID)
	if err != nil || len(turns) != 1 || turns[0].Request.AnswerID != turn.Request.AnswerID ||
		turns[0].Result.SummaryStatus != "succeeded" || len(turns[0].Result.Citations) != 1 ||
		turns[0].Result.Citations[0] != result.Pack.Evidence[0].ID {
		t.Fatalf("real RTW accepted history differs from BTW turn: %+v %v", turns, err)
	}
	if _, err := client.GetAcceptedAnswer(ctx, ridethewind.GetAcceptedAnswerReq{
		AnswerId: turn.Request.AnswerID, AuthorityId: subject.AuthorityID,
		TenantId: subject.TenantID, SubjectId: "another-user", SessionId: turn.Request.SessionID}); err == nil {
		t.Fatal("real RTW exposed the accepted answer to another subject")
	}
	traceID := trace.SpanFromContext(ctx).SpanContext().TraceID()
	if !traceID.IsValid() || fixture.TraceIDPath == "" {
		t.Fatal("cross-repository W3C trace or evidence path missing")
	}
	if err := os.WriteFile(fixture.TraceIDPath, []byte(traceID.String()), 0600); err != nil {
		t.Fatal(err)
	}
}
