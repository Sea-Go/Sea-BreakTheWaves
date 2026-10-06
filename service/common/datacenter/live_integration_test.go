package datacenter

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// TestLiveDataCenterModelPath exercises the deployed HTTP boundary rather than
// an httptest substitute. It is opt-in because it needs an isolated DataCenter
// with an active workload identity and a real representation backend.
func TestLiveDataCenterModelPath(t *testing.T) {
	address := os.Getenv("BTW_E2E_DATACENTER_URL")
	if address == "" || os.Getenv("DATACENTER_WORKLOAD_TOKEN_FILE") == "" {
		t.Skip("set BTW_E2E_DATACENTER_URL and DATACENTER_WORKLOAD_TOKEN_FILE for the live DataCenter test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	connector, err := New(address, DeploymentIdentity())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := connector.Manifest(ctx)
	if err != nil {
		t.Fatalf("BTW could not authenticate and read DataCenter's active model contract: %v", err)
	}
	if err := connector.Inference().Ready(ctx, manifest.Retrieval); err != nil {
		t.Fatalf("DataCenter could not reach its representation backend: %v", err)
	}
	encoded, err := connector.Inference().Encode(ctx, manifest.Retrieval.Model, manifest.Retrieval.Space, []string{"鞘翅目属于昆虫纲。"})
	if err != nil {
		t.Fatalf("BTW → DataCenter → representation backend failed: %v", err)
	}
	if len(encoded) != 1 || len(encoded[0].Segments) == 0 || len(encoded[0].Segments[0].Dense) != manifest.Retrieval.Dimensions || len(encoded[0].Segments[0].Sparse.Indices) == 0 || len(encoded[0].Segments[0].Multi) == 0 {
		t.Fatalf("DataCenter returned incomplete Dense/Sparse/Multi representation: segments=%d", len(encoded[0].Segments))
	}
	if _, err := connector.Inference().Encode(ctx, manifest.Retrieval.Model, manifest.Retrieval.Space+"-mismatch", []string{"鞘翅目"}); err == nil {
		t.Fatal("DataCenter accepted an incompatible representation space")
	}
	badIdentity, err := New(address, func(context.Context) (string, error) { return "invalid-e2e-workload-identity", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badIdentity.Manifest(ctx); err == nil {
		t.Fatal("DataCenter accepted an invalid workload identity")
	}
	chat, err := connector.Chat(manifest.Chat.Agent, "agent")
	if err != nil {
		t.Fatal(err)
	}
	responses, err := chat.GenerateContent(ctx, &model.Request{Messages: []model.Message{model.NewUserMessage("用一句话回答：1 加 1 等于几？")}})
	if err != nil {
		t.Fatalf("BTW → DataCenter generation route failed: %v", err)
	}
	var complete strings.Builder
	for response := range responses {
		if response != nil && len(response.Choices) > 0 {
			complete.WriteString(response.Choices[0].Message.Content)
		}
	}
	if strings.TrimSpace(complete.String()) == "" {
		t.Fatal("DataCenter generation returned no completed content")
	}
	stream, err := chat.GenerateContent(ctx, &model.Request{Messages: []model.Message{model.NewUserMessage("只回答一个数字：2 加 2 等于几？")}, GenerationConfig: model.GenerationConfig{Stream: true}})
	if err != nil {
		t.Fatalf("BTW → DataCenter streaming route failed: %v", err)
	}
	var delta strings.Builder
	for response := range stream {
		if response != nil && len(response.Choices) > 0 {
			delta.WriteString(response.Choices[0].Delta.Content)
		}
	}
	if strings.TrimSpace(delta.String()) == "" {
		t.Fatal("DataCenter streaming route returned no text delta")
	}
	t.Logf("live DataCenter connection passed: Dense=%d, Sparse=%d, Multi=%d, chat and SSE delivered", len(encoded[0].Segments[0].Dense), len(encoded[0].Segments[0].Sparse.Indices), len(encoded[0].Segments[0].Multi))
}
