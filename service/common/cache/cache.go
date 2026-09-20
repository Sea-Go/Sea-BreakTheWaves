// Package cache cache.go — 三级缓存编排（Task 13.4）。
//
// 该文件实现 TieredCache（L1 LRU + L2 Redis 编排）与 L3PromptCache（Prompt Cache 抽象）。
//
// TieredCache 职责：
//   - Get: L1 → L2 → miss；L2 命中时回填 L1
//   - Set: 同时写 L1 + L2（L2 仅接受 string 值，非 string 仅写 L1）
//   - Delete: 同时删 L1 + L2
//   - Stats: 返回 L1Hits/L2Hits/Misses/Total 统计
//   - HitRate: (L1Hits + L2Hits) / Total，Total=0 时返回 0
//
// L3PromptCache 职责：
//   - 抽象 LLM Prompt Cache（通过 PromptCacheClient interface）
//
// 二开扩展点：通过 NewTieredCache / NewL3PromptCache 注入自定义 L1/L2/L3 实现。
package cache

import (
	"context"
	"sync"
	"sync/atomic"
)

// CacheStats 缓存统计。
//
// 字段语义：
//   - L1Hits：L1 命中次数
//   - L2Hits：L2 命中次数
//   - Misses：未命中次数
//   - Total：总查询次数（L1Hits + L2Hits + Misses）
type CacheStats struct {
	// L1Hits L1 命中次数。
	L1Hits int64
	// L2Hits L2 命中次数。
	L2Hits int64
	// Misses 未命中次数。
	Misses int64
	// Total 总查询次数。
	Total int64
}

// TieredCache L1 + L2 两级缓存编排。
//
// 字段语义：
//   - l1：L1 LRU 内存缓存（可为 nil）
//   - l2：L2 Redis 缓存（可为 nil，表示无 L2）
//   - stats：统计计数（atomic 操作各字段）
//   - mu：保护内部状态（预留扩展）
type TieredCache struct {
	// l1 L1 LRU 内存缓存。
	l1 *LRUCache
	// l2 L2 Redis 缓存。
	l2 *RedisCache
	// stats 统计计数（atomic 操作 L1Hits/L2Hits/Misses/Total 字段）。
	stats CacheStats
	// mu 保护内部状态（预留扩展，如配置热更新）。
	mu sync.Mutex
}

// NewTieredCache 构造 TieredCache。
// l1 L1 LRU 缓存（可为 nil）；l2 L2 Redis 缓存（可为 nil，表示无 L2）。
// 返回 *TieredCache。
func NewTieredCache(l1 *LRUCache, l2 *RedisCache) *TieredCache {
	return &TieredCache{
		l1: l1,
		l2: l2,
	}
}

// Get 查询缓存：L1 → L2 → miss。
//
// 流程：
//  1. L1 命中：返回值，L1Hits++
//  2. L1 未命中，L2 命中：返回值，回填 L1，L2Hits++
//  3. L1/L2 均未命中：返回 ErrCacheMiss，Misses++
//
// Total 始终 +1。
func (c *TieredCache) Get(ctx context.Context, key string) (any, error) {
	if c == nil {
		return nil, ErrCacheMiss
	}
	atomic.AddInt64(&c.stats.Total, 1)
	// L1
	if c.l1 != nil {
		if val, ok := c.l1.Get(key); ok {
			atomic.AddInt64(&c.stats.L1Hits, 1)
			return val, nil
		}
	}
	// L2
	if c.l2 != nil {
		val, err := c.l2.Get(ctx, key)
		if err == nil {
			atomic.AddInt64(&c.stats.L2Hits, 1)
			// 回填 L1（L2 返回 string，存入 L1）。
			if c.l1 != nil {
				c.l1.Set(key, val)
			}
			return val, nil
		}
		// 非 ErrCacheMiss 的真实错误保守视为 miss（不阻断，由调用方决策）。
	}
	// miss
	atomic.AddInt64(&c.stats.Misses, 1)
	return nil, ErrCacheMiss
}

