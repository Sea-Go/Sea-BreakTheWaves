package search

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type lateTransportHistory struct {
	mu      sync.Mutex
	turns   []searchdomain.AcceptedRootTurn
	commits atomic.Int32
}

func (h *lateTransportHistory) Commit(ctx context.Context, turn searchdomain.AcceptedRootTurn) error {
	h.mu.Lock()
	h.turns = append(h.turns, turn) // durable fixture write precedes the timeout
	h.mu.Unlock()
	h.commits.Add(1)
	<-ctx.Done()
	return nil // a transport can acknowledge the completed write too late
}

func (h *lateTransportHistory) List(_ context.Context, subject btwruntime.SubjectRef, session string) ([]searchdomain.AcceptedRootTurn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var result []searchdomain.AcceptedRootTurn
	for _, turn := range h.turns {
		if turn.Request.Subject == subject && turn.Request.SessionID == session {
			result = append(result, turn)
		}
	}
	return result, nil
}

type latePlannerModel struct {
	calls atomic.Int32
}

func (*latePlannerModel) Info() model.Info { return model.Info{Name: "late-plan-fixture"} }

func (m *latePlannerModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	ref, ok := searchdomain.ModelInvocationFromContext(ctx)
	if !ok || ref.SearchID != "search-medium-late" || ref.AnswerID != "answer-medium-late" ||
		request.MaxTokens == nil || *request.MaxTokens != 128 || len(request.Tools) != 0 {
		return nil, errors.New("late-commit planner missed fixed signed scope or token limit")
	}
	m.calls.Add(1)
	stream := make(chan *model.Response, 1)
	finish := "stop"
	stream <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(`{"queries":["new retrieval phrase"]}`), FinishReason: &finish}}}
	close(stream)
	return stream, nil
}

func postLateCommitSummary(t *testing.T, server *httptest.Server, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+Route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := server.Client()
	client.Timeout = 4 * time.Second
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var output bytes.Buffer
	if _, err := io.Copy(&output, response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, output.String()
}

func TestFastMediumLateDurableCommitNeverReturnsHTTP200AndLowStillDoes(t *testing.T) {
	if os.Getenv("SEARCH_MEDIUM_LATE_COMMIT_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumLateDurableCommitNeverReturnsHTTP200AndLowStillDoes$", "-test.v")
		cmd.Env = append(os.Environ(), "SEARCH_MEDIUM_LATE_COMMIT_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("late-commit real HTTP subprocess: %v\n%s", err, output)
		} else if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	var logs bytes.Buffer
	bundle, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-late-commit-fixture",
		Environment: "test", Version: "fixture-v1", InstanceID: "late-commit", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &traceSink{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	subject := btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "uid-one"}
	snapshot := fixedSnapshot()
	lowScope := TrustedScope{Subject: subject, SessionID: "fixed-session", SearchID: "search-old-low",
		AnswerID: "answer-old-low", Snapshot: snapshot}
	lowHistory := new(acceptedHistory)
	lowAccepted := new(atomic.Bool)
	lowModel := &fixtureModel{accepted: lowAccepted}
	lowBoundary, err := searchdomain.NewRootSessionBoundary(fixedDelivery(t, lowAccepted, snapshot), lowModel, lowHistory, bundle,
		searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	lowHandler, err := NewHandler(ScopeFunc(func(context.Context, *http.Request, PublicRequest) (TrustedScope, error) {
		return lowScope, nil
	}), lowBoundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	lowServer := httptest.NewServer(lowHandler)
	lowStatus, lowBody := postLateCommitSummary(t, lowServer, `{"module_id":"module-1","query":"why","depth":"fast","intelligence":"low"}`)
	lowServer.Close()
	if lowStatus != http.StatusOK || !strings.Contains(lowBody, `"fixture answer"`) ||
		lowModel.calls.Load() != 1 || len(lowHistory.turns) != 1 {
		t.Fatalf("old low request regressed after post-commit deadline check: status=%d body=%s model/history=%d/%d",
			lowStatus, lowBody, lowModel.calls.Load(), len(lowHistory.turns))
	}
	if err := lowBoundary.Close(); err != nil {
		t.Fatal(err)
	}

	mediumScope := lowScope
	mediumScope.SearchID, mediumScope.AnswerID = "search-medium-late", "answer-medium-late"
	lateHistory := new(lateTransportHistory)
	mediumAccepted := new(atomic.Bool)
	mediumSummary := &fixtureModel{accepted: mediumAccepted}
	mediumPlanner := new(latePlannerModel)
	mediumBoundary, err := searchdomain.NewRootSessionBoundaryWithFastMedium(
		fixedDelivery(t, mediumAccepted, snapshot), mediumSummary, mediumPlanner, lateHistory, bundle,
		searchdomain.FastMediumModelLimits{MaxOutputTokens: 128, WallTime: time.Second},
		searchdomain.SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	mediumHandler, err := NewHandler(ScopeFunc(func(context.Context, *http.Request, PublicRequest) (TrustedScope, error) {
		return mediumScope, nil
	}), mediumBoundary, bundle)
	if err != nil {
		t.Fatal(err)
	}
	mediumServer := httptest.NewServer(mediumHandler)
	mediumStatus, mediumBody := postLateCommitSummary(t, mediumServer, `{"module_id":"module-1","query":"why","depth":"fast","intelligence":"medium"}`)
	mediumServer.Close()
	if mediumStatus != http.StatusGatewayTimeout || !strings.Contains(mediumBody, `"TIMEOUT"`) ||
		strings.Contains(mediumBody, "fixture answer") || mediumPlanner.calls.Load() != 1 ||
		mediumSummary.calls.Load() != 1 || lateHistory.commits.Load() != 1 {
		t.Fatalf("saved nil Commit after deadline produced public answer: status=%d body=%s planner/summary/commit=%d/%d/%d",
			mediumStatus, mediumBody, mediumPlanner.calls.Load(), mediumSummary.calls.Load(), lateHistory.commits.Load())
	}
	turns, err := mediumBoundary.History(context.Background(), subject, mediumScope.SessionID)
	if err != nil || len(turns) != 1 || turns[0].Request.SearchID != mediumScope.SearchID ||
		turns[0].Request.AnswerID != mediumScope.AnswerID || turns[0].Result.Answer == "" {
		t.Fatalf("timeout lost the same fixed-key durable history lookup: %+v, err=%v", turns, err)
	}
	if !strings.Contains(logs.String(), `"error_code":"TIMEOUT"`) ||
		!strings.Contains(logs.String(), `"outcome":"timed_out"`) {
		t.Fatalf("late HTTP completion missed legal OBS timeout record")
	}
	if err := mediumBoundary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
