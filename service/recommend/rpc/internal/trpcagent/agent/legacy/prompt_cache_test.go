// Package agent prompt_cache_test.go — PromptCacheOptimizer 单元测试（Task 13.7）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - BuildCacheKey 按 channel/user/intent/prefix 构建
//   - OptimizePrompt 按 \n\n---\n\n 拆分
//   - Complete cache hit/miss 流程
//   - Metrics 命中率统计
//
// stub 命名加 PC（PromptCache）前缀避免与已有 stub 冲突。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现（PC 前缀 = PromptCache）
// ----------------------------------------------------------------------------

// stubPCLLM 测试用 LLMClient stub。
type stubPCLLM struct {
	mu       sync.Mutex
	calls    int64
	lastOpts LLMOptions
	resp     string
	err      error
}

func (s *stubPCLLM) Complete(_ context.Context, _ string, opts LLMOptions) (string, error) {
	atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	s.lastOpts = opts
	s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	return s.resp, nil
}

func (s *stubPCLLM) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}

func (s *stubPCLLM) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return "", nil, nil
}

// stubPCCache 测试用 L3Cache stub。
type stubPCCache struct {
	mu      sync.Mutex
	store   map[string]string
	getErr  error
	setErr  error
	gets    int64
	sets    int64
	missErr error // GetCached 未命中时返回的错误（默认 ErrPromptCacheMiss）
}

func newStubPCCache() *stubPCCache {
	return &stubPCCache{
		store:   make(map[string]string),
		missErr: ErrPromptCacheMiss,
	}
}

func (s *stubPCCache) GetCached(_ context.Context, key string) (string, error) {
	atomic.AddInt64(&s.gets, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", s.getErr
	}
	if v, ok := s.store[key]; ok {
		return v, nil
	}
	return "", s.missErr
}

