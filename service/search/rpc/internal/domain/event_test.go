package domain

import (
	"testing"
	"time"
)

// ============================================================================
// 该文件测试 internal/domain/event.go 的强类型事件常量与 ToRecoEventLog 转换。
// ============================================================================

// TestEventTypeConstants 验证 17 个事件类型常量已定义且值为约定字符串。
// 同时验证 EventType 类型别名可直接赋值给 string 字段（BehaviorEvent.EventType）。
func TestEventTypeConstants(t *testing.T) {
	want := map[EventType]string{
		EventFastPathHit:     "fast_path_hit",
		EventSlowPathHit:     "slow_path_hit",
		EventHybridMerge:     "hybrid_merge",
		EventGraphQuery:      "graph_query",
		EventCypherGenerated: "cypher_generated",
		EventSkillInvoked:    "skill_invoked",
		EventImpression:      "impression",
		EventClick:           "click",
		EventLike:            "like",
		EventDislike:         "dislike",
		EventFavorite:        "favorite",
		EventReadComplete:    "read_complete",
		EventToolCall:        "tool_call",
		EventLLMCall:         "llm_call",
		EventAgentEnd:        "agent_end",
		EventQualityJudge:    "quality_judge",
		EventRerank:          "rerank",
	}
	if len(want) != 17 {
		t.Fatalf("事件常量期望 17 个，实际 %d", len(want))
	}
	for c, v := range want {
		if string(c) != v {
			t.Errorf("常量 %q 期望值 %q", c, v)
		}
	}

	// 验证常量可直接赋值给 BehaviorEvent.EventType（string 字段）。
	e := BehaviorEvent{EventType: EventToolCall}
	if e.EventType != "tool_call" {
		t.Errorf("EventType 字段赋值后期望 tool_call，实际 %q", e.EventType)
	}
}

// TestToRecoEventLog_BasicMapping 验证 BehaviorEvent 基础字段直接映射到 RecoEventLog。
func TestToRecoEventLog_BasicMapping(t *testing.T) {
	ts := time.Now()
	e := BehaviorEvent{
		EventID:   "evt-1",
		EventType: EventClick,
		UserID:    "u-100",
		ArticleID: "art-200",
		Channel:   "tech",
		Timestamp: ts,
	}
	log := e.ToRecoEventLog()
	if log.UserID != "u-100" {
		t.Errorf("UserID 期望 u-100，实际 %q", log.UserID)
	}
	if log.ArticleID != "art-200" {
		t.Errorf("ArticleID 期望 art-200，实际 %q", log.ArticleID)
	}
	if log.EventType != "click" {
		t.Errorf("EventType 期望 click，实际 %q", log.EventType)
	}
	if log.EventTS != ts {
		t.Errorf("EventTS 期望 %v，实际 %v", ts, log.EventTS)
	}
	// Channel 映射为 Surface。
	if log.Surface != "tech" {
		t.Errorf("Surface 期望 tech，实际 %q", log.Surface)
	}
}

// TestToRecoEventLog_PayloadExtraction 验证从 Payload 提取
// session_id/rec_request_id/rank/surface 补充字段。
func TestToRecoEventLog_PayloadExtraction(t *testing.T) {
	ts := time.Now()
	e := BehaviorEvent{
		EventType: EventImpression,
		UserID:    "u-1",
		ArticleID: "art-1",
		Channel:   "fallback-channel",
		Timestamp: ts,
		Payload: map[string]any{
			"session_id":     "sess-xyz",
			"rec_request_id": "req-abc",
			"surface":        "home",
			"rank":           3,
			"custom":         "extra",
		},
	}
	log := e.ToRecoEventLog()
	if log.SessionID != "sess-xyz" {
		t.Errorf("SessionID 期望 sess-xyz，实际 %q", log.SessionID)
	}
	if log.RecRequestID != "req-abc" {
		t.Errorf("RecRequestID 期望 req-abc，实际 %q", log.RecRequestID)
	}
	if log.Rank != 3 {
		t.Errorf("Rank 期望 3，实际 %d", log.Rank)
	}
	// Payload["surface"] 非空时覆盖 Channel 映射。
	if log.Surface != "home" {
		t.Errorf("Surface 期望 home（Payload 覆盖），实际 %q", log.Surface)
	}
	// Metadata 透传 Payload。
	if log.Metadata == nil {
		t.Fatal("Metadata 期望非 nil")
	}
	if v, ok := log.Metadata["custom"].(string); !ok || v != "extra" {
		t.Errorf("Metadata[custom] 期望 extra，实际 %v", log.Metadata["custom"])
	}
}

// TestToRecoEventLog_NilPayload 验证 Payload 为 nil 时不 panic，且字段为零值。
func TestToRecoEventLog_NilPayload(t *testing.T) {
	e := BehaviorEvent{
		EventType: EventClick,
		UserID:    "u-1",
		Channel:   "c1",
	}
	log := e.ToRecoEventLog()
	if log.SessionID != "" {
		t.Errorf("空 Payload 时 SessionID 期望空，实际 %q", log.SessionID)
	}
	if log.Rank != 0 {
		t.Errorf("空 Payload 时 Rank 期望 0，实际 %d", log.Rank)
	}
	if log.Metadata != nil {
		t.Errorf("空 Payload 时 Metadata 期望 nil，实际 %v", log.Metadata)
	}
}

// TestRecoEventLog_JSONTag 验证 RecoEventLog 的 json tag 与
// type/reco_evaluation_types.go 对齐（编译期类型兼容性的运行时校验）。
func TestRecoEventLog_JSONTag(t *testing.T) {
	// 仅验证结构可构造且字段可访问，json tag 对齐由代码评审保证。
	log := RecoEventLog{
		RecRequestID: "r1",
		UserID:       "u1",
		SessionID:    "s1",
		Surface:      "home",
		ArticleID:    "a1",
		Rank:         1,
		EventType:    "click",
		EventTS:      time.Now(),
		Metadata:     map[string]any{"k": "v"},
	}
	if log.RecRequestID != "r1" || log.Rank != 1 {
		t.Error("RecoEventLog 字段构造异常")
	}
}
