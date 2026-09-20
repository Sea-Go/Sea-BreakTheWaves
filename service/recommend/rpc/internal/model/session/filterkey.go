package session

import (
	"context"
	"sync"
)

// ============================================================================
// 该文件实现 ChannelFilterKey / ChannelFilter：频道隔离 filterKey。
// 对应 trpc-agent-go 的 filterKey 能力，频道切换时不跨频道污染 Memory/Session。
// 仅依赖标准库。
// ============================================================================

// filterKeyCtxKey filterKey 在 context 中的 key 类型（避免与其他包冲突）。
type filterKeyCtxKey struct{}

// ChannelFilterKey 频道隔离 key，用于 Memory/Session 按频道隔离。
//
// 职责：封装 userID + channel，作为 Memory/Session 存储命名空间的隔离维度，
// 频道切换时按 filterKey 隔离，避免跨频道污染。
//
// 二开扩展点：扩展 String() 自定义 filterKey 编码格式（如增加 namespace 前缀）。
type ChannelFilterKey struct {
	// UserID 用户 ID。
	UserID string
	// Channel 频道名。
	Channel string
}

// String 返回 filterKey 字符串，格式 {userID}:{channel}。
func (k ChannelFilterKey) String() string {
	return k.UserID + ":" + k.Channel
}

// NewChannelFilterKey 构造 ChannelFilterKey。
// userID 用户 ID；channel 频道名。
// 返回 ChannelFilterKey 实例。
func NewChannelFilterKey(userID, channel string) ChannelFilterKey {
	return ChannelFilterKey{UserID: userID, Channel: channel}
}

// ChannelFilter 频道过滤器，管理用户当前频道。
//
// 职责：维护用户当前所在频道的 filterKey，支持 Set/Get/Isolate，
// 频道切换时更新 filterKey 以实现 Memory/Session 隔离。
//
// 二开扩展点：
//   - 自定义隔离策略：覆盖 Isolate 方法注入自定义 context key 或命名空间
//   - 持久化：扩展为从 Redis/Postgres 加载用户当前频道
type ChannelFilter struct {
	mu   sync.RWMutex
	keys map[string]ChannelFilterKey
}

// NewChannelFilter 构造 ChannelFilter。
// 返回 ChannelFilter 实例。
func NewChannelFilter() *ChannelFilter {
	return &ChannelFilter{keys: make(map[string]ChannelFilterKey)}
}

// Set 设置用户当前频道。
// userID 用户 ID；channel 频道名。
func (f *ChannelFilter) Set(userID, channel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[userID] = NewChannelFilterKey(userID, channel)
}

// Get 获取用户当前频道的 filterKey。
// userID 用户 ID。
// 返回 filterKey 与 bool（用户未设置频道时返回 false）。
func (f *ChannelFilter) Get(userID string) (ChannelFilterKey, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	k, ok := f.keys[userID]
	return k, ok
}

// Isolate 将 filterKey 注入 context，实现频道切换时不跨频道污染。
// ctx 上下文；userID 用户 ID；channel 频道。
// 返回注入 filterKey 的新 context。
//
// 二开：Memory/Session 读写时通过 FilterKeyFromContext 提取 filterKey，
// 按 filterKey 隔离存储命名空间（如 key 前缀拼入 channel）。
func (f *ChannelFilter) Isolate(ctx context.Context, userID, channel string) context.Context {
	f.Set(userID, channel)
	key := NewChannelFilterKey(userID, channel)
	return context.WithValue(ctx, filterKeyCtxKey{}, key)
}

// FilterKeyFromContext 从 context 提取 filterKey。
// ctx 上下文。
// 返回 filterKey 与 bool（context 中无 filterKey 时返回 false）。
//
// 二开：Memory/Session 读写时调用本函数获取 filterKey，按频道隔离存储。
func FilterKeyFromContext(ctx context.Context) (ChannelFilterKey, bool) {
	k, ok := ctx.Value(filterKeyCtxKey{}).(ChannelFilterKey)
	return k, ok
}
