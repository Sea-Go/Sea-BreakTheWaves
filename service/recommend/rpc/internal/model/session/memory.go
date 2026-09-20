package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 MemoryService：基于 trpc-agent-go Memory 模块的增量更新。
// 仅依赖标准库 + encoding/json，通过 MemoryStore interface 抽象底层存储，
// 便于二开对接 trpc-agent-go Memory / Redis / Postgres 等后端。
// ============================================================================

// MemoryStore KV 存储抽象，对接 trpc-agent-go Memory 或 Redis 等后端。
//
// 职责：抽象用户状态增量更新所需的键值读写删除操作，使 MemoryService 与
// 具体存储后端解耦，便于单测 mock 与二开替换。
//
// 二开扩展点：
//   - 对接 trpc-agent-go Memory：实现该 interface 包装 trpc-agent-go 的 Memory API
//   - 对接 Redis：实现该 interface 直接操作 Redis（GET/SET/DEL）
//   - 对接 Postgres：复用 storage.MemoryRepo 思路实现该 interface
type MemoryStore interface {
	// Get 读取 key 对应的值。
	// ctx 上下文；key 键名。
	// 返回值与 error（key 不存在时返回 "" 与 nil）。
	Get(ctx context.Context, key string) (string, error)
	// Set 写入 key-value。
	// ctx 上下文；key 键名；value 值。
	// 返回 error。
	Set(ctx context.Context, key, value string) error
	// Delete 删除 key。
	// ctx 上下文；key 键名。
	// 返回 error（key 不存在视为成功）。
	Delete(ctx context.Context, key string) error
}

// memoryNamespace 用户状态 key 命名空间，对应 trpc-agent-go Memory 命名空间。
const memoryNamespace = "user"

// MemoryService 用户状态增量更新服务，封装 trpc-agent-go Memory 写入。
//
// 职责：将 UserProfile + TemporalProfile 拆分为多个细粒度 key 写入 MemoryStore，
// 支持 Instruction 模板占位符注入（{{user:topics}}/{{user:tier}}/...），
// 供 Agent Prompt 构造时按需读取实际值。
//
// key 命名约定：{user:<userID>:<field>}，其中 <field> 为 topics/tier/tags/
// long_term/short_term/periodic。doc.go 中 {user:topics} 为命名空间简写，
// 实际 key 拼入 userID 以支持共享存储下的用户隔离。
//
// 二开扩展点：
//   - 替换 MemoryStore：实现 MemoryStore interface 切换底层存储后端
//   - 扩展 key 体系：新增 {user:xxx} key 并在 ResolveInstruction 占位符映射中注册
type MemoryService struct {
	store MemoryStore
}

// NewMemoryService 构造 MemoryService。
// store KV 存储后端（trpc-agent-go Memory / Redis / Postgres 均可）。
// 返回 MemoryService 实例。
func NewMemoryService(store MemoryStore) *MemoryService {
	return &MemoryService{store: store}
}

