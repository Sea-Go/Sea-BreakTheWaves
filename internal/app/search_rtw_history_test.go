package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"go.opentelemetry.io/otel"
)

func acceptedEmptyTurn() searchdomain.AcceptedRootTurn {
	snapshot := searchdomain.Snapshot{ModuleID: "module-1", ReleaseID: "release-1", Generation: 3,
		PublicationRevision: "7", ValidRevisionIDs: []string{},
		Indexes: map[searchdomain.Lane]corpus.Ref{
			searchdomain.Dense:       artifacts.Reference([]byte("dense")),
			searchdomain.Sparse:      artifacts.Reference([]byte("sparse")),
			searchdomain.MultiVector: artifacts.Reference([]byte("multi"))}}
	profile := searchdomain.Profile{RequestedDepth: searchdomain.Fast, EffectiveDepth: searchdomain.Fast,
		RequestedIntelligence: searchdomain.Low, EffectiveIntelligence: searchdomain.Low, PolicyVersion: "fixture-p1"}
	q := searchdomain.SummaryRequest{SearchID: "search-1", AnswerID: "answer-1",
		Subject:   btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "conversation-1", Search: searchdomain.Request{Query: "why", Depth: searchdomain.Fast,
			Intelligence: searchdomain.Low, Snapshot: snapshot}}
	result := searchdomain.SummaryResult{AnswerID: q.AnswerID, SummaryStatus: "insufficient",
		Search: searchdomain.SearchResult{Retrieval: searchdomain.Result{Status: "empty", StopReason: "no_evidence",
			Profile: profile, Snapshot: snapshot}, Pack: searchdomain.EvidencePack{SearchID: q.SearchID,
			Snapshot: snapshot, Profile: profile, Status: "empty", StopReason: "no_evidence",
			CoverageStatus: "not_assessed", Gaps: []string{}, Evidence: []searchdomain.Evidence{}}}}
	return searchdomain.AcceptedRootTurn{Request: q, Result: result}
}

func TestRTWAcceptedHistoryHTTPCommitRecoveryAndScope(t *testing.T) {
	_ = testWorkerObservation(t)
	turn := acceptedEmptyTurn()
	var mu sync.Mutex
	var stored ridethewind.AcceptedAnswer
	var commitTrace, getTrace string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/knowledge/accepted-answers":
			var q ridethewind.CommitAcceptedAnswerReq
			if json.NewDecoder(r.Body).Decode(&q) != nil || q.AnswerId != turn.Request.AnswerID ||
				q.SearchId != turn.Request.SearchID || q.Subject.SubjectId != turn.Request.Subject.SubjectID {
				http.Error(w, "bad commit", http.StatusConflict)
				return
			}
			mu.Lock()
			commitTrace = r.Header.Get("traceparent")
			stored = ridethewind.AcceptedAnswer{AnswerId: q.AnswerId, SearchId: q.SearchId,
				Subject: q.Subject, SessionId: q.SessionId, TurnJson: q.TurnJson,
				Status: "insufficient", AcceptedOrdinal: 1, AcceptedAt: "2026-09-14T10:00:00Z"}
			mu.Unlock()
			// RTW committed the product turn, then its HTTP response was lost.
			http.Error(w, "post-commit receipt lost", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && r.URL.Path == "/internal/v1/knowledge/accepted-answers/answer-1":
			mu.Lock()
			getTrace = r.Header.Get("traceparent")
			answer := stored
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": answer})
		case r.Method == http.MethodGet && r.URL.Path == "/internal/v1/knowledge/accepted-answers":
			mu.Lock()
			answer := stored
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": ridethewind.AcceptedAnswersPage{
				Items: []ridethewind.AcceptedAnswer{answer}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := ridethewind.New(httpclient.Config{BaseURL: server.URL, Token: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewRTWAcceptedRootHistory(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := otel.Tracer("rtw-history-consumer-test").Start(context.Background(), "answer-parent")
	if err := history.Commit(ctx, turn); err != nil {
		t.Fatalf("lost RTW Commit receipt did not recover by AnswerID: %v", err)
	}
	turns, err := history.List(ctx, turn.Request.Subject, turn.Request.SessionID)
	span.End()
	if err != nil || len(turns) != 1 || turns[0].Request.AnswerID != turn.Request.AnswerID ||
		turns[0].Result.SummaryStatus != "insufficient" {
		t.Fatalf("accepted product history not read: %+v %v", turns, err)
	}
	mu.Lock()
	postParent, getParent := commitTrace, getTrace
	mu.Unlock()
	if len(postParent) < 35 || len(getParent) < 35 || postParent[:35] != getParent[:35] {
		t.Fatalf("RTW commit/get lost same W3C run: commit=%q get=%q", postParent, getParent)
	}
	wrong := turn.Request.Subject
	wrong.TenantID = "other"
	if _, err := history.List(context.Background(), wrong, turn.Request.SessionID); err == nil {
		t.Fatalf("wrong subject received product history: %v", err)
	}
	mu.Lock()
	stored.TurnJson = `{"invalid":"history"}`
	mu.Unlock()
	if _, err := history.List(context.Background(), turn.Request.Subject, turn.Request.SessionID); !errors.Is(err, ErrRTWAcceptedHistory) {
		t.Fatalf("corrupted RTW turn reached product history: %v", err)
	}
}

func TestRTWAcceptedHistoryRejectsMalformedScope(t *testing.T) {
	if _, err := NewRTWAcceptedRootHistory(nil); !errors.Is(err, ErrRTWAcceptedHistory) {
		t.Fatalf("nil client: %v", err)
	}
	invalid := acceptedEmptyTurn()
	invalid.Request.Subject.TenantID = ""
	if err := (&RTWAcceptedRootHistory{}).Commit(context.Background(), invalid); !errors.Is(err, ErrRTWAcceptedHistory) {
		t.Fatalf("incomplete subject reached RTW: %v", err)
	}
}
