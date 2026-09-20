package session

import (
	"context"
	"fmt"
)

// ============================================================================
// 该文件实现 SummaryService：Session Summary 异步压缩 + session_load/session_search。
// 对应 trpc-agent-go 的 SummaryBoundary / session_search / session_load 能力。
// 当前为 stub 实现（压缩逻辑待对接 trpc-agent-go Summary API），仅依赖标准库。
// ============================================================================

// sessionNamespace Session Summary key 命名空间。
const sessionNamespace = "session"

// summaryField Summary key 字段名。
const summaryField = "summary"

// SummaryConfig Summary 服务配置。
//
// 二开：通过 config.yaml 注入，控制异步/同步策略与触发边界。
type SummaryConfig struct {
	// Async 是否异步生成 Summary（true 时不阻塞主链路，用 goroutine 触发）。
	Async bool
	// Boundary 触发 Summary 的消息数边界（msgCount >= Boundary 时触发）。
	Boundary int
	// SyncSummaryIntraRun 仅长 ReAct loop 同步生成 Summary。
	// 对应 trpc-agent-go 的 WithSyncSummaryIntraRun(true) 配置：
	// 默认异步 Summary 不阻塞主链路，仅长 ReAct loop 同步生成。
	// TODO 对接 trpc-agent-go：桥接至 trpc-agent-go WithSyncSummaryIntraRun 选项。
	SyncSummaryIntraRun bool
}

// SummaryService Session Summary 压缩服务。
//
// 职责：当会话消息数达到 Boundary 时触发 Summary 压缩（异步/同步），
// 将会话历史压缩为摘要存入 {session:<sessionID>:summary} key，
// 支持 session_load 恢复摘要与 session_search 搜索摘要。
//
// 二开扩展点：
//   - 自定义压缩逻辑：覆盖 generateSummary 方法注入 LLM 压缩
//   - 替换 MemoryStore：实现 MemoryStore interface 切换存储后端
//   - 向量搜索：覆盖 SearchSummary 方法对接 Milvus / trpc-agent-go vector store
type SummaryService struct {
	cfg   SummaryConfig
	store MemoryStore
}

// NewSummaryService 构造 SummaryService。
// cfg Summary 配置；store KV 存储后端。
// 返回 SummaryService 实例。
func NewSummaryService(cfg SummaryConfig, store MemoryStore) *SummaryService {
	return &SummaryService{cfg: cfg, store: store}
}

// MaybeSummary 根据消息数判断是否触发 Summary 压缩。
// sessionID 会话 ID；msgCount 当前会话消息数。
// 返回 error（同步模式下生成失败返回错误；异步模式立即返回 nil）。
//
// 触发条件：msgCount >= Boundary。
// 异步模式（cfg.Async=true）：用 goroutine 触发，不阻塞主链路。
// 同步模式（cfg.Async=false）：阻塞直至生成完成。
func (s *SummaryService) MaybeSummary(ctx context.Context, sessionID string, msgCount int) error {
	if msgCount < s.cfg.Boundary {
		return nil
	}
	if s.cfg.Async {
		// TODO 可替换为 worker pool，避免 goroutine 泄漏与失控
		go s.generateSummary(ctx, sessionID)
		return nil
	}
	return s.generateSummary(ctx, sessionID)
}

// generateSummary 生成 Summary 并存入 {session:<sessionID>:summary} key。
// sessionID 会话 ID。
// 返回 error（存储写入失败时返回）。
//
// TODO 对接 trpc-agent-go Summary API：调用 LLM 压缩会话历史为摘要。
// 当前为 stub 实现，仅写入占位摘要。
func (s *SummaryService) generateSummary(ctx context.Context, sessionID string) error {
	// TODO 替换为 trpc-agent-go Summary 压缩逻辑（LLM 调用）
	summary := fmt.Sprintf("session %s summary (stub)", sessionID)
	key := s.sessionKey(sessionID)
	if err := s.store.Set(ctx, key, summary); err != nil {
		return fmt.Errorf("写入 summary key %s 失败: %w", key, err)
	}
	return nil
}

// LoadSummary 恢复 Session Summary（session_load）。
// sessionID 会话 ID。
// 返回摘要字符串与 error（key 不存在时返回 "" 与 nil）。
func (s *SummaryService) LoadSummary(ctx context.Context, sessionID string) (string, error) {
	key := s.sessionKey(sessionID)
	val, err := s.store.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("读取 summary key %s 失败: %w", key, err)
	}
	return val, nil
}

// SearchSummary session_search 搜索 Summary。
// query 搜索查询。
// 返回匹配的摘要列表与 error。
//
// TODO 实际向量搜索待集成：对接 Milvus 或 trpc-agent-go vector store，
// 当前为 stub 返回空列表。
func (s *SummaryService) SearchSummary(ctx context.Context, query string) ([]string, error) {
	// stub：实际向量搜索待集成
	_ = query
	return nil, nil
}

// sessionKey 构造 Session Summary key，格式 {session:<sessionID>:summary}。
// sessionID 会话 ID。
// 返回完整 key 字符串。
func (s *SummaryService) sessionKey(sessionID string) string {
	return fmt.Sprintf("{%s:%s:%s}", sessionNamespace, sessionID, summaryField)
}
