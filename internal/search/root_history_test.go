package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

// historyProbeModel mirrors the fixed summary fixture but captures the exact
// user prompt bytes the isolated summary agent received.
type historyProbeModel struct {
	mu      sync.Mutex
	prompts []string
	calls   atomic.Int32
	invalid atomic.Bool
}

func (*historyProbeModel) Info() model.Info { return model.Info{Name: "history-probe-fixture"} }

func (m *historyProbeModel) GenerateContent(_ context.Context, q *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	var prompt struct {
		Pack EvidencePack    `json:"fixed_evidence_pack"`
		Hist json.RawMessage `json:"accepted_session_history"`
	}
	for i := len(q.Messages) - 1; i >= 0; i-- {
		if q.Messages[i].Role == model.RoleUser {
			m.mu.Lock()
			m.prompts = append(m.prompts, q.Messages[i].Content)
			m.mu.Unlock()
			_ = json.Unmarshal([]byte(q.Messages[i].Content), &prompt)
			break
		}
	}
	answer := `{"answer":"fixture summary","citations":["wrong"]}`
	if len(prompt.Pack.Evidence) > 0 {
		answer = fmt.Sprintf(`{"answer":"fixture summary","citations":[%q]}`, prompt.Pack.Evidence[0].ID)
	}
	if m.invalid.Load() {
		answer = `{"answer":"unsupported","citations":["invented"]}`
	}
	ch := make(chan *model.Response, 1)
	finish := "stop"
	ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(answer), FinishReason: &finish}}}
	close(ch)
	return ch, nil
}

func (m *historyProbeModel) lastPrompt() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) == 0 {
		return ""
	}
	return m.prompts[len(m.prompts)-1]
}

