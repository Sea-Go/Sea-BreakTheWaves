package search

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session/postgres"
)

func TestRootSessionReadBoundary(t *testing.T) {
	if os.Getenv("SEARCH_SESSION_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRootSessionReadBoundary$", "-test.v")
		cmd.Env = append(os.Environ(), "SEARCH_SESSION_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated framework Session acceptance: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-session-test",
		Environment: "test", Version: "fixture-v1", InstanceID: "session-test", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := observed.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()

	for _, backend := range []struct {
		name string
		open func(*testing.T) session.Service
	}{
		{"inmemory", func(*testing.T) session.Service { return inmemory.NewSessionService() }},
		{"postgres", openSessionPostgres},
	} {
		t.Run(backend.name, func(t *testing.T) {
			svc := backend.open(t)
			defer svc.Close()
			model := &sessionProbeModel{}
			delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
			root, err := NewRootSummarizer(delivery, model, svc, observed)
			if err != nil {
				t.Fatal(err)
			}
			q := rootFixtureRequest("raw-" + backend.name)
			model.invalid.Store(true)
			out, err := root.Summarize(context.Background(), q)
			if err == nil || !strings.Contains(err.Error(), ErrSummary.Error()) || out.Answer != "" {
				t.Fatalf("bad citation unexpectedly accepted: %+v %v", out, err)
			}
			user, err := q.Subject.UserKey()
			if err != nil {
				t.Fatal(err)
			}
			stored, err := svc.GetSession(context.Background(), session.Key{AppName: "search_summary_root", UserID: user, SessionID: q.SessionID})
			if err != nil || stored == nil {
				t.Fatalf("framework Session missing: %v", err)
			}
			raw, err := json.Marshal(stored)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "unsupported") || !strings.Contains(string(raw), "invented") {
				t.Fatalf("failed answer was not found in readable Session: %s", raw)
			}
			model.invalid.Store(false)
			followup := rootFixtureRequest("raw-followup-" + backend.name)
			followup.SessionID = q.SessionID
			continued, err := root.Summarize(context.Background(), followup)
			if err != nil || continued.SummaryStatus != "succeeded" {
				t.Fatalf("framework Session follow-up failed: %+v %v", continued, err)
			}
			if model.sawRejected.Load() {
				t.Fatal("current isolated LLMAgent unexpectedly read the rejected answer")
			}
			t.Log("isolated LLMAgent did not receive the rejected answer; GetSession still exposes it")
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("accepted-boundary", func(t *testing.T) {
		model := &summaryModel{}
		history := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		boundary, err := NewRootSessionBoundary(delivery, model, history, observed)
		if err != nil {
			t.Fatal(err)
		}
		defer boundary.Close()
		q := rootFixtureRequest("boundary-invalid")
		model.invalid.Store(true)
		bad, err := boundary.Summarize(context.Background(), q)
		if err == nil || bad.Answer != "" || bad.SummaryStatus != "failed" {
			t.Fatalf("invalid citation left safe boundary: %+v %v", bad, err)
		}
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 0)
		assertNoPrivateSessions(t, boundary, q.Subject)

		model.invalid.Store(false)
		q = rootFixtureRequest("boundary-success")
		good, err := boundary.Summarize(context.Background(), q)
		if err != nil || good.SummaryStatus != "succeeded" || good.Answer != "fixture summary" {
			t.Fatalf("valid answer did not commit: %+v %v", good, err)
		}
		if !model.sawTrace.Load() {
			t.Fatal("private Session path lost the framework model trace")
		}
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 1)
		owned, err := boundary.History(context.Background(), q.Subject, q.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		owned[0].Request.Search.Snapshot.ValidRevisionIDs[0] = "poisoned"
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 1)
		assertNoPrivateSessions(t, boundary, q.Subject)

		model.invalid.Store(true)
		q = rootFixtureRequest("boundary-invalid-followup")
		bad, err = boundary.Summarize(context.Background(), q)
		if err == nil || bad.Answer != "" {
			t.Fatalf("later invalid answer left safe boundary: %+v %v", bad, err)
		}
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 1)
		model.invalid.Store(false)
		q = rootFixtureRequest("boundary-followup")
		good, err = boundary.Summarize(context.Background(), q)
		if err != nil || good.SummaryStatus != "succeeded" {
			t.Fatalf("logical session cannot continue: %+v %v", good, err)
		}
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 2)
		assertNoPrivateSessions(t, boundary, q.Subject)

		history.mu.Lock()
		history.reject = true
		history.mu.Unlock()
		q = rootFixtureRequest("boundary-commit-failure")
		denied, err := boundary.Summarize(context.Background(), q)
		if err == nil || denied.Answer != "" || denied.SummaryStatus != "failed" {
			t.Fatalf("failed accepted-history commit published answer: %+v %v", denied, err)
		}
		assertAcceptedHistory(t, boundary, q.Subject, q.SessionID, 2)
	})
}

