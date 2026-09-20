// Package cache redis_test.go — RedisCache 单元测试（Task 13.4）。
//
// 用 stdlib 手写 stub 覆盖：
//   - Get 命中 / 未命中（空串 → ErrCacheMiss）/ 真实错误透传
//   - Set/Delete 操作
//   - NoopRedisClient 默认行为
//   - nil client 安全处理
//   - ErrCacheMiss 消息
package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubRedisClient 测试用 RedisClient stub。
type stubRedisClient struct {
	mu       sync.Mutex
	data     map[string]string
	getErr   error
	setErr   error
	delErr   error
	getCalls int
	setCalls int
	delCalls int
	lastTTL  time.Duration
}

func newStubRedisClient() *stubRedisClient {
	return &stubRedisClient{data: make(map[string]string)}
}

func (s *stubRedisClient) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getCalls++
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.data[key], nil
}

func (s *stubRedisClient) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls++
	s.lastTTL = ttl
	if s.setErr != nil {
		return s.setErr
	}
	s.data[key] = value
	return nil
}

func (s *stubRedisClient) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delCalls++
	if s.delErr != nil {
		return s.delErr
	}
	for _, k := range keys {
		delete(s.data, k)
	}
	return nil
}

// TestRedisCache_GetHit 验证命中场景。
func TestRedisCache_GetHit(t *testing.T) {
	client := newStubRedisClient()
	client.data["k1"] = "v1"
	rc := NewRedisCache(client, time.Minute)

	val, err := rc.Get(context.Background(), "k1")
	if err != nil {
		t.Fatalf("Get 错误: %v", err)
	}
	if val != "v1" {
		t.Errorf("Get = %q, 期望 v1", val)
	}
}

// TestRedisCache_GetMiss 验证未命中返回 ErrCacheMiss。
func TestRedisCache_GetMiss(t *testing.T) {
	client := newStubRedisClient()
	rc := NewRedisCache(client, time.Minute)

	_, err := rc.Get(context.Background(), "nonexistent")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("Get 未命中应返回 ErrCacheMiss, 实际: %v", err)
	}
}

// TestRedisCache_GetEmptyString 验证空串转换为 ErrCacheMiss。
func TestRedisCache_GetEmptyString(t *testing.T) {
	client := newStubRedisClient()
	client.data["empty"] = ""
	rc := NewRedisCache(client, time.Minute)

	_, err := rc.Get(context.Background(), "empty")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("空串应返回 ErrCacheMiss, 实际: %v", err)
	}
}

// TestRedisCache_GetError 验证真实错误透传（不转换为 ErrCacheMiss）。
func TestRedisCache_GetError(t *testing.T) {
	client := newStubRedisClient()
	client.getErr = errors.New("redis connection refused")
	rc := NewRedisCache(client, time.Minute)

	_, err := rc.Get(context.Background(), "k1")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if errors.Is(err, ErrCacheMiss) {
		t.Error("真实错误不应被转换为 ErrCacheMiss")
	}
	if !strings.Contains(err.Error(), "redis connection refused") {
		t.Errorf("错误消息应包含原始错误, 实际: %v", err)
	}
}

