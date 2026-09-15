package datacenter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

func predictionNumber(value float64) *float64 { return &value }

func predictionRequest() prediction.Request {
	return prediction.Request{Model: "recommend-engagement", ConfigurationID: "11111111-1111-4111-8111-111111111111",
		OutputContract: prediction.OutputContract(prediction.Ranker), FeatureContractID: "engagement-features.v1",
		PairID: "sea.recommend.pair.fixture", SpaceID: "sea.recommend.space.fixture", Task: prediction.Ranker,
		Input: []prediction.Input{{ID: "pair-a", UserInterest: predictionNumber(0.2), ItemQuality: predictionNumber(0.1)}}}
}

func predictionResponse(q prediction.Request) prediction.Response {
	dot, logit := -0.0027, 0.0375
	return prediction.Response{Model: q.Model, ConfigurationID: q.ConfigurationID,
		ArtifactSHA256: strings.Repeat("a", 64), ModelCallID: "22222222-2222-4222-8222-222222222222",
		PointerRevision: 1, OutputContract: q.OutputContract, FeatureContractID: q.FeatureContractID,
		PairID: q.PairID, SpaceID: q.SpaceID, Task: q.Task,
		ModelConfig: prediction.ModelConfig{Recipe: "sea.recommend.cpu-towers-lr.v1", FeatureContractID: q.FeatureContractID,
			FeatureOrder:        []string{"user_interest", "item_quality"},
			RankerFeatureOrder:  []string{"bias", "user_interest", "item_quality", "interaction", "tower_dot"},
			PreprocessingSHA256: strings.Repeat("b", 64), EmbeddingDimension: 2, Metric: "dot",
			ScoreSemantics: "uncalibrated_logit", Calibration: "none", PairID: q.PairID, SpaceID: q.SpaceID},
		Data: []prediction.Output{{ID: "pair-a", UserEmbedding: []float64{0.1, 0.2},
			ItemEmbedding: []float64{0.3, 0.4}, TowerDot: &dot, RankerLogit: &logit}},
		Usage: &prediction.Usage{InputRows: 1, InputFeatureValues: 2, OutputRows: 1, OutputValues: 6}}
}

