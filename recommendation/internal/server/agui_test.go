// Package server agui_test.go — AGUIServer 单元测试（Task 13.9）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - Stream 成功事件序列（run_started→step_started→step_finished→run_finished）
//   - Stream 失败 error 事件
//   - Stream ctx 取消
//   - StreamHandler SSE 输出格式（headers + data: {json}\n\n）
//   - StreamHandler 错误请求（非 JSON body）
//   - nil orchestrator 错误处理
//   - NoopAGUIEmitter 默认行为
//
// stub 类型用 AGUI 后缀避免与 graderoute_test.go 的 stub 冲突。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sea/internal/domain"
)

// stubOrchestratorAGUI 测试用 Orchestrator stub（与 graderoute_test.go 的 stubOrchestratorGrayscale 区分命名）。
type stubOrchestratorAGUI struct {
	mu      sync.Mutex
	resp    domain.RecommendResponse
	err     error
	calls   int
	delay   time.Duration
	lastReq domain.RecommendRequest
}

func (s *stubOrchestratorAGUI) Recommend(ctx context.Context, req domain.RecommendRequest) (domain.RecommendResponse, error) {
	s.mu.Lock()
	s.calls++
	s.lastReq = req
	s.mu.Unlock()
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return domain.RecommendResponse{}, ctx.Err()
		}
	}
	return s.resp, s.err
}

// captureAGUIEmitter 测试用 AGUIEventEmitter（与 graderoute_test.go 的 captureEmitterGrayscale 区分命名）。
type captureAGUIEmitter struct {
	mu      sync.Mutex
	events  []AGUIEvent
	emitErr error
}

func (c *captureAGUIEmitter) Emit(_ context.Context, event AGUIEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return c.emitErr
}

// makeAGUICandidates 构造 n 个测试候选（与 graderoute_test.go 的 makeGrayscaleCandidates 区分命名）。
func makeAGUICandidates(n int) []domain.Candidate {
	cs := make([]domain.Candidate, n)
	for i := 0; i < n; i++ {
		cs[i] = domain.Candidate{
			ArticleID: fmt.Sprintf("a%d", i+1),
			Score:     float64(n-i) / float64(n),
		}
	}
	return cs
}

// drainEvents 从 channel 读取所有事件直到关闭。
func drainEvents(ch <-chan AGUIEvent) []AGUIEvent {
	var events []AGUIEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

// TestAGUIServer_StreamSuccess 验证成功事件序列。
func TestAGUIServer_StreamSuccess(t *testing.T) {
	orch := &stubOrchestratorAGUI{
		resp: domain.RecommendResponse{
			PathTaken:  "fast",
			Candidates: makeAGUICandidates(3),
			Cost: domain.CostReport{
				TokensIn:       100,
				TokensOut:      50,
				CachedTokens:   20,
				LLMCalls:       1,
				EstimatedCost:  0.01,
			},
		},
	}
	emitter := &captureAGUIEmitter{}
	srv := NewAGUIServer(orch, emitter)

	ch := make(chan AGUIEvent, 128)
	err := srv.Stream(context.Background(), domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "u1"},
		Channel:  "home",
		PathMode: "auto",
		TopK:     10,
	}, ch)
	close(ch)

	if err != nil {
		t.Fatalf("Stream 错误: %v", err)
	}
	if orch.calls != 1 {
		t.Errorf("orchestrator 调用次数 = %d, 期望 1", orch.calls)
	}

	events := drainEvents(ch)
	// 验证事件序列：run_started → step_started → step_finished → run_finished。
	if len(events) != 4 {
		t.Fatalf("事件数 = %d, 期望 4", len(events))
	}
	if events[0].Type != AGUIEventRunStarted {
		t.Errorf("events[0].Type = %q, 期望 %q", events[0].Type, AGUIEventRunStarted)
	}
	if events[1].Type != AGUIEventStepStarted {
		t.Errorf("events[1].Type = %q, 期望 %q", events[1].Type, AGUIEventStepStarted)
	}
	if events[1].Step != AGUIStepRoute {
		t.Errorf("events[1].Step = %q, 期望 %q", events[1].Step, AGUIStepRoute)
	}
	if events[2].Type != AGUIEventStepFinished {
		t.Errorf("events[2].Type = %q, 期望 %q", events[2].Type, AGUIEventStepFinished)
	}
	if events[3].Type != AGUIEventRunFinished {
		t.Errorf("events[3].Type = %q, 期望 %q", events[3].Type, AGUIEventRunFinished)
	}

	// 验证 run_finished 数据。
	finishData := events[3].Data
	if finishData["path_taken"] != "fast" {
		t.Errorf("run_finished path_taken = %v, 期望 fast", finishData["path_taken"])
	}
	if finishData["articles"] != 3 {
		t.Errorf("run_finished articles = %v, 期望 3", finishData["articles"])
	}
	if finishData["tokens_in"] != 100 {
		t.Errorf("run_finished tokens_in = %v, 期望 100", finishData["tokens_in"])
	}
	if finishData["cost"] != 0.01 {
		t.Errorf("run_finished cost = %v, 期望 0.01", finishData["cost"])
	}

	// 验证 emitter 也收到事件。
	emitter.mu.Lock()
	emitterCount := len(emitter.events)
	emitter.mu.Unlock()
	if emitterCount != 4 {
		t.Errorf("emitter 事件数 = %d, 期望 4", emitterCount)
	}
}

