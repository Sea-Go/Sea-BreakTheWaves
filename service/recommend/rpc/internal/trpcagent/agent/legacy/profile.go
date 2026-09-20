// Package agent profile.go — ProfileAgent 画像异步更新（Task 11.8）。
//
// 职责：异步消费用户行为事件，更新长期/短期兴趣画像与 Session Summary，
// 通过 memory.Save 持久化。Run 立即返回（不等待异步完成），避免阻塞主流程。
// 异步任务通过 buffered channel + consumer goroutine 实现，Close() 等待所有
// 异步任务完成（测试用）。
//
// 二开扩展点：
//   - 替换 MemoryStore：通过 WithMemoryStore 注入自研记忆存储（Redis/Postgres）
//   - 替换 LLMClient：接入自研/第三方 LLM 生成画像 Summary
//   - 自定义 topic 提取：覆盖 deriveTopicsFromEvent
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"sea/service/recommend/rpc/internal/domain"
)

// ProfileAgent 画像更新 Agent。
//
// 职责：
//   - 异步消费 BehaviorEvent，更新长期兴趣（topics）与短期兴趣（short_term）
//   - 当 events 数量 > 50 时触发 Session Summary 压缩
//   - 调用 LLM.CompleteWithStructuredOutput 生成画像 Summary（异步，失败不阻断）
//
// 实现要点：
//   - 通过 buffered channel（1024）+ consumer goroutine 异步处理事件
//   - Run 立即返回，写入 profile_updated/profile_async 状态
//   - Close() 关闭 channel 并等待所有异步任务完成（测试用）
//
// 二开扩展点：通过 WithMemoryStore 注入自研记忆存储；替换 LLMClient 接入自研 LLM。
type ProfileAgent struct {
	// llm LLM 客户端（用于 Summary 生成，可为 nil）。
	llm LLMClient
	// memory 记忆存储（通过 WithMemoryStore 注入，可为 nil）。
	memory MemoryStore
	// opts Agent 选项。
	opts domain.AgentOptions

	// eventCh 事件消费 channel（buffered 1024）。
	eventCh chan domain.BehaviorEvent
	// summaryCh Summary 任务 channel（buffered 64）。
	summaryCh chan profileSummaryTask

	// consumerWG consumer goroutine 同步。
	consumerWG sync.WaitGroup
	// summaryWG summary worker goroutine 同步。
	summaryWG sync.WaitGroup

	// stopped 关闭标记，避免重复 close。
	stopped bool
	// mu 保护 stopped 字段。
	mu sync.Mutex
}

// profileSummaryTask Summary 异步任务。
type profileSummaryTask struct {
	// userID 用户 ID。
	userID string
	// events 待汇总事件。
	events []domain.BehaviorEvent
	// compress 是否为 Session Summary 压缩（events > 50 触发）。
	compress bool
}

// 编译期断言：ProfileAgent 实现 domain.Agent。
var _ domain.Agent = (*ProfileAgent)(nil)

// NewProfileAgent 构造 ProfileAgent。
// llm LLM 客户端（用于 Summary 生成）；_ 工具执行器（ProfileAgent 不使用工具，
// 参数为构造函数签名统一）；opts Agent 选项。
// 返回 domain.Agent 接口。MemoryStore 通过 WithMemoryStore 链式注入。
func NewProfileAgent(llm LLMClient, _ ToolExecutor, opts domain.AgentOptions) domain.Agent {
	a := &ProfileAgent{
		llm:       llm,
		opts:      opts,
		eventCh:   make(chan domain.BehaviorEvent, 1024),
		summaryCh: make(chan profileSummaryTask, 64),
	}
	a.startConsumer()
	a.startSummaryWorker()
	return a
}

// WithMemoryStore 链式注入 MemoryStore。
// store 记忆存储实现。返回 *ProfileAgent 便于链式调用。
func (a *ProfileAgent) WithMemoryStore(store MemoryStore) *ProfileAgent {
	a.memory = store
	return a
}