func (s *stubPCCache) SetCached(_ context.Context, key string, value string) error {
	atomic.AddInt64(&s.sets, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.store[key] = value
	return nil
}

// 编译期断言：stub 实现 interface。
var _ LLMClient = (*stubPCLLM)(nil)
var _ L3Cache = (*stubPCCache)(nil)

// makeLongPrompt 生成长度 >= minLen 的 prompt（用于测试 MinPromptLength）。
func makeLongPrompt(prefix, suffix string, minLen int) string {
	p := prefix + "\n\n---\n\n" + suffix
	for len(p) < minLen {
		p = "x" + p
	}
	return p
}

// ----------------------------------------------------------------------------
// BuildCacheKey 测试
// ----------------------------------------------------------------------------

// TestBuildCacheKey_Basic 验证基本 key 格式。
func TestBuildCacheKey_Basic(t *testing.T) {
	cfg := PromptCacheConfig{
		Enabled:           true,
		CacheKeyByChannel: true,
		CacheKeyByUser:    false,
	}
	o := NewPromptCacheOptimizer(nil, cfg)
	key := o.BuildCacheKey(PromptCacheRequest{
		UserID:  "u1",
		Channel: "tech",
		Query:   "hello",
		Intent:  "informational",
	})
	// key 应包含 channel:intent:prefix。
	if key == "" {
		t.Error("key 不应为空")
	}
	if !contains(key, "channel:tech") {
		t.Errorf("key 应包含 channel:tech, 实际 %q", key)
	}
	if !contains(key, "intent:informational") {
		t.Errorf("key 应包含 intent:informational, 实际 %q", key)
	}
	if !contains(key, "prefix:") {
		t.Errorf("key 应包含 prefix:, 实际 %q", key)
	}
}

// TestBuildCacheKey_WithUser 验证 CacheKeyByUser=true 包含 user。
func TestBuildCacheKey_WithUser(t *testing.T) {
	cfg := PromptCacheConfig{
		Enabled:           true,
		CacheKeyByChannel: true,
		CacheKeyByUser:    true,
	}
	o := NewPromptCacheOptimizer(nil, cfg)
	key := o.BuildCacheKey(PromptCacheRequest{
		UserID:  "u1",
		Channel: "tech",
		Query:   "hello",
		Intent:  "informational",
	})
	if !contains(key, "user:u1") {
		t.Errorf("key 应包含 user:u1, 实际 %q", key)
	}
}

// TestBuildCacheKey_NoChannel 验证 CacheKeyByChannel=false 不包含 channel。
func TestBuildCacheKey_NoChannel(t *testing.T) {
	cfg := PromptCacheConfig{
		Enabled:           true,
		CacheKeyByChannel: false,
		CacheKeyByUser:    false,
	}
	o := NewPromptCacheOptimizer(nil, cfg)
	key := o.BuildCacheKey(PromptCacheRequest{
		UserID:  "u1",
		Channel: "tech",
		Query:   "hello",
		Intent:  "informational",
	})
	if contains(key, "channel:") {
		t.Errorf("key 不应包含 channel:, 实际 %q", key)
	}
}

// TestBuildCacheKey_Deterministic 验证相同输入生成相同 key。
func TestBuildCacheKey_Deterministic(t *testing.T) {
	cfg := PromptCacheConfig{Enabled: true, CacheKeyByChannel: true, CacheKeyByUser: true}
	o := NewPromptCacheOptimizer(nil, cfg)
	req := PromptCacheRequest{
		UserID:  "u1",
		Channel: "tech",
		Query:   "hello",
		Intent:  "informational",
	}
	key1 := o.BuildCacheKey(req)
	key2 := o.BuildCacheKey(req)
	if key1 != key2 {
		t.Errorf("相同输入应生成相同 key, key1=%q key2=%q", key1, key2)
	}
}

// TestBuildCacheKey_DifferentQueryDifferentKey 验证不同 query 生成不同 key。
func TestBuildCacheKey_DifferentQueryDifferentKey(t *testing.T) {
	cfg := PromptCacheConfig{Enabled: true}
	o := NewPromptCacheOptimizer(nil, cfg)
	key1 := o.BuildCacheKey(PromptCacheRequest{Query: "hello"})
	key2 := o.BuildCacheKey(PromptCacheRequest{Query: "world"})
	if key1 == key2 {
		t.Errorf("不同 query 应生成不同 key, key1=%q key2=%q", key1, key2)
	}
}

// ----------------------------------------------------------------------------
// OptimizePrompt 测试
// ----------------------------------------------------------------------------

// TestOptimizePrompt_WithSeparator 验证按分隔符拆分。
func TestOptimizePrompt_WithSeparator(t *testing.T) {
	cfg := PromptCacheConfig{PrefixStable: true}
	o := NewPromptCacheOptimizer(nil, cfg)
	prompt := "system prompt\n\n---\n\nuser query"
	stable, dynamic := o.OptimizePrompt(prompt)
	if stable != "system prompt" {
		t.Errorf("stable = %q, 期望 %q", stable, "system prompt")
	}
	if dynamic != "user query" {
		t.Errorf("dynamic = %q, 期望 %q", dynamic, "user query")
	}
}

// TestOptimizePrompt_NoSeparator 验证无分隔符时整个 prompt 作为稳定前缀。
func TestOptimizePrompt_NoSeparator(t *testing.T) {
	cfg := PromptCacheConfig{PrefixStable: true}
	o := NewPromptCacheOptimizer(nil, cfg)
	prompt := "system prompt only"
	stable, dynamic := o.OptimizePrompt(prompt)
	if stable != prompt {
		t.Errorf("stable = %q, 期望 %q", stable, prompt)
	}
	if dynamic != "" {
		t.Errorf("dynamic = %q, 期望空", dynamic)
	}
}

// TestOptimizePrompt_PrefixStableFalse 验证 PrefixStable=false 时返回空稳定前缀。
func TestOptimizePrompt_PrefixStableFalse(t *testing.T) {
	cfg := PromptCacheConfig{PrefixStable: false}
	o := NewPromptCacheOptimizer(nil, cfg)
	prompt := "system prompt\n\n---\n\nuser query"
	stable, dynamic := o.OptimizePrompt(prompt)
	if stable != "" {
		t.Errorf("stable = %q, 期望空", stable)
	}
	if dynamic != prompt {
		t.Errorf("dynamic = %q, 期望 %q", dynamic, prompt)
	}
}

// ----------------------------------------------------------------------------
// Complete 测试
// ----------------------------------------------------------------------------

// TestComplete_CacheMissThenHit 验证 cache miss 后写回，第二次 hit。
func TestComplete_CacheMissThenHit(t *testing.T) {
	llm := &stubPCLLM{resp: "llm response"}
	cache := newStubPCCache()
	cfg := PromptCacheConfig{
		Enabled:         true,
		MinPromptLength: 8,
	}
	o := NewPromptCacheOptimizer(llm, cfg)
	o.SetCache(cache)
	prompt := "this is a long prompt for testing cache"

	// 第一次：miss → 调 LLM → 写回 cache。
	resp1, err := o.Complete(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Complete 错误: %v", err)
	}
	if resp1 != "llm response" {
		t.Errorf("resp1 = %q, 期望 %q", resp1, "llm response")
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", got)
	}
	if got := atomic.LoadInt64(&cache.gets); got != 1 {
		t.Errorf("cache.gets = %d, 期望 1", got)
	}
	if got := atomic.LoadInt64(&cache.sets); got != 1 {
		t.Errorf("cache.sets = %d, 期望 1", got)
	}

	// 第二次：hit → 不调 LLM。
	resp2, err := o.Complete(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Complete 错误: %v", err)
	}
	if resp2 != "llm response" {
		t.Errorf("resp2 = %q, 期望 %q", resp2, "llm response")
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("第二次 hit 后 LLM 调用次数 = %d, 期望 1", got)
	}
	if got := atomic.LoadInt64(&cache.gets); got != 2 {
		t.Errorf("cache.gets = %d, 期望 2", got)
	}
}

// TestComplete_CacheDisabled 验证 Enabled=false 时不查 cache。
func TestComplete_CacheDisabled(t *testing.T) {
	llm := &stubPCLLM{resp: "response"}
	cache := newStubPCCache()
	cfg := PromptCacheConfig{
		Enabled:         false,
		MinPromptLength: 8,
	}
	o := NewPromptCacheOptimizer(llm, cfg)
	o.SetCache(cache)
	_, err := o.Complete(context.Background(), "long prompt")
	if err != nil {
		t.Fatalf("Complete 错误: %v", err)
	}
	if got := atomic.LoadInt64(&cache.gets); got != 0 {
		t.Errorf("Enabled=false 时 cache.gets 应为 0, 实际 %d", got)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", got)
	}
}

// TestComplete_ShortPrompt 验证短 prompt 不查 cache。
func TestComplete_ShortPrompt(t *testing.T) {
	llm := &stubPCLLM{resp: "response"}
	cache := newStubPCCache()
	cfg := PromptCacheConfig{
		Enabled:         true,
		MinPromptLength: 100,
	}
	o := NewPromptCacheOptimizer(llm, cfg)
	o.SetCache(cache)
	_, err := o.Complete(context.Background(), "short")
	if err != nil {
		t.Fatalf("Complete 错误: %v", err)
	}
	if got := atomic.LoadInt64(&cache.gets); got != 0 {
		t.Errorf("短 prompt 时 cache.gets 应为 0, 实际 %d", got)
	}
	if got := atomic.LoadInt64(&llm.calls); got != 1 {
		t.Errorf("LLM 调用次数 = %d, 期望 1", got)
	}
}

// TestComplete_NilCache 验证 nil cache 时直接调 LLM。
func TestComplete_NilCache(t *testing.T) {
	llm := &stubPCLLM{resp: "response"}
	cfg := PromptCacheConfig{Enabled: true, MinPromptLength: 8}
	o := NewPromptCacheOptimizer(llm, cfg)
	// 不调用 SetCache。
	resp, err := o.Complete(context.Background(), "long prompt for test")
	if err != nil {
		t.Fatalf("Complete 错误: %v", err)
	}
	if resp != "response" {
		t.Errorf("resp = %q, 期望 %q", resp, "response")
	}
}

// TestComplete_LLMError 验证 LLM 错误透传。
func TestComplete_LLMError(t *testing.T) {
	llmErr := errors.New("llm failed")
	llm := &stubPCLLM{err: llmErr}
	cfg := PromptCacheConfig{Enabled: false}
	o := NewPromptCacheOptimizer(llm, cfg)
	_, err := o.Complete(context.Background(), "prompt")
	if !errors.Is(err, llmErr) {
		t.Errorf("err = %v, 期望 %v", err, llmErr)
	}
}

// TestComplete_NilLLM 验证 nil llm 返回错误。
func TestComplete_NilLLM(t *testing.T) {
	cfg := PromptCacheConfig{Enabled: false}
	o := NewPromptCacheOptimizer(nil, cfg)
	_, err := o.Complete(context.Background(), "prompt")
	if err == nil {
		t.Error("nil llm 应返回错误")
	}
}

// TestComplete_NilReceiver 验证 nil receiver 返回错误。
func TestComplete_NilReceiver(t *testing.T) {
	var o *PromptCacheOptimizer
	_, err := o.Complete(context.Background(), "prompt")
	if err == nil {
		t.Error("nil receiver 应返回错误")
	}
}

// ----------------------------------------------------------------------------
// Metrics 测试
// ----------------------------------------------------------------------------

// TestMetrics_CacheHitRatio 验证命中率统计。
func TestMetrics_CacheHitRatio(t *testing.T) {
	llm := &stubPCLLM{resp: "response"}
	cache := newStubPCCache()
	cfg := PromptCacheConfig{Enabled: true, MinPromptLength: 8}
	o := NewPromptCacheOptimizer(llm, cfg)
	o.SetCache(cache)
	prompt := "long prompt for testing"

	// 第一次：miss。
	_, _ = o.Complete(context.Background(), prompt)
	// 第二次：hit。
	_, _ = o.Complete(context.Background(), prompt)

	m := o.Metrics()
	if m.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, 期望 2", m.TotalRequests)
	}
	if m.CacheHits != 1 {
		t.Errorf("CacheHits = %d, 期望 1", m.CacheHits)
	}
	if m.CacheMisses != 1 {
		t.Errorf("CacheMisses = %d, 期望 1", m.CacheMisses)
	}
	if m.HitRatio != 0.5 {
		t.Errorf("HitRatio = %v, 期望 0.5", m.HitRatio)
	}
}

