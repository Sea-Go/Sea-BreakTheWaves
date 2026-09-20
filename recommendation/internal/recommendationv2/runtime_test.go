package recommendationv2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sea/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecommendHybridReturnsUnifiedResponse(t *testing.T) {
	rt := NewRecommendationRuntime()
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     3,
		PathMode: PathHybrid,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if resp.PathTaken != PathHybrid {
		t.Fatalf("PathTaken = %q, want %q", resp.PathTaken, PathHybrid)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(resp.Items))
	}
	if resp.TraceID == "" {
		t.Fatal("trace_id must be populated")
	}
	if len(resp.Steps) == 0 {
		t.Fatal("debug response must include graph steps")
	}
	if resp.Cost.LLMCalls == 0 {
		t.Fatal("hybrid path should include simulated agent/LLM cost")
	}
	summary := rt.Summary()
	if summary.Requests != 1 || summary.ByPath[PathHybrid] != 1 {
		t.Fatalf("summary not updated: %+v", summary)
	}
}

func TestRecommendAutoRoutesQueryToSlow(t *testing.T) {
	rt := NewRecommendationRuntime()
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		User:  UserIdentity{UserID: "u1"},
		Query: "graph search",
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if resp.PathTaken != PathSlow {
		t.Fatalf("PathTaken = %q, want %q", resp.PathTaken, PathSlow)
	}
}

func TestRecordEventsUpdatesSummary(t *testing.T) {
	rt := NewRecommendationRuntime()
	resp := rt.RecordEvents(context.Background(), EventBatchRequest{
		Events: []BehaviorEvent{{EventType: EventClick, User: UserIdentity{UserID: "u1"}}},
	})
	if resp.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", resp.Accepted)
	}
	if got := rt.Summary().EventsAccepted; got != 1 {
		t.Fatalf("events accepted summary = %d, want 1", got)
	}
}

func TestScopedConfigAndRequestOverridePriority(t *testing.T) {
	provider := NewStaticConfigProvider()
	fast := PathFast
	slow := PathSlow
	hybrid := PathHybrid
	provider.SetTenantChannelConfig("tenant-a", "home_feed", RecommendConfig{PathMode: &fast})
	provider.SetScenarioConfig("commerce", RecommendConfig{PathMode: &slow})
	rt := NewRecommendationRuntime(WithConfigProvider(provider))

	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		Scenario: "commerce",
		Channel:  "home_feed",
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if resp.PathTaken != PathSlow {
		t.Fatalf("scenario config should override tenant/channel config, got %q", resp.PathTaken)
	}

	resp, err = rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		Scenario: "commerce",
		Channel:  "home_feed",
		User:     UserIdentity{UserID: "u1"},
		Config:   &RecommendConfig{PathMode: &hybrid},
		TopK:     2,
	})
	if err != nil {
		t.Fatalf("Recommend with request config returned error: %v", err)
	}
	if resp.PathTaken != PathHybrid {
		t.Fatalf("request config should override scenario config, got %q", resp.PathTaken)
	}
}

func TestRequestCanDisableStepsAndExplain(t *testing.T) {
	returnSteps := false
	returnExplain := false
	resp, err := NewRecommendationRuntime().Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
		Config: &RecommendConfig{Obs: ObservabilityConfig{
			ReturnSteps:   &returnSteps,
			ReturnExplain: &returnExplain,
		}},
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if len(resp.Steps) != 0 {
		t.Fatalf("steps should be omitted when request explicitly disables them: %+v", resp.Steps)
	}
	if len(resp.Explanations) != 0 {
		t.Fatalf("explanations should be omitted when request explicitly disables them: %+v", resp.Explanations)
	}
}

func TestIdentityValidationRejectsAmbiguousTenantAndMissingUser(t *testing.T) {
	rt := NewRecommendationRuntime()
	if _, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{TenantID: "tenant-b", UserID: "u1"},
	}); err == nil {
		t.Fatal("expected tenant mismatch to be rejected")
	}
	if _, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{},
	}); err == nil {
		t.Fatal("expected missing user identity to be rejected")
	}
}

func TestBlockingHookCanRejectRecommendation(t *testing.T) {
	hooks := NewHookRegistry()
	hooks.RegisterBlocking(testHook{
		name: "gate",
		fn: func(context.Context, BehaviorEvent) (HookResult, error) {
			return HookResult{Decision: HookReject, Reason: "tenant disabled"}, nil
		},
	})
	rt := NewRecommendationRuntime(WithEventBus(NewEventBus(hooks)))
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
	})
	if err == nil {
		t.Fatal("expected blocking hook rejection")
	}
	if resp.Fallback == nil || !resp.Fallback.Triggered {
		t.Fatalf("expected rejection response to include fallback report: %+v", resp)
	}
}

