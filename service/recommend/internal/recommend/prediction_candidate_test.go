package recommend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

type predictionTestExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *predictionTestExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}
func (*predictionTestExporter) Shutdown(context.Context) error { return nil }
func (e *predictionTestExporter) snapshot() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), e.spans...)
}

type predictionFixtureCaller struct {
	mu             sync.Mutex
	calls          map[string]int
	unknownFirst   bool
	inflightSecond bool
	mutate         func(*prediction.Response)
}

type predictionPendingCaller struct {
	mode    string
	entered chan struct{}
	calls   int
}

type predictionResumeCaller struct {
	mu      sync.Mutex
	seen    map[string]int
	fixture *predictionFixtureCaller
}

func (c *predictionResumeCaller) Predict(ctx context.Context, request prediction.Request, key string) (datacenter.PredictionResult, error) {
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[string]int{}
	}
	c.seen[key]++
	attempt := c.seen[key]
	c.mu.Unlock()
	if request.Task == prediction.UserTower && attempt <= 3 {
		return datacenter.PredictionResult{}, datacenter.ErrPredictionInFlight
	}
	return c.fixture.Predict(ctx, request, key)
}

func (c *predictionPendingCaller) Predict(ctx context.Context, _ prediction.Request, _ string) (datacenter.PredictionResult, error) {
	c.calls++
	if c.entered != nil && c.calls == 1 {
		close(c.entered)
	}
	if c.mode == "cancel" {
		<-ctx.Done()
		return datacenter.PredictionResult{}, ctx.Err()
	}
	return datacenter.PredictionResult{}, datacenter.ErrPredictionInFlight
}

func (c *predictionFixtureCaller) Predict(_ context.Context, request prediction.Request,
	key string) (datacenter.PredictionResult, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[key]++
	attempt := c.calls[key]
	c.mu.Unlock()
	if c.unknownFirst && strings.HasSuffix(key, ".user") && attempt == 1 {
		return datacenter.PredictionResult{}, datacenter.ErrPredictionOutcomeUnknown
	}
	if c.inflightSecond && strings.HasSuffix(key, ".user") && attempt == 2 {
		return datacenter.PredictionResult{}, datacenter.ErrPredictionInFlight
	}
	input := request.Input[0]
	output := prediction.Output{ID: input.ID}
	usage := prediction.Usage{InputRows: 1, OutputRows: 1}
	user := func(value float64) []float64 { return []float64{value + 1, value + 2} }
	item := func(value float64) []float64 { return []float64{value + 3, value + 4} }
	callID := "11111111-1111-4111-8111-111111111111"
	switch request.Task {
	case prediction.UserTower:
		output.UserEmbedding = user(*input.UserInterest)
		usage.InputFeatureValues, usage.OutputValues = 1, 2
	case prediction.ItemTower:
		callID = "22222222-2222-4222-8222-222222222222"
		output.ItemEmbedding = item(*input.ItemQuality)
		usage.InputFeatureValues, usage.OutputValues = 1, 2
	case prediction.Ranker:
		callID = "33333333-3333-4333-8333-333333333333"
		output.UserEmbedding, output.ItemEmbedding = user(*input.UserInterest), item(*input.ItemQuality)
		dot := output.UserEmbedding[0]*output.ItemEmbedding[0] + output.UserEmbedding[1]*output.ItemEmbedding[1]
		logit := 0.1 + dot
		output.TowerDot, output.RankerLogit = &dot, &logit
		usage.InputFeatureValues, usage.OutputValues = 2, 6
	}
	response := prediction.Response{Model: request.Model, ConfigurationID: request.ConfigurationID,
		ArtifactSHA256: strings.Repeat("a", 64), ModelCallID: callID, PointerRevision: 7,
		OutputContract: request.OutputContract, FeatureContractID: request.FeatureContractID,
		PairID: request.PairID, SpaceID: request.SpaceID, Task: request.Task,
		ModelConfig: prediction.ModelConfig{Recipe: "sea.recommend.cpu-towers-lr.v1",
			FeatureContractID: request.FeatureContractID, FeatureOrder: []string{"user_interest", "item_quality"},
			RankerFeatureOrder:  []string{"bias", "user_interest", "item_quality", "interaction", "tower_dot"},
			PreprocessingSHA256: strings.Repeat("b", 64), EmbeddingDimension: 2, Metric: "dot",
			ScoreSemantics: "uncalibrated_logit", Calibration: "none", PairID: request.PairID, SpaceID: request.SpaceID},
		Data: []prediction.Output{output}, Usage: &usage}
	if c.mutate != nil {
		c.mutate(&response)
	}
	body, err := json.Marshal(response)
	if err != nil {
		return datacenter.PredictionResult{}, err
	}
	return datacenter.PredictionResult{Response: response, Body: body}, nil
}