type sessionProbeModel struct {
	summaryModel
	sawRejected atomic.Bool
}

func (m *sessionProbeModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "unsupported") || strings.Contains(message.Content, "invented") {
			m.sawRejected.Store(true)
		}
	}
	return m.summaryModel.GenerateContent(ctx, request)
}

type acceptedHistoryKey struct {
	subject btwruntime.SubjectRef
	session string
}

type acceptedHistoryFixture struct {
	mu     sync.Mutex
	turns  map[acceptedHistoryKey][]AcceptedRootTurn
	reject bool
}

func (s *acceptedHistoryFixture) Commit(_ context.Context, turn AcceptedRootTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject {
		return ErrAcceptedHistory
	}
	key := acceptedHistoryKey{turn.Request.Subject, turn.Request.SessionID}
	for _, existing := range s.turns[key] {
		if existing.Request.AnswerID == turn.Request.AnswerID {
			return ErrAcceptedHistory
		}
	}
	s.turns[key] = append(s.turns[key], turn)
	return nil
}

func (s *acceptedHistoryFixture) List(_ context.Context, subject btwruntime.SubjectRef, sessionID string) ([]AcceptedRootTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AcceptedRootTurn(nil), s.turns[acceptedHistoryKey{subject, sessionID}]...), nil
}

func assertAcceptedHistory(t *testing.T, boundary *RootSessionBoundary, subject btwruntime.SubjectRef, sessionID string, want int) {
	t.Helper()
	turns, err := boundary.History(context.Background(), subject, sessionID)
	if err != nil || len(turns) != want {
		t.Fatalf("accepted history = %d, want %d: %v", len(turns), want, err)
	}
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "unsupported") || strings.Contains(string(raw), "invented") ||
		strings.Contains(string(raw), "search-attempt-") {
		t.Fatalf("private or rejected model content entered accepted history: %s", raw)
	}
}

func assertNoPrivateSessions(t *testing.T, boundary *RootSessionBoundary, subject btwruntime.SubjectRef) {
	t.Helper()
	user, err := subject.UserKey()
	if err != nil {
		t.Fatal(err)
	}
	list, err := boundary.attempts.ListSessions(context.Background(), session.UserKey{AppName: "search_summary_root", UserID: user})
	if err != nil || len(list) != 0 {
		t.Fatalf("private framework sessions not removed: %d %v", len(list), err)
	}
}

func openSessionPostgres(t *testing.T) session.Service {
	t.Helper()
	dsn := os.Getenv("SEARCH_SESSION_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SEARCH_SESSION_TEST_POSTGRES_DSN not set")
	}
	svc, err := postgres.NewService(postgres.WithPostgresClientDSN(dsn),
		postgres.WithSchema("public"), postgres.WithTablePrefix("search_session_acceptance_"),
		postgres.WithEnableAsyncPersist(false))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