// Name 返回 Agent 名称。
func (a *ProfileAgent) Name() string { return "profile" }

// Run 实现 domain.Agent.Run。
//
// 流程：
//  1. 从 input.State["events"] 读取 BehaviorEvent 列表
//  2. 异步发送到 eventCh（consumer 写入 memory topics/short_term）
//  3. 当 events 数量 > 50 时触发 Session Summary 压缩；events > 0 时调度 LLM Summary
//  4. 立即返回，State 写入 profile_updated/profile_async，Result 写入 "async scheduled"
func (a *ProfileAgent) Run(_ context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	trace := make([]string, 0, 2)
	events := extractBehaviorEvents(input.State["events"])

	// 异步发送事件到 consumer channel（非阻塞，channel 满则丢弃）。
	for _, e := range events {
		select {
		case a.eventCh <- e:
		default:
			// channel 满，丢弃事件（生产环境可加监控指标）。
		}
	}
	trace = append(trace, "profile.consume")

	// Summary 触发：events > 0 时调度 LLM Summary（异步，失败不阻断）。
	// events > 50 时额外标记 Session Summary 压缩。
	if len(events) > 0 {
		a.scheduleSummary(input.UserID, events, len(events) > 50)
		trace = append(trace, "profile.summary")
	}

	state := copyState(input.State)
	state["profile_updated"] = true
	state["profile_async"] = true
	return domain.AgentOutput{
		State:  state,
		Result: "async scheduled",
		Trace:  trace,
	}, nil
}

// startConsumer 启动事件 consumer goroutine。
// 从 eventCh 读取事件，写入 memory topics/short_term。
func (a *ProfileAgent) startConsumer() {
	a.consumerWG.Add(1)
	go func() {
		defer a.consumerWG.Done()
		for e := range a.eventCh {
			a.handleEvent(e)
		}
	}()
}

// startSummaryWorker 启动 Summary worker goroutine。
// 从 summaryCh 读取任务，调用 LLM 生成 Summary。
func (a *ProfileAgent) startSummaryWorker() {
	a.summaryWG.Add(1)
	go func() {
		defer a.summaryWG.Done()
		for task := range a.summaryCh {
			a.generateSummary(task)
		}
	}()
}

// scheduleSummary 非阻塞投递 Summary 任务到 summaryCh。
// userID 用户 ID；events 待汇总事件；compress 是否为 Session Summary 压缩。
func (a *ProfileAgent) scheduleSummary(userID string, events []domain.BehaviorEvent, compress bool) {
	task := profileSummaryTask{userID: userID, events: events, compress: compress}
	select {
	case a.summaryCh <- task:
	default:
		// summary channel 满，丢弃任务。
	}
}

// handleEvent 处理单条事件：写入长期兴趣与短期兴趣到 memory。
// e 行为事件。失败不阻断（best-effort 写入）。
func (a *ProfileAgent) handleEvent(e domain.BehaviorEvent) {
	if a.memory == nil {
		return
	}
	ctx := context.Background()
	uid := e.UserID
	if uid == "" {
		return
	}

	// 长期兴趣：从事件提取 topics，写入 user:{uid}:topics。
	topics := deriveTopicsFromEvent(e)
	if len(topics) > 0 {
		data, _ := json.Marshal(topics)
		_ = a.memory.Save(ctx, fmt.Sprintf("user:%s:topics", uid), string(data))
	}

	// 短期兴趣：写入 user:{uid}:short_term。
	shortTerm := deriveShortTermFromEvent(e)
	data, _ := json.Marshal(shortTerm)
	_ = a.memory.Save(ctx, fmt.Sprintf("user:%s:short_term", uid), string(data))
}