func TestAsyncHookFailureDoesNotBlockRecommendation(t *testing.T) {
	var calls atomic.Int64
	hooks := NewHookRegistry()
	hooks.RegisterAsync(testHook{
		name: "async-observer",
		fn: func(context.Context, BehaviorEvent) (HookResult, error) {
			calls.Add(1)
			return HookResult{}, errors.New("async failure")
		},
	})
	rt := NewRecommendationRuntime(WithEventBus(NewEventBus(hooks)))
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     1,
	})
	if err != nil {
		t.Fatalf("async hook failure must not block recommendation: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(resp.Items))
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("expected async hook to be invoked")
	}
	deadline = time.Now().Add(time.Second)
	for rt.Summary().HookFailures == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rt.Summary().HookFailures; got == 0 {
		t.Fatal("expected async hook failure to be recorded in observation summary")
	}
}

func TestCustomProvidersOverrideGraphBehavior(t *testing.T) {
	rt := NewRecommendationRuntime(
		WithRecallProvider(testRecallProvider{
			items: []RecommendItem{
				{ID: "custom-low", ArticleID: "custom-low", Score: 0.1, Source: "custom"},
				{ID: "custom-high", ArticleID: "custom-high", Score: 0.9, Source: "custom"},
			},
		}),
		WithRankProvider(testRankProvider{}),
	)
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
		PathMode: PathFast,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(resp.Items))
	}
	if resp.Items[0].ID != "custom-high" || resp.Items[1].ID != "custom-low" {
		t.Fatalf("custom provider ordering was not preserved: %+v", resp.Items)
	}
	if resp.Items[0].Features["rank_provider"] != "test" {
		t.Fatalf("custom rank provider features missing: %+v", resp.Items[0].Features)
	}
}

func TestRecommendationMetricsRecorded(t *testing.T) {
	tenantID := "tenant-metrics-" + randID()
	channel := "metric_channel"
	scenario := "metric_scenario"
	rt := NewRecommendationRuntime()
	_, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: tenantID,
		Scenario: scenario,
		Channel:  channel,
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
		PathMode: PathFast,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}
	if got := testutil.ToFloat64(metrics.RecoV2RequestsTotal.WithLabelValues(tenantID, channel, scenario, PathFast, "ok")); got != 1 {
		t.Fatalf("v2 request metric = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.RecoV2CallsTotal.WithLabelValues(tenantID, channel, scenario, PathFast, "tool")); got == 0 {
		t.Fatal("expected v2 tool call metric to be recorded")
	}
	if got := testutil.ToFloat64(metrics.RecoV2TraceStepsTotal.WithLabelValues(PathFast, "normalize_request", "function", "ok")); got == 0 {
		t.Fatal("expected v2 trace step metric to be recorded")
	}
}

func TestSkillRegistryListsDefaultCommercialSkills(t *testing.T) {
	rt := NewRecommendationRuntime()
	skills := rt.ListSkills()
	if len(skills) == 0 {
		t.Fatal("expected default v2 skills to be registered")
	}
	found := map[string]bool{}
	for _, skill := range skills {
		found[skill.Name] = true
		if skill.CostLevel == "" {
			t.Fatalf("skill %s missing cost level", skill.Name)
		}
		if len(skill.Paths) == 0 {
			t.Fatalf("skill %s missing path policy", skill.Name)
		}
	}
	for _, want := range []string{"profile.load", "recall.hybrid", "rank.traditional", "rerank.self", "quality.score", "explain.recommend", "event.report"} {
		if !found[want] {
			t.Fatalf("default skill registry missing %s: %+v", want, skills)
		}
	}
}

func TestSkillRegistryLoadsSkillDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "custom.high_value"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "legacy_tool_without_manifest"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `---
name: custom.high_value
category: recall
description: LLM assisted custom recall
tools:
  - custom_recall
inputs:
  - name: user_id
    type: string
    required: true
outputs:
  - name: candidates
    type: list
---

# custom.high_value
`
	if err := os.WriteFile(filepath.Join(root, "custom.high_value", "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := NewSkillRegistry()
	if err := registry.LoadDirectory(root); err != nil {
		t.Fatalf("LoadDirectory returned error: %v", err)
	}
	skill, ok := registry.Get("custom.high_value")
	if !ok {
		t.Fatal("expected custom.high_value to be loaded")
	}
	if skill.Category != "recall" {
		t.Fatalf("category = %q, want recall", skill.Category)
	}
	if skill.CostLevel != CostLevelHigh {
		t.Fatalf("cost level = %q, want high", skill.CostLevel)
	}
	if !hasPermission(skill.Permissions, PermissionLLM) {
		t.Fatalf("expected loaded LLM skill to require llm permission: %+v", skill.Permissions)
	}
	if _, ok := skill.InputSchema["user_id"]; !ok {
		t.Fatalf("input schema missing user_id: %+v", skill.InputSchema)
	}
	if tools, _ := skill.Extension["tools"].([]string); len(tools) != 1 || tools[0] != "custom_recall" {
		t.Fatalf("tools extension not parsed: %+v", skill.Extension)
	}
}

func TestTraceQueryRecordsRecommendationChain(t *testing.T) {
	rt := NewRecommendationRuntime()
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-trace",
		Channel:  "trace_channel",
		User:     UserIdentity{UserID: "u-trace"},
		TopK:     2,
		PathMode: PathHybrid,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}

	byTrace := rt.Trace(TraceQueryRequest{TraceID: resp.TraceID})
	if byTrace.Total != 1 {
		t.Fatalf("trace query total = %d, want 1: %+v", byTrace.Total, byTrace)
	}
	trace := byTrace.Traces[0]
	if trace.RequestID != resp.RequestID || trace.UserID != "u-trace" || trace.Path != PathHybrid {
		t.Fatalf("trace record not aligned with response: %+v", trace)
	}
	if len(trace.Steps) == 0 {
		t.Fatal("trace record must include graph steps")
	}
	if !traceHasSkill(trace, "recall.hybrid") || !traceHasSkill(trace, "rerank.self") {
		t.Fatalf("trace should include invoked commercial skills: %+v", trace.Skills)
	}

	bySkill := rt.Trace(TraceQueryRequest{Skill: "recall.hybrid", UserID: "u-trace"})
	if bySkill.Total != 1 {
		t.Fatalf("trace query by skill/user total = %d, want 1: %+v", bySkill.Total, bySkill)
	}
}

