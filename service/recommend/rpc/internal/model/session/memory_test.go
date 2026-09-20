package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 MemoryService 的增量更新与 Instruction 占位符替换。
// 覆盖：
//   - UpdateUserState 写入全部 6 个 key 且值正确
//   - ResolveInstruction 替换 {{user:topics}}/{{user:tier}}/... 占位符
//   - 静态/动态画像为 nil 时不 panic
// ============================================================================

// mockMemoryStore 内存版 MemoryStore，用于单测 mock。
type mockMemoryStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newMockMemoryStore() *mockMemoryStore {
	return &mockMemoryStore{data: make(map[string]string)}
}

func (s *mockMemoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

func (s *mockMemoryStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *mockMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// snapshot 返回 data 副本，避免测试读取时被并发修改。
func (s *mockMemoryStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// TestMemoryService_UpdateUserState 验证 UpdateUserState 写入全部 6 个 key 且值正确。
func TestMemoryService_UpdateUserState(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewMemoryService(store)

	profile := domain.UserProfile{
		Static: &domain.StaticProfile{
			Tier: "vip",
			Tags: []string{"tech", "ai"},
		},
		Dynamic: &domain.DynamicProfile{
			Topics: []string{"go", "rust"},
		},
	}
	temporal := domain.TemporalProfile{
		LongTerm:  []domain.Interest{{Tag: "ml", Weight: 0.9}},
		ShortTerm: []domain.Interest{{Tag: "news", Weight: 0.5}},
		Periodic:  map[string][]domain.Interest{"weekday-9": {{Tag: "coffee", Weight: 0.3}}},
	}

	if err := svc.UpdateUserState(context.Background(), "u1", profile, temporal); err != nil {
		t.Fatalf("UpdateUserState 返回错误: %v", err)
	}

	data := store.snapshot()
	// topics
	if got := data["{user:u1:topics}"]; got != "go,rust" {
		t.Errorf("{user:u1:topics} = %q, 期望 go,rust", got)
	}
	// tier
	if got := data["{user:u1:tier}"]; got != "vip" {
		t.Errorf("{user:u1:tier} = %q, 期望 vip", got)
	}
	// tags
	if got := data["{user:u1:tags}"]; got != "tech,ai" {
		t.Errorf("{user:u1:tags} = %q, 期望 tech,ai", got)
	}
	// long_term：与 json.Marshal 输出一致
	wantLong, _ := json.Marshal(temporal.LongTerm)
	if got := data["{user:u1:long_term}"]; got != string(wantLong) {
		t.Errorf("{user:u1:long_term} = %q, 期望 %q", got, string(wantLong))
	}
	// short_term
	wantShort, _ := json.Marshal(temporal.ShortTerm)
	if got := data["{user:u1:short_term}"]; got != string(wantShort) {
		t.Errorf("{user:u1:short_term} = %q, 期望 %q", got, string(wantShort))
	}
	// periodic
	wantPeriodic, _ := json.Marshal(temporal.Periodic)
	if got := data["{user:u1:periodic}"]; got != string(wantPeriodic) {
		t.Errorf("{user:u1:periodic} = %q, 期望 %q", got, string(wantPeriodic))
	}
	// 确认写入 6 个 key
	if len(data) != 6 {
		t.Errorf("写入 key 数 = %d, 期望 6（data=%v）", len(data), data)
	}
}

// TestMemoryService_UpdateUserState_NilProfiles 验证 Static/Dynamic 为 nil 时不 panic 且对应值为空。
func TestMemoryService_UpdateUserState_NilProfiles(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewMemoryService(store)

	profile := domain.UserProfile{} // Static/Dynamic 均为 nil
	temporal := domain.TemporalProfile{}

	if err := svc.UpdateUserState(context.Background(), "u2", profile, temporal); err != nil {
		t.Fatalf("UpdateUserState 返回错误: %v", err)
	}
	data := store.snapshot()
	if got := data["{user:u2:topics}"]; got != "" {
		t.Errorf("{user:u2:topics} = %q, 期望空串", got)
	}
	if got := data["{user:u2:tier}"]; got != "" {
		t.Errorf("{user:u2:tier} = %q, 期望空串", got)
	}
	if got := data["{user:u2:tags}"]; got != "" {
		t.Errorf("{user:u2:tags} = %q, 期望空串", got)
	}
}

// TestMemoryService_ResolveInstruction 验证占位符替换为实际值。
func TestMemoryService_ResolveInstruction(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewMemoryService(store)

	profile := domain.UserProfile{
		Static:  &domain.StaticProfile{Tier: "vip", Tags: []string{"tech"}},
		Dynamic: &domain.DynamicProfile{Topics: []string{"go", "rust"}},
	}
	temporal := domain.TemporalProfile{
		LongTerm:  []domain.Interest{{Tag: "ml"}},
		ShortTerm: []domain.Interest{{Tag: "news"}},
		Periodic:  map[string][]domain.Interest{"weekday-9": {{Tag: "coffee"}}},
	}
	if err := svc.UpdateUserState(context.Background(), "u1", profile, temporal); err != nil {
		t.Fatalf("UpdateUserState 返回错误: %v", err)
	}

	template := "topics={{user:topics}};tier={{user:tier}};tags={{user:tags}};long={{user:long_term}};short={{user:short_term}};periodic={{user:periodic}}"
	got, err := svc.ResolveInstruction(context.Background(), "u1", template)
	if err != nil {
		t.Fatalf("ResolveInstruction 返回错误: %v", err)
	}
	wantLong, _ := json.Marshal(temporal.LongTerm)
	wantShort, _ := json.Marshal(temporal.ShortTerm)
	wantPeriodic, _ := json.Marshal(temporal.Periodic)
	want := "topics=go,rust;tier=vip;tags=tech;long=" + string(wantLong) +
		";short=" + string(wantShort) + ";periodic=" + string(wantPeriodic)
	if got != want {
		t.Errorf("ResolveInstruction 结果不匹配\n got: %s\nwant: %s", got, want)
	}
}

// TestMemoryService_ResolveInstruction_NoData 验证未写入数据时占位符替换为空串。
func TestMemoryService_ResolveInstruction_NoData(t *testing.T) {
	store := newMockMemoryStore()
	svc := NewMemoryService(store)

	got, err := svc.ResolveInstruction(context.Background(), "nobody", "topics={{user:topics}};tier={{user:tier}}")
	if err != nil {
		t.Fatalf("ResolveInstruction 返回错误: %v", err)
	}
	if got != "topics=;tier=" {
		t.Errorf("ResolveInstruction = %q, 期望 topics=;tier=", got)
	}
}
