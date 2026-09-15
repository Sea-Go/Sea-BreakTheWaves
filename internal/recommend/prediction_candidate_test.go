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

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
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
	mu           sync.Mutex
	calls        map[string]int
	unknownFirst bool
	mutate       func(*prediction.Response)
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
	return PredictionCandidateConfig{Enabled: true, Model: "recommend-engagement",
		ConfigurationID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ArtifactSHA256: strings.Repeat("a", 64),
		FeatureContractID: "engagement-features.v1", PairID: "sea.recommend.pair.fixture",
		SpaceID: "sea.recommend.space.fixture"}
}

func predictionCandidateTestRequest(logicalCall string) PredictionCandidateRequest {
	return PredictionCandidateRequest{Subject: PredictionSubject{Issuer: "rtw-user-center", Realm: "platform", UID: "synthetic-user-1"},
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
	caller := &predictionFixtureCaller{unknownFirst: true}
	useCase, err := NewPredictionCandidateUseCase(predictionCandidateTestConfig(), caller, observed)
	if err != nil {
		t.Fatal(err)
	}
	directRequest := predictionCandidateTestRequest("prediction-direct-0001")
	direct, err := useCase.BuildCandidate(context.Background(), directRequest)
	if err != nil || !validPredictionCandidate(direct) || direct.Status != "candidate_default_off" ||
		direct.BusinessActivation != "none" || direct.UserTower.Attempts != 2 || direct.ItemTower.Attempts != 1 ||
		direct.Ranker.Attempts != 1 || direct.Features.Source != "synthetic_typed_fixture" {
		t.Fatalf("default-off typed candidate=%+v err=%v", direct, err)
	}
	if caller.count(directRequest.LogicalCall+".user") != 2 || caller.count(directRequest.LogicalCall+".item") != 1 ||
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
	for _, span := range exporter.snapshot() {
		spanNames[span.Name()] = true
	}
	for _, name := range []string{"recommend.prediction_candidate", "recommend.prediction_call",
		"runtime.run", "invoke_agent recommend_prediction_candidate",
		"workflow execute_graph recommend_prediction_candidate",
		"workflow execute_function_node build_prediction_candidate"} {
		if !spanNames[name] {
			t.Fatalf("missing prediction/tRPC span %q: %v", name, spanNames)
		}
	}
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
	}
}
