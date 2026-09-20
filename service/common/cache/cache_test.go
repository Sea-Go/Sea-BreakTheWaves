// Package cache cache_test.go — TieredCache + L3PromptCache 单元测试（Task 13.4）。
//
// 用 stdlib 手写 stub 覆盖：
//   - TieredCache L1 命中 / L2 命中回填 / miss
//   - Set 同时写 L1+L2 / Delete 同时删 L1+L2
//   - Stats 统计正确性 / HitRate 计算
//   - 无 L2 场景
//   - 并发安全
//   - L3PromptCache Get/Set/miss/nil client
package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stubPromptCacheClient 测试用 PromptCacheClient stub。
type stubPromptCacheClient struct {
	mu       sync.Mutex
	data     map[string]string
	getErr   error
	setErr   error
	getCalls int
	setCalls int
}

func newStubPromptCacheClient() *stubPromptCacheClient {
	return &stubPromptCacheClient{data: make(map[string]string)}
}

func (s *stubPromptCacheClient) GetCached(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.getErr != nil {
		return "", s.getErr
	}
	val, ok := s.data[key]
	if !ok {
		return "", ErrCacheMiss
	}
	return val, nil
}

func (s *stubPromptCacheClient) SetCached(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls++
	if s.setErr != nil {
		return s.setErr
	}
	s.data[key] = value
	return nil
}

// TestTieredCache_L1Hit 验证 L1 命中。
func TestTieredCache_L1Hit(t *testing.T) {
	l1 := NewLRUCache(10)
	l2 := NewRedisCache(newStubRedisClient(), time.Minute)
	tc := NewTieredCache(l1, l2)

	tc.Set(context.Background(), "k1", "v1")
	val, err := tc.Get(context.Background(), "k1")
	if err != nil || val != "v1" {
		t.Fatalf("Get = (%v, %v), 期望 (v1, nil)", val, err)
	}

	stats := tc.Stats()
	if stats.L1Hits != 1 {
		t.Errorf("L1Hits = %d, 期望 1", stats.L1Hits)
	}
	if stats.L2Hits != 0 {
		t.Errorf("L2Hits = %d, 期望 0", stats.L2Hits)
	}
	if stats.Total != 1 {
		t.Errorf("Total = %d, 期望 1", stats.Total)
	}
}

// TestTieredCache_L2HitAndBackfill 验证 L2 命中后回填 L1。
func TestTieredCache_L2HitAndBackfill(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	client.data["k1"] = "v1"
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	// L1 未命中，L2 命中。
	val, err := tc.Get(context.Background(), "k1")
	if err != nil || val != "v1" {
		t.Fatalf("Get = (%v, %v), 期望 (v1, nil)", val, err)
	}

	stats := tc.Stats()
	if stats.L1Hits != 0 {
		t.Errorf("L1Hits = %d, 期望 0", stats.L1Hits)
	}
	if stats.L2Hits != 1 {
		t.Errorf("L2Hits = %d, 期望 1", stats.L2Hits)
	}

	// 验证回填 L1。
	if v, ok := l1.Get("k1"); !ok || v != "v1" {
		t.Errorf("L2 命中后应回填 L1, l1.Get(k1) = (%v, %v)", v, ok)
	}

	// 第二次 Get：L1 命中。
	val2, err2 := tc.Get(context.Background(), "k1")
	if err2 != nil || val2 != "v1" {
		t.Fatalf("第二次 Get = (%v, %v), 期望 (v1, nil)", val2, err2)
	}
	stats2 := tc.Stats()
	if stats2.L1Hits != 1 {
		t.Errorf("回填后 L1Hits = %d, 期望 1", stats2.L1Hits)
	}
	if stats2.L2Hits != 1 {
		t.Errorf("L2Hits 应保持 1, 实际 %d", stats2.L2Hits)
	}
}

// TestTieredCache_Miss 验证 L1/L2 均未命中。
func TestTieredCache_Miss(t *testing.T) {
	l1 := NewLRUCache(10)
	l2 := NewRedisCache(newStubRedisClient(), time.Minute)
	tc := NewTieredCache(l1, l2)

	_, err := tc.Get(context.Background(), "nonexistent")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("Get 未命中应返回 ErrCacheMiss, 实际: %v", err)
	}

	stats := tc.Stats()
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, 期望 1", stats.Misses)
	}
	if stats.Total != 1 {
		t.Errorf("Total = %d, 期望 1", stats.Total)
	}
}

