// Package agent channel_test.go — ChannelAgent 单测（Task 11.9）。
//
// 覆盖：
//   - Name/Run 基本流程
//   - LLM 路由成功（结构化输出 ChannelRoute）
//   - LLM 失败时规则兜底（关键词匹配）
//   - LLM 返回未知频道时规则兜底
//   - tools.Invoke 被调用
//   - tools 为 nil 时跳过
//   - 查询从 State["query"] 读取（Message 为空时）
//   - fallbackRoute 关键词匹配（旅游/游戏/美食/科技/default）
//
// 使用 stdlib 手写 stub，不依赖 testify。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// stub 实现
// ============================================================================

// stubChannelLLM 测试用 LLMClient，可配置结构化输出响应。
type stubChannelLLM struct {
	mu              sync.Mutex
	structuredCalls int
	resp            json.RawMessage
	err             error
	lastPrompt      string
}

func (s *stubChannelLLM) Complete(_ context.Context, prompt string, _ LLMOptions) (string, error) {
	return "", nil
}

func (s *stubChannelLLM) CompleteWithStructuredOutput(_ context.Context, prompt string, _ map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.structuredCalls++
	s.lastPrompt = prompt
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func (s *stubChannelLLM) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, nil
}

func (s *stubChannelLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.structuredCalls
}

// stubTool 测试用 ToolExecutor，记录 Invoke 调用（通用，跨测试文件复用）。
type stubTool struct {
	mu        sync.Mutex
	invokes   []invokeRecord
	listErr   error
	invokeErr error
	resp      json.RawMessage
}

type invokeRecord struct {
	name  string
	input json.RawMessage
}

func (s *stubTool) Invoke(_ context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invokes = append(s.invokes, invokeRecord{name: name, input: input})
	if s.invokeErr != nil {
		return nil, s.invokeErr
	}
	if len(s.resp) > 0 {
		return s.resp, nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (s *stubTool) List(_ context.Context) ([]string, error) {
	return nil, s.listErr
}

func (s *stubTool) invokeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.invokes)
}

func (s *stubTool) lastInvoke() (string, json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.invokes) == 0 {
		return "", nil, false
	}
	last := s.invokes[len(s.invokes)-1]
	return last.name, last.input, true
}

func (s *stubTool) invokeNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, len(s.invokes))
	for i, r := range s.invokes {
		names[i] = r.name
	}
	return names
}

// ============================================================================
// 测试用例
// ============================================================================

// TestChannelAgent_Name 验证 Name 返回 "channel"。
func TestChannelAgent_Name(t *testing.T) {
	a := NewChannelAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "channel" {
		t.Errorf("Name = %q, 期望 channel", a.Name())
	}
}