// TestAGUIServer_StreamRunStartedData 验证 run_started 事件含 req 摘要。
func TestAGUIServer_StreamRunStartedData(t *testing.T) {
	orch := &stubOrchestratorAGUI{resp: domain.RecommendResponse{PathTaken: "fast"}}
	emitter := &captureAGUIEmitter{}
	srv := NewAGUIServer(orch, emitter)

	ch := make(chan AGUIEvent, 128)
	_ = srv.Stream(context.Background(), domain.RecommendRequest{
		UserKey:  domain.UserKey{UserID: "test_user"},
		Channel:  "tech",
		PathMode: "auto",
		TopK:     20,
	}, ch)
	close(ch)

	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if len(emitter.events) == 0 {
		t.Fatal("emitter 未收到事件")
	}
	first := emitter.events[0]
	if first.Type != AGUIEventRunStarted {
		t.Errorf("第一个事件 Type = %q, 期望 %q", first.Type, AGUIEventRunStarted)
	}
	if first.Data["user_id"] != "test_user" {
		t.Errorf("run_started user_id = %v, 期望 test_user", first.Data["user_id"])
	}
	if first.Data["channel"] != "tech" {
		t.Errorf("run_started channel = %v, 期望 tech", first.Data["channel"])
	}
	if first.Data["path_mode"] != "auto" {
		t.Errorf("run_started path_mode = %v, 期望 auto", first.Data["path_mode"])
	}
	if first.Data["top_k"] != 20 {
		t.Errorf("run_started top_k = %v, 期望 20", first.Data["top_k"])
	}
	if first.Timestamp == 0 {
		t.Error("run_started Timestamp 应非零")
	}
}

// TestAGUIServer_StreamError 验证失败时发射 error 事件。
func TestAGUIServer_StreamError(t *testing.T) {
	orch := &stubOrchestratorAGUI{
		err: errors.New("orchestrator failure"),
	}
	srv := NewAGUIServer(orch, nil)

	ch := make(chan AGUIEvent, 128)
	err := srv.Stream(context.Background(), domain.RecommendRequest{}, ch)
	close(ch)

	if err == nil {
		t.Fatal("Stream 应返回错误")
	}
	if !strings.Contains(err.Error(), "orchestrator failure") {
		t.Errorf("Stream 错误 = %v, 应包含 'orchestrator failure'", err)
	}

	events := drainEvents(ch)
	// run_started, step_started, error。
	if len(events) != 3 {
		t.Fatalf("事件数 = %d, 期望 3", len(events))
	}
	if events[2].Type != AGUIEventError {
		t.Errorf("events[2].Type = %q, 期望 %q", events[2].Type, AGUIEventError)
	}
	if errMsg, _ := events[2].Data["error"].(string); !strings.Contains(errMsg, "orchestrator failure") {
		t.Errorf("error 事件消息 = %q, 应包含 'orchestrator failure'", errMsg)
	}
}

// TestAGUIServer_StreamCtxCancel 验证 ctx 取消返回 ctx.Err() 并发射 error 事件。
func TestAGUIServer_StreamCtxCancel(t *testing.T) {
	orch := &stubOrchestratorAGUI{
		delay: 500 * time.Millisecond,
	}
	srv := NewAGUIServer(orch, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ch := make(chan AGUIEvent, 128)
	err := srv.Stream(ctx, domain.RecommendRequest{}, ch)
	close(ch)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stream 错误 = %v, 期望 context.DeadlineExceeded", err)
	}

	events := drainEvents(ch)
	// run_started, step_started, error(cancel)。
	if len(events) != 3 {
		t.Fatalf("事件数 = %d, 期望 3", len(events))
	}
	if events[2].Type != AGUIEventError {
		t.Errorf("events[2].Type = %q, 期望 %q", events[2].Type, AGUIEventError)
	}
	if cancelFlag, _ := events[2].Data["cancel"].(bool); !cancelFlag {
		t.Errorf("error 事件 cancel 标志 = %v, 期望 true", cancelFlag)
	}
}

