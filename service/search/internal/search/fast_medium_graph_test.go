package search

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type mediumPlannerFixture struct {
	output   atomic.Value
	expected atomic.Value
	calls    atomic.Int32
	scope    atomic.Bool
	tokens   atomic.Bool
}

func newSummaryModelFixture(receipt *atomic.Bool) *summaryModel {
	return &summaryModel{receipt: receipt}
}

type deadlineAcceptedHistory struct{ commits atomic.Int32 }

type lateAcceptedHistory struct {
	inner   *acceptedHistoryFixture
	commits atomic.Int32
}

func (h *lateAcceptedHistory) Commit(ctx context.Context, turn AcceptedRootTurn) error {
	if err := h.inner.Commit(context.Background(), turn); err != nil {
		return err
	}
	h.commits.Add(1)
	<-ctx.Done()
	return nil
}

func (h *lateAcceptedHistory) List(ctx context.Context, subject btwruntime.SubjectRef, session string) ([]AcceptedRootTurn, error) {
	return h.inner.List(ctx, subject, session)
}

func (h *deadlineAcceptedHistory) Commit(ctx context.Context, _ AcceptedRootTurn) error {
	h.commits.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func (*deadlineAcceptedHistory) List(context.Context, btwruntime.SubjectRef, string) ([]AcceptedRootTurn, error) {
	return []AcceptedRootTurn{}, nil
}

func (*mediumPlannerFixture) Info() model.Info { return model.Info{Name: "medium-plan-fixture"} }

func (m *mediumPlannerFixture) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	expected, expectedOK := m.expected.Load().(ModelInvocationRef)
	if ref, ok := ModelInvocationFromContext(ctx); !ok || !expectedOK || ref != expected {
		return nil, errors.New("native planning model missed exact RTW signed invocation scope")
	}
	m.scope.Store(true)
	if request.MaxTokens != nil && *request.MaxTokens == 128 && !request.Stream && len(request.Tools) == 0 {
		m.tokens.Store(true)
	}
	if len(request.Messages) == 0 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, `"max_new_queries":2`) {
		return nil, errors.New("native planner missed fixed question and bound")
	}
	content, _ := m.output.Load().(string)
	ch := make(chan *model.Response, 1)
	finish := "stop"
	ch <- &model.Response{Done: true, Choices: []model.Choice{{Message: model.NewAssistantMessage(content), FinishReason: &finish}}}
	close(ch)
	return ch, nil
}

func fastMediumFixtureDelivery(t *testing.T) (*Delivery, *calls, *atomic.Bool) {
	t.Helper()
	observed := new(calls)
	policy := Policy{Version: "low-v1", Profiles: map[Depth]map[Intelligence]Limits{Fast: {
		Low: {1, 1, 2, 1, 5 * time.Second}, Medium: {1, 3, 2, 2, 15 * time.Second},
	}}, ProfileVersions: map[Depth]map[Intelligence]string{Fast: {Medium: "medium-v1"}}}
	planner := PlanFunc(func(ctx context.Context, in PlanInput) ([]string, error) {
		if in.Round != 1 || in.Depth != Fast {
			return nil, ErrUnavailable
		}
		if in.Intelligence == Low {
			return []string{in.Query}, nil
		}
		return PlanFastMedium(ctx, in)
	})
	s := service(observed, planner, CheckFunc(allow), policy)
	s.dense = denseStub{observed, func(q dense.Query) (dense.Result, error) {
		out := dense.Result{Candidates: []dense.Candidate{}}
		if q.Text == "pet proactive feedback" {
			out.Candidates = append(out.Candidates, dense.Candidate{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID,
				Generation: q.Generation, Lane: "dense", Chunk: sourceChunk("a"), IndexRef: q.IndexRef, Score: 0.9, Rank: 1})
		}
		return out, nil
	}}
	s.sparse = sparseStub{observed, func(q sparse.Query) (sparse.Result, error) {
		out := sparse.Result{Candidates: []sparse.Candidate{}}
		if q.Text == "desktop reflection" {
			out.Candidates = append(out.Candidates, sparse.Candidate{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID,
				Generation: q.Generation, Lane: "sparse", Chunk: sourceChunk("b"), IndexRef: q.IndexRef, Score: 2, Rank: 1})
		}
		return out, nil
	}}
	s.multi = multiStub{observed, func(q multivector.Query) (multivector.Result, error) {
		out := multivector.Result{Candidates: []multivector.Candidate{}}
		if q.Text == "desktop reflection" {
			out.Candidates = append(out.Candidates, multivector.Candidate{ModuleID: q.ModuleID, ReleaseID: q.ReleaseID,
				Generation: q.Generation, Lane: "multivector", Chunk: sourceChunk("b"), IndexRef: q.IndexRef, Score: 0.4, Rank: 1})
		}
		return out, nil
	}}
	acceptedReceipt := new(atomic.Bool)
	accept := AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
		acceptedReceipt.Store(true)
		return accepted(ctx, p)
	})
	d, err := NewDelivery(s, CheckFunc(allow), SourceReadFunc(exactSource), accept,
		EvidenceLimits{MaxReads: 2, MaxQuoteRunes: 80})
	if err != nil {
		t.Fatal(err)
	}
	return d, observed, acceptedReceipt
}

