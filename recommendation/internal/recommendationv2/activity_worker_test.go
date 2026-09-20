package recommendationv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivityWorkerClientForwardsCompleteRawEventAndCachesDecision(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		body := new(activityWorkerRequest)
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.SchemaVersion != activityWorkerRequestSchema || body.RequestID != "raw-1" {
			t.Fatalf("unexpected worker request: %+v", body)
		}
		if !bytes.Contains(body.RawEvent, []byte(`"secretless_visible_text":"keep this field"`)) {
			t.Fatalf("complete raw event was not forwarded: %s", body.RawEvent)
		}
		_ = json.NewEncoder(w).Encode(activityWorkerResponse{
			SchemaVersion: activityWorkerResponseSchema,
			RequestID:     body.RequestID,
			Events: []ActivityEvent{{
				SourceEventIDs: []string{"raw-1"},
				Activity:       "development",
				GoalRelevance:  "direct",
				Confidence:     0.9,
				ReasonCodes:    []string{"visible_content"},
				Evidence:       []string{"editor is active"},
			}},
			Score:       0.8,
			ScoreReason: "direct progress",
		})
	}))
	defer server.Close()

	client, err := NewActivityWorkerClient(ActivityAnalysisConfig{
		Enabled:        true,
		Endpoint:       server.URL,
		BearerToken:    "test-token",
		ScoreThreshold: 0.75,
	})
	if err != nil {
		t.Fatalf("NewActivityWorkerClient returned error: %v", err)
	}
	input := ActivityAnalysisInput{
		EventID: "raw-1",
		User:    UserIdentity{UserID: "user-1"},
		RawEvent: json.RawMessage(`{
			"event_id":"raw-1",
			"kind":"application.visible_content",
			"secretless_visible_text":"keep this field",
			"nested":{"preserved":true}
		}`),
	}
	first, err := client.Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if !first.ShouldCallAgent || first.TriggerID == "" {
		t.Fatalf("expected threshold trigger, got %+v", first)
	}
	if first.AccumulatedScore != 0.8 || len(first.Events) != 1 {
		t.Fatalf("unexpected analysis result: %+v", first)
	}
	second, err := client.Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("duplicate Analyze returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("duplicate event made %d worker calls, want 1", calls)
	}
	if second.TriggerID != first.TriggerID || !second.ShouldCallAgent {
		t.Fatalf("cached trigger result changed: first=%+v second=%+v", first, second)
	}
}

func TestActivityWorkerClientAccumulatesScoresPerIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request activityWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(activityWorkerResponse{
			SchemaVersion: activityWorkerResponseSchema,
			RequestID:     request.RequestID,
			Events: []ActivityEvent{{
				SourceEventIDs: []string{request.RequestID},
				Activity:       "research",
				GoalRelevance:  "supporting",
				Confidence:     0.8,
				ReasonCodes:    []string{"visible_content"},
			}},
			Score:       0.4,
			ScoreReason: "supporting progress",
		})
	}))
	defer server.Close()

	client, err := NewActivityWorkerClient(ActivityAnalysisConfig{
		Enabled:        true,
		Endpoint:       server.URL,
		BearerToken:    "test-token",
		ScoreThreshold: 0.7,
	})
	if err != nil {
		t.Fatalf("NewActivityWorkerClient returned error: %v", err)
	}
	for index, eventID := range []string{"raw-1", "raw-2"} {
		result, err := client.Analyze(context.Background(), ActivityAnalysisInput{
			EventID:  eventID,
			TenantID: "tenant-a",
			User:     UserIdentity{UserID: "user-1"},
			RawEvent: json.RawMessage(`{"event_id":"` + eventID + `"}`),
		})
		if err != nil {
			t.Fatalf("Analyze %s returned error: %v", eventID, err)
		}
		if index == 0 && result.ShouldCallAgent {
			t.Fatalf("first score should not trigger: %+v", result)
		}
		if index == 1 && (!result.ShouldCallAgent || result.TriggerID == "") {
			t.Fatalf("second score should trigger: %+v", result)
		}
	}
}

