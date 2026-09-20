// Package search history.go — 搜索历史与点击反馈。
//
// 该文件实现搜索历史记录与点击反馈：
//   - HistoryStore interface 抽象历史存储后端（Postgres/Redis/内存）
//   - HistoryService 业务服务层，封装历史记录/查询/点击反馈
//   - MemoryHistoryStore 内存实现，用于测试与默认兜底
//
// 二开扩展点：实现 HistoryStore interface 对接 Postgres/Redis 等后端，
// 通过 NewHistoryService 注入即可替换默认内存实现。
package search

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"sea/internal/domain"
)

// HistoryStore 搜索历史存储抽象。
//
// 职责：持久化用户搜索历史与点击反馈，使搜索服务与具体存储后端解耦。
// 注意：本 interface 与 Task 10.5 的 HistoryRepo 区别开，本文件用 HistoryStore 名字。
//
// 二开扩展点：实现该 interface 对接 Postgres/Redis/ES 等后端，
// 通过 NewHistoryService 注入即可。
type HistoryStore interface {
	// Record 记录一次搜索。
	// ctx 上下文；entry 搜索历史条目（含查询词/用户/时间）。
	// 返回 error。
	Record(ctx context.Context, entry domain.SearchHistoryEntry) error
	// List 列出用户搜索历史，按时间倒序，最多 topK 条。
	// ctx 上下文；userID 用户 ID；topK 返回数量上限（<=0 时返回全部）。
	// 返回历史条目列表与 error。
	List(ctx context.Context, userID string, topK int) ([]domain.SearchHistoryEntry, error)
	// RecordClick 记录点击反馈，将用户点击的文章 ID 关联到最近一次搜索。
	// ctx 上下文；userID 用户 ID；query 搜索词；articleID 点击的文章 ID。
	// 返回 error。
	RecordClick(ctx context.Context, userID, query, articleID string) error
}

// HistoryService 搜索历史业务服务，封装历史记录/查询/点击反馈。
//
// 职责：在 HistoryStore 之上提供业务语义方法，自动填充时间戳，
// 屏蔽底层存储细节，供 SearchAgent 与搜索 API 调用。
type HistoryService struct {
	store HistoryStore
}

// NewHistoryService 构造 HistoryService。
// store 历史存储后端（实现 HistoryStore interface）。
// 返回 HistoryService 实例。
func NewHistoryService(store HistoryStore) *HistoryService {
	return &HistoryService{store: store}
}

// Record 记录一次搜索，自动填充 SearchedAt（若调用方未设置）。
// ctx 上下文；entry 搜索历史条目。
// 返回 error。
func (s *HistoryService) Record(ctx context.Context, entry domain.SearchHistoryEntry) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("history service: store 未注入")
	}
	if entry.SearchedAt == 0 {
		entry.SearchedAt = time.Now().UnixMilli()
	}
	return s.store.Record(ctx, entry)
}

// List 列出用户搜索历史，按时间倒序。
// ctx 上下文；userID 用户 ID；topK 返回数量上限（<=0 时返回全部）。
// 返回历史条目列表与 error。
func (s *HistoryService) List(ctx context.Context, userID string, topK int) ([]domain.SearchHistoryEntry, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("history service: store 未注入")
	}
	if userID == "" {
		return nil, nil
	}
	return s.store.List(ctx, userID, topK)
}

// RecordClick 记录点击反馈。
// ctx 上下文；userID 用户 ID；query 搜索词；articleID 点击的文章 ID。
// 返回 error。
func (s *HistoryService) RecordClick(ctx context.Context, userID, query, articleID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("history service: store 未注入")
	}
	if userID == "" || articleID == "" {
		return fmt.Errorf("history service: userID 与 articleID 不能为空")
	}
	return s.store.RecordClick(ctx, userID, query, articleID)
}

// MemoryHistoryStore 内存历史存储，用于测试与默认兜底。
//
// 线程安全（sync.Mutex 保护 slice）。数据仅存内存，进程重启丢失。
// 适合单测与无外部依赖的默认实现；生产环境应替换为 Postgres/Redis 实现。
type MemoryHistoryStore struct {
	mu      sync.Mutex
	entries []domain.SearchHistoryEntry
}

// NewMemoryHistoryStore 构造内存历史存储。
func NewMemoryHistoryStore() *MemoryHistoryStore {
	return &MemoryHistoryStore{}
}

// Record 记录一次搜索到内存 slice。
// 实现 HistoryStore.Record。
func (m *MemoryHistoryStore) Record(_ context.Context, entry domain.SearchHistoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

// List 列出用户搜索历史，按时间倒序，最多 topK 条。
// 实现 HistoryStore.List。
func (m *MemoryHistoryStore) List(_ context.Context, userID string, topK int) ([]domain.SearchHistoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if userID == "" {
		return nil, nil
	}
	// 收集该用户的条目。
	filtered := make([]domain.SearchHistoryEntry, 0)
	for _, e := range m.entries {
		if e.UserID == userID {
			filtered = append(filtered, e)
		}
	}
	// 按时间倒序排序。
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].SearchedAt > filtered[j].SearchedAt
	})
	if topK > 0 && len(filtered) > topK {
		filtered = filtered[:topK]
	}
	return filtered, nil
}

// RecordClick 记录点击反馈，将点击事件关联到该用户最近一次匹配 query 的搜索条目。
// 若找不到匹配条目，则追加一条新的点击记录。
// 实现 HistoryStore.RecordClick。
func (m *MemoryHistoryStore) RecordClick(_ context.Context, userID, query, articleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UnixMilli()
	// 倒序查找最近一条匹配 (userID, query) 且未点击的记录。
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.UserID == userID && e.Query == query && e.ClickedArticleID == "" {
			e.ClickedArticleID = articleID
			e.ClickedAt = now
			return nil
		}
	}
	// 未找到匹配记录，追加一条点击记录。
	m.entries = append(m.entries, domain.SearchHistoryEntry{
		Query:            query,
		UserID:           userID,
		SearchedAt:       now,
		ClickedArticleID: articleID,
		ClickedAt:        now,
	})
	return nil
}
