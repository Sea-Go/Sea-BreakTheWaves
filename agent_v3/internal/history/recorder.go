package history

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domainevents "agent_v3/internal/domain/events"
	"agent_v3/internal/graph"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

const ActionChatUser = "chat_user"

type TravelRecorder struct {
	client        *graph.Client
	runID         string
	threadID      string
	userID        string
	assistantText strings.Builder
}

func NewTravelRecorder(runID, threadID, userID string) *TravelRecorder {
	client := graph.GetClient()
	if client == nil || !client.IsEnabled() {
		return nil
	}
	return &TravelRecorder{
		client:   client,
		runID:    runID,
		threadID: threadID,
		userID:   userID,
	}
}

func (r *TravelRecorder) Start(ctx context.Context, titleMessage, userMessage string) {
	if r == nil || r.client == nil {
		return
	}
	now := time.Now().Format(time.RFC3339Nano)
	run := graph.ExplorationRunNode{
		ID:          r.runID,
		ThreadID:    r.threadID,
		SessionID:   r.threadID,
		UserID:      r.userID,
		Title:       compactHistoryText(titleMessage, 42),
		Stage:       "requirement_intake",
		Status:      "running",
		LastMessage: compactHistoryText(userMessage, 160),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := r.client.UpsertExplorationRun(ctx, run); err != nil {
		log.Warnf("[travel-history] upsert run failed: %v", err)
		return
	}
	if strings.TrimSpace(userMessage) == "" {
		return
	}
	if err := r.client.AppendExplorationStep(ctx, graph.ExplorationStepNode{
		RunID:       r.runID,
		ThreadID:    r.threadID,
		Seq:         0,
		ActionType:  ActionChatUser,
		MessageRole: "user",
		Message:     userMessage,
		Status:      "done",
		CreatedAt:   now,
	}); err != nil {
		log.Warnf("[travel-history] append user message failed: %v", err)
	}
}

func (r *TravelRecorder) RecordPlanningEvent(ctx context.Context, ev domainevents.PublicPlanningEvent) {
	if r == nil || r.client == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		log.Warnf("[travel-history] marshal event failed: %v", err)
		return
	}
	messageRole := ""
	if ev.Type == domainevents.EventChatMessageDelta || ev.Type == domainevents.EventPlanningError {
		messageRole = "assistant"
	}
	if err := r.client.AppendExplorationStep(ctx, graph.ExplorationStepNode{
		RunID:          r.runID,
		ThreadID:       r.threadID,
		Seq:            ev.Seq,
		Level:          ev.Level,
		ActionType:     ev.Type,
		EventType:      ev.Type,
		PublicAction:   ev.PublicAction,
		ThoughtSummary: ev.ThoughtSummary,
		RecordedFacts:  ev.RecordedFacts,
		MessageRole:    messageRole,
		Message:        ev.Message,
		PayloadJSON:    string(payload),
		Status:         ev.Status,
		CreatedAt:      ev.CreatedAt,
	}); err != nil {
		log.Warnf("[travel-history] append event failed: %v", err)
	}

	runUpdate := graph.ExplorationRunNode{
		ID:        r.runID,
		ThreadID:  r.threadID,
		SessionID: r.threadID,
		UserID:    r.userID,
		Stage:     ev.Stage,
		Status:    "running",
		UpdatedAt: ev.CreatedAt,
	}
	if ev.Type == domainevents.EventPlanningCompleted {
		runUpdate.Status = "completed"
	} else if ev.Type == domainevents.EventPlanningError {
		runUpdate.Status = "failed"
		runUpdate.FinalSummary = compactHistoryText(ev.Message, 160)
	} else if ev.Status != "" {
		runUpdate.Status = ev.Status
	}
	if ev.Message != "" {
		runUpdate.LastMessage = compactHistoryText(ev.Message, 160)
		if ev.Type == domainevents.EventChatMessageDelta {
			r.assistantText.WriteString(ev.Message)
			runUpdate.FinalSummary = compactHistoryText(r.assistantText.String(), 160)
		}
	} else if ev.PublicAction != "" {
		runUpdate.LastMessage = ev.PublicAction
	}
	if err := r.client.UpsertExplorationRun(ctx, runUpdate); err != nil {
		log.Warnf("[travel-history] update run failed: %v", err)
	}
}

func compactHistoryText(value string, limit int) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return fmt.Sprintf("%s...", string(runes[:limit]))
}