// TestMetrics_ZeroValue 验证零值安全。
func TestMetrics_ZeroValue(t *testing.T) {
	o := NewPromptCacheOptimizer(nil, PromptCacheConfig{})
	m := o.Metrics()
	if m.TotalRequests != 0 || m.HitRatio != 0 {
		t.Errorf("零值 Metrics 不正确: %+v", m)
	}
}

// TestMetrics_NilReceiver 验证 nil receiver 返回零值。
func TestMetrics_NilReceiver(t *testing.T) {
	var o *PromptCacheOptimizer
	m := o.Metrics()
	if m.TotalRequests != 0 {
		t.Errorf("nil receiver Metrics 应为零值, 实际 %+v", m)
	}
}

// ----------------------------------------------------------------------------
// 辅助函数测试
// ----------------------------------------------------------------------------

// TestDefaultPromptCacheConfig 验证默认配置。
func TestDefaultPromptCacheConfig(t *testing.T) {
	cfg := DefaultPromptCacheConfig()
	if !cfg.Enabled {
		t.Error("默认 Enabled 应为 true")
	}
	if !cfg.PrefixStable {
		t.Error("默认 PrefixStable 应为 true")
	}
	if !cfg.CacheKeyByChannel {
		t.Error("默认 CacheKeyByChannel 应为 true")
	}
	if cfg.MinPromptLength != 64 {
		t.Errorf("默认 MinPromptLength = %d, 期望 64", cfg.MinPromptLength)
	}
}

// TestHashPrefix_Deterministic 验证相同输入生成相同 hash。
func TestHashPrefix_Deterministic(t *testing.T) {
	h1 := hashPrefix("test")
	h2 := hashPrefix("test")
	if h1 != h2 {
		t.Errorf("相同输入应生成相同 hash, h1=%q h2=%q", h1, h2)
	}
}

// TestHashPrefix_DifferentInput 验证不同输入生成不同 hash。
func TestHashPrefix_DifferentInput(t *testing.T) {
	h1 := hashPrefix("test1")
	h2 := hashPrefix("test2")
	if h1 == h2 {
		t.Errorf("不同输入应生成不同 hash, h1=%q h2=%q", h1, h2)
	}
}

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// contains 检查 s 是否包含 substr。
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsStr(s, substr))
}

// containsStr 用 strings.Contains 检查（避免与 strings.Contains 同名）。
func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// 编译期断言：domain.BehaviorEvent 用于 stub（确保 import domain）。
var _ = domain.BehaviorEvent{}