func TestToolPolicyRejectsDisabledSkill(t *testing.T) {
	policy := NewStaticToolPolicy()
	policy.DisableSkill("recall.hybrid")
	rt := NewRecommendationRuntime(WithToolPolicy(policy))
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
		PathMode: PathFast,
	})
	if err == nil {
		t.Fatal("expected disabled recall skill to reject request")
	}
	if resp.Fallback == nil || resp.Fallback.Source != "error" {
		t.Fatalf("expected policy rejection to return error fallback: %+v", resp)
	}
	if !strings.Contains(err.Error(), "recall.hybrid") {
		t.Fatalf("policy error should mention disabled skill, got %v", err)
	}
}

func TestToolPolicyRejectsHighCostSkillOnFastPath(t *testing.T) {
	registry := NewSkillRegistry()
	registry.skills["explain.recommend"] = SkillDefinition{
		Name:        "explain.recommend",
		Category:    "explain",
		CostLevel:   CostLevelHigh,
		Permissions: []string{PermissionLLM},
		Paths:       []string{PathFast, PathSlow, PathHybrid},
		Online:      true,
	}
	rt := NewRecommendationRuntime(WithSkillRegistry(registry), WithToolPolicy(NewStaticToolPolicy()))
	resp, err := rt.Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     2,
		PathMode: PathFast,
	})
	if err == nil {
		t.Fatal("expected high cost explain skill to be rejected on fast path")
	}
	if resp.Fallback == nil || resp.Fallback.Source != "error" {
		t.Fatalf("expected policy rejection to return error fallback: %+v", resp)
	}
	if !strings.Contains(err.Error(), "high cost skill explain.recommend") {
		t.Fatalf("policy error should mention high cost explain skill, got %v", err)
	}
}

func TestStreamRecommendEmitsRealtimeStepEvents(t *testing.T) {
	rt := NewRecommendationRuntime()
	events, err := rt.StreamRecommend(context.Background(), RecommendRequest{
		TenantID: "tenant-a",
		User:     UserIdentity{UserID: "u1"},
		TopK:     1,
	})
	if err != nil {
		t.Fatalf("StreamRecommend returned error: %v", err)
	}
	var started, stepStarted, stepFinished, finished bool
	for event := range events {
		switch event.Type {
		case "run_started":
			started = true
		case "step_started":
			stepStarted = true
			if event.Step == nil || event.Step.Name == "" {
				t.Fatalf("step_started event missing step payload: %+v", event)
			}
		case "step_finished":
			stepFinished = true
			if event.Step == nil || event.Step.Status == "" {
				t.Fatalf("step_finished event missing step payload: %+v", event)
			}
		case "run_finished":
			finished = true
			if event.Response == nil || event.Response.TraceID == "" {
				t.Fatalf("run_finished event missing response payload: %+v", event)
			}
		}
	}
	if !started || !stepStarted || !stepFinished || !finished {
		t.Fatalf("stream events incomplete: started=%v stepStarted=%v stepFinished=%v finished=%v", started, stepStarted, stepFinished, finished)
	}
}

type testHook struct {
	name string
	fn   func(context.Context, BehaviorEvent) (HookResult, error)
}

func (h testHook) Name() string { return h.name }

func (h testHook) OnEvent(ctx context.Context, event BehaviorEvent) (HookResult, error) {
	return h.fn(ctx, event)
}

type testRecallProvider struct {
	items []RecommendItem
}

func (p testRecallProvider) Recall(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	items := cloneItems(p.items)
	cost := rctx.Cost
	cost.ToolCalls++
	return items, cost, nil
}

type testRankProvider struct{}

func (testRankProvider) Rank(_ context.Context, rctx RecommendationContext) ([]RecommendItem, CostReport, error) {
	items := cloneItems(rctx.Items)
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})
	for i := range items {
		items[i].Rank = i + 1
		items[i].Features = mergeAnyMap(items[i].Features, map[string]any{"rank_provider": "test"})
	}
	cost := rctx.Cost
	cost.ToolCalls++
	return items, cost, nil
}
