// Package agent factory_test.go — Factory 与共享测试 stub。
//
// 该文件测试 internal/agent/factory.go 的 Factory 实现，
// 并定义共享测试 stub（stubFALLMClient/stubFAToolExecutor/stubFAMemoryStore/stubFAAgent），
// 供 intent_test.go / recall_planner_test.go 复用。
//
// 覆盖：默认构造函数绑定/NewIntentAgent/NewRecallPlannerAgent/
// NewOrchestratorAgent 返回 nil/未实现 Agent panic/Configure 替换/全量 Option 注入。
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// stubFALLMClient 测试用 LLMClient stub，可控制结构化输出/补全/logprobs 返回值。
type stubFALLMClient struct {
	structuredResp json.RawMessage
	structuredErr  error
	completeResp   string
	completeErr    error
	logprobsResp   string
	logprobsErr    error
}

// Complete 返回预设的补全响应。
func (s *stubFALLMClient) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	return s.completeResp, s.completeErr
}

// CompleteWithStructuredOutput 返回预设的结构化 JSON 响应。
func (s *stubFALLMClient) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return s.structuredResp, s.structuredErr
}

// CompleteWithLogprobs 返回预设的 logprobs 响应。
func (s *stubFALLMClient) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return s.logprobsResp, nil, s.logprobsErr
}

// stubFAToolExecutor 测试用 ToolExecutor stub。
type stubFAToolExecutor struct{}

// Invoke 返回 nil。
func (s *stubFAToolExecutor) Invoke(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

// List 返回空列表。
func (s *stubFAToolExecutor) List(_ context.Context) ([]string, error) {
	return nil, nil
}

// stubFAMemoryStore 测试用 MemoryStore stub。
type stubFAMemoryStore struct{}

func (s *stubFAMemoryStore) Load(_ context.Context, _ string) (string, error) { return "", nil }
func (s *stubFAMemoryStore) Save(_ context.Context, _, _ string) error        { return nil }
func (s *stubFAMemoryStore) Delete(_ context.Context, _ string) error         { return nil }

// stubFAAgent 测试用 domain.Agent stub。
type stubFAAgent struct {
	name string
}

func (a *stubFAAgent) Run(_ context.Context, _ domain.AgentInput) (domain.AgentOutput, error) {
	return domain.AgentOutput{}, nil
}

func (a *stubFAAgent) Name() string {
	return a.name
}

// 编译期断言：测试 stub 实现对应 interface。
var _ LLMClient = (*stubFALLMClient)(nil)
var _ ToolExecutor = (*stubFAToolExecutor)(nil)
var _ MemoryStore = (*stubFAMemoryStore)(nil)
var _ domain.Agent = (*stubFAAgent)(nil)

// TestNewFactory_DefaultConstructors 验证 NewFactory 注入依赖并默认绑定 Intent/RecallPlanner 构造函数。
func TestNewFactory_DefaultConstructors(t *testing.T) {
	llm := &stubFALLMClient{}
	tools := &stubFAToolExecutor{}
	mem := &stubFAMemoryStore{}
	f := NewFactory(llm, tools, mem)

	if f.llm != llm {
		t.Errorf("llm 未正确注入")
	}
	if f.tools != tools {
		t.Errorf("tools 未正确注入")
	}
	if f.memory != mem {
		t.Errorf("memory 未正确注入")
	}
	if f.intentCtor == nil {
		t.Errorf("intentCtor 应默认非 nil")
	}
	if f.recallPlannerCtor == nil {
		t.Errorf("recallPlannerCtor 应默认非 nil")
	}
	// 其他 9 个 Agent 构造函数应为 nil（未实现）。
	if f.graphCtor != nil || f.rerankCtor != nil || f.qualityCtor != nil ||
		f.explainCtor != nil || f.searchCtor != nil || f.profileCtor != nil ||
		f.channelCtor != nil || f.feedbackCtor != nil || f.evalCtor != nil {
		t.Errorf("其他 9 个 Agent 构造函数应初始为 nil")
	}
}

// TestFactory_NewIntentAgent 验证 Factory.NewIntentAgent 返回 IntentAgent。
func TestFactory_NewIntentAgent(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	a := f.NewIntentAgent(domain.AgentOptions{})
	if a == nil {
		t.Fatalf("NewIntentAgent 返回 nil")
	}
	if a.Name() != "intent" {
		t.Errorf("Name = %q, 期望 intent", a.Name())
	}
}

// TestFactory_NewRecallPlannerAgent 验证 Factory.NewRecallPlannerAgent 返回 RecallPlannerAgent。
func TestFactory_NewRecallPlannerAgent(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	a := f.NewRecallPlannerAgent(domain.AgentOptions{})
	if a == nil {
		t.Fatalf("NewRecallPlannerAgent 返回 nil")
	}
	if a.Name() != "recall_planner" {
		t.Errorf("Name = %q, 期望 recall_planner", a.Name())
	}
}

// TestFactory_NewOrchestratorAgent_ReturnsNil 验证 NewOrchestratorAgent 返回 nil（Phase 12 实现）。
func TestFactory_NewOrchestratorAgent_ReturnsNil(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	a := f.NewOrchestratorAgent(domain.AgentOptions{})
	if a != nil {
		t.Errorf("NewOrchestratorAgent 应返回 nil（Phase 12 实现）, 实际 %T", a)
	}
}

// TestFactory_UnimplementedAgent_Panics 验证 9 个未实现 Agent 调用时 panic。
func TestFactory_UnimplementedAgent_Panics(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	tests := []struct {
		name string
		fn   func(domain.AgentOptions) domain.Agent
	}{
		{"GraphAgent", f.NewGraphAgent},
		{"RerankAgent", f.NewRerankAgent},
		{"QualityAgent", f.NewQualityAgent},
		{"ExplainAgent", f.NewExplainAgent},
		{"SearchAgent", f.NewSearchAgent},
		{"ProfileAgent", f.NewProfileAgent},
		{"ChannelAgent", f.NewChannelAgent},
		{"FeedbackAgent", f.NewFeedbackAgent},
		{"EvalAgent", f.NewEvalAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s 未注入构造函数时应 panic", tt.name)
				}
			}()
			tt.fn(domain.AgentOptions{})
		})
	}
}

