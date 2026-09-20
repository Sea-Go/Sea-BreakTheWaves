package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	recommendationv2 "sea/internal/recommendationv2"
)

func TestV2EventsRouteForwardsRawEventToActivityAnalyzer(t *testing.T) {
	analyzer := &routeActivityAnalyzer{}
	runtime := recommendationv2.NewRecommendationRuntime(recommendationv2.WithActivityAnalyzer(analyzer))
	router := NewRouter(nil, recommendationv2.NewRecommendationService(runtime), nil, nil, nil, nil, nil)

	body := []byte(`{
		"events": [{
			"event_id": "raw-route-1",
			"event_type": "activity.raw",
			"user": {"user_id": "user-1"},
			"raw_event": {
				"source": "desktop-client",
				"nested": {"must_reach_model": true},
				"records": [{"id": "record-1", "value": 42}]
			},
			"activity_context": {"goal": "finish a task"}
		}]
	}`)
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			Accepted         int                                       `json:"accepted"`
			ActivityAnalysis []recommendationv2.ActivityAnalysisResult `json:"activity_analysis"`
		} `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || response.Data.Accepted != 1 || len(response.Data.ActivityAnalysis) != 1 {
		t.Fatalf("unexpected event response: %s", writer.Body.String())
	}
	if !response.Data.ActivityAnalysis[0].ShouldCallAgent {
		t.Fatalf("missing trigger decision: %s", writer.Body.String())
	}
	var forwarded map[string]any
	if err := json.Unmarshal(analyzer.input.RawEvent, &forwarded); err != nil {
		t.Fatalf("decode forwarded raw event: %v", err)
	}
	nested, nestedOK := forwarded["nested"].(map[string]any)
	records, recordsOK := forwarded["records"].([]any)
	if forwarded["source"] != "desktop-client" || !nestedOK || nested["must_reach_model"] != true || !recordsOK || len(records) != 1 || analyzer.input.ActivityContext["goal"] != "finish a task" {
		t.Fatalf("raw event or context was not forwarded: %+v", analyzer.input)
	}
}

type routeActivityAnalyzer struct {
	input recommendationv2.ActivityAnalysisInput
}

func (a *routeActivityAnalyzer) Analyze(_ context.Context, input recommendationv2.ActivityAnalysisInput) (recommendationv2.ActivityAnalysisResult, error) {
	a.input = input
	return recommendationv2.ActivityAnalysisResult{
		Status:           "analyzed",
		RequestID:        input.EventID,
		Score:            1,
		ScoreReason:      "test score",
		AccumulatedScore: 1,
		ShouldCallAgent:  true,
		TriggerID:        "activity_test_trigger",
	}, nil
}