// TestRedisCache_SetDelete 验证 Set 与 Delete 操作。
func TestRedisCache_SetDelete(t *testing.T) {
	client := newStubRedisClient()
	rc := NewRedisCache(client, time.Minute)

	if err := rc.Set(context.Background(), "k1", "v1"); err != nil {
		t.Fatalf("Set 错误: %v", err)
	}
	if client.setCalls != 1 {
		t.Errorf("setCalls = %d, 期望 1", client.setCalls)
	}
	if client.lastTTL != time.Minute {
		t.Errorf("lastTTL = %v, 期望 1m", client.lastTTL)
	}

	val, err := rc.Get(context.Background(), "k1")
	if err != nil || val != "v1" {
		t.Errorf("Get after Set = (%q, %v), 期望 (v1, nil)", val, err)
	}

	if err := rc.Delete(context.Background(), "k1"); err != nil {
		t.Fatalf("Delete 错误: %v", err)
	}
	if client.delCalls != 1 {
		t.Errorf("delCalls = %d, 期望 1", client.delCalls)
	}

	_, err = rc.Get(context.Background(), "k1")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("Delete 后 Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
}

// TestRedisCache_SetError 验证 Set 错误透传。
func TestRedisCache_SetError(t *testing.T) {
	client := newStubRedisClient()
	client.setErr = errors.New("redis write failure")
	rc := NewRedisCache(client, time.Minute)

	err := rc.Set(context.Background(), "k1", "v1")
	if err == nil || !strings.Contains(err.Error(), "redis write failure") {
		t.Errorf("Set 错误应透传, 实际: %v", err)
	}
}

// TestRedisCache_DeleteMultipleKeys 验证 Delete 多 key。
func TestRedisCache_DeleteMultipleKeys(t *testing.T) {
	client := newStubRedisClient()
	client.data["a"] = "1"
	client.data["b"] = "2"
	client.data["c"] = "3"
	rc := NewRedisCache(client, time.Minute)

	if err := rc.Delete(context.Background(), "a", "b"); err != nil {
		t.Fatalf("Delete 错误: %v", err)
	}
	if _, ok := client.data["a"]; ok {
		t.Error("a 应被删除")
	}
	if _, ok := client.data["b"]; ok {
		t.Error("b 应被删除")
	}
	if _, ok := client.data["c"]; !ok {
		t.Error("c 应保留")
	}
}

// TestNoopRedisClient 验证 NoopRedisClient 默认行为。
func TestNoopRedisClient(t *testing.T) {
	var client NoopRedisClient
	rc := NewRedisCache(client, time.Minute)

	_, err := rc.Get(context.Background(), "any")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("NoopRedisClient Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := rc.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("NoopRedisClient Set 应返回 nil, 实际: %v", err)
	}
	if err := rc.Delete(context.Background(), "k"); err != nil {
		t.Errorf("NoopRedisClient Delete 应返回 nil, 实际: %v", err)
	}
}

// TestRedisCache_NilClient 验证 nil client 安全处理。
func TestRedisCache_NilClient(t *testing.T) {
	rc := NewRedisCache(nil, time.Minute)

	_, err := rc.Get(context.Background(), "k")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil client Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := rc.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("nil client Set 应返回 nil, 实际: %v", err)
	}
	if err := rc.Delete(context.Background(), "k"); err != nil {
		t.Errorf("nil client Delete 应返回 nil, 实际: %v", err)
	}
}

// TestRedisCache_NilReceiver 验证 nil receiver 安全处理。
func TestRedisCache_NilReceiver(t *testing.T) {
	var rc *RedisCache
	_, err := rc.Get(context.Background(), "k")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("nil receiver Get 应返回 ErrCacheMiss, 实际: %v", err)
	}
	if err := rc.Set(context.Background(), "k", "v"); err != nil {
		t.Errorf("nil receiver Set 应返回 nil, 实际: %v", err)
	}
	if err := rc.Delete(context.Background(), "k"); err != nil {
		t.Errorf("nil receiver Delete 应返回 nil, 实际: %v", err)
	}
}

// TestErrCacheMissMessage 验证 ErrCacheMiss 错误消息。
func TestErrCacheMissMessage(t *testing.T) {
	if !strings.Contains(ErrCacheMiss.Error(), "miss") {
		t.Errorf("ErrCacheMiss 消息应包含 'miss', 实际: %q", ErrCacheMiss.Error())
	}
	if !errors.Is(ErrCacheMiss, ErrCacheMiss) {
		t.Error("errors.Is(ErrCacheMiss, ErrCacheMiss) 应为 true")
	}
}

// TestRedisCache_TTLPropagation 验证 TTL 透传给底层 client。
func TestRedisCache_TTLPropagation(t *testing.T) {
	client := newStubRedisClient()
	rc := NewRedisCache(client, 30*time.Second)

	_ = rc.Set(context.Background(), "k", "v")
	if client.lastTTL != 30*time.Second {
		t.Errorf("TTL = %v, 期望 30s", client.lastTTL)
	}
}
