package compile

import (
	"context"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

func graphTestSources() []SourceRef {
	return []SourceRef{
		{DocKey: "doc-a", RevisionID: "rev-a", Summary: "海洋观测 建立 基线 冗余 容量"},
		{DocKey: "doc-b", RevisionID: "rev-b", Summary: "气候能源 量化 不确定性 公开 披露"},
		{DocKey: "doc-c", RevisionID: "rev-c", Summary: "城市交通 仿真 环境 验证 精度"},
	}
}

func graphTestRequest(skipPolish bool) CompileGraphRequest {
	return CompileGraphRequest{
		JobID: "job-graph-1", ModuleID: "mod-1",
		Sources: graphTestSources(), SkipPolish: skipPolish,
	}
}

// runCompileGraph 用真实 Runner 驱动 GraphAgent 一次，返回完成回执。
// 这是 C17 要求的框架链路：Graph 编译 → GraphAgent → Runner → 完成事件。
func runCompileGraph(t *testing.T, appName string, request CompileGraphRequest) (CompileGraphReceipt, bool) {
	t.Helper()
	ag, err := NewCompileGraphAgent(DevExecutor())
	if err != nil {
		t.Fatalf("NewCompileGraphAgent: %v", err)
	}
	r := runner.NewRunner(appName, ag)
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close runner: %v", err)
		}
	}()
	option, err := CompileGraphRunOption(request)
	if err != nil {
		t.Fatalf("CompileGraphRunOption: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := r.Run(ctx, "user-1", "session-1", model.NewUserMessage("compile"), option)
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	var receipt CompileGraphReceipt
	var found bool
	for e := range stream {
		if e == nil {
			continue
		}
		if e.Error != nil {
			t.Fatalf("runner event error: %v", e.Error)
		}
		got, ok, err := CompileGraphReceiptFromCompletion(e)
		if err != nil {
			t.Fatalf("receipt from completion: %v", err)
		}
		if ok {
			receipt, found = got, true
		}
	}
	return receipt, found
}

// TestCompileGraphAgentRunsRealFramework 断言四阶段在框架 Graph 内完成并
// 产出合法回执——C17：编制编排必须经框架 Agent/Graph 运行。
func TestCompileGraphAgentRunsRealFramework(t *testing.T) {
	receipt, found := runCompileGraph(t, "compile_graph_test", graphTestRequest(false))
	if !found {
		t.Fatal("no graph completion event carrying a receipt")
	}
	if receipt.JobID != "job-graph-1" || receipt.ModuleID != "mod-1" {
		t.Fatalf("receipt identity mismatch: %+v", receipt)
	}
	if receipt.Status != string(StatusDone) {
		t.Fatalf("status = %q, want %q", receipt.Status, StatusDone)
	}
	if receipt.StageCount != len(Stages()) {
		t.Fatalf("stage count = %d, want %d", receipt.StageCount, len(Stages()))
	}
	if receipt.SectionCount <= 0 {
		t.Fatalf("section count = %d, want > 0", receipt.SectionCount)
	}
	if receipt.CitationCount <= 0 {
		t.Fatalf("citation count = %d, want > 0", receipt.CitationCount)
	}
	if receipt.ClaimCount <= 0 {
		t.Fatalf("claim count = %d, want > 0", receipt.ClaimCount)
	}
}

// TestCompileGraphSkipPolish 验证 do_polish=false 经框架链路生效。
func TestCompileGraphSkipPolish(t *testing.T) {
	receipt, found := runCompileGraph(t, "compile_graph_skip_test", graphTestRequest(true))
	if !found {
		t.Fatal("no receipt for skip-polish run")
	}
	if receipt.Status != string(StatusDone) {
		t.Fatalf("status = %q, want %q", receipt.Status, StatusDone)
	}
}

// TestCompileGraphRunOptionRejectsBadRequest 非法请求在注入前被拒。
func TestCompileGraphRunOptionRejectsBadRequest(t *testing.T) {
	if _, err := CompileGraphRunOption(CompileGraphRequest{JobID: "", ModuleID: "m", Sources: graphTestSources()}); err == nil {
		t.Fatal("expected error for empty job id")
	}
	if _, err := CompileGraphRunOption(CompileGraphRequest{JobID: "j", ModuleID: "m"}); err == nil {
		t.Fatal("expected error for empty sources")
	}
}

// TestCompileGraphAgentRejectsIncompleteExecutor 四阶段缺一即拒。
func TestCompileGraphAgentRejectsIncompleteExecutor(t *testing.T) {
	full := DevExecutor()
	if _, err := NewCompileGraphAgent(Executor{Curate: full.Curate}); err == nil {
		t.Fatal("expected error for executor missing stages")
	}
}

// TestCompileGraphReceiptIgnoresNonCompletionEvent 非完成事件不产出回执。
func TestCompileGraphReceiptIgnoresNonCompletionEvent(t *testing.T) {
	if _, ok, err := CompileGraphReceiptFromCompletion(&event.Event{}); ok || err != nil {
		t.Fatalf("non-completion event: ok=%v err=%v, want false/nil", ok, err)
	}
}

// TestCompileGraphReceiptRejectsCompletionWithoutReceipt 完成事件缺回执键即报错。
func TestCompileGraphReceiptRejectsCompletionWithoutReceipt(t *testing.T) {
	ag, err := NewCompileGraphAgent(DevExecutor())
	if err != nil {
		t.Fatalf("NewCompileGraphAgent: %v", err)
	}
	r := runner.NewRunner("compile_graph_strip_test", ag)
	defer func() { _ = r.Close() }()
	option, err := CompileGraphRunOption(graphTestRequest(false))
	if err != nil {
		t.Fatalf("CompileGraphRunOption: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := r.Run(ctx, "u", "s", model.NewUserMessage("strip"), option)
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	var stripped *event.Event
	for e := range stream {
		if e != nil && graph.IsGraphCompletionEvent(e) {
			copied := *e
			copied.StateDelta = map[string][]byte{}
			stripped = &copied
		}
	}
	if stripped == nil {
		t.Fatal("no graph completion event observed")
	}
	if _, ok, err := CompileGraphReceiptFromCompletion(stripped); !ok || err == nil {
		t.Fatalf("completion without receipt: ok=%v err=%v, want true/error", ok, err)
	}
}

// TestExecutorStageFunc 按阶段取执行器，未知阶段为 nil。
func TestExecutorStageFunc(t *testing.T) {
	ex := DevExecutor()
	for _, s := range Stages() {
		if ex.StageFunc(s) == nil {
			t.Fatalf("stage %s has nil executor", s)
		}
	}
	if ex.StageFunc(Stage("nope")) != nil {
		t.Fatal("unknown stage should return nil")
	}
}

// TestCompileGraphReceiptCarriesNoArticleText 回执只含标识与计数，不夹带正文。
func TestCompileGraphReceiptCarriesNoArticleText(t *testing.T) {
	receipt, found := runCompileGraph(t, "compile_graph_receipt_test", graphTestRequest(false))
	if !found {
		t.Fatal("no receipt")
	}
	for _, field := range []string{receipt.JobID, receipt.ModuleID, receipt.Status} {
		if strings.Contains(field, "要点") || strings.Contains(field, "第") {
			t.Fatalf("receipt field carries article text: %q", field)
		}
	}
}
