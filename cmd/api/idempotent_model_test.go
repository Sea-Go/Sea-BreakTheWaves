package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestGatewayModelSendsStablePerRequestIdempotencyKey(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer local-test-key" ||
			!strings.HasPrefix(r.Header.Get("Idempotency-Key"), "btw-summary-") {
			t.Errorf("framework model request missed gateway contract: %s %s", r.Method, r.URL.Path)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["reasoning_effort"] != "none" {
			t.Errorf("fast/low summary did not request bounded no-reasoning output: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "local-model-fixture", "object": "chat.completion",
			"model": "agent", "choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	client := gatewayModel{name: "agent", url: server.URL + "/v1", key: "local-test-key"}
	base := searchdomain.ModelInvocationRef{AuthorityID: "rtw.identity", TenantID: "platform",
		SubjectID: "user-one", SearchID: "search-one", AnswerID: "answer-one",
		SnapshotSHA: artifacts.Hash([]byte("snapshot-one"))}
	otherSearch, otherSubject, otherSnapshot := base, base, base
	otherSearch.SearchID = "search-two"
	otherSubject.SubjectID = "user-two"
	otherSnapshot.SnapshotSHA = artifacts.Hash([]byte("snapshot-two"))
	for _, scope := range []searchdomain.ModelInvocationRef{base, base, otherSearch, otherSubject, otherSnapshot} {
		ctx, err := searchdomain.WithModelInvocationRef(context.Background(), scope)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := client.GenerateContent(ctx, &model.Request{
			Messages: []model.Message{model.NewUserMessage("same prompt text")}})
		if err != nil {
			t.Fatal(err)
		}
		for response := range stream {
			if response.Error != nil {
				t.Fatalf("OpenAI adapter rejected gateway response: %+v", response.Error)
			}
		}
	}
	if len(keys) != 5 || keys[0] != keys[1] || keys[0] == keys[2] ||
		keys[0] == keys[3] || keys[0] == keys[4] {
		t.Fatalf("retry key was unstable or distinct RTW scope collided: %v", keys)
	}
	if _, err := client.GenerateContent(context.Background(), &model.Request{
		Messages: []model.Message{model.NewUserMessage("same prompt text")}}); err == nil || len(keys) != 5 {
		t.Fatal("missing fixed RTW invocation scope reached DataCenter gateway")
	}
}
