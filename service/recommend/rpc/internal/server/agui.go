// Package server agui.go — AG-UI 流式响应（Task 13.9）。
//
// 该文件实现 AGUIServer：将推荐主流程包装为 AG-UI 兼容的流式事件序列，
// 通过 SSE（Server-Sent Events）推送给前端。
//
// 职责：
//   - Stream：发射 run_started → step_started(route) → orchestrator.Recommend
//     → step_finished(route) → run_finished 事件序列
//   - 失败时发射 error 事件（含 error message）
//   - 支持 ctx.Done() 取消（返回 ctx.Err()）
//   - StreamHandler：HTTP SSE handler，用 buffered channel（128）推送事件
//
// 二开扩展点：
//   - 实现 AGUIEventEmitter interface 接入自定义事件出口（WebSocket/Kafka）
//   - 通过 NewAGUIServer(orchestrator, emitter) 注入自定义编排器与发射器
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// AGUI 事件类型常量。
const (
	// AGUIEventRunStarted 流开始事件（含 req 摘要）。
	AGUIEventRunStarted = "run_started"
	// AGUIEventStepStarted 步骤开始事件。
	AGUIEventStepStarted = "step_started"
	// AGUIEventStepFinished 步骤结束事件。
	AGUIEventStepFinished = "step_finished"
	// AGUIEventToolCall 工具调用事件。
	AGUIEventToolCall = "tool_call"
	// AGUIEventToolResult 工具结果事件。
	AGUIEventToolResult = "tool_result"
	// AGUIEventRunFinished 流结束事件（含 PathTaken/Cost/Articles 数量）。
	AGUIEventRunFinished = "run_finished"
	// AGUIEventError 错误事件（含 error message）。
	AGUIEventError = "error"
)

// AGUI 步骤名常量。
const (
	// AGUIStepRoute 路由步骤（路径决策）。
	AGUIStepRoute = "route"
	// AGUIStepFast 快路径步骤。
	AGUIStepFast = "fast"
	// AGUIStepSlow 慢路径步骤。
	AGUIStepSlow = "slow"
	// AGUIStepHybrid 混合路径步骤。
	AGUIStepHybrid = "hybrid"
	// AGUIStepMerge 融合步骤。
	AGUIStepMerge = "merge"
	// AGUIStepFinish 完成步骤。
	AGUIStepFinish = "finish"
)

// AGUIEventEmitter AG-UI 事件发射 interface。
//
// 职责：抽象事件出口，供 AGUIServer.Stream 发射事件。
// 真实实现可接入 WebSocket/Kafka/SSE；默认 NoopAGUIEmitter 空操作。
//
// 二开扩展点：实现该 interface 接入自定义事件出口。
type AGUIEventEmitter interface {
	// Emit 发射 AG-UI 事件。
	// ctx 上下文；event AG-UI 事件。
	// 返回 error（错误不影响主流程，由调用方决定是否记录）。
	Emit(ctx context.Context, event AGUIEvent) error
}

// AGUIEvent AG-UI 流式事件。
//
// 字段语义：
//   - Type：事件类型（run_started/step_started/step_finished/tool_call/tool_result/run_finished/error）
//   - Step：步骤名（route/fast/slow/hybrid/merge/finish）
//   - Data：事件数据（可为 nil）
//   - Timestamp：事件时间戳（UnixMilli）
type AGUIEvent struct {
	// Type 事件类型。
	Type string `json:"type"`
	// Step 步骤名。
	Step string `json:"step,omitempty"`
	// Data 事件数据。
	Data map[string]any `json:"data,omitempty"`
	// Timestamp 事件时间戳（UnixMilli）。
	Timestamp int64 `json:"timestamp"`
}

// AGUIServer AG-UI 流式响应服务器。
//
// 职责：
//   - Stream：将 orchestrator.Recommend 包装为 AG-UI 事件序列
//   - StreamHandler：HTTP SSE handler
//
// 字段语义：
//   - orchestrator：推荐编排器（domain.Orchestrator interface）
//   - emitter：事件发射器（可为 nil，用 NoopAGUIEmitter）
type AGUIServer struct {
	// orchestrator 推荐编排器。
	orchestrator domain.Orchestrator
	// emitter 事件发射器（可为 nil）。
	emitter AGUIEventEmitter
}

// NewAGUIServer 构造 AGUIServer。
// orchestrator 推荐编排器（不可为 nil）；emitter 事件发射器（可为 nil，用 NoopAGUIEmitter）。
// 返回 *AGUIServer。
func NewAGUIServer(orchestrator domain.Orchestrator, emitter AGUIEventEmitter) *AGUIServer {
	if emitter == nil {
		emitter = NoopAGUIEmitter{}
	}
	return &AGUIServer{
		orchestrator: orchestrator,
		emitter:      emitter,
	}
}