// Set 同时写 L1 + L2。
//
// L1 接受任意类型值；L2 仅接受 string 值（非 string 仅写 L1，不报错）。
func (c *TieredCache) Set(ctx context.Context, key string, value any) error {
	if c == nil {
		return nil
	}
	if c.l1 != nil {
		c.l1.Set(key, value)
	}
	if c.l2 != nil {
		// L2 是 Redis（string 存储），仅 string 值写入；非 string 跳过 L2。
		if s, ok := value.(string); ok {
			return c.l2.Set(ctx, key, s)
		}
	}
	return nil
}

// Delete 同时删 L1 + L2。
func (c *TieredCache) Delete(ctx context.Context, key string) error {
	if c == nil {
		return nil
	}
	if c.l1 != nil {
		c.l1.Delete(key)
	}
	if c.l2 != nil {
		return c.l2.Delete(ctx, key)
	}
	return nil
}

// Stats 返回当前缓存统计快照（线程安全，atomic 读取）。
func (c *TieredCache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	return CacheStats{
		L1Hits: atomic.LoadInt64(&c.stats.L1Hits),
		L2Hits: atomic.LoadInt64(&c.stats.L2Hits),
		Misses: atomic.LoadInt64(&c.stats.Misses),
		Total:  atomic.LoadInt64(&c.stats.Total),
	}
}

// HitRate 返回命中率 (L1Hits + L2Hits) / Total。
//
// Total 为 0 时返回 0（避免除零）。
func (c *TieredCache) HitRate() float64 {
	if c == nil {
		return 0
	}
	total := atomic.LoadInt64(&c.stats.Total)
	if total == 0 {
		return 0
	}
	hits := atomic.LoadInt64(&c.stats.L1Hits) + atomic.LoadInt64(&c.stats.L2Hits)
	return float64(hits) / float64(total)
}

// PromptCacheClient LLM Prompt Cache 客户端抽象 interface。
//
// 职责：抽象 LLM 的 Prompt Cache 能力（如 Anthropic Prompt Cache / OpenAI Cache）。
// Get 获取缓存的 prompt 响应；Set 缓存 prompt 响应。
//
// 二开扩展点：实现该 interface 接入具体 LLM Prompt Cache。
type PromptCacheClient interface {
	// GetCached 获取缓存的 prompt 响应。
	// key 缓存键（如 prompt hash）。
	// 返回缓存的响应文本与 error（未命中时返回 ErrCacheMiss）。
	GetCached(ctx context.Context, key string) (string, error)
	// SetCached 缓存 prompt 响应。
	// key 缓存键；value 响应文本。
	SetCached(ctx context.Context, key string, value string) error
}

// L3PromptCache L3 Prompt Cache 抽象。
//
// 字段语义：
//   - llm：PromptCacheClient 实现
type L3PromptCache struct {
	// llm PromptCacheClient 实现。
	llm PromptCacheClient
}

// NewL3PromptCache 构造 L3PromptCache。
// llm PromptCacheClient 实现（可为 nil，此时 Get 返回 ErrCacheMiss，Set 空操作）。
// 返回 *L3PromptCache。
func NewL3PromptCache(llm PromptCacheClient) *L3PromptCache {
	return &L3PromptCache{llm: llm}
}

// Get 获取缓存的 prompt 响应。
//
// llm 为 nil 时返回 ErrCacheMiss；否则透传 llm.GetCached 结果。
func (c *L3PromptCache) Get(ctx context.Context, key string) (string, error) {
	if c == nil || c.llm == nil {
		return "", ErrCacheMiss
	}
	return c.llm.GetCached(ctx, key)
}

// Set 缓存 prompt 响应。
//
// llm 为 nil 时返回 nil（空操作）；否则透传 llm.SetCached 结果。
func (c *L3PromptCache) Set(ctx context.Context, key string, value string) error {
	if c == nil || c.llm == nil {
		return nil
	}
	return c.llm.SetCached(ctx, key, value)
}