// TestAGUIServer_StreamHandlerSSE 验证 SSE 输出格式。
func TestAGUIServer_StreamHandlerSSE(t *testing.T) {
	orch := &stubOrchestratorAGUI{
		resp: domain.RecommendResponse{
			PathTaken:  "fast",
			Candidates: makeAGUICandidates(2),
		},
	}
	srv := NewAGUIServer(orch, nil)

	body := `{"UserKey":{"UserID":"u1"},"Channel":"home","TopK":10}`
	req := httptest.NewRequest(http.MethodPost, "/agui/stream", strings.NewReader(body))
	rec := httptest.NewRecorder()

	srv.StreamHandler(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, 期望 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, 期望 text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, 期望 no-cache", cc)
	}
	if conn := resp.Header.Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection = %q, 期望 keep-alive", conn)
	}

	// 验证 SSE 格式：每个事件以 "data: " 开头。
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "data: ") {
		t.Errorf("响应体应包含 'data: ', 实际: %q", bodyStr)
	}

	// 解析 SSE 事件。
	lines := strings.Split(bodyStr, "data: ")
	var events []AGUIEvent
	for _, line := range lines[1:] { // 跳过第一段（空）。
		line = strings.TrimSuffix(line, "\n\n")
		var ev AGUIEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("解析 SSE 事件失败: %v, line=%q", err, line)
			continue
		}
		events = append(events, ev)
	}
	if len(events) < 4 {
		t.Errorf("SSE 事件数 = %d, 期望 >= 4", len(events))
	}
	// 验证事件序列。
	if events[0].Type != AGUIEventRunStarted {
		t.Errorf("events[0].Type = %q, 期望 %q", events[0].Type, AGUIEventRunStarted)
	}
	if events[len(events)-1].Type != AGUIEventRunFinished {
		t.Errorf("最后事件 Type = %q, 期望 %q", events[len(events)-1].Type, AGUIEventRunFinished)
	}
}

// TestAGUIServer_StreamHandlerBadBody 验证非 JSON body 返回 400。
func TestAGUIServer_StreamHandlerBadBody(t *testing.T) {
	srv := NewAGUIServer(&stubOrchestratorAGUI{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/agui/stream", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	srv.StreamHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, 期望 400", rec.Code)
	}
}

// TestAGUIServer_NilOrchestrator 验证 nil orchestrator 返回错误。
func TestAGUIServer_NilOrchestrator(t *testing.T) {
	srv := NewAGUIServer(nil, nil)
	ch := make(chan AGUIEvent, 128)
	err := srv.Stream(context.Background(), domain.RecommendRequest{}, ch)
	close(ch)
	if err == nil {
		t.Error("nil orchestrator 应返回错误")
	}
}

// TestAGUIServer_NilReceiver 验证 nil receiver 安全处理。
func TestAGUIServer_NilReceiver(t *testing.T) {
	var srv *AGUIServer
	ch := make(chan AGUIEvent, 128)
	err := srv.Stream(context.Background(), domain.RecommendRequest{}, ch)
	close(ch)
	if err == nil {
		t.Error("nil receiver Stream 应返回错误")
	}
}

// TestNoopAGUIEmitter 验证 NoopAGUIEmitter 默认行为。
func TestNoopAGUIEmitter(t *testing.T) {
	var e NoopAGUIEmitter
	if err := e.Emit(context.Background(), AGUIEvent{}); err != nil {
		t.Errorf("NoopAGUIEmitter.Emit 应返回 nil, 实际: %v", err)
	}
}

// TestAGUIServer_DefaultNoopEmitter 验证 nil emitter 时使用 NoopAGUIEmitter。
func TestAGUIServer_DefaultNoopEmitter(t *testing.T) {
	orch := &stubOrchestratorAGUI{resp: domain.RecommendResponse{PathTaken: "fast"}}
	srv := NewAGUIServer(orch, nil)
	// emitter 应被设为 NoopAGUIEmitter（非 nil）。
	if srv.emitter == nil {
		t.Error("nil emitter 应被替换为 NoopAGUIEmitter")
	}
	// 验证 Stream 不 panic。
	ch := make(chan AGUIEvent, 128)
	_ = srv.Stream(context.Background(), domain.RecommendRequest{}, ch)
	close(ch)
}

// TestAGUIServer_StreamHandlerNilServer 验证未初始化 server 返回 500。
func TestAGUIServer_StreamHandlerNilServer(t *testing.T) {
	var srv *AGUIServer
	req := httptest.NewRequest(http.MethodPost, "/agui/stream", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.StreamHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, 期望 500", rec.Code)
	}
}

// TestAGUIServer_EventsHaveTimestamps 验证所有事件都有非零 Timestamp。
func TestAGUIServer_EventsHaveTimestamps(t *testing.T) {
	orch := &stubOrchestratorAGUI{resp: domain.RecommendResponse{PathTaken: "fast"}}
	srv := NewAGUIServer(orch, nil)

	ch := make(chan AGUIEvent, 128)
	_ = srv.Stream(context.Background(), domain.RecommendRequest{}, ch)
	close(ch)

	events := drainEvents(ch)
	for i, ev := range events {
		if ev.Timestamp == 0 {
			t.Errorf("events[%d].Timestamp 应非零", i)
		}
	}
}