func (c *predictionFixtureCaller) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

func predictionCandidateTestConfig() PredictionCandidateConfig {
	return PredictionCandidateConfig{SyntheticEvaluationEnabled: true, Model: "recommend-engagement",
		ConfigurationID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ArtifactSHA256: strings.Repeat("a", 64),
		FeatureContractID: "engagement-features.v1", PairID: "sea.recommend.pair.fixture",
		SpaceID: "sea.recommend.space.fixture"}
}

func predictionCandidateTestRequest(logicalCall string) PredictionCandidateRequest {
	return PredictionCandidateRequest{Subject: PredictionSubject{Issuer: "rtw.identity", SubjectID: "1001"},
		Features:    SyntheticPredictionFeatures{Source: "synthetic_typed_fixture", UserInterest: 0.2, ItemQuality: 0.1},
		LogicalCall: logicalCall}
}

func TestPredictionCandidateDefaultsOffAndRunsThroughTRPCGraph(t *testing.T) {
	disabled, err := NewPredictionCandidateUseCase(PredictionCandidateConfig{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.BuildCandidate(context.Background(), predictionCandidateTestRequest("prediction-disabled-0001")); !errors.Is(err, ErrPredictionDisabled) {
		t.Fatalf("zero config did not remain default-off: %v", err)
	}

	var logs bytes.Buffer
	exporter := &predictionTestExporter{}
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-prediction-test",
		Environment: "test", Version: "fixture", InstanceID: "prediction-unit", Output: &logs,
		Level: slog.LevelInfo, TraceExporter: exporter, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	caller := &predictionFixtureCaller{unknownFirst: true, inflightSecond: true}
	useCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), caller, observed)
	if err != nil {
		t.Fatal(err)
	}
	directRequest := predictionCandidateTestRequest("prediction-direct-0001")
	direct, err := useCase.BuildCandidate(context.Background(), directRequest)
	if err != nil || !validPredictionCandidate(direct) || direct.Status != "candidate_default_off" ||
		direct.BusinessActivation != "none" || direct.Features.Source != "synthetic_typed_fixture" {
		t.Fatalf("default-off typed candidate=%+v err=%v", direct, err)
	}
	if caller.count(directRequest.LogicalCall+".user") != 3 || caller.count(directRequest.LogicalCall+".item") != 1 ||
		caller.count(directRequest.LogicalCall+".ranker") != 1 {
		t.Fatalf("logical call recovery changed keys: %+v", caller.calls)
	}

	sessions := inmemory.NewSessionService()
	runtime, err := NewPredictionGraphRuntime("recommend-prediction-test", useCase, sessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	graphRequest := predictionCandidateTestRequest("prediction-graph-0001")
	throughGraph, err := runtime.Run(context.Background(), PredictionGraphRequest{Candidate: graphRequest,
		SessionID: "prediction-session-1", RunID: "prediction-run-1"})
	if err != nil || !validPredictionCandidate(throughGraph) || throughGraph.BusinessActivation != "none" {
		t.Fatalf("tRPC Graph candidate=%+v err=%v", throughGraph, err)
	}
	if err := runtime.Close(); err != nil {
		t.Error(err)
	}
	if err := sessions.Close(); err != nil {
		t.Error(err)
	}

	for name, mutate := range map[string]func(*prediction.Response){
		"configuration": func(response *prediction.Response) { response.ConfigurationID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" },
		"pair":          func(response *prediction.Response) { response.PairID = "sea.recommend.pair.other" },
		"space":         func(response *prediction.Response) { response.SpaceID = "sea.recommend.space.other" },
		"shape": func(response *prediction.Response) {
			response.Data[0].UserEmbedding = []float64{1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			badCaller := &predictionFixtureCaller{mutate: mutate}
			badUseCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), badCaller, observed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := badUseCase.BuildCandidate(context.Background(), predictionCandidateTestRequest("prediction-bad-"+name+"-0001")); !errors.Is(err, ErrPredictionContract) {
				t.Fatalf("wrong %s response accepted: %v", name, err)
			}
		})
	}

	pendingCaller := &predictionPendingCaller{mode: "inflight"}
	pendingCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), pendingCaller, observed)
	if err != nil {
		t.Fatal(err)
	}
	pendingSessions := inmemory.NewSessionService()
	pendingGraph, err := NewPredictionGraphRuntime("recommend-prediction-pending", pendingCase, pendingSessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	pendingCtx, pendingParent := observed.Tracer().Start(context.Background(), "prediction.pending.root")
	pendingTrace := pendingParent.SpanContext().TraceID()
	pending, pendingErr := pendingGraph.Run(pendingCtx, PredictionGraphRequest{
		Candidate: predictionCandidateTestRequest("prediction-pending-0001"), SessionID: "prediction-pending-session",
		RunID: "prediction-pending-run"})
	pendingParent.End()
	if !errors.Is(pendingErr, datacenter.ErrPredictionInFlight) || pending.ID != "" || pendingCaller.calls != 3 {
		t.Fatalf("persistent DC lease became candidate: %+v err=%v attempts=%d", pending, pendingErr, pendingCaller.calls)
	}
	if err := pendingGraph.Close(); err != nil {
		t.Error(err)
	}
	if err := pendingSessions.Close(); err != nil {
		t.Error(err)
	}
	resumeCaller := &predictionResumeCaller{fixture: &predictionFixtureCaller{}}
	resumeCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), resumeCaller, observed)
	if err != nil {
		t.Fatal(err)
	}
	resumeSessions := inmemory.NewSessionService()
	resumeGraph, err := NewPredictionGraphRuntime("recommend-prediction-resume", resumeCase, resumeSessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	ownedRequest := predictionCandidateTestRequest("prediction-caller-owned-0001")
	first, firstErr := resumeGraph.Run(context.Background(), PredictionGraphRequest{Candidate: ownedRequest,
		SessionID: "prediction-caller-owned-first", RunID: "prediction-caller-owned-run-1"})
	if !errors.Is(firstErr, datacenter.ErrPredictionInFlight) || first.ID != "" {
		t.Fatalf("first caller-owned run published active lease: %+v %v", first, firstErr)
	}
	second, secondErr := resumeGraph.Run(context.Background(), PredictionGraphRequest{Candidate: ownedRequest,
		SessionID: "prediction-caller-owned-second", RunID: "prediction-caller-owned-run-2"})
	if secondErr != nil || !validPredictionCandidate(second) || second.BusinessActivation != "none" ||
		resumeCaller.seen[ownedRequest.LogicalCall+".user"] != 4 ||
		resumeCaller.seen[ownedRequest.LogicalCall+".item"] != 1 ||
		resumeCaller.seen[ownedRequest.LogicalCall+".ranker"] != 1 {
		t.Fatalf("caller-owned same-key Graph resume failed: %+v seen=%v err=%v", second, resumeCaller.seen, secondErr)
	}
	if err := resumeGraph.Close(); err != nil {
		t.Error(err)
	}
	if err := resumeSessions.Close(); err != nil {
		t.Error(err)
	}

	cancelCaller := &predictionPendingCaller{mode: "cancel", entered: make(chan struct{})}
	cancelCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), cancelCaller, observed)
	if err != nil {
		t.Fatal(err)
	}
	cancelSessions := inmemory.NewSessionService()
	cancelGraph, err := NewPredictionGraphRuntime("recommend-prediction-cancel", cancelCase, cancelSessions, observed)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancelRun := context.WithCancel(context.Background())
	cancelCtx, cancelParent := observed.Tracer().Start(cancelCtx, "prediction.cancel.root")
	cancelTrace := cancelParent.SpanContext().TraceID()
	cancelledDone := make(chan error, 1)
	go func() {
		_, err := cancelGraph.Run(cancelCtx, PredictionGraphRequest{
			Candidate: predictionCandidateTestRequest("prediction-cancel-0001"), SessionID: "prediction-cancel-session",
			RunID: "prediction-cancel-run"})
		cancelledDone <- err
	}()
	select {
	case <-cancelCaller.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel test did not enter Graph prediction node")
	}
	cancelRun()
	if cancelErr := <-cancelledDone; !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("cancelled Graph run did not preserve cancellation: %v", cancelErr)
	}
	cancelParent.End()
	if err := cancelGraph.Close(); err != nil {
		t.Error(err)
	}
	if err := cancelSessions.Close(); err != nil {
		t.Error(err)
	}

	metrics := httptest.NewRecorder()
	observed.MetricsHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "trpc_agent_go_agent_") ||
		!strings.Contains(metrics.Body.String(), `sea_btw_operations_total{component="recommend",outcome="succeeded"}`) {
		t.Fatalf("prediction Graph metrics missing: %s", metrics.Body.String())
	}
	if err := observed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	spanNames := map[string]bool{}
	nativeFailure := map[string]bool{"pending_function": false, "cancel_function": false,
		"pending_agent": false, "cancel_agent": false}
	for _, span := range exporter.snapshot() {
		spanNames[span.Name()] = true
		if span.InstrumentationScope().Name != "trpc.agent.go" || span.Status().Code != codes.Error {
			continue
		}
		kind := ""
		switch span.Name() {
		case "workflow execute_function_node build_prediction_candidate":
			kind = "function"
		case "invoke_agent recommend_prediction_candidate":
			kind = "agent"
		default:
			continue
		}
		switch span.SpanContext().TraceID() {
		case pendingTrace:
			nativeFailure["pending_"+kind] = true
		case cancelTrace:
			nativeFailure["cancel_"+kind] = true
		}
	}
	for name, observed := range nativeFailure {
		if !observed {
			t.Fatalf("native Graph span hid %s outcome: %v", name, nativeFailure)
		}
	}
	t.Logf("native tRPC pending/cancel agent and function span statuses: %v", nativeFailure)
	for _, name := range []string{"recommend.prediction_candidate", "recommend.prediction_call",
		"runtime.run", "invoke_agent recommend_prediction_candidate",
		"workflow execute_graph recommend_prediction_candidate",
		"workflow execute_function_node build_prediction_candidate"} {
		if !spanNames[name] {
			t.Fatalf("missing prediction/tRPC span %q: %v", name, spanNames)
		}
	}
	var pendingLog bool
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("prediction emitted non-JSON log: %q", line)
		}
		if record["event"] == "recommend.prediction_call.finished" && record["outcome"] == "succeeded" {
			for _, field := range []string{"duration_ms", "model_call_id", "input_rows", "input_feature_values", "output_rows", "output_values"} {
				if record[field] == nil {
					t.Fatalf("prediction success log omitted %s: %v", field, record)
				}
			}
		}
		if record["event"] == "recommend.prediction_call.finished" && record["outcome"] == "partial" &&
			record["error_code"] == "DC_PREDICTION_INFLIGHT" && record["retryable"] == true &&
			record["attempts"] == float64(3) {
			pendingLog = true
		}
	}
	if !pendingLog || !strings.Contains(metrics.Body.String(), `sea_btw_operations_total{component="recommend",outcome="partial"}`) {
		t.Fatal("inflight Graph failed without a retryable partial observation")
	}
}
