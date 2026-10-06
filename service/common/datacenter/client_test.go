package datacenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestOneAddressUsesDataCenterManifestAndAuthenticatedChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer deployment-credential" {
			t.Errorf("workload identity missing on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/model/manifest":
			_, _ = w.Write([]byte(`{"retrieval":{"model":"managed-encoder","space":"managed-space","dimensions":1024,"dense":"d","sparse":"s","multi":"m","search_ranker":"rank"},"chat":{"agent":"agent","planning":"planning","content_identity":"physical-content-1"}}`))
		case "/v1/model/encode":
			_, _ = w.Write([]byte(`{"data":[{"model":"managed-encoder","space":"managed-space","segments":[{"start":0,"end":1,"dense":[1],"sparse":{"indices":[1],"values":[2]},"multi":[[1]]}]}]}`))
		case "/v1/model/rank":
			_, _ = w.Write([]byte(`{"model":"rank","scores":[0.7]}`))
		case "/v1/model/chat/completions":
			if r.Header.Get("X-WhaleHall-Model-Purpose") != "planning" || len(r.Header.Get("Idempotency-Key")) < 8 {
				t.Error("DataCenter purpose or idempotency was lost")
			}
			var body struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "planning" {
				t.Errorf("physical model was selected by BTW: %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"planning","choices":[{"index":0,"message":{"role":"assistant","content":"grounded"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	connector, err := New(server.URL, func(context.Context) (string, error) { return "deployment-credential", nil })
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := connector.Manifest(context.Background())
	if err != nil || manifest.Retrieval.Model != "managed-encoder" {
		t.Fatalf("manifest: %+v %v", manifest, err)
	}
	encoded, err := connector.Inference().Encode(context.Background(), manifest.Retrieval.Model, manifest.Retrieval.Space, []string{"x"})
	if err != nil || len(encoded) != 1 || encoded[0].Segments[0].Sparse.Values[0] != 2 {
		t.Fatalf("DataCenter-controlled three-way encoding: %+v %v", encoded, err)
	}
	scores, err := connector.Inference().Rank(context.Background(), manifest.Retrieval.SearchRanker, [][]float64{{1}})
	if err != nil || len(scores) != 1 || scores[0] != 0.7 {
		t.Fatalf("DataCenter-controlled ranker: %+v %v", scores, err)
	}
	chat, err := connector.Chat(manifest.Chat.Planning, "planning")
	if err != nil {
		t.Fatal(err)
	}
	responses, err := chat.GenerateContent(context.Background(), &model.Request{Messages: []model.Message{model.NewUserMessage("source-grounded question")}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for response := range responses {
		if response != nil && len(response.Choices) > 0 && strings.Contains(response.Choices[0].Message.Content, "grounded") {
			found = true
		}
	}
	if !found {
		t.Fatal("DataCenter chat response was not delivered")
	}
}

func TestDataCenterChatStreamingKeepsPurposeAndIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/model/chat/completions" || r.Header.Get("Authorization") != "Bearer workload" || r.Header.Get("X-WhaleHall-Model-Purpose") != "agent" || len(r.Header.Get("Idempotency-Key")) < 8 {
			t.Error("streaming model request lost DataCenter routing identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"stream\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"agent\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"stream\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"agent\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	connector, err := New(server.URL, func(context.Context) (string, error) { return "workload", nil })
	if err != nil {
		t.Fatal(err)
	}
	chat, err := connector.Chat("agent", "agent")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := chat.GenerateContent(context.Background(), &model.Request{Messages: []model.Message{model.NewUserMessage("hello")}, GenerationConfig: model.GenerationConfig{Stream: true}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for response := range stream {
		if response != nil && len(response.Choices) > 0 && strings.Contains(response.Choices[0].Delta.Content, "hello") {
			found = true
		}
	}
	if !found {
		t.Fatal("DataCenter streaming delta was not consumed")
	}
}