// UpdateUserState 增量写入用户状态到 MemoryStore。
// userID 用户 ID；profile 用户画像（提供 Tier/Tags/Topics）；temporal 时间画像（提供长期/短期/周期兴趣）。
// 返回 error（JSON 序列化或存储写入失败时返回）。
//
// 写入 key 列表（命名空间 {user:<userID>:<field>}）：
//   - {user:<userID>:topics}     — 用户主题（从 profile.Dynamic.Topics 派生，逗号分隔）
//   - {user:<userID>:tier}       — 用户层级（profile.Static.Tier）
//   - {user:<userID>:tags}       — 用户标签（profile.Static.Tags，逗号分隔）
//   - {user:<userID>:long_term}  — 长期兴趣（temporal.LongTerm JSON 序列化）
//   - {user:<userID>:short_term} — 短期兴趣（temporal.ShortTerm JSON 序列化）
//   - {user:<userID>:periodic}   — 周期模式（temporal.Periodic JSON 序列化）
//
// 二开：可覆盖 userKey 方法自定义 key 命名空间（如增加 channel 前缀实现频道隔离）。
func (m *MemoryService) UpdateUserState(ctx context.Context, userID string, profile domain.UserProfile, temporal domain.TemporalProfile) error {
	// topics：从 Dynamic.Topics 派生（spec 描述为 StaticProfile.Interests，
	// 实际 Topics 字段位于 DynamicProfile 层，按真实类型定义派生）。
	topics := ""
	if profile.Dynamic != nil {
		topics = strings.Join(profile.Dynamic.Topics, ",")
	}
	// tier / tags：从 Static 派生
	tier := ""
	tags := ""
	if profile.Static != nil {
		tier = profile.Static.Tier
		tags = strings.Join(profile.Static.Tags, ",")
	}
	// long_term：JSON 序列化 LongTerm（[]Interest）
	longTermJSON, err := json.Marshal(temporal.LongTerm)
	if err != nil {
		return fmt.Errorf("序列化 long_term 失败: %w", err)
	}
	// short_term：JSON 序列化 ShortTerm（[]Interest）
	shortTermJSON, err := json.Marshal(temporal.ShortTerm)
	if err != nil {
		return fmt.Errorf("序列化 short_term 失败: %w", err)
	}
	// periodic：JSON 序列化 Periodic（map[string][]Interest）
	periodicJSON, err := json.Marshal(temporal.Periodic)
	if err != nil {
		return fmt.Errorf("序列化 periodic 失败: %w", err)
	}

	kv := map[string]string{
		m.userKey(userID, "topics"):     topics,
		m.userKey(userID, "tier"):       tier,
		m.userKey(userID, "tags"):       tags,
		m.userKey(userID, "long_term"):  string(longTermJSON),
		m.userKey(userID, "short_term"): string(shortTermJSON),
		m.userKey(userID, "periodic"):   string(periodicJSON),
	}
	for k, v := range kv {
		if err := m.store.Set(ctx, k, v); err != nil {
			return fmt.Errorf("写入 key %s 失败: %w", k, err)
		}
	}
	return nil
}

// ResolveInstruction 替换 Instruction 模板中的占位符为实际值。
// userID 用户 ID；template 含 {{user:topics}}/{{user:tier}}/... 占位符的模板。
// 返回替换后的 Instruction 字符串与 error（存储读取失败时返回）。
//
// 支持的占位符：
//   - {{user:topics}}     — 用户主题
//   - {{user:tier}}       — 用户层级
//   - {{user:tags}}       — 用户标签
//   - {{user:long_term}}  — 长期兴趣 JSON
//   - {{user:short_term}} — 短期兴趣 JSON
//   - {{user:periodic}}   — 周期模式 JSON
//
// 二开：新增占位符时在 placeholderFields 中注册对应 field 名即可。
func (m *MemoryService) ResolveInstruction(ctx context.Context, userID string, template string) (string, error) {
	// 占位符名 → key field 映射
	placeholderFields := map[string]string{
		"{{user:topics}}":     "topics",
		"{{user:tier}}":       "tier",
		"{{user:tags}}":       "tags",
		"{{user:long_term}}":  "long_term",
		"{{user:short_term}}": "short_term",
		"{{user:periodic}}":   "periodic",
	}
	result := template
	for placeholder, field := range placeholderFields {
		val, err := m.store.Get(ctx, m.userKey(userID, field))
		if err != nil {
			return "", fmt.Errorf("读取 key %s 失败: %w", m.userKey(userID, field), err)
		}
		result = strings.ReplaceAll(result, placeholder, val)
	}
	return result, nil
}

// userKey 构造用户状态 key，格式 {user:<userID>:<field>}。
// userID 用户 ID；field 字段名（topics/tier/tags/long_term/short_term/periodic）。
// 返回完整 key 字符串。
func (m *MemoryService) userKey(userID, field string) string {
	return fmt.Sprintf("{%s:%s:%s}", memoryNamespace, userID, field)
}