func TestFastMediumNativePlannerChangesThreeLaneSummaryRetrieval(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumNativePlannerChangesThreeLaneSummaryRetrieval$", "-test.v")
		cmd.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native medium Graph fixture failed: %v\n%s", err, output)
		} else if testing.Verbose() {
			t.Log(strings.TrimSpace(string(output)))
		}
		return
	}
	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-fast-medium-fixture",
		Environment: "test", Version: "fixture-v1", InstanceID: "medium-fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observed.Close(context.Background()) }()
	d, laneCalls, receipt := fastMediumFixtureDelivery(t)
	plannerModel := &mediumPlannerFixture{}
	plannerModel.output.Store(`{"queries":["pet proactive feedback","desktop reflection"]}`)
	summaryModel := &summaryModel{receipt: receipt}
	sessions := inmemory.NewSessionService()
	defer sessions.Close()
	root, err := NewRootSummarizerWithFastMedium(d, summaryModel, plannerModel, sessions, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 15 * time.Second}, SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	q := rootFixtureRequest("medium-real-lanes")
	q.Search.Intelligence = Medium
	firstInvocation, err := invocationForSummary(q)
	if err != nil {
		t.Fatal(err)
	}
	plannerModel.expected.Store(firstInvocation)
	out, err := root.Summarize(context.Background(), q)
	if err != nil || out.SummaryStatus != "succeeded" || out.Answer == "" ||
		out.Search.Retrieval.Profile.PolicyVersion != "medium-v1" ||
		!reflect.DeepEqual(out.Search.Retrieval.Batches[0].Queries, []string{"where", "pet proactive feedback", "desktop reflection"}) ||
		out.Search.Retrieval.UsedSubqueries != 3 || len(out.Search.Pack.Evidence) != 2 ||
		plannerModel.calls.Load() != 1 || summaryModel.calls.Load() != 1 || summaryModel.maxTokens.Load() != 512 || !receipt.Load() ||
		!plannerModel.scope.Load() || !plannerModel.tokens.Load() || !summaryModel.sawReceipt.Load() || summaryModel.sawTools.Load() {
		t.Fatalf("medium was not a fixed native model→three-lane→receipt→summary run: out=%+v err=%v plan=%+v summary=%+v", out, err, plannerModel, summaryModel)
	}
	laneCalls.Lock()
	for _, query := range laneCalls.dense {
		if query.ModuleID != q.Search.Snapshot.ModuleID || query.ReleaseID != q.Search.Snapshot.ReleaseID ||
			query.Generation != q.Search.Snapshot.Generation || query.IndexRef != q.Search.Snapshot.Indexes[Dense] ||
			!reflect.DeepEqual(query.ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) {
			laneCalls.Unlock()
			t.Fatalf("medium Dense query changed RTW fixed snapshot")
		}
	}
	for _, query := range laneCalls.sparse {
		if query.IndexRef != q.Search.Snapshot.Indexes[Sparse] || !reflect.DeepEqual(query.ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) {
			laneCalls.Unlock()
			t.Fatalf("medium Sparse query changed fixed representation scope")
		}
	}
	for _, query := range laneCalls.multi {
		if query.IndexRef != q.Search.Snapshot.Indexes[MultiVector] || !reflect.DeepEqual(query.ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) {
			laneCalls.Unlock()
			t.Fatalf("medium Multi-vector query changed fixed representation scope")
		}
	}
	for _, got := range [][]string{denseTexts(laneCalls.dense), sparseTexts(laneCalls.sparse), multiTexts(laneCalls.multi)} {
		if !reflect.DeepEqual(got, []string{"where", "pet proactive feedback", "desktop reflection"}) {
			laneCalls.Unlock()
			t.Fatalf("model queries did not reach every actual lane: %v", got)
		}
	}
	laneCalls.Unlock()
	beforePlan, beforeSummary := plannerModel.calls.Load(), summaryModel.calls.Load()
	low := rootFixtureRequest("old-low")
	insufficient, err := root.Summarize(context.Background(), low)
	if err != nil || insufficient.SummaryStatus != "insufficient" || insufficient.Search.Retrieval.Profile.PolicyVersion != "low-v1" ||
		plannerModel.calls.Load() != beforePlan || summaryModel.calls.Load() != beforeSummary {
		t.Fatalf("old low request invoked model planning or changed policy: %+v, err=%v", insufficient, err)
	}
	highShift := rootFixtureRequest("high-would-hide-medium-downgrade")
	highShift.Search.Intelligence, highShift.Search.AllowLowerIntelligence = High, true
	preflightProfile, _, preflightErr := d.search.(*Service).effective(highShift.Search)
	if preflightErr != nil || preflightProfile.EffectiveIntelligence != Medium {
		t.Fatalf("test did not exercise high-to-medium policy shift: %+v, err=%v", preflightProfile, preflightErr)
	}
	beforeHiddenLanes := len(laneCalls.dense)
	hiddenHigh, hiddenErr := root.Summarize(context.Background(), highShift)
	if !errors.Is(hiddenErr, ErrUnavailable) || hiddenHigh.Answer != "" || len(laneCalls.dense) != beforeHiddenLanes ||
		plannerModel.calls.Load() != beforePlan || summaryModel.calls.Load() != beforeSummary {
		t.Fatalf("signed high-to-medium shift escaped without public profile disclosure: %+v, err=%v", hiddenHigh, hiddenErr)
	}
	beforeLane := len(laneCalls.dense)
	for _, malformed := range []struct{ id, raw string }{
		{"repeat-original", `{"queries":["where"]}`},
		{"duplicate-key", `{"queries":["pet proactive feedback"],"queries":["desktop reflection"]}`},
		{"escaped-alias", `{"queries":["pet proactive feedback"],"\u0071ueries":["desktop reflection"]}`},
	} {
		plannerModel.output.Store(malformed.raw)
		bad := rootFixtureRequest("invalid-model-plan-" + malformed.id)
		bad.Search.Intelligence = Medium
		badInvocation, invocationErr := invocationForSummary(bad)
		if invocationErr != nil {
			t.Fatal(invocationErr)
		}
		plannerModel.expected.Store(badInvocation)
		failed, err := root.Summarize(context.Background(), bad)
		if err == nil || failed.Answer != "" || failed.SummaryStatus != "failed" || len(laneCalls.dense) != beforeLane ||
			summaryModel.calls.Load() != beforeSummary {
			t.Fatalf("%s model plan reached lanes or summary: %+v, err=%v", malformed.id, failed, err)
		}
	}
	empty := rootFixtureRequest("no-revisions")
	empty.Search.Intelligence = Medium
	empty.Search.Snapshot.ValidRevisionIDs = nil
	beforePlan = plannerModel.calls.Load()
	noEvidence, err := root.Summarize(context.Background(), empty)
	if err != nil || noEvidence.SummaryStatus != "insufficient" || plannerModel.calls.Load() != beforePlan {
		t.Fatalf("empty authoritative revision set still called planner: %+v, err=%v", noEvidence, err)
	}
	lowOnlyCalls := new(calls)
	lowOnlyService := service(lowOnlyCalls, PlanFunc(oneQuery), CheckFunc(allow), Policy{Version: "low-only-v1",
		Profiles: map[Depth]map[Intelligence]Limits{Fast: {Low: {1, 1, 2, 1, 5 * time.Second}}}})
	lowOnlyDelivery, err := NewDelivery(lowOnlyService, CheckFunc(allow), SourceReadFunc(exactSource), AcceptFunc(accepted),
		EvidenceLimits{MaxReads: 1, MaxQuoteRunes: 80})
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, err := NewRootSummarizer(lowOnlyDelivery, summaryModel, sessions, observed, SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	missingPolicy := rootFixtureRequest("missing-medium-policy")
	missingPolicy.Search.Intelligence = Medium
	missing, err := oldRoot.Summarize(context.Background(), missingPolicy)
	if !errors.Is(err, ErrUnavailable) || missing.Answer != "" || len(lowOnlyCalls.dense) != 0 ||
		plannerModel.calls.Load() != beforePlan || summaryModel.calls.Load() != beforeSummary {
		t.Fatalf("no medium policy was silently upgraded: %+v, err=%v", missing, err)
	}
	allowHiddenMedium := rootFixtureRequest("missing-medium-but-allow-lower")
	allowHiddenMedium.Search.Intelligence, allowHiddenMedium.Search.AllowLowerIntelligence = Medium, true
	hiddenMedium, hiddenMediumErr := oldRoot.Summarize(context.Background(), allowHiddenMedium)
	if !errors.Is(hiddenMediumErr, ErrUnavailable) || hiddenMedium.Answer != "" || len(lowOnlyCalls.dense) != 0 ||
		plannerModel.calls.Load() != beforePlan || summaryModel.calls.Load() != beforeSummary {
		t.Fatalf("signed allow-lower flag concealed missing medium policy: %+v, err=%v", hiddenMedium, hiddenMediumErr)
	}
	if err := oldRoot.Close(); err != nil {
		t.Fatal(err)
	}
	var uncheckedReceipt atomic.Bool
	uncheckedDelivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource),
		AcceptFunc(func(ctx context.Context, p EvidencePack) (CitationReceipt, error) {
			uncheckedReceipt.Store(true)
			return accepted(ctx, p)
		}))
	uncheckedService := uncheckedDelivery.search.(*Service)
	uncheckedService.policy = Policy{Version: "hidden-low-v1", Profiles: map[Depth]map[Intelligence]Limits{
		Fast: {Low: {1, 1, 2, 1, 5 * time.Second}},
	}}
	uncheckedDelivery.search = ExecuteFunc(uncheckedService.Execute) // no preflight interface
	uncheckedHistory := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
	uncheckedSummary := newSummaryModelFixture(&uncheckedReceipt)
	uncheckedBoundary, err := NewRootSessionBoundary(uncheckedDelivery, uncheckedSummary, uncheckedHistory, observed,
		SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	uncheckedQ := rootFixtureRequest("unchecked-executor-hidden-shift")
	uncheckedQ.Search.Intelligence, uncheckedQ.Search.AllowLowerIntelligence = Medium, true
	unchecked, uncheckedErr := uncheckedBoundary.Summarize(context.Background(), uncheckedQ)
	if !errors.Is(uncheckedErr, ErrUnavailable) || unchecked.Answer != "" || uncheckedSummary.calls.Load() != 1 ||
		!uncheckedSummary.sawReceipt.Load() {
		t.Fatalf("non-Service executor published hidden downgraded answer: %+v, err=%v, model=%+v",
			unchecked, uncheckedErr, uncheckedSummary)
	}
	assertAcceptedHistory(t, uncheckedBoundary, uncheckedQ.Subject, uncheckedQ.SessionID, 0)
	if err := uncheckedBoundary.Close(); err != nil {
		t.Fatal(err)
	}
	plannerModel.output.Store(`{"queries":["pet proactive feedback","desktop reflection"]}`)
	acceptedHistory := &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}
	boundary, err := NewRootSessionBoundaryWithFastMedium(d, summaryModel, plannerModel, acceptedHistory, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 15 * time.Second}, SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	product := rootFixtureRequest("medium-accepted-product")
	product.Search.Intelligence = Medium
	product.SessionID = "actual-product-session"
	productInvocation, err := invocationForSummary(product)
	if err != nil {
		t.Fatal(err)
	}
	plannerModel.expected.Store(productInvocation)
	acceptedResult, err := boundary.Summarize(context.Background(), product)
	if err != nil || acceptedResult.SummaryStatus != "succeeded" ||
		acceptedResult.Search.Retrieval.Profile.PolicyVersion != "medium-v1" {
		t.Fatalf("medium result failed private-session/history boundary: %+v, err=%v", acceptedResult, err)
	}
	assertAcceptedHistory(t, boundary, product.Subject, product.SessionID, 1)
	plannerModel.output.Store(`{"queries":["pet proactive feedback"],"queries":["desktop reflection"]}`)
	rejectedProduct := rootFixtureRequest("medium-rejected-product")
	rejectedProduct.Search.Intelligence = Medium
	rejectedProduct.SessionID = product.SessionID
	rejectedInvocation, err := invocationForSummary(rejectedProduct)
	if err != nil {
		t.Fatal(err)
	}
	plannerModel.expected.Store(rejectedInvocation)
	if rejected, err := boundary.Summarize(context.Background(), rejectedProduct); err == nil || rejected.Answer != "" {
		t.Fatalf("duplicate model plan entered product history: %+v, err=%v", rejected, err)
	}
	assertAcceptedHistory(t, boundary, product.Subject, product.SessionID, 1)
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	plannerModel.output.Store(`{"queries":["pet proactive feedback","desktop reflection"]}`)
	deadlineHistory := new(deadlineAcceptedHistory)
	deadlineBoundary, err := NewRootSessionBoundaryWithFastMedium(d, summaryModel, plannerModel, deadlineHistory, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: time.Second}, SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	deadlineQ := rootFixtureRequest("medium-history-deadline")
	deadlineQ.Search.Intelligence = Medium
	deadlineInvocation, err := invocationForSummary(deadlineQ)
	if err != nil {
		t.Fatal(err)
	}
	plannerModel.expected.Store(deadlineInvocation)
	deadlineResult, deadlineErr := deadlineBoundary.Summarize(context.Background(), deadlineQ)
	if !errors.Is(deadlineErr, context.DeadlineExceeded) || deadlineResult.Answer != "" ||
		deadlineHistory.commits.Load() != 1 {
		t.Fatalf("medium wall time did not cover accepted-history commit: %+v, err=%v, commits=%d",
			deadlineResult, deadlineErr, deadlineHistory.commits.Load())
	}
	assertAcceptedHistory(t, deadlineBoundary, deadlineQ.Subject, deadlineQ.SessionID, 0)
	if err := deadlineBoundary.Close(); err != nil {
		t.Fatal(err)
	}
	lateHistory := &lateAcceptedHistory{inner: &acceptedHistoryFixture{turns: make(map[acceptedHistoryKey][]AcceptedRootTurn)}}
	lateBoundary, err := NewRootSessionBoundaryWithFastMedium(d, summaryModel, plannerModel, lateHistory, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: time.Second}, SummaryModelLimits{MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	lateQ := rootFixtureRequest("medium-commit-nil-after-deadline")
	lateQ.Search.Intelligence = Medium
	lateInvocation, err := invocationForSummary(lateQ)
	if err != nil {
		t.Fatal(err)
	}
	plannerModel.expected.Store(lateInvocation)
	beforeLatePlan, beforeLateSummary := plannerModel.calls.Load(), summaryModel.calls.Load()
	lateResult, lateErr := lateBoundary.Summarize(context.Background(), lateQ)
	if !errors.Is(lateErr, context.DeadlineExceeded) || lateResult.Answer != "" || lateResult.SummaryStatus != "failed" ||
		lateHistory.commits.Load() != 1 || plannerModel.calls.Load() != beforeLatePlan+1 ||
		summaryModel.calls.Load() != beforeLateSummary+1 {
		t.Fatalf("durable nil Commit after deadline escaped as public success: %+v, err=%v, commits/model=%d/%d/%d",
			lateResult, lateErr, lateHistory.commits.Load(), plannerModel.calls.Load()-beforeLatePlan,
			summaryModel.calls.Load()-beforeLateSummary)
	}
	lateTurns, err := lateBoundary.History(context.Background(), lateQ.Subject, lateQ.SessionID)
	if err != nil || len(lateTurns) != 1 || lateTurns[0].Request.SearchID != lateQ.SearchID ||
		lateTurns[0].Request.AnswerID != lateQ.AnswerID || lateTurns[0].Result.Answer == "" {
		t.Fatalf("timeout did not preserve fixed-key durable history lookup: %+v, err=%v", lateTurns, err)
	}
	if err := lateBoundary.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), `"subject_id"`) || strings.Contains(logs.String(), `"queries"`) {
		t.Fatalf("OBS runtime log included subject or planned query payload")
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), "trpc_agent_go_chat_") ||
		strings.Contains(metrics.Body.String(), q.Subject.SubjectID) ||
		strings.Contains(metrics.Body.String(), q.SearchID) ||
		strings.Contains(metrics.Body.String(), "pet proactive feedback") {
		t.Fatalf("native planning metrics lacked bounded labels or included subject/query values")
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"invoke_agent plan_fast_medium", "chat medium-plan-fixture",
		"workflow execute_function_node validate_fast_medium_plan", "workflow execute_function_node search_and_accept",
		"invoke_agent search_summary", "chat summary-fixture"} {
		if !strings.Contains(strings.Join(exporter.names(), "\n"), wanted) {
			t.Fatalf("missing native framework model/Graph span %q: %v", wanted, exporter.names())
		}
	}
	for _, wanted := range []string{"invoke_agent plan_fast_medium", "chat medium-plan-fixture",
		"workflow execute_function_node validate_fast_medium_plan"} {
		seen := false
		for _, span := range exporter.snapshot() {
			if span.Name() == wanted {
				seen = true
				if span.InstrumentationScope().Name != "trpc.agent.go" {
					t.Fatalf("%q is an application wrapper rather than framework native: %s", wanted, span.InstrumentationScope().Name)
				}
			}
		}
		if !seen {
			t.Fatalf("native span %q absent after provider flush", wanted)
		}
	}
}

func denseTexts(in []dense.Query) []string {
	out := make([]string, len(in))
	for i, q := range in {
		out[i] = q.Text
	}
	return out
}

func sparseTexts(in []sparse.Query) []string {
	out := make([]string, len(in))
	for i, q := range in {
		out[i] = q.Text
	}
	return out
}

func multiTexts(in []multivector.Query) []string {
	out := make([]string, len(in))
	for i, q := range in {
		out[i] = q.Text
	}
	return out
}