// TestTieredCache_SetWritesBoth 验证 Set 同时写 L1 + L2。
func TestTieredCache_SetWritesBoth(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	if err := tc.Set(context.Background(), "k1", "v1"); err != nil {
		t.Fatalf("Set 错误: %v", err)
	}

	// 验证 L1。
	if v, ok := l1.Get("k1"); !ok || v != "v1" {
		t.Errorf("L1 未写入: Get(k1) = (%v, %v)", v, ok)
	}
	// 验证 L2。
	if v, ok := client.data["k1"]; !ok || v != "v1" {
		t.Errorf("L2 未写入: data[k1] = (%v, %v)", v, ok)
	}
}

// TestTieredCache_SetNonStringSkipsL2 验证非 string 值仅写 L1。
func TestTieredCache_SetNonStringSkipsL2(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	// 设置 int 值（非 string），应仅写 L1。
	if err := tc.Set(context.Background(), "k1", 123); err != nil {
		t.Fatalf("Set 错误: %v", err)
	}
	if v, ok := l1.Get("k1"); !ok || v != 123 {
		t.Errorf("L1 应写入, Get(k1) = (%v, %v)", v, ok)
	}
	if _, ok := client.data["k1"]; ok {
		t.Error("L2 不应写入非 string 值")
	}
	if client.setCalls != 0 {
		t.Errorf("L2 setCalls = %d, 期望 0", client.setCalls)
	}
}

// TestTieredCache_DeleteBoth 验证 Delete 同时删 L1 + L2。
func TestTieredCache_DeleteBoth(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	tc.Set(context.Background(), "k1", "v1")
	if err := tc.Delete(context.Background(), "k1"); err != nil {
		t.Fatalf("Delete 错误: %v", err)
	}

	if _, ok := l1.Get("k1"); ok {
		t.Error("Delete 后 L1 应无 k1")
	}
	if _, ok := client.data["k1"]; ok {
		t.Error("Delete 后 L2 应无 k1")
	}
}

// TestTieredCache_HitRate 验证命中率计算。
func TestTieredCache_HitRate(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	client.data["l2key"] = "v2"
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	tc.Set(context.Background(), "l1key", "v1")
	tc.Get(context.Background(), "l1key")   // L1 hit
	tc.Get(context.Background(), "l2key")   // L2 hit
	tc.Get(context.Background(), "misskey") // miss

	// HitRate = (1 + 1) / 3 ≈ 0.667
	hr := tc.HitRate()
	if hr < 0.66 || hr > 0.67 {
		t.Errorf("HitRate = %f, 期望约 0.667", hr)
	}
}

// TestTieredCache_HitRateZeroTotal 验证 Total=0 时 HitRate 返回 0。
func TestTieredCache_HitRateZeroTotal(t *testing.T) {
	tc := NewTieredCache(NewLRUCache(10), NewRedisCache(newStubRedisClient(), time.Minute))
	if hr := tc.HitRate(); hr != 0 {
		t.Errorf("Total=0 时 HitRate 应为 0, 实际: %f", hr)
	}
}

// TestTieredCache_Stats 验证完整统计。
func TestTieredCache_Stats(t *testing.T) {
	l1 := NewLRUCache(10)
	client := newStubRedisClient()
	client.data["l2key"] = "v2"
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	tc.Set(context.Background(), "l1key", "v1")
	tc.Get(context.Background(), "l1key")   // L1 hit
	tc.Get(context.Background(), "l2key")   // L2 hit
	tc.Get(context.Background(), "misskey") // miss

	stats := tc.Stats()
	if stats.L1Hits != 1 {
		t.Errorf("L1Hits = %d, 期望 1", stats.L1Hits)
	}
	if stats.L2Hits != 1 {
		t.Errorf("L2Hits = %d, 期望 1", stats.L2Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, 期望 1", stats.Misses)
	}
	if stats.Total != 3 {
		t.Errorf("Total = %d, 期望 3", stats.Total)
	}
}

// TestTieredCache_NoL2 验证无 L2 时仍可工作。
func TestTieredCache_NoL2(t *testing.T) {
	l1 := NewLRUCache(10)
	tc := NewTieredCache(l1, nil)

	tc.Set(context.Background(), "k1", "v1")
	val, err := tc.Get(context.Background(), "k1")
	if err != nil || val != "v1" {
		t.Errorf("Get = (%v, %v), 期望 (v1, nil)", val, err)
	}
	_, err = tc.Get(context.Background(), "miss")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("无 L2 时 miss 应返回 ErrCacheMiss, 实际: %v", err)
	}
	// 验证 Delete 不 panic。
	if err := tc.Delete(context.Background(), "k1"); err != nil {
		t.Errorf("无 L2 时 Delete 应返回 nil, 实际: %v", err)
	}
}

