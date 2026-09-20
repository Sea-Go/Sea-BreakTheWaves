// Package agent profile_test.go — ProfileAgent 单测（Task 11.8）。
//
// 覆盖：
//   - Name/Run 基本流程
//   - 异步消费事件更新 memory（用 Close 等待）
//   - Summary 触发（events > 50 时 LLM 被调用 + session_summary 写入）
//   - 无事件时 trace 仅含 profile.consume
//   - memory 为 nil 时不 panic
//   - 多次 Close 安全
//
// 使用 stdlib 手写 stub，不依赖 testify。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sea/internal/domain"
)

// errLLMFail 测试用 LLM 错误。
var errLLMFail = errors.New("llm fail")

// ============================================================================
// stub 实现
// ============================================================================

// stubMemoryStore 测试用 MemoryStore，记录所有 Save 调用。
type stubMemoryStore struct {
	mu      sync.Mutex
	saved   map[string]string
	loads   map[string]string
	deletes map[string]bool
	saveErr error
}

func newStubMemoryStore() *stubMemoryStore {
	return &stubMemoryStore{
		saved:   make(map[string]string),
		loads:   make(map[string]string),
		deletes: make(map[string]bool),
	}
}

func (s *stubMemoryStore) Load(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads[key], nil
}

func (s *stubMemoryStore) Save(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved[key] = value
	return nil
}

func (s *stubMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes[key] = true
	return nil
}

func (s *stubMemoryStore) get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.saved[key]
	return v, ok
}

func (s *stubMemoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

// stubProfileLLM 测试用 LLMClient，记录 CompleteWithStructuredOutput 调用。
type stubProfileLLM struct {
	mu              sync.Mutex
	structuredCalls int
	completeCalls   int
	logprobsCalls   int
	resp            string
	err             error
}

func (s *stubProfileLLM) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	return s.resp, s.err
}

func (s *stubProfileLLM) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.structuredCalls++
	if s.err != nil {
		return nil, s.err
	}
	return json.RawMessage(`{"summary":"test summary","topics":["travel"]}`), nil
}

func (s *stubProfileLLM) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logprobsCalls++
	return s.resp, nil, s.err
}

func (s *stubProfileLLM) structuredCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.structuredCalls
}

// ============================================================================
// 测试用例
// ============================================================================

// makeEvents 构造 n 条 BehaviorEvent。
func makeEvents(n int, userID string) []domain.BehaviorEvent {
	events := make([]domain.BehaviorEvent, n)
	for i := range events {
		events[i] = domain.BehaviorEvent{
			EventID:   "evt-" + userID + "-" + itoa(i),
			EventType: domain.EventLike,
			UserID:    userID,
			ArticleID: "art-" + itoa(i),
			Channel:   "travel",
			Timestamp: time.Now(),
			Payload: map[string]any{
				"tags":  []any{"travel", "vacation"},
				"topic": "旅行",
			},
		}
	}
	return events
}

// itoa 简易 int → string（避免引入 strconv 仅作下标用）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 8)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// TestProfileAgent_Name 验证 Name 返回 "profile"。
func TestProfileAgent_Name(t *testing.T) {
	a := NewProfileAgent(nil, nil, domain.AgentOptions{})
	if a.Name() != "profile" {
		t.Errorf("Name = %q, 期望 profile", a.Name())
	}
}

// TestProfileAgent_Run_NoEvents 验证无事件时 trace 仅含 profile.consume。
func TestProfileAgent_Run_NoEvents(t *testing.T) {
	llm := &stubProfileLLM{}
	a := NewProfileAgent(llm, nil, domain.AgentOptions{}).(*ProfileAgent)
	defer a.Close()

	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State:  map[string]any{},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(out.Trace) != 1 || out.Trace[0] != "profile.consume" {
		t.Errorf("trace = %v, 期望 [profile.consume]", out.Trace)
	}
	if out.State["profile_updated"] != true {
		t.Errorf("profile_updated 应为 true")
	}
	if out.State["profile_async"] != true {
		t.Errorf("profile_async 应为 true")
	}
	if out.Result != "async scheduled" {
		t.Errorf("Result = %v, 期望 async scheduled", out.Result)
	}
}