// The framework telemetry globals are process-wide; all gates below run in one
// re-executed child process with a single installed bundle.
func historyTestTelemetry() (*telemetry.Bundle, error) {
	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	return telemetry.New(context.Background(), telemetry.Config{Service: "btw-search-history-test",
		Environment: "test", Version: "fixture-v1", InstanceID: "history-test", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
}

// TestAcceptedHistoryInjectionGates re-executes itself in an isolated child
// process so the five gates share one installed telemetry bundle.
func TestAcceptedHistoryInjectionGates(t *testing.T) {
	if os.Getenv("SEARCH_HISTORY_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAcceptedHistoryInjectionGates$", "-test.v")
		cmd.Env = append(os.Environ(), "SEARCH_HISTORY_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated accepted-history gates: %v\n%s", err, output)
		} else if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	observed, err := historyTestTelemetry()
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

	t.Run("SeedsAcceptedTurnsOnly", func(t *testing.T) {
		probe := &historyProbeModel{}
		history := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		boundary, bErr := NewRootSessionBoundaryWithHistorySeed(delivery, probe, history, observed,
			RootHistoryBudget{MaxTurns: 4, MaxBytes: 8192})
		if bErr != nil {
			t.Fatal(bErr)
		}
		defer boundary.Close()

		first := rootFixtureRequest("history-rejected")
		probe.invalid.Store(true)
		if bad, sumErr := boundary.Summarize(context.Background(), first); sumErr == nil || bad.Answer != "" {
			t.Fatalf("rejected attempt unexpectedly succeeded: %+v %v", bad, sumErr)
		}
		probe.invalid.Store(false)
		for _, id := range []string{"history-accepted-1", "history-accepted-2"} {
			if out, sumErr := boundary.Summarize(context.Background(), rootFixtureRequest(id)); sumErr != nil ||
				out.SummaryStatus != "succeeded" {
				t.Fatalf("accepted attempt %s failed: %+v %v", id, out, sumErr)
			}
		}
		assertAcceptedHistory(t, boundary, first.Subject, first.SessionID, 2)

		next := rootFixtureRequest("history-injected")
		out, sumErr := boundary.Summarize(context.Background(), next)
		if sumErr != nil || out.SummaryStatus != "succeeded" {
			t.Fatalf("injected attempt failed: %+v %v", out, sumErr)
		}
		prompt := probe.lastPrompt()
		var envelope struct {
			Question string          `json:"question"`
			Pack     EvidencePack    `json:"fixed_evidence_pack"`
			Hist     json.RawMessage `json:"accepted_session_history"`
		}
		if json.Unmarshal([]byte(prompt), &envelope) != nil {
			t.Fatalf("prompt is not the fixed envelope: %s", prompt)
		}
		var injected struct {
			Turns  []acceptedHistoryTurn `json:"accepted_session_history"`
			Budget RootHistoryBudget     `json:"accepted_history_budget"`
		}
		if json.Unmarshal(envelope.Hist, &injected) != nil {
			t.Fatalf("accepted_session_history is not the canonical block: %s", envelope.Hist)
		}
		if injected.Budget != (RootHistoryBudget{MaxTurns: 4, MaxBytes: 8192}) {
			t.Fatalf("injected budget drifted: %+v", injected.Budget)
		}
		if len(injected.Turns) != 2 {
			t.Fatalf("injected turns = %d, want the two accepted turns: %s", len(injected.Turns), envelope.Hist)
		}
		for i, id := range []string{"answer-history-accepted-1", "answer-history-accepted-2"} {
			turn := injected.Turns[i]
			if turn.AnswerID != id || turn.Question != "where" || turn.Answer != "fixture summary" ||
				len(turn.Citations) != 1 || turn.Citations[0].EvidenceID == "" || turn.Citations[0].Quote == "" {
				t.Fatalf("injected turn %d lost accepted facts: %+v", i, turn)
			}
		}
		if strings.Contains(prompt, "unsupported") || strings.Contains(prompt, "invented") {
			t.Fatalf("rejected model output entered the next prompt: %s", prompt)
		}
		if len(out.Citations) != 1 || out.Citations[0] != envelope.Pack.Evidence[0].ID {
			t.Fatalf("injected attempt cited history instead of the current pack: %+v", out.Citations)
		}
		assertNoPrivateSessions(t, boundary, next.Subject)
	})

	t.Run("WithoutBudgetNeverInjects", func(t *testing.T) {
		probe := &historyProbeModel{}
		history := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		boundary, bErr := NewRootSessionBoundary(delivery, probe, history, observed)
		if bErr != nil {
			t.Fatal(bErr)
		}
		defer boundary.Close()
		first := rootFixtureRequest("nobudget-first")
		if out, sumErr := boundary.Summarize(context.Background(), first); sumErr != nil || out.SummaryStatus != "succeeded" {
			t.Fatalf("first attempt failed: %+v %v", out, sumErr)
		}
		second := rootFixtureRequest("nobudget-second")
		if out, sumErr := boundary.Summarize(context.Background(), second); sumErr != nil || out.SummaryStatus != "succeeded" {
			t.Fatalf("second attempt failed: %+v %v", out, sumErr)
		}
		assertAcceptedHistory(t, boundary, first.Subject, first.SessionID, 2)
		if strings.Contains(probe.lastPrompt(), "accepted_session_history") {
			t.Fatalf("zero-budget boundary injected history: %s", probe.lastPrompt())
		}
	})

	t.Run("FailsClosedOnCorruptedQuote", func(t *testing.T) {
		probe := &historyProbeModel{}
		history := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		boundary, bErr := NewRootSessionBoundaryWithHistorySeed(delivery, probe, history, observed,
			RootHistoryBudget{MaxTurns: 4, MaxBytes: 8192})
		if bErr != nil {
			t.Fatal(bErr)
		}
		defer boundary.Close()
		first := rootFixtureRequest("corrupt-first")
		if out, sumErr := boundary.Summarize(context.Background(), first); sumErr != nil || out.SummaryStatus != "succeeded" {
			t.Fatalf("first attempt failed: %+v %v", out, sumErr)
		}
		key := acceptedHistoryKey{first.Subject, first.SessionID}
		history.mu.Lock()
		doctored := history.turns[key][0]
		for i := range doctored.Result.Search.Pack.Evidence {
			doctored.Result.Search.Pack.Evidence[i].Quote = ""
		}
		history.turns[key] = append(history.turns[key], doctored)
		history.mu.Unlock()

		before := probe.calls.Load()
		if _, sumErr := boundary.Summarize(context.Background(), rootFixtureRequest("corrupt-next")); !errors.Is(sumErr, ErrAcceptedHistory) {
			t.Fatalf("corrupted history did not fail closed: %v", sumErr)
		}
		if called := probe.calls.Load(); called != before {
			t.Fatalf("model ran against a corrupted history: calls=%d", called-before)
		}
	})

	t.Run("FailsClosedWhenOwnerUnavailable", func(t *testing.T) {
		probe := &historyProbeModel{}
		history := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		boundary, bErr := NewRootSessionBoundaryWithHistorySeed(delivery, probe, history, observed,
			RootHistoryBudget{MaxTurns: 2, MaxBytes: 4096})
		if bErr != nil {
			t.Fatal(bErr)
		}
		defer boundary.Close()
		history.mu.Lock()
		history.failList = true
		history.mu.Unlock()
		before := probe.calls.Load()
		if _, sumErr := boundary.Summarize(context.Background(), rootFixtureRequest("unavailable-owner")); sumErr == nil ||
			!strings.Contains(sumErr.Error(), "accepted history owner unavailable") {
			t.Fatalf("unavailable owner did not fail the request closed: %v", sumErr)
		}
		if called := probe.calls.Load(); called != before {
			t.Fatalf("model ran without its accepted history: calls=%d", called-before)
		}
	})

	t.Run("TamperedContextSeedFailsClosed", func(t *testing.T) {
		probe := &historyProbeModel{}
		sessions := inmemory.NewSessionService()
		defer sessions.Close()
		delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource), AcceptFunc(accepted))
		root, bErr := NewRootSummarizer(delivery, probe, sessions, observed)
		if bErr != nil {
			t.Fatal(bErr)
		}
		defer root.Close()
		seed := acceptedHistorySeed{Budget: RootHistoryBudget{MaxTurns: 2, MaxBytes: 4096},
			Turns: []acceptedHistoryTurn{{SearchID: "s", AnswerID: "a", Question: "q", Answer: "x",
				Citations: []acceptedHistoryCitation{{EvidenceID: "e", Quote: "q"}}}},
			Block: "{\"tampered\":true}"}
		before := probe.calls.Load()
		if _, sumErr := root.Summarize(WithAcceptedHistorySeed(context.Background(), seed), rootFixtureRequest("tampered")); sumErr == nil {
			t.Fatal("tampered seed entered the summary prompt")
		}
		if called := probe.calls.Load(); called != before {
			t.Fatalf("model consumed a tampered seed: calls=%d", called-before)
		}
	})
}