// TestTieredCache_NoL1 验证无 L1 时仍可工作。
func TestTieredCache_NoL1(t *testing.T) {
	client := newStubRedisClient()
	client.data["k1"] = "v1"
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(nil, l2)

	val, err := tc.Get(context.Background(), "k1")
	if err != nil || val != "v1" {
		t.Errorf("Get = (%v, %v), 期望 (v1, nil)", val, err)
	}
	stats := tc.Stats()
	if stats.L2Hits != 1 {
		t.Errorf("L2Hits = %d, 期望 1", stats.L2Hits)
	}
}

// TestTieredCache_Concurrent 验证并发安全。
func TestTieredCache_Concurrent(t *testing.T) {
	l1 := NewLRUCache(100)
	client := newStubRedisClient()
	l2 := NewRedisCache(client, time.Minute)
	tc := NewTieredCache(l1, l2)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := "k" + string(rune('a'+idx%26))
			_ = tc.Set(context.Background(), key, "v")
			_, _ = tc.Get(context.Background(), key)
			_ = tc.Delete(context.Background(), key)
		}(i)
	}
	wg.Wait()
	// 验证不 panic、不 race。
	_ = tc.Stats()
	_ = tc.HitRate()
}

// TestTieredCache_NilReceiver 验证 nil receiver 安全处理。
func TestTieredCache_NilReceiver(t *testing.T) {
	var tc *TieredCache
	_, err := tc.Get(context.Background(), "k")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := tc.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("nil Set 应返回 nil, 实际: %v", err)
	}
	if err := tc.Delete(context.Background(), "k"); err != nil {
		t.Errorf("nil Delete 应返回 nil, 实际: %v", err)
	}
	if stats := tc.Stats(); stats != (CacheStats{}) {
		t.Errorf("nil Stats 应返回零值, got %+v", stats)
	}
	if hr := tc.HitRate(); hr != 0 {
		t.Errorf("nil HitRate 应返回 0, got %f", hr)
	}
}

// TestL3PromptCache_GetSet 验证 L3 Prompt Cache 基本 Get/Set。
func TestL3PromptCache_GetSet(t *testing.T) {
	client := newStubPromptCacheClient()
	l3 := NewL3PromptCache(client)

	if err := l3.Set(context.Background(), "p1", "response1"); err != nil {
		t.Fatalf("Set 错误: %v", err)
	}
	if client.setCalls != 1 {
		t.Errorf("setCalls = %d, 期望 1", client.setCalls)
	}

	val, err := l3.Get(context.Background(), "p1")
	if err != nil || val != "response1" {
		t.Errorf("Get = (%q, %v), 期望 (response1, nil)", val, err)
	}
	if client.getCalls != 1 {
		t.Errorf("getCalls = %d, 期望 1", client.getCalls)
	}
}

// TestL3PromptCache_Miss 验证未命中返回 ErrCacheMiss。
func TestL3PromptCache_Miss(t *testing.T) {
	client := newStubPromptCacheClient()
	l3 := NewL3PromptCache(client)

	_, err := l3.Get(context.Background(), "nonexistent")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("Get 未命中应返回 ErrCacheMiss, 实际: %v", err)
	}
}

// TestL3PromptCache_NilClient 验证 nil client 安全处理。
func TestL3PromptCache_NilClient(t *testing.T) {
	l3 := NewL3PromptCache(nil)

	_, err := l3.Get(context.Background(), "k")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil client Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := l3.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("nil client Set 应返回 nil, 实际: %v", err)
	}
}

// TestL3PromptCache_NilReceiver 验证 nil receiver 安全处理。
func TestL3PromptCache_NilReceiver(t *testing.T) {
	var l3 *L3PromptCache
	_, err := l3.Get(context.Background(), "k")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil receiver Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := l3.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("nil receiver Set 应返回 nil, 实际: %v", err)
	}
}

// TestL3PromptCache_ErrorPropagation 验证错误透传。
func TestL3PromptCache_ErrorPropagation(t *testing.T) {
	client := newStubPromptCacheClient()
	client.getErr = errors.New("prompt cache backend down")
	l3 := NewL3PromptCache(client)

	_, err := l3.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if errors.Is(err, ErrCacheMiss) {
		t.Error("真实错误不应被转换为 ErrCacheMiss")
	}
}
