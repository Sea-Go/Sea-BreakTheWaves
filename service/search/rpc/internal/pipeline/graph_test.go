package pipeline

import (
	"context"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

// runSearchGraph 经真实 Runner 驱动检索 Graph 一次，返回完成回执。
// 这是 C17 要求的框架链路：Graph 编译 → GraphAgent → Runner → 完成事件。
func runSearchGraph(t *testing.T, appName string, request SearchGraphRequest) (SearchGraphReceipt, bool) {
	t.Helper()
	ag, err := NewSearchGraphAgent(newSeedsPipeline(t))
	if err != nil {
		t.Fatalf("NewSearchGraphAgent: %v", err)
	}
	r := runner.NewRunner(appName, ag)
	defer func() { _ = r.Close() }()
	option, err := SearchGraphRunOption(request)
	if err != nil {
		t.Fatalf("SearchGraphRunOption: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stream, err := r.Run(ctx, "u", "s", model.NewUserMessage("search"), option)
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	var receipt SearchGraphReceipt
	var found bool
	for e := range stream {
		if e == nil {
			continue
		}
		if e.Error != nil {
			t.Fatalf("runner event error: %v", e.Error)
		}
		got, ok, err := SearchGraphReceiptFromCompletion(e)
		if err != nil {
			t.Fatalf("receipt from completion: %v", err)
		}
		if ok {
			receipt, found = got, true
		}
	}
	return receipt, found
}

// TestSearchGraphAgentRunsRealFramework 检索装配在框架 Graph 内完成（C17
// 点名"检索装配以框架公开 API 为基础"）。
func TestSearchGraphAgentRunsRealFramework(t *testing.T) {
	receipt, found := runSearchGraph(t, "search_graph_test", SearchGraphRequest{
		Query: matrixQuery, Tier: "balanced", Delivery: "summary",
	})
	if !found {
		t.Fatal("no graph completion event carrying a receipt")
	}
	if receipt.QueryID == "" {
		t.Fatal("receipt query id is empty")
	}
	if receipt.EffectiveTier != "balanced" {
		t.Fatalf("effective tier = %q, want balanced", receipt.EffectiveTier)
	}
	if receipt.Candidates <= 0 {
		t.Fatalf("candidates = %d, want > 0", receipt.Candidates)
	}
	if !receipt.HasAnswer {
		t.Fatal("summary delivery must carry an answer")
	}
	if receipt.Citations <= 0 {
		t.Fatalf("citations = %d, want > 0", receipt.Citations)
	}
}

// TestSearchGraphToolsDelivery tools 交付不产答案但仍有候选。
func TestSearchGraphToolsDelivery(t *testing.T) {
	receipt, found := runSearchGraph(t, "search_graph_tools_test", SearchGraphRequest{
		Query: matrixQuery, Tier: "fast", Delivery: "tools",
	})
	if !found {
		t.Fatal("no receipt")
	}
	if receipt.HasAnswer {
		t.Fatal("tools delivery must not carry an answer")
	}
	if receipt.Citations != 0 {
		t.Fatalf("tools delivery citations = %d, want 0", receipt.Citations)
	}
	if receipt.Candidates <= 0 {
		t.Fatalf("tools delivery candidates = %d, want > 0", receipt.Candidates)
	}
}

// TestSearchGraphRunOptionDefaults 未指定档位/交付时用默认值。
func TestSearchGraphRunOptionDefaults(t *testing.T) {
	if _, err := SearchGraphRunOption(SearchGraphRequest{Query: "x"}); err != nil {
		t.Fatalf("defaults should be accepted: %v", err)
	}
}

// TestSearchGraphRunOptionRejectsBadInput 空查询/未知档位/未知交付均拒。
func TestSearchGraphRunOptionRejectsBadInput(t *testing.T) {
	if _, err := SearchGraphRunOption(SearchGraphRequest{Query: ""}); err == nil {
		t.Fatal("expected error for empty query")
	}
	if _, err := SearchGraphRunOption(SearchGraphRequest{Query: "x", Tier: "turbo"}); err == nil {
		t.Fatal("expected error for unknown tier")
	}
	if _, err := SearchGraphRunOption(SearchGraphRequest{Query: "x", Delivery: "raw"}); err == nil {
		t.Fatal("expected error for unknown delivery")
	}
}

// TestSearchGraphAgentRequiresPipeline nil 管线即拒。
func TestSearchGraphAgentRequiresPipeline(t *testing.T) {
	if _, err := NewSearchGraphAgent(nil); err == nil {
		t.Fatal("expected error for nil pipeline")
	}
}

// TestSearchGraphDeterministic 同输入经框架链路同输出。
func TestSearchGraphDeterministic(t *testing.T) {
	req := SearchGraphRequest{Query: matrixQuery, Tier: "balanced", Delivery: "summary"}
	first, ok := runSearchGraph(t, "search_graph_det_test", req)
	if !ok {
		t.Fatal("no receipt on first run")
	}
	for i := 0; i < 3; i++ {
		again, ok := runSearchGraph(t, "search_graph_det_test", req)
		if !ok {
			t.Fatalf("no receipt on run %d", i)
		}
		if again.Candidates != first.Candidates || again.Citations != first.Citations {
			t.Fatalf("run %d differs: %+v vs %+v", i, again, first)
		}
	}
}