func TestActivityWorkerClientCoalescesConcurrentDuplicateEvents(t *testing.T) {
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWorker()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			close(firstStarted)
		} else {
			close(secondStarted)
		}
		<-release
		var request activityWorkerRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(activityWorkerResponse{
			SchemaVersion: activityWorkerResponseSchema,
			RequestID:     request.RequestID,
			Events: []ActivityEvent{{
				SourceEventIDs: []string{request.RequestID},
				Activity:       "development",
				GoalRelevance:  "direct",
				Confidence:     0.9,
			}},
			Score:       0.6,
			ScoreReason: "direct progress",
		})
	}))
	defer server.Close()

	client, err := NewActivityWorkerClient(ActivityAnalysisConfig{
		Enabled:        true,
		Endpoint:       server.URL,
		BearerToken:    "test-token",
		ScoreThreshold: 0.5,
	})
	if err != nil {
		t.Fatalf("NewActivityWorkerClient returned error: %v", err)
	}
	input := ActivityAnalysisInput{
		EventID:  "concurrent-raw-1",
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "user-1"},
		RawEvent: json.RawMessage(`{"event_id":"concurrent-raw-1","complete":true}`),
	}
	type outcome struct {
		result ActivityAnalysisResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	analyze := func() {
		result, err := client.Analyze(context.Background(), input)
		outcomes <- outcome{result: result, err: err}
	}
	go analyze()
	<-firstStarted
	go analyze()
	select {
	case <-secondStarted:
		t.Fatalf("concurrent duplicate reached the worker twice")
	case <-time.After(100 * time.Millisecond):
	}
	releaseWorker()
	for range 2 {
		outcome := <-outcomes
		if outcome.err != nil {
			t.Fatalf("Analyze returned error: %v", outcome.err)
		}
		if !outcome.result.ShouldCallAgent || outcome.result.AccumulatedScore != 0.6 {
			t.Fatalf("unexpected duplicate result: %+v", outcome.result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("worker calls = %d, want 1", calls.Load())
	}
}

func TestRecordEventsReturnsActivityResultWithoutRejectingTheRawEvent(t *testing.T) {
	analyzer := &testActivityAnalyzer{result: ActivityAnalysisResult{
		Status:           "analyzed",
		RequestID:        "raw-1",
		Score:            0.8,
		AccumulatedScore: 0.8,
		ShouldCallAgent:  true,
		TriggerID:        "activity_trigger_1",
	}}
	runtime := NewRecommendationRuntime(WithActivityAnalyzer(analyzer))
	response := runtime.RecordEvents(context.Background(), EventBatchRequest{Events: []BehaviorEvent{{
		EventID:   "raw-1",
		EventType: "activity.raw",
		User:      UserIdentity{UserID: "user-1"},
		RawEvent:  json.RawMessage(`{"event_id":"raw-1","complete":true}`),
	}}})
	if response.Accepted != 1 || len(response.ActivityAnalysis) != 1 {
		t.Fatalf("unexpected event response: %+v", response)
	}
	if !response.ActivityAnalysis[0].ShouldCallAgent {
		t.Fatalf("expected local trigger decision: %+v", response.ActivityAnalysis[0])
	}
	if !bytes.Contains(analyzer.inputs[0].RawEvent, []byte(`"complete":true`)) {
		t.Fatalf("runtime did not pass the complete raw event: %s", analyzer.inputs[0].RawEvent)
	}
	summary := runtime.Summary()
	if summary.ActivityAnalyzed != 1 || summary.ActivityAgentTriggers != 1 {
		t.Fatalf("activity observations not recorded: %+v", summary)
	}
}

func TestRecordEventsKeepsBehaviorEventWhenActivityWorkerFails(t *testing.T) {
	runtime := NewRecommendationRuntime(WithActivityAnalyzer(&testActivityAnalyzer{err: errors.New("worker offline")}))
	response := runtime.RecordEvents(context.Background(), EventBatchRequest{Events: []BehaviorEvent{{
		EventID:   "raw-1",
		EventType: "activity.raw",
		User:      UserIdentity{UserID: "user-1"},
		RawEvent:  json.RawMessage(`{"event_id":"raw-1"}`),
	}}})
	if response.Accepted != 1 || len(response.ActivityAnalysis) != 1 {
		t.Fatalf("raw event should stay accepted: %+v", response)
	}
	result := response.ActivityAnalysis[0]
	if result.Status != "failed" || result.ErrorCode != "worker_unavailable" || result.ShouldCallAgent {
		t.Fatalf("unexpected failed analysis result: %+v", result)
	}
}

type testActivityAnalyzer struct {
	mu     sync.Mutex
	inputs []ActivityAnalysisInput
	result ActivityAnalysisResult
	err    error
}

func (a *testActivityAnalyzer) Analyze(_ context.Context, input ActivityAnalysisInput) (ActivityAnalysisResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inputs = append(a.inputs, input)
	return a.result, a.err
}
