package trace

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
)

func NewTraceEmitter(runID string, recorders ...PublicPlanningEventRecorder) *TraceEmitter {
	if strings.TrimSpace(runID) == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	var recorder PublicPlanningEventRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	return &TraceEmitter{
		runID:    runID,
		out:      make(chan PublicPlanningEvent, 256),
		recorder: recorder,
	}
}

func (e *TraceEmitter) Events() <-chan PublicPlanningEvent {
	return e.out
}

func (e *TraceEmitter) RunID() string {
	if e == nil {
		return ""
	}
	return e.runID
}

func (e *TraceEmitter) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.done {
		return
	}
	e.done = true
	close(e.out)
}

func (e *TraceEmitter) Emit(ctx context.Context, ev PublicPlanningEvent) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	if e.done {
		e.mu.Unlock()
		return false
	}
	e.seq++
	ev.Seq = e.seq
	ev.RunID = e.runID
	ev.CreatedAt = time.Now().Format(time.RFC3339Nano)
	ev = SanitizePublicPlanningEvent(ev)
	recorder := e.recorder
	e.mu.Unlock()

	if recorder != nil {
		recorder.RecordPlanningEvent(ctx, ev)
	}

	select {
	case e.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *TraceEmitter) EmitChatDelta(ctx context.Context, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventChatMessageDelta,
		Message: text,
	})
}

func (e *TraceEmitter) EmitStage(ctx context.Context, stage, status, action, summary string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:           EventPlanningStageChanged,
		Stage:          stage,
		Status:         status,
		PublicAction:   action,
		ThoughtSummary: summary,
	})
	if status != "waiting" && (strings.TrimSpace(action) != "" || strings.TrimSpace(summary) != "") {
		e.Emit(ctx, PublicPlanningEvent{
			Type:           EventMapAnnotationAdded,
			Level:          publicLevelForStage(stage),
			Stage:          stage,
			Status:         "active",
			PublicAction:   action,
			ThoughtSummary: summary,
			Annotation: &PublicMapAnnotation{
				ID:      fmt.Sprintf("ann-thought-%s-%d", strings.ReplaceAll(stage, "_", "-"), time.Now().UnixNano()),
				Kind:    "thought",
				Source:  "planning",
				Title:   defaultPublicAnnotationTitle(action, "规划思考"),
				Summary: summary,
				Status:  "active",
				Anchor: PublicMapAnnotationAnchor{
					Type:  "scope",
					Label: defaultPublicAnnotationTitle(stage, "当前规划阶段"),
				},
			},
		})
	}
}

func (e *TraceEmitter) EmitError(ctx context.Context, message string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventPlanningError,
		Status:  "failed",
		Message: message,
	})
}

func (e *TraceEmitter) EmitCompleted(ctx context.Context, message string) {
	e.Emit(ctx, PublicPlanningEvent{
		Type:    EventPlanningCompleted,
		Status:  "completed",
		Message: message,
	})
}

func (e *TraceEmitter) EmitModelUsage(ctx context.Context, usage PublicModelUsage, stage string) {
	if usage.TotalTokens <= 0 && usage.PromptTokens <= 0 && usage.CompletionTokens <= 0 {
		return
	}
	stage = defaultIfEmpty(stage, "planning")
	label := defaultIfEmpty(usage.AgentLabel, "模型调用")
	e.Emit(ctx, PublicPlanningEvent{
		Type:           EventMapAnnotationAdded,
		Level:          publicLevelForStage(stage),
		Stage:          stage,
		Status:         "completed",
		PublicAction:   "记录模型用量",
		ThoughtSummary: fmt.Sprintf("%s 使用 %s，消耗 %d token。", label, defaultIfEmpty(usage.Model, "模型"), usage.TotalTokens),
		Usage:          &usage,
		Annotation: &PublicMapAnnotation{
			ID:      fmt.Sprintf("ann-model-usage-%s-%d", strings.ReplaceAll(stage, "_", "-"), time.Now().UnixNano()),
			Kind:    "model_usage",
			Source:  "model",
			Title:   label,
			Summary: fmt.Sprintf("模型：%s；Token：%d（输入 %d / 输出 %d）。", defaultIfEmpty(usage.Model, "未知模型"), usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens),
			Status:  "completed",
			Tags:    []string{"模型", "Token"},
			Anchor: PublicMapAnnotationAnchor{
				Type:  "scope",
				Label: defaultPublicAnnotationTitle(stage, "当前规划阶段"),
			},
		},
	})
}

func traceEmitterFromInvocation(inv *agentcore.Invocation) *TraceEmitter {
	if inv == nil || inv.RunOptions.RuntimeState == nil {
		return nil
	}
	if emitter, ok := inv.RunOptions.RuntimeState[runtimeTraceEmitterKey].(*TraceEmitter); ok {
		return emitter
	}
	return nil
}

func traceEmitterFromContext(ctx context.Context) *TraceEmitter {
	if ctx == nil {
		return nil
	}
	if emitter, ok := ctx.Value(traceEmitterContextKey{}).(*TraceEmitter); ok {
		return emitter
	}
	return nil
}
