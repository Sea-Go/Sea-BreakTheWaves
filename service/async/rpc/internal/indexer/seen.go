package indexer

import "sync"

// MemSeen 是 SeenStore 的进程内实现：mutex 保护的 event_id 集合。重启即
// 失忆（与 artifact.Switcher 同一边界声明）；跨进程幂等由持久化实现替换。
type MemSeen struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewMemSeen 返回空的内存账本。
func NewMemSeen() *MemSeen {
	return &MemSeen{seen: make(map[string]struct{})}
}

// Claim 原子记账：首次见到 eventID 返回 true；已存在返回 false。
func (s *MemSeen) Claim(eventID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[eventID]; ok {
		return false
	}
	s.seen[eventID] = struct{}{}
	return true
}

// Release 撤销记账，使失败的 eventID 可重试。不存在的 ID 是 no-op。
func (s *MemSeen) Release(eventID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, eventID)
}

// Len 返回当前记账条数（测试辅助）。
func (s *MemSeen) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}