// TestProfileAgent_Run_AsyncUpdate 验证异步消费事件更新 memory。
// 用 Close() 等待异步完成，避免 race condition。
func TestProfileAgent_Run_AsyncUpdate(t *testing.T) {
	mem := newStubMemoryStore()
	llm := &stubProfileLLM{}
	a := NewProfileAgent(llm, nil, domain.AgentOptions{}).(*ProfileAgent).WithMemoryStore(mem)

	events := makeEvents(5, "u1")
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u1",
		State: map[string]any{
			"events": events,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(out.Trace) != 2 || out.Trace[0] != "profile.consume" || out.Trace[1] != "profile.summary" {
		t.Errorf("trace = %v, 期望 [profile.consume profile.summary]", out.Trace)
	}

	// 等待异步任务完成。
	a.Close()

	// 验证 topics 写入（每事件 like + channel + tags 均触发 topics 写入）。
	topicsVal, ok := mem.get("user:u1:topics")
	if !ok {
		t.Errorf("期望 memory.Save 写入 user:u1:topics, 实际未写入（saved=%v）", mem.saved)
	} else if !strings.Contains(topicsVal, "travel") {
		t.Errorf("topics = %s, 期望包含 travel", topicsVal)
	}

	// 验证 short_term 写入。
	if _, ok := mem.get("user:u1:short_term"); !ok {
		t.Errorf("期望 memory.Save 写入 user:u1:short_term, 实际未写入（saved=%v）", mem.saved)
	}

	// 验证 summary 写入（LLM 被调用）。
	if llm.structuredCallCount() != 1 {
		t.Errorf("期望 LLM CompleteWithStructuredOutput 调用 1 次, 实际 %d 次", llm.structuredCallCount())
	}
	if _, ok := mem.get("user:u1:summary"); !ok {
		t.Errorf("期望 memory.Save 写入 user:u1:summary, 实际未写入")
	}
}

// TestProfileAgent_Run_SummaryCompression 验证 events > 50 触发 Session Summary 压缩。
// 验证 session_summary key 被写入。
func TestProfileAgent_Run_SummaryCompression(t *testing.T) {
	mem := newStubMemoryStore()
	llm := &stubProfileLLM{}
	a := NewProfileAgent(llm, nil, domain.AgentOptions{}).(*ProfileAgent).WithMemoryStore(mem)

	events := makeEvents(60, "u2") // > 50 触发压缩
	_, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u2",
		State: map[string]any{
			"events": events,
		},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	a.Close()

	// session_summary 应被写入（compress=true 时）。
	if _, ok := mem.get("user:u2:session_summary"); !ok {
		t.Errorf("events>50 时应写入 user:u2:session_summary, 实际未写入（saved=%v）", mem.saved)
	}
	if _, ok := mem.get("user:u2:summary"); !ok {
		t.Errorf("summary 应写入, 实际未写入")
	}
}

// TestProfileAgent_Run_LLMFailNoBlock 验证 LLM 失败不阻断主流程。
func TestProfileAgent_Run_LLMFailNoBlock(t *testing.T) {
	mem := newStubMemoryStore()
	llm := &stubProfileLLM{err: errLLMFail}
	a := NewProfileAgent(llm, nil, domain.AgentOptions{}).(*ProfileAgent).WithMemoryStore(mem)

	events := makeEvents(3, "u3")
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u3",
		State:  map[string]any{"events": events},
	})
	if err != nil {
		t.Fatalf("LLM 失败不应阻断 Run, 实际: %v", err)
	}
	if out.Result != "async scheduled" {
		t.Errorf("Result = %v, 期望 async scheduled", out.Result)
	}

	a.Close()

	// topics/short_term 仍应被写入（consumer 不依赖 LLM）。
	if _, ok := mem.get("user:u3:topics"); !ok {
		t.Errorf("LLM 失败时 topics 仍应写入, 实际未写入")
	}
	if _, ok := mem.get("user:u3:short_term"); !ok {
		t.Errorf("LLM 失败时 short_term 仍应写入, 实际未写入")
	}
	// summary 不应被写入（LLM 失败）。
	if _, ok := mem.get("user:u3:summary"); ok {
		t.Errorf("LLM 失败时 summary 不应写入")
	}
}

// TestProfileAgent_Run_NilMemory 验证 memory 为 nil 时不 panic。
func TestProfileAgent_Run_NilMemory(t *testing.T) {
	llm := &stubProfileLLM{}
	a := NewProfileAgent(llm, nil, domain.AgentOptions{}).(*ProfileAgent)

	events := makeEvents(2, "u4")
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u4",
		State:  map[string]any{"events": events},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if out.State["profile_updated"] != true {
		t.Errorf("profile_updated 应为 true")
	}

	a.Close()
}

// TestProfileAgent_Run_NilLLM 验证 llm 为 nil 时不 panic（Summary 跳过）。
func TestProfileAgent_Run_NilLLM(t *testing.T) {
	mem := newStubMemoryStore()
	a := NewProfileAgent(nil, nil, domain.AgentOptions{}).(*ProfileAgent).WithMemoryStore(mem)

	events := makeEvents(2, "u5")
	out, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u5",
		State:  map[string]any{"events": events},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if len(out.Trace) != 2 {
		t.Errorf("trace 长度 = %d, 期望 2", len(out.Trace))
	}

	a.Close()

	// topics/short_term 仍应写入（consumer 不依赖 LLM）。
	if _, ok := mem.get("user:u5:short_term"); !ok {
		t.Errorf("nil LLM 时 short_term 仍应写入")
	}
}

// TestProfileAgent_Close_Idempotent 验证多次 Close 安全。
func TestProfileAgent_Close_Idempotent(t *testing.T) {
	a := NewProfileAgent(nil, nil, domain.AgentOptions{}).(*ProfileAgent)
	a.Close()
	a.Close() // 不应 panic
	a.Close()
}

// TestProfileAgent_Run_EventsFromAnySlice 验证从 []any 形式读取 events。
func TestProfileAgent_Run_EventsFromAnySlice(t *testing.T) {
	mem := newStubMemoryStore()
	a := NewProfileAgent(nil, nil, domain.AgentOptions{}).(*ProfileAgent).WithMemoryStore(mem)

	// 构造 []any 形式的事件切片。
	eventsTyped := makeEvents(3, "u6")
	eventsAny := make([]any, len(eventsTyped))
	for i, e := range eventsTyped {
		eventsAny[i] = e
	}

	_, err := a.Run(context.Background(), domain.AgentInput{
		UserID: "u6",
		State:  map[string]any{"events": eventsAny},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}

	a.Close()

	if _, ok := mem.get("user:u6:short_term"); !ok {
		t.Errorf("从 []any 读取 events 后应写入 short_term, 实际未写入")
	}
}

// TestProfileAgent_ImplementsDomainAgent 验证 ProfileAgent 实现 domain.Agent 接口。
func TestProfileAgent_ImplementsDomainAgent(t *testing.T) {
	var _ domain.Agent = (*ProfileAgent)(nil)
	var _ domain.Agent = NewProfileAgent(nil, nil, domain.AgentOptions{})
}
