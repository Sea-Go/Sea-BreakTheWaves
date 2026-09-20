package domain

import "time"

// ============================================================================
// 该文件为 BehaviorEvent（定义于 types.go）补充强类型事件常量与辅助方法。
// types.go 已定义 BehaviorEvent struct（含 EventID/EventType/UserID/ArticleID/
// Channel/PathTaken/Timestamp/Payload 字段），本文件不重复定义该结构，
// 仅补充：
//   - EventType 类型别名（string）与 17 个事件类型常量
//   - RecoEventLog 本地兼容结构（对齐 type/reco_evaluation_types.go）
//   - ToRecoEventLog 转换方法
// 所有代码仅依赖标准库，确保离线可编译。
// ============================================================================

// EventType 行为事件类型，string 类型别名。
// 用于 BehaviorEvent.EventType 字段的强类型常量，便于编译期校验、IDE 自动补全
// 与事件分发 switch 的可维护性。二开方引用常量而非裸字符串可避免拼写错误。
type EventType = string

// 事件类型常量。覆盖双路径架构（快/慢/混合）、图谱、Skill、反馈行为、
// Agent 循环（工具调用/LLM 调用/Agent 结束）、质量评判与重排等全部事件。
const (
	// EventFastPathHit 快路径命中：传统推荐快路径（recall+rank）命中事件。
	EventFastPathHit EventType = "fast_path_hit"
	// EventSlowPathHit 慢路径命中：Agent 慢路径（多 Agent 协作）命中事件。
	EventSlowPathHit EventType = "slow_path_hit"
	// EventHybridMerge 混合路径融合：快慢路径候选融合事件。
	EventHybridMerge EventType = "hybrid_merge"
	// EventGraphQuery 图谱查询：知识图谱查询事件（Neo4j Cypher 执行）。
	EventGraphQuery EventType = "graph_query"
	// EventCypherGenerated Cypher 生成：GraphAgent 生成 Cypher 语句事件。
	EventCypherGenerated EventType = "cypher_generated"
	// EventSkillInvoked Skill 调用：Skill 被 Agent 调用事件（Anthropic Agent Skills）。
	EventSkillInvoked EventType = "skill_invoked"
	// EventImpression 曝光：文章曝光事件（推荐结果展示给用户）。
	EventImpression EventType = "impression"
	// EventClick 点击：文章点击事件。
	EventClick EventType = "click"
	// EventLike 点赞：文章点赞事件。
	EventLike EventType = "like"
	// EventDislike 不感兴趣：文章 dislike 事件。
	EventDislike EventType = "dislike"
	// EventFavorite 收藏：文章收藏事件。
	EventFavorite EventType = "favorite"
	// EventReadComplete 完成阅读：文章完成阅读事件。
	EventReadComplete EventType = "read_complete"
	// EventToolCall 工具调用：Agent 工具调用事件（Function Call）。
	EventToolCall EventType = "tool_call"
	// EventLLMCall LLM 调用：Agent LLM 调用事件（含 token 与费用统计）。
	EventLLMCall EventType = "llm_call"
	// EventAgentEnd Agent 结束：Agent 主循环结束事件。
	EventAgentEnd EventType = "agent_end"
	// EventQualityJudge 质量评判：文章质量评判事件（6 维评分 + Best-of-N）。
	EventQualityJudge EventType = "quality_judge"
	// EventRerank 重排：候选重排事件（self/external/llm rerank）。
	EventRerank EventType = "rerank"
)

// RecoEventLog 推荐事件日志，对齐 type/reco_evaluation_types.go 的 RecoEventLog。
// domain 包仅依赖标准库（不依赖 sea/type 包以避免循环依赖），故在此定义本地
// 兼容结构，字段名与 json tag 与 types.RecoEventLog 保持一致。
// TODO: 后续若 type 包拆分独立或 domain 允许依赖 type，可直接引用 types.RecoEventLog。
type RecoEventLog struct {
	// RecRequestID 推荐请求 ID。
	RecRequestID string `json:"rec_request_id"`
	// UserID 用户 ID。
	UserID string `json:"user_id"`
	// SessionID 会话 ID。
	SessionID string `json:"session_id"`
	// Surface 曝光面（对应频道 Channel）。
	Surface string `json:"surface"`
	// ArticleID 文章 ID。
	ArticleID string `json:"article_id"`
	// Rank 文章排序位次。
	Rank int `json:"rank"`
	// EventType 事件类型。
	EventType string `json:"event_type"`
	// EventTS 事件时间戳。
	EventTS time.Time `json:"event_ts"`
	// Metadata 扩展元数据。
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ToRecoEventLog 将 BehaviorEvent 转换为 RecoEventLog（兼容 reco_evaluation_types.go）。
// 现有 BehaviorEvent 字段映射：
//   - UserID/ArticleID/EventType/Timestamp 直接映射
//   - Channel 映射为 Surface（推荐日志以 Surface 表示曝光面）
//   - SessionID/RecRequestID/Rank 从 Payload 中按约定 key 提取
//     （session_id/rec_request_id/rank）
//   - Payload 整体作为 Metadata 透传，便于下游消费扩展字段
//
// 二开扩展点：若业务方在 Payload 中存放自定义字段，可通过 Metadata 透传至日志。
func (e BehaviorEvent) ToRecoEventLog() RecoEventLog {
	log := RecoEventLog{
		UserID:    e.UserID,
		Surface:   e.Channel,
		ArticleID: e.ArticleID,
		EventType: e.EventType,
		EventTS:   e.Timestamp,
		Metadata:  e.Payload,
	}
	// 从 Payload 提取补充字段（按 reco_evaluation_types.go 约定的 key）。
	if e.Payload != nil {
		if v, ok := e.Payload["session_id"].(string); ok {
			log.SessionID = v
		}
		if v, ok := e.Payload["rec_request_id"].(string); ok {
			log.RecRequestID = v
		}
		if v, ok := e.Payload["surface"].(string); ok && v != "" {
			log.Surface = v
		}
		if v, ok := e.Payload["rank"].(int); ok {
			log.Rank = v
		}
	}
	return log
}