// TestFactory_Configure_ReplacesConstructor 验证 Configure 替换构造函数。
func TestFactory_Configure_ReplacesConstructor(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	custom := &stubFAAgent{name: "custom-intent"}
	f.Configure(WithIntentConstructor(func(_ LLMClient, _ ToolExecutor, _ domain.AgentOptions) domain.Agent {
		return custom
	}))
	a := f.NewIntentAgent(domain.AgentOptions{})
	if a != custom {
		t.Errorf("Configure 未替换 IntentAgent 构造函数")
	}
}

// TestFactory_Configure_AllOptions 验证 12 个 WithXxxConstructor 全部可注入。
func TestFactory_Configure_AllOptions(t *testing.T) {
	f := NewFactory(&stubFALLMClient{}, &stubFAToolExecutor{}, &stubFAMemoryStore{})
	ctor := func(_ LLMClient, _ ToolExecutor, _ domain.AgentOptions) domain.Agent {
		return &stubFAAgent{name: "stub"}
	}
	f.Configure(
		WithIntentConstructor(ctor),
		WithRecallPlannerConstructor(ctor),
		WithGraphConstructor(ctor),
		WithRerankConstructor(ctor),
		WithQualityConstructor(ctor),
		WithExplainConstructor(ctor),
		WithSearchConstructor(ctor),
		WithProfileConstructor(ctor),
		WithChannelConstructor(ctor),
		WithFeedbackConstructor(ctor),
		WithEvalConstructor(ctor),
		WithOrchestratorConstructor(ctor),
	)
	// 验证所有 ctor 已注入（orchestratorCtor 也注入但 NewOrchestratorAgent 仍返回 nil）。
	if f.intentCtor == nil || f.recallPlannerCtor == nil || f.graphCtor == nil ||
		f.rerankCtor == nil || f.qualityCtor == nil || f.explainCtor == nil ||
		f.searchCtor == nil || f.profileCtor == nil || f.channelCtor == nil ||
		f.feedbackCtor == nil || f.evalCtor == nil || f.orchestratorCtor == nil {
		t.Errorf("部分构造函数未注入")
	}
	// 注入后 GraphAgent 不再 panic。
	a := f.NewGraphAgent(domain.AgentOptions{})
	if a == nil {
		t.Errorf("注入构造函数后 NewGraphAgent 应返回非 nil")
	}
}

// TestFactory_ImplementsAgentFactory 编译期验证 Factory 实现 domain.AgentFactory。
func TestFactory_ImplementsAgentFactory(t *testing.T) {
	var _ domain.AgentFactory = (*Factory)(nil)
}