func TestPredictionClientPinsLogicalKeyAndValidatesResponse(t *testing.T) {
	q := predictionRequest()
	response := predictionResponse(q)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	keys := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		keys[key]++
		attempt := keys[key]
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/predictions" ||
			r.Header.Get("Authorization") != "Bearer native-subject-token" ||
			(key != "prediction-logical-call-0001" && key != "prediction-logical-call-0002" &&
				key != "prediction-logical-call-0003") {
			t.Errorf("request contract drift method=%s path=%s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		var actual prediction.Request
		if err := prediction.Decode(body, &actual); err != nil || actual.ConfigurationID != q.ConfigurationID || actual.PairID != q.PairID {
			t.Errorf("typed request drift: %+v %v", actual, err)
		}
		if key == "prediction-logical-call-0003" && attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"prediction outcome is unknown; retry the same idempotency key"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client, err := New(httpclient.Config{BaseURL: server.URL, Token: "native-subject-token"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Predict(context.Background(), q, "prediction-logical-call-0001")
	if err != nil || result.Response.ModelCallID != response.ModelCallID || string(result.Body) != string(raw) {
		t.Fatalf("prediction result=%+v err=%v", result, err)
	}
	if _, err := client.Predict(context.Background(), q, "short"); err == nil {
		t.Fatal("invalid logical call key reached DataCenter")
	}
	if _, err := client.Predict(context.Background(), q, "prediction-logical-call-0003"); !errors.Is(err, ErrPredictionOutcomeUnknown) {
		t.Fatalf("503 unknown response was not recoverable: %v", err)
	} else {
		var status *httpclient.HTTPError
		if !errors.As(err, &status) || status.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("503 recovery lost HTTP receipt: %v", err)
		}
	}
	recovered, err := client.Predict(context.Background(), q, "prediction-logical-call-0003")
	mu.Lock()
	recoveryCalls := keys["prediction-logical-call-0003"]
	mu.Unlock()
	if err != nil || recovered.Response.ModelCallID != response.ModelCallID || recoveryCalls != 2 {
		t.Fatalf("same-key 503 recovery result=%+v calls=%d err=%v", recovered, recoveryCalls, err)
	}
	response.SpaceID = "wrong-space"
	raw, _ = json.Marshal(response)
	if _, err := client.Predict(context.Background(), q, "prediction-logical-call-0002"); err == nil {
		t.Fatal("wrong-space prediction response accepted")
	}
}

func TestPredictionClientSeparatesInFlightFromBindingConflict(t *testing.T) {
	q := predictionRequest()
	response := predictionResponse(q)
	completeBody, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		seen[key]++
		attempt := seen[key]
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/predictions" || key == "" {
			t.Errorf("prediction recovery changed HTTP request: %s %s %q", r.Method, r.URL.Path, key)
		}
		switch key {
		case "prediction-sequence-0001":
			switch attempt {
			case 1:
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"prediction outcome is unknown; retry the same idempotency key"}`)
				return
			case 2:
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"prediction logical call is still in progress"}`)
				return
			}
		case "prediction-conflict-0001":
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"prediction state conflict"}`)
			return
		case "prediction-no-retry-0001":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"prediction logical call is still in progress"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(completeBody)
	}))
	defer server.Close()
	client, err := New(httpclient.Config{BaseURL: server.URL, Token: "native-subject-token"})
	if err != nil {
		t.Fatal(err)
	}
	key := "prediction-sequence-0001"
	if _, err := client.Predict(context.Background(), q, key); !errors.Is(err, ErrPredictionOutcomeUnknown) {
		t.Fatalf("503 unknown lost retryable type: %v", err)
	}
	if _, err := client.Predict(context.Background(), q, key); !errors.Is(err, ErrPredictionInFlight) {
		t.Fatalf("409 lease lost retryable type: %v", err)
	} else {
		var status *httpclient.HTTPError
		if !errors.As(err, &status) || status.StatusCode != http.StatusConflict || status.RetryAfter != "1" {
			t.Fatalf("409 lease lost HTTP receipt: %v", err)
		}
	}
	completed, err := client.Predict(context.Background(), q, key)
	if err != nil || completed.Response.ModelCallID != response.ModelCallID || string(completed.Body) != string(completeBody) {
		t.Fatalf("same-key completed replay=%+v err=%v", completed, err)
	}
	mu.Lock()
	count := seen[key]
	mu.Unlock()
	if count != 3 {
		t.Fatalf("unknown/lease recovery changed logical key: calls=%d", count)
	}
	for _, conflictKey := range []string{"prediction-conflict-0001", "prediction-no-retry-0001"} {
		_, err := client.Predict(context.Background(), q, conflictKey)
		var status *httpclient.HTTPError
		if !errors.As(err, &status) || status.StatusCode != http.StatusConflict || errors.Is(err, ErrPredictionInFlight) {
			t.Fatalf("binding conflict was misclassified as lease: key=%s err=%v", conflictKey, err)
		}
	}
}

func TestPredictionLeaseEnvelopeRejectsAmbiguousJSON(t *testing.T) {
	expected := "prediction logical call is still in progress"
	for _, body := range []string{
		`{"error":"prediction state conflict"}`,
		`{"error":"prediction state conflict","error":"prediction logical call is still in progress"}`,
		`{"error":"prediction logical call is still in progress","other":true}`,
		`{"error":"prediction logical call is still in progress"}{}`,
		`null`,
	} {
		if predictionErrorEnvelope(body, expected) {
			t.Fatalf("ambiguous/non-lease envelope classified as in-flight: %s", body)
		}
	}
}