// TestChannelAgent_Run_LLMRoute 验证 LLM 路由成功。
func TestChannelAgent_Run_LLMRoute(t *testing.T) {
	llm := &stubChannelLLM{
		resp: json.RawMessage(`{"channel":"travel","ip_name":"旅游 IP","exclusive":true,"reason":"user query about travel"}`),
	}
	tool := &stubTool{}
	a := NewChannelAgent(llm, tool, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "我想去旅游",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	route, ok := out.Result.(ChannelRoute)
	if !ok {
		t.Fatalf("Result 类型不是 ChannelRoute: %T", out.Result)
	}
	if route.Channel != "travel" {
		t.Errorf("Channel = %q, 期望 travel", route.Channel)
	}
	if route.IPName != "旅游 IP" {
		t.Errorf("IPName = %q, 期望 旅游 IP", route.IPName)
	}
	if !route.Exclusive {
		t.Errorf("Exclusive 应为 true")
	}

	// State 校验。
	if cr, ok := out.State["channel_route"].(ChannelRoute); !ok || cr.Channel != "travel" {
		t.Errorf("State[channel_route] = %v, 期望 ChannelRoute{travel}", out.State["channel_route"])
	}
	if ch, ok := out.State["channel"].(string); !ok || ch != "travel" {
		t.Errorf("State[channel] = %v, 期望 travel", out.State["channel"])
	}

	// Trace 校验。
	if len(out.Trace) != 1 || out.Trace[0] != "channel.route" {
		t.Errorf("trace = %v, 期望 [channel.route]", out.Trace)
	}

	// tools.Invoke 应被调用 1 次（channel.route）。
	if tool.invokeCount() != 1 {
		t.Errorf("期望 tools.Invoke 调用 1 次, 实际 %d 次", tool.invokeCount())
	}
	if name, _, ok := tool.lastInvoke(); !ok || name != "channel.route" {
		t.Errorf("期望 Invoke channel.route, 实际 %s", name)
	}
}

// TestChannelAgent_Run_LLMFail_Fallback 验证 LLM 失败时规则兜底。
func TestChannelAgent_Run_LLMFail_Fallback(t *testing.T) {
	llm := &stubChannelLLM{err: errors.New("llm down")}
	tool := &stubTool{}
	a := NewChannelAgent(llm, tool, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "最近有什么好玩的游戏推荐",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	route := out.Result.(ChannelRoute)
	if route.Channel != "game" {
		t.Errorf("LLM 失败时应兜底到 game, 实际 %s", route.Channel)
	}
	if route.IPName != "游戏 IP" {
		t.Errorf("IPName = %q, 期望 游戏 IP", route.IPName)
	}
	if llm.callCount() != 1 {
		t.Errorf("期望 LLM 调用 1 次, 实际 %d 次", llm.callCount())
	}
}

// TestChannelAgent_Run_LLMUnknownChannel_Fallback 验证 LLM 返回未知频道时兜底。
func TestChannelAgent_Run_LLMUnknownChannel_Fallback(t *testing.T) {
	llm := &stubChannelLLM{
		resp: json.RawMessage(`{"channel":"unknown_channel","ip_name":"","exclusive":false,"reason":""}`),
	}
	a := NewChannelAgent(llm, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "美食推荐",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	route := out.Result.(ChannelRoute)
	// 未知频道应触发兜底，"美食"匹配 food 频道。
	if route.Channel != "food" {
		t.Errorf("未知频道应兜底, 期望 food, 实际 %s", route.Channel)
	}
}

// TestChannelAgent_Run_NilTools 验证 tools 为 nil 时跳过 Invoke（不 panic）。
func TestChannelAgent_Run_NilTools(t *testing.T) {
	llm := &stubChannelLLM{
		resp: json.RawMessage(`{"channel":"tech","ip_name":"科技 IP","exclusive":false,"reason":"tech query"}`),
	}
	a := NewChannelAgent(llm, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "AI 编程",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.Result.(ChannelRoute).Channel != "tech" {
		t.Errorf("Channel = %s, 期望 tech", out.Result.(ChannelRoute).Channel)
	}
}

// TestChannelAgent_Run_NilLLM_Fallback 验证 LLM 为 nil 时规则兜底。
func TestChannelAgent_Run_NilLLM_Fallback(t *testing.T) {
	a := NewChannelAgent(nil, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "旅游攻略",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	route := out.Result.(ChannelRoute)
	if route.Channel != "travel" {
		t.Errorf("nil LLM 应兜底, 期望 travel, 实际 %s", route.Channel)
	}
}

// TestChannelAgent_Run_QueryFromState 验证 Message 为空时从 State["query"] 读取。
func TestChannelAgent_Run_QueryFromState(t *testing.T) {
	a := NewChannelAgent(nil, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "", // 空消息
		State: map[string]any{
			"query": "游戏机推荐",
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	route := out.Result.(ChannelRoute)
	if route.Channel != "game" {
		t.Errorf("从 State[query] 读取后应兜底到 game, 实际 %s", route.Channel)
	}
}

// TestChannelAgent_Run_DefaultFallback 验证无关键词匹配时兜底到 default。
func TestChannelAgent_Run_DefaultFallback(t *testing.T) {
	a := NewChannelAgent(nil, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID:  "u1",
		Message: "今天天气怎么样",
		State:   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	route := out.Result.(ChannelRoute)
	if route.Channel != "default" {
		t.Errorf("无关键词匹配应兜底到 default, 实际 %s", route.Channel)
	}
	if route.IPName != "默认 IP" {
		t.Errorf("IPName = %q, 期望 默认 IP", route.IPName)
	}
}

// TestFallbackRoute_Keywords 验证 fallbackRoute 关键词匹配覆盖各频道。
func TestFallbackRoute_Keywords(t *testing.T) {
	cases := []struct {
		query      string
		wantChan   string
		wantIPName string
	}{
		{"我想去旅游", "travel", "旅游 IP"},
		{"旅行攻略", "travel", "旅游 IP"},
		{"酒店预订", "travel", "旅游 IP"},
		{"玩游戏", "game", "游戏 IP"},
		{"电竞比赛", "game", "游戏 IP"},
		{"美食推荐", "food", "美食 IP"},
		{"菜谱大全", "food", "美食 IP"},
		{"AI 编程", "tech", "科技 IP"},
		{"数码评测", "tech", "科技 IP"},
		{"今天天气", "default", "默认 IP"},
		{"", "default", "默认 IP"},
	}
	for _, c := range cases {
		route := fallbackRoute(c.query)
		if route.Channel != c.wantChan {
			t.Errorf("fallbackRoute(%q).Channel = %s, 期望 %s", c.query, route.Channel, c.wantChan)
		}
		if route.IPName != c.wantIPName {
			t.Errorf("fallbackRoute(%q).IPName = %s, 期望 %s", c.query, route.IPName, c.wantIPName)
		}
	}
}

// TestChannelAgent_Run_StatePreserved 验证 Run 保留输入 State 中的其他键。
func TestChannelAgent_Run_StatePreserved(t *testing.T) {
	llm := &stubChannelLLM{
		resp: json.RawMessage(`{"channel":"travel","ip_name":"旅游 IP","exclusive":false,"reason":""}`),
	}
	a := NewChannelAgent(llm, nil, domain.AgentOptions{})

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"existing_key": "existing_value",
			"count":        42,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.State["existing_key"] != "existing_value" {
		t.Errorf("输入 State 应被保留, existing_key = %v", out.State["existing_key"])
	}
	if out.State["count"] != 42 {
		t.Errorf("输入 State 应被保留, count = %v", out.State["count"])
	}
}

// TestChannelAgent_ImplementsDomainAgent 验证 ChannelAgent 实现 domain.Agent 接口。
func TestChannelAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*ChannelAgent)(nil)
	var _ domain.Agent = NewChannelAgent(nil, nil, domain.AgentOptions{})
}

// TestDefaultChannelRegistry 验证内置频道注册表包含 5 个频道。
func TestDefaultChannelRegistry(t *testing.T) {
	expected := []string{"travel", "game", "food", "tech", "default"}
	for _, name := range expected {
		cfg, ok := defaultChannelRegistry[name]
		if !ok {
			t.Errorf("频道注册表缺少 %s", name)
			continue
		}
		if cfg.Name != name {
			t.Errorf("频道 %s 的 Name = %s, 不一致", name, cfg.Name)
		}
		if cfg.IPName == "" {
			t.Errorf("频道 %s 的 IPName 为空", name)
		}
	}
}