// TestRenderAcceptedHistoryBlockBudget pins the budget math directly: newest
// turns win, whole turns drop oldest-first, and every budget must be a valid
// explicit pair.
func TestRenderAcceptedHistoryBlockBudget(t *testing.T) {
	budget := RootHistoryBudget{MaxTurns: 2, MaxBytes: 32 << 10}
	turns := make([]acceptedHistoryTurn, 0, 4)
	for i := 0; i < 4; i++ {
		turns = append(turns, acceptedHistoryTurn{SearchID: fmt.Sprintf("s%d", i), AnswerID: fmt.Sprintf("a%d", i),
			Question: fmt.Sprintf("q%d", i), Answer: fmt.Sprintf("x%d", i),
			Citations: []acceptedHistoryCitation{{EvidenceID: "e", Quote: "quote"}}})
	}
	block, err := renderAcceptedHistoryBlock(turns, budget)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Turns []acceptedHistoryTurn `json:"accepted_session_history"`
	}
	if json.Unmarshal([]byte(block), &parsed) != nil || len(parsed.Turns) != 2 ||
		parsed.Turns[0].AnswerID != "a2" || parsed.Turns[1].AnswerID != "a3" {
		t.Fatalf("budget kept the wrong turns: %s", block)
	}
	tight := RootHistoryBudget{MaxTurns: 8, MaxBytes: len(`{"accepted_session_history":[],"accepted_history_budget":{"max_turns":8,"max_bytes":0}}`) + 180}
	dropped, err := renderAcceptedHistoryBlock(turns, tight)
	if err != nil {
		t.Fatal(err)
	}
	if dropped == "" || strings.Contains(dropped, "a0\")") {
		t.Fatalf("oversized block did not drop whole oldest turns: %s", dropped)
	}
	empty, err := renderAcceptedHistoryBlock(nil, budget)
	if err != nil || empty != "" {
		t.Fatalf("empty history must render no block: %q %v", empty, err)
	}
	if _, err := renderAcceptedHistoryBlock(turns, RootHistoryBudget{MaxTurns: 0, MaxBytes: 1024}); !errors.Is(err, ErrHistoryBudget) {
		t.Fatalf("one-sided budget accepted: %v", err)
	}
	if _, err := renderAcceptedHistoryBlock(turns, RootHistoryBudget{MaxTurns: 9, MaxBytes: 1024}); !errors.Is(err, ErrHistoryBudget) {
		t.Fatalf("out-of-range budget accepted: %v", err)
	}
}
