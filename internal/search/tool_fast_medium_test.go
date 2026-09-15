package search

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type cancelledToolsPlanner struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (*cancelledToolsPlanner) Info() model.Info {
	return model.Info{Name: "cancelled-medium-tools-plan"}
}

func (m *cancelledToolsPlanner) GenerateContent(ctx context.Context, _ *model.Request) (<-chan *model.Response, error) {
	m.calls.Add(1)
	close(m.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func fastMediumToolRequest(id string, level Intelligence) ToolRunRequest {
	return ToolRunRequest{Subject: btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "medium-tools-session", OperationID: "medium-tools-parent", BudgetRef: "medium-tools-budget",
		SearchID: id, Search: searchRequest(Fast, level), Limits: EvidenceLimits{MaxReads: 2, MaxQuoteRunes: 80}}
}

func expectedToolInvocation(t *testing.T, q ToolRunRequest) ModelInvocationRef {
	t.Helper()
	raw, err := json.Marshal(q.Search.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return ModelInvocationRef{AuthorityID: q.Subject.AuthorityID, TenantID: q.Subject.TenantID,
		SubjectID: q.Subject.SubjectID, SearchID: q.SearchID, AnswerID: q.OperationID,
		SnapshotSHA: artifacts.Hash(raw)}
}

func TestFastMediumToolsNativePlannerAndBareEvidence(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_TOOLS_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumToolsNativePlannerAndBareEvidence$", "-test.v")
		command.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_TOOLS_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native medium Tools Runner failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	exporter := &searchSpanExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-fast-medium-tools-fixture",
		Environment: "test", Version: "fixture-v1", InstanceID: "medium-tools-fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observed.Close(context.Background()) }()
	delivery, lanes, accepted := fastMediumFixtureDelivery(t)
	planner := &mediumPlannerFixture{}
	planner.output.Store(`{"queries":["pet proactive feedback","desktop reflection"]}`)
	boundary, err := NewToolRunBoundaryWithFastMedium(delivery, planner, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	q := fastMediumToolRequest("medium-tools-search", Medium)
	planner.expected.Store(expectedToolInvocation(t, q))
	found, err := boundary.Search(context.Background(), q)
	if err != nil || !accepted.Load() || planner.calls.Load() != 1 || !planner.scope.Load() || !planner.tokens.Load() ||
		found.Pack.Profile.PolicyVersion != "medium-v1" || found.Pack.Profile.EffectiveIntelligence != Medium ||
		len(found.Retrieval.Batches) != 1 || found.Retrieval.UsedSubqueries != 3 ||
		!reflect.DeepEqual(found.Retrieval.Batches[0].Queries, []string{"where", "pet proactive feedback", "desktop reflection"}) ||
		len(found.Pack.Evidence) != 2 || found.Receipt.DurableRef == "" || found.Pack.CoverageStatus != "not_assessed" {
		t.Fatalf("medium Tools did not return fixed three-lane accepted evidence: found=%+v err=%v planner=%+v", found, err, planner)
	}
	lanes.Lock()
	if len(lanes.dense) != 3 || len(lanes.sparse) != 3 || len(lanes.multi) != 3 {
		lanes.Unlock()
		t.Fatalf("medium Tools ran multiple batches or missed one lane: %+v", lanes)
	}
	for i, want := range []string{"where", "pet proactive feedback", "desktop reflection"} {
		if lanes.dense[i].Text != want || lanes.sparse[i].Text != want || lanes.multi[i].Text != want ||
			lanes.dense[i].IndexRef != q.Search.Snapshot.Indexes[Dense] ||
			lanes.sparse[i].IndexRef != q.Search.Snapshot.Indexes[Sparse] ||
			lanes.multi[i].IndexRef != q.Search.Snapshot.Indexes[MultiVector] ||
			!reflect.DeepEqual(lanes.dense[i].ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) ||
			!reflect.DeepEqual(lanes.sparse[i].ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) ||
			!reflect.DeepEqual(lanes.multi[i].ValidRevisionIDs, q.Search.Snapshot.ValidRevisionIDs) {
			lanes.Unlock()
			t.Fatalf("medium Tools lane %d changed query or RTW publication", i)
		}
	}
	lanes.Unlock()
	encoded, err := json.Marshal(found)
	if err != nil || bytes.Contains(encoded, []byte(`"answer"`)) || bytes.Contains(encoded, []byte(`"summary_status"`)) {
		t.Fatalf("Tools result carried a final summary: %s %v", encoded, err)
	}
	beforePlan := planner.calls.Load()
	low := fastMediumToolRequest("low-tools-regression", Low)
	lowFound, lowErr := boundary.Search(context.Background(), low)
	if lowErr != nil || planner.calls.Load() != beforePlan || lowFound.Pack.Profile.PolicyVersion != "low-v1" ||
		lowFound.Retrieval.UsedSubqueries != 1 || !reflect.DeepEqual(lowFound.Retrieval.Batches[0].Queries, []string{"where"}) {
		t.Fatalf("old low Tools contract regressed: %+v %v", lowFound, lowErr)
	}
	noRevision := fastMediumToolRequest("medium-tools-empty-publication", Medium)
	noRevision.Search.Snapshot.ValidRevisionIDs = []string{}
	lanes.Lock()
	beforeEmptyLane := len(lanes.dense)
	lanes.Unlock()
	beforeEmptyPlan := planner.calls.Load()
	empty, emptyErr := boundary.Search(context.Background(), noRevision)
	lanes.Lock()
	afterEmptyLane := len(lanes.dense)
	lanes.Unlock()
	if emptyErr != nil || empty.Pack.Status != "empty" || len(empty.Pack.Evidence) != 0 ||
		empty.Receipt != (CitationReceipt{}) || planner.calls.Load() != beforeEmptyPlan || afterEmptyLane != beforeEmptyLane {
		t.Fatalf("empty publication touched Planner/Reader or fabricated a receipt: %+v %v", empty, emptyErr)
	}
	for _, invalid := range []string{`{"queries":["pet proactive feedback"],"queries":["desktop reflection"]}`,
		`{"\u0071ueries":["pet proactive feedback"]}`, `{"queries":["where"]}`, `{"queries":["same","same"]}`} {
		planner.output.Store(invalid)
		bad := fastMediumToolRequest("bad-medium-tools-"+artifacts.Hash([]byte(invalid))[:16], Medium)
		planner.expected.Store(expectedToolInvocation(t, bad))
		lanes.Lock()
		beforeLane := len(lanes.dense)
		lanes.Unlock()
		failed, badErr := boundary.Search(context.Background(), bad)
		lanes.Lock()
		afterLane := len(lanes.dense)
		lanes.Unlock()
		if badErr == nil || len(failed.Pack.Evidence) != 0 || failed.Receipt != (CitationReceipt{}) || afterLane != beforeLane {
			t.Fatalf("invalid Planner changed Reader or citation state: raw=%s found=%+v err=%v lanes=%d/%d", invalid, failed, badErr, beforeLane, afterLane)
		}
	}
	if err := boundary.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), `"subject_id"`) || strings.Contains(logs.String(), "pet proactive feedback") {
		t.Fatal("runtime log included subject or model-planned query payload")
	}
	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), "trpc_agent_go_chat_") ||
		strings.Contains(metrics.Body.String(), q.Subject.SubjectID) ||
		strings.Contains(metrics.Body.String(), q.SearchID) ||
		strings.Contains(metrics.Body.String(), "pet proactive feedback") {
		t.Fatal("native Tools Planner metrics missing or carrying unbounded labels")
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var nativePlan, nativeChat, nativeCheck, nativeSearch bool
	for _, span := range exporter.snapshot() {
		if span.InstrumentationScope().Name != "trpc.agent.go" {
			continue
		}
		if span.Name() == "invoke_agent plan_fast_medium_tools" {
			nativePlan = true
		}
		if span.Name() == "chat medium-plan-fixture" {
			nativeChat = true
		}
		if span.Name() == "workflow execute_function_node validate_fast_medium_tools_plan" {
			nativeCheck = true
		}
		if span.Name() == "workflow execute_function_node search_and_accept_tool_evidence" {
			nativeSearch = true
		}
	}
	if !nativePlan || !nativeChat || !nativeCheck || !nativeSearch {
		t.Fatalf("framework native Planner/Chat/Check/Search spans missing: %t/%t/%t/%t",
			nativePlan, nativeChat, nativeCheck, nativeSearch)
	}
}

func TestFastMediumToolsMissingPolicyAndCancellationBeforeRunnerHaveNoEffects(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_TOOLS_PREFLIGHT_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumToolsMissingPolicyAndCancellationBeforeRunnerHaveNoEffects$")
		command.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_TOOLS_PREFLIGHT_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native medium Tools preflight failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-medium-tools-preflight",
		Environment: "test", Version: "fixture-v1", InstanceID: "preflight", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &searchSpanExporter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	planner := &mediumPlannerFixture{}
	planner.output.Store(`{"queries":["pet proactive feedback"]}`)
	delivery, _, acceptedReceipt := fastMediumFixtureDelivery(t)
	// The low-only profile cannot authorize an implicit signed downgrade.
	lowOnlyPolicy := Policy{Version: "low-only-v1", Profiles: map[Depth]map[Intelligence]Limits{
		Fast: {Low: {1, 1, 2, 1, 5 * time.Second}},
	}}
	lowLanes := new(calls)
	delivery.search = service(lowLanes, PlanFunc(oneQuery), CheckFunc(allow), lowOnlyPolicy)
	if _, err := NewToolRunBoundaryWithFastMedium(delivery, nil, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 5 * time.Second}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("medium Tools accepted a missing model: %v", err)
	}
	noModel, err := NewToolRunBoundary(delivery, observed)
	if err != nil {
		t.Fatal(err)
	}
	defer noModel.Close()
	if _, err := noModel.Search(context.Background(), fastMediumToolRequest("no-medium-model", Medium)); !errors.Is(err, ErrUnavailable) || planner.calls.Load() != 0 || acceptedReceipt.Load() ||
		len(lowLanes.dense) != 0 || len(lowLanes.sparse) != 0 || len(lowLanes.multi) != 0 {
		t.Fatalf("unassembled model reached Reader or RTW: %v lanes=%+v", err, lowLanes)
	}
	boundary, err := NewToolRunBoundaryWithFastMedium(delivery, planner, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	missing := fastMediumToolRequest("missing-medium-tools", Medium)
	missing.Search.AllowLowerIntelligence = true
	if _, err := boundary.Search(context.Background(), missing); !errors.Is(err, ErrUnavailable) ||
		planner.calls.Load() != 0 || acceptedReceipt.Load() || len(lowLanes.dense) != 0 ||
		len(lowLanes.sparse) != 0 || len(lowLanes.multi) != 0 {
		t.Fatalf("missing medium policy touched Planner/Reader/RTW or downgraded: %v calls=%d lanes=%+v", err, planner.calls.Load(), lowLanes)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := boundary.Search(cancelled, fastMediumToolRequest("cancelled-medium-tools", Medium)); !errors.Is(err, context.Canceled) || planner.calls.Load() != 0 {
		t.Fatalf("cancelled medium call touched Planner: %v calls=%d", err, planner.calls.Load())
	}
}

func TestFastMediumToolsCancellationDuringNativePlannerStopsBeforeReaders(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_TOOLS_CANCEL_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumToolsCancellationDuringNativePlannerStopsBeforeReaders$")
		command.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_TOOLS_CANCEL_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native medium Tools cancellation failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-medium-tools-cancel",
		Environment: "test", Version: "fixture-v1", InstanceID: "cancel", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &searchSpanExporter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	delivery, lanes, accepted := fastMediumFixtureDelivery(t)
	planner := &cancelledToolsPlanner{entered: make(chan struct{})}
	boundary, err := NewToolRunBoundaryWithFastMedium(delivery, planner, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		found SearchResult
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		found, runErr := boundary.Search(ctx, fastMediumToolRequest("cancel-in-plan", Medium))
		finished <- result{found, runErr}
	}()
	select {
	case <-planner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native Tools Planner never started")
	}
	cancel()
	select {
	case out := <-finished:
		lanes.Lock()
		counts := [3]int{len(lanes.dense), len(lanes.sparse), len(lanes.multi)}
		lanes.Unlock()
		if !errors.Is(out.err, context.Canceled) || len(out.found.Pack.Evidence) != 0 ||
			out.found.Receipt != (CitationReceipt{}) || accepted.Load() || counts != ([3]int{}) || planner.calls.Load() != 1 {
			t.Fatalf("cancelled Planner advanced to Reader/RTW: %+v err=%v lanes=%v", out.found, out.err, counts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled native Tools Planner did not finish")
	}
}

func TestFastMediumToolsLateRTWCitationAcceptanceDoesNotPublishAfterCancel(t *testing.T) {
	if os.Getenv("SEARCH_FAST_MEDIUM_TOOLS_LATE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFastMediumToolsLateRTWCitationAcceptanceDoesNotPublishAfterCancel$")
		command.Env = append(os.Environ(), "SEARCH_FAST_MEDIUM_TOOLS_LATE_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native late RTW acceptance failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-medium-tools-late",
		Environment: "test", Version: "fixture-v1", InstanceID: "late", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: &searchSpanExporter{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer observed.Close(context.Background())
	delivery, _, _ := fastMediumFixtureDelivery(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var committed atomic.Bool
	var releaseOnce atomic.Bool
	finishCommit := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer finishCommit()
	delivery.accept = AcceptFunc(func(_ context.Context, pack EvidencePack) (CitationReceipt, error) {
		close(entered)
		<-release
		committed.Store(true)
		return accepted(context.Background(), pack)
	})
	planner := &mediumPlannerFixture{}
	planner.output.Store(`{"queries":["pet proactive feedback","desktop reflection"]}`)
	boundary, err := NewToolRunBoundaryWithFastMedium(delivery, planner, observed,
		FastMediumModelLimits{MaxOutputTokens: 128, WallTime: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Close()
	q := fastMediumToolRequest("late-medium-tools-search", Medium)
	planner.expected.Store(expectedToolInvocation(t, q))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		found SearchResult
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		found, runErr := boundary.Search(ctx, q)
		finished <- result{found, runErr}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("medium Tools never reached RTW citation acceptor")
	}
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- boundary.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while borrowed RTW acceptance was active: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	finishCommit()
	select {
	case out := <-finished:
		if out.err == nil || len(out.found.Pack.Evidence) != 0 || out.found.Receipt != (CitationReceipt{}) {
			t.Fatalf("cancelled medium Tools exposed late accepted citation: %+v %v", out.found, out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled medium Tools remained active after RTW release")
	}
	select {
	case err := <-closed:
		if err != nil || !committed.Load() {
			t.Fatalf("Close did not wait for RTW commit: err=%v committed=%t", err, committed.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("medium Tools Close did not settle after RTW acceptance")
	}
}