// Stream 执行推荐主流程并发射 AG-UI 事件序列。
//
// 事件序列：
//  1. run_started（含 req 摘要：user_id/channel/path_mode/top_k）
//  2. step_started（step=route）
//  3. 调用 orchestrator.Recommend（在 goroutine 中执行，select 监听 ctx.Done）
//  4. 推荐完成后发射 step_finished（step=route）+ run_finished（含 PathTaken/Cost/Articles 数量）
//  5. 失败时发射 error 事件（含 error message）
//
// ctx 取消时发射 error 事件（含 cancel=true）并返回 ctx.Err()。
// ch 用于推送事件给调用方（如 StreamHandler）；emitter 也同步发射（错误忽略）。
// ch 写入非阻塞（channel 已满时丢弃事件，避免阻塞流）。
func (s *AGUIServer) Stream(ctx context.Context, req domain.RecommendRequest, ch chan<- AGUIEvent) error {
	if s == nil || s.orchestrator == nil {
		return fmt.Errorf("agui: nil server or orchestrator")
	}

	// 1. run_started（含 req 摘要）。
	s.emit(ctx, ch, AGUIEvent{
		Type:      AGUIEventRunStarted,
		Step:      AGUIStepRoute,
		Timestamp: nowMillis(),
		Data: map[string]any{
			"user_id":   req.UserKey.UserID,
			"channel":   req.Channel,
			"path_mode": req.PathMode,
			"top_k":     req.TopK,
		},
	})

	// 2. step_started（step=route）。
	s.emit(ctx, ch, AGUIEvent{
		Type:      AGUIEventStepStarted,
		Step:      AGUIStepRoute,
		Timestamp: nowMillis(),
	})

	// 3. 调用 orchestrator.Recommend（在 goroutine 中执行，select 监听 ctx.Done）。
	type result struct {
		resp domain.RecommendResponse
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		resp, err := s.orchestrator.Recommend(ctx, req)
		resultCh <- result{resp: resp, err: err}
	}()

	select {
	case <-ctx.Done():
		// ctx 取消：发射 error 事件并返回 ctx.Err()。
		s.emit(ctx, ch, AGUIEvent{
			Type:      AGUIEventError,
			Step:      AGUIStepRoute,
			Timestamp: nowMillis(),
			Data: map[string]any{
				"error":  ctx.Err().Error(),
				"cancel": true,
			},
		})
		return ctx.Err()
	case r := <-resultCh:
		if r.err != nil {
			// 5. 失败 → error 事件。
			s.emit(ctx, ch, AGUIEvent{
				Type:      AGUIEventError,
				Step:      AGUIStepRoute,
				Timestamp: nowMillis(),
				Data: map[string]any{
					"error": r.err.Error(),
				},
			})
			return r.err
		}
		// 4. step_finished（step=route）。
		s.emit(ctx, ch, AGUIEvent{
			Type:      AGUIEventStepFinished,
			Step:      AGUIStepRoute,
			Timestamp: nowMillis(),
			Data: map[string]any{
				"path_taken": r.resp.PathTaken,
			},
		})
		// 5. run_finished（含 PathTaken/Cost/Articles 数量）。
		s.emit(ctx, ch, AGUIEvent{
			Type:      AGUIEventRunFinished,
			Step:      AGUIStepFinish,
			Timestamp: nowMillis(),
			Data: map[string]any{
				"path_taken":    r.resp.PathTaken,
				"articles":      len(r.resp.Candidates),
				"tokens_in":     r.resp.Cost.TokensIn,
				"tokens_out":    r.resp.Cost.TokensOut,
				"cached_tokens": r.resp.Cost.CachedTokens,
				"llm_calls":     r.resp.Cost.LLMCalls,
				"cost":          r.resp.Cost.EstimatedCost,
			},
		})
		return nil
	}
}

// emit 同时推送到 channel 和 emitter。
//
// channel 写入非阻塞（channel 已满时丢弃事件，避免阻塞流）。
// emitter 错误忽略（不影响主流程）。
func (s *AGUIServer) emit(ctx context.Context, ch chan<- AGUIEvent, event AGUIEvent) {
	if s.emitter != nil {
		_ = s.emitter.Emit(ctx, event)
	}
	if ch != nil {
		select {
		case ch <- event:
		default:
			// channel 已满，丢弃事件（避免阻塞流）。
		}
	}
}

// StreamHandler HTTP SSE handler。
//
// 流程：
//  1. 从 r.Body 读取 RecommendRequest JSON（json.NewDecoder）
//  2. 设置 SSE headers（Content-Type: text/event-stream + Cache-Control: no-cache + Connection: keep-alive）
//  3. 创建 buffered channel（128）
//  4. 在 goroutine 中调用 Stream（defer close channel）
//  5. 用 SSE 协议（data: {json}\n\n）写响应，每个 event 一行
//  6. 用 http.Flusher 刷新
//
// 客户端断开（r.Context().Done()）时 Stream 因 ctx 取消返回，channel 关闭，循环退出。
func (s *AGUIServer) StreamHandler(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.orchestrator == nil {
		http.Error(w, "agui: server not initialized", http.StatusInternalServerError)
		return
	}

	// 1. 解析请求。
	var req domain.RecommendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("agui: decode request: %v", err), http.StatusBadRequest)
		return
	}

	// 2. 设置 SSE headers。
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	// 3. 创建 buffered channel（128）。
	ch := make(chan AGUIEvent, 128)
	streamErr := make(chan error, 1)

	// 4. 在 goroutine 中调用 Stream，结束后 close channel。
	go func() {
		defer close(ch)
		streamErr <- s.Stream(r.Context(), req, ch)
	}()

	// 5. 主循环：读取 channel 写 SSE。
	// channel 关闭后循环退出，最后读取 streamErr 确保 goroutine 完成。
	for event := range ch {
		data, err := json.Marshal(event)
		if err != nil {
			continue
		}
		// 6. SSE 协议：data: {json}\n\n，每个 event 一行。
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	// 排空 streamErr（确保 goroutine 完成）。
	_ = <-streamErr
}

// NoopAGUIEmitter 空操作事件发射器，用于默认场景。
//
// Emit 直接返回 nil，不产生副作用。
type NoopAGUIEmitter struct{}

// Emit 空操作，返回 nil。
func (NoopAGUIEmitter) Emit(_ context.Context, _ AGUIEvent) error {
	return nil
}

// nowMillis 返回当前时间的 UnixMilli。
func nowMillis() int64 {
	return time.Now().UnixMilli()
}