// generateSummary 调用 LLM 生成画像 Summary。
// 失败不阻断主流程（best-effort）。
// task Summary 任务（含 userID/events/compress 标记）。
func (a *ProfileAgent) generateSummary(task profileSummaryTask) {
	if a.llm == nil {
		return
	}
	ctx := context.Background()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"topics": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required": []string{"summary"},
	}
	prompt := fmt.Sprintf("用户 %s 在最近会话中产生 %d 条行为事件，请生成兴趣画像 Summary。",
		task.userID, len(task.events))
	raw, err := a.llm.CompleteWithStructuredOutput(ctx, prompt, schema)
	if err != nil {
		return
	}
	// 写入 memory（best-effort）。
	if a.memory != nil && len(raw) > 0 {
		_ = a.memory.Save(ctx, fmt.Sprintf("user:%s:summary", task.userID), string(raw))
		// Session Summary 压缩：events > 50 时额外写入 session_summary 标记。
		if task.compress {
			_ = a.memory.Save(ctx, fmt.Sprintf("user:%s:session_summary", task.userID), string(raw))
		}
	}
}

// Close 关闭 channel 并等待所有异步任务完成（测试用）。
// 多次调用安全。
func (a *ProfileAgent) Close() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()

	close(a.eventCh)
	close(a.summaryCh)
	a.consumerWG.Wait()
	a.summaryWG.Wait()
}

// extractBehaviorEvents 从 input.State["events"] 提取 BehaviorEvent 列表。
// 支持 []domain.BehaviorEvent 与 []any（每项为 domain.BehaviorEvent）两种形式。
// raw 原始值。返回 BehaviorEvent 列表。
func extractBehaviorEvents(raw any) []domain.BehaviorEvent {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []domain.BehaviorEvent:
		return v
	case []any:
		out := make([]domain.BehaviorEvent, 0, len(v))
		for _, item := range v {
			if e, ok := item.(domain.BehaviorEvent); ok {
				out = append(out, e)
			}
		}
		return out
	}
	return nil
}

// deriveTopicsFromEvent 从事件提取长期兴趣 topics。
// 简单实现：从 EventType + Channel + Payload.tags/topic 派生。
// 二开扩展点：业务方可覆盖此函数注入自定义 topic 提取逻辑。
// e 行为事件。返回 topic 列表（已去重）。
func deriveTopicsFromEvent(e domain.BehaviorEvent) []string {
	topics := make([]string, 0, 4)
	// EventType 信号：like/favorite/read_complete 强化兴趣。
	switch e.EventType {
	case domain.EventLike, domain.EventFavorite, domain.EventReadComplete:
		if e.Channel != "" {
			topics = append(topics, e.Channel)
		}
	}
	if e.Payload != nil {
		if tags, ok := e.Payload["tags"].([]any); ok {
			for _, t := range tags {
				if s, ok := t.(string); ok && s != "" {
					topics = append(topics, s)
				}
			}
		}
		if topic, ok := e.Payload["topic"].(string); ok && topic != "" {
			topics = append(topics, topic)
		}
	}
	return dedupStrings(topics)
}

// deriveShortTermFromEvent 从事件提取短期兴趣。
// 简单实现：取 EventType + ArticleID + Channel + Timestamp 作为短期信号。
// e 行为事件。返回短期兴趣 map。
func deriveShortTermFromEvent(e domain.BehaviorEvent) map[string]any {
	short := map[string]any{
		"event_type": e.EventType,
		"timestamp":  e.Timestamp.Unix(),
	}
	if e.ArticleID != "" {
		short["article_id"] = e.ArticleID
	}
	if e.Channel != "" {
		short["channel"] = e.Channel
	}
	return short
}

// copyState 拷贝 state map（避免共享底层 map 导致并发问题）。
// in 输入 state（可为 nil）。返回新 map（至少为空 map，永不为 nil）。
func copyState(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// dedupStrings 字符串切片去重（保序）。
// in 输入切片。返回去重后的切片。
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
