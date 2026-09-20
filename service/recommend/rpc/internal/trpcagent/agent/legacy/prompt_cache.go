// Package agent prompt_cache.go — Prompt Cache 优化（Task 13.7）。
//
// 该文件实现 PromptCacheOptimizer：构建 cache key、拆分稳定前缀+动态后缀、
// 带 cache 的 LLM 调用与缓存指标统计。
//
// 职责：
//   - BuildCacheKey：按 channel/user/intent/stable_prefix_hash 构建缓存 key
//   - OptimizePrompt：按分隔符拆分稳定前缀（system+rubric+few-shot）与动态后缀
//     （user query+context）
//   - Complete：构建缓存 key → 查 L3 cache → miss 时调 LLM（EnableCache=true）
//     → 写回 cache
//   - Metrics：返回缓存命中率、cached tokens 等指标
//
// 二开扩展点：
//   - 通过 NewPromptCacheOptimizer(llm, config) 注入 LLMClient 与配置
//   - 通过本地 L3Cache interface 注入自研 cache（如 cache.L3PromptCache）
//
// 不直接 import trpc-agent-go / testify，所有依赖通过 interface 注入。
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ----------------------------------------------------------------------------
// 配置与请求结构
// ----------------------------------------------------------------------------

// PromptCacheConfig Prompt Cache 配置。
//
// 字段语义：
//   - Enabled：是否启用 Prompt Cache（false 时 Complete 不查 cache，直接调 LLM）
//   - PrefixStable：是否启用稳定前缀拆分（false 时 OptimizePrompt 返回原 prompt）
//   - CacheKeyByChannel：cache key 是否包含 channel
//   - CacheKeyByUser：cache key 是否包含 user
//   - MinPromptLength：最小 prompt 长度（短于此长度不缓存）
type PromptCacheConfig struct {
	// Enabled 是否启用 Prompt Cache。
	Enabled bool
	// PrefixStable 是否启用稳定前缀拆分。
	PrefixStable bool
	// CacheKeyByChannel cache key 是否包含 channel。
	CacheKeyByChannel bool
	// CacheKeyByUser cache key 是否包含 user。
	CacheKeyByUser bool
	// MinPromptLength 最小 prompt 长度。
	MinPromptLength int
}

// DefaultPromptCacheConfig 默认 Prompt Cache 配置。
//
// 启用 cache + 启用前缀拆分 + 按 channel+user 构建 key + 最小长度 64。
func DefaultPromptCacheConfig() PromptCacheConfig {
	return PromptCacheConfig{
		Enabled:           true,
		PrefixStable:      true,
		CacheKeyByChannel: true,
		CacheKeyByUser:    false,
		MinPromptLength:   64,
	}
}

// PromptCacheRequest Prompt Cache 请求。
//
// 字段语义：
//   - UserID：用户 ID（CacheKeyByUser=true 时纳入 key）
//   - Channel：频道（CacheKeyByChannel=true 时纳入 key）
//   - Query：用户查询（动态部分，不纳入 key）
//   - Intent：意图（纳入 key）
type PromptCacheRequest struct {
	// UserID 用户 ID。
	UserID string
	// Channel 频道。
	Channel string
	// Query 用户查询。
	Query string
	// Intent 意图。
	Intent string
}

// ----------------------------------------------------------------------------
// 缓存指标
// ----------------------------------------------------------------------------

// PromptCacheMetrics Prompt Cache 指标。
//
// 字段语义：
//   - TotalRequests：总请求数
//   - CacheHits：cache 命中数
//   - CacheMisses：cache 未命中数
//   - HitRatio：命中率 = CacheHits / TotalRequests
//   - CachedTokens：cached token 数（命中时累计）
//   - AvgFirstTokenMs：平均首 token 延迟（毫秒）
type PromptCacheMetrics struct {
	// TotalRequests 总请求数。
	TotalRequests int64
	// CacheHits cache 命中数。
	CacheHits int64
	// CacheMisses cache 未命中数。
	CacheMisses int64
	// HitRatio 命中率。
	HitRatio float64
	// CachedTokens cached token 数。
	CachedTokens int64
	// AvgFirstTokenMs 平均首 token 延迟（毫秒）。
	AvgFirstTokenMs int64
}

// ----------------------------------------------------------------------------
// L3 cache interface（本地定义，与 cache.L3PromptCache 同构）
// ----------------------------------------------------------------------------

// L3Cache L3 Prompt Cache 抽象 interface（本地定义）。
//
// 职责：抽象 LLM 的 Prompt Cache 能力（如 Anthropic Prompt Cache / OpenAI Cache）。
// 与 internal/cache.PromptCacheClient 同构，但本包不直接依赖 cache 包，
// 避免循环依赖。
//
// 二开扩展点：实现该 interface 接入具体 LLM Prompt Cache。
type L3Cache interface {
	// GetCached 获取缓存的 prompt 响应。
	// key 缓存键；返回缓存的响应文本与 error（未命中时返回 ErrPromptCacheMiss）。
	GetCached(ctx context.Context, key string) (string, error)
	// SetCached 缓存 prompt 响应。
	SetCached(ctx context.Context, key string, value string) error
}

// ErrPromptCacheMiss Prompt Cache 未命中错误。
var ErrPromptCacheMiss = errors.New("agent: prompt cache miss")

// ----------------------------------------------------------------------------
// PromptCacheOptimizer
// ----------------------------------------------------------------------------

// PromptCacheOptimizer Prompt Cache 优化器。
//
// 字段语义：
//   - llm：底层 LLMClient 实现
//   - config：Prompt Cache 配置
//   - cache：L3 cache 实现（可为 nil，nil 时 Complete 不查 cache）
//   - metrics：缓存指标（atomic 操作各字段）
//   - totalLatencyMs：累计首 token 延迟（用于计算平均值）
//
// 二开扩展点：通过 NewPromptCacheOptimizer(llm, config) 创建实例；
// 通过 SetCache 注入 L3 cache 实现。
type PromptCacheOptimizer struct {
	llm            LLMClient
	config         PromptCacheConfig
	cache          L3Cache
	metrics        PromptCacheMetrics
	totalLatencyMs int64
	mu             sync.Mutex
}

// NewPromptCacheOptimizer 构造 PromptCacheOptimizer。
// llm 底层 LLMClient 实现；config Prompt Cache 配置。
// 返回 *PromptCacheOptimizer（未注入 cache，需调用 SetCache）。
func NewPromptCacheOptimizer(llm LLMClient, config PromptCacheConfig) *PromptCacheOptimizer {
	return &PromptCacheOptimizer{
		llm:    llm,
		config: config,
	}
}

// SetCache 注入 L3 cache 实现。
// cache L3 cache 实现（可为 nil，nil 时 Complete 不查 cache，直接调 LLM）。
func (p *PromptCacheOptimizer) SetCache(cache L3Cache) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = cache
}

// BuildCacheKey 构建缓存 key。
//
// key 格式：channel:{channel}:intent:{intent}:prefix:{stable_prefix_hash}
// 若 CacheKeyByChannel=true 包含 channel；CacheKeyByUser=true 包含 user。
// stable_prefix_hash 为 req.Query 的 SHA256 前 16 字符（简化实现）。
//
// 实际生产环境应使用稳定前缀的 hash，这里简化用 query 的 hash 模拟。
func (p *PromptCacheOptimizer) BuildCacheKey(req PromptCacheRequest) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.config.CacheKeyByChannel && req.Channel != "" {
		parts = append(parts, "channel:"+req.Channel)
	}
	if p.config.CacheKeyByUser && req.UserID != "" {
		parts = append(parts, "user:"+req.UserID)
	}
	if req.Intent != "" {
		parts = append(parts, "intent:"+req.Intent)
	}
	// prefix hash：用 query 计算（实际应用 stable prefix）。
	prefixHash := hashPrefix(req.Query)
	parts = append(parts, "prefix:"+prefixHash)
	return strings.Join(parts, ":")
}

// OptimizePrompt 拆分稳定前缀 + 动态后缀。
//
// 简单实现：按 "\n\n---\n\n" 分隔符拆分。
//   - 存在分隔符：分隔符前为稳定前缀，分隔符后为动态后缀
//   - 不存在分隔符：整个 prompt 作为稳定前缀，动态后缀为空
//   - PrefixStable=false 时：稳定前缀为空，整个 prompt 作为动态后缀
//
// 稳定前缀：system prompt + rubric + few-shot（不变部分）
// 动态后缀：用户查询 + 上下文（变化部分）
func (p *PromptCacheOptimizer) OptimizePrompt(prompt string) (stablePrefix string, dynamicSuffix string) {
	if p == nil {
		return "", prompt
	}
	if !p.config.PrefixStable {
		return "", prompt
	}
	const sep = "\n\n---\n\n"
	if idx := strings.Index(prompt, sep); idx >= 0 {
		stablePrefix = prompt[:idx]
		dynamicSuffix = prompt[idx+len(sep):]
		return stablePrefix, dynamicSuffix
	}
	// 无分隔符时，整个 prompt 作为稳定前缀。
	return prompt, ""
}

// Complete 带 cache 的 LLM 调用。
//
// 流程：
//  1. 构建 cache key（基于 PromptCacheRequest）
//  2. 若 Enabled=true 且 cache != nil，查 L3 cache
//  3. 命中：返回缓存响应，CacheHits++
//  4. 未命中：调 LLM（EnableCache=true），CacheMisses++
//  5. 写回 cache（异步或同步，这里同步）
//  6. 更新指标（TotalRequests / HitRatio / AvgFirstTokenMs）
//
// 注意：本方法签名简化为 Complete(ctx, prompt)，未接收 PromptCacheRequest，
// cache key 用 prompt 自身 hash 构建（简化实现，生产环境应传入 req）。
func (p *PromptCacheOptimizer) Complete(ctx context.Context, prompt string) (string, error) {
	if p == nil {
		return "", errors.New("prompt cache optimizer: nil receiver")
	}
	if p.llm == nil {
		return "", errors.New("prompt cache optimizer: nil llm")
	}
	atomic.AddInt64(&p.metrics.TotalRequests, 1)
	start := time.Now()

	// 短 prompt 不查 cache，直接调 LLM。
	if p.config.Enabled && p.cache != nil && len(prompt) >= p.config.MinPromptLength {
		key := p.BuildCacheKey(PromptCacheRequest{Query: prompt})
		if cached, err := p.cache.GetCached(ctx, key); err == nil {
			// cache 命中。
			atomic.AddInt64(&p.metrics.CacheHits, 1)
			// 估算 cached tokens（粗略：每 4 字符 1 token）。
			atomic.AddInt64(&p.metrics.CachedTokens, int64(len(cached)/4))
			p.updateLatency(time.Since(start).Milliseconds())
			return cached, nil
		}
		// cache 未命中。
		atomic.AddInt64(&p.metrics.CacheMisses, 1)
		resp, err := p.llm.Complete(ctx, prompt, LLMOptions{EnableCache: true})
		if err != nil {
			return "", err
		}
		// 写回 cache（忽略错误）。
		_ = p.cache.SetCached(ctx, key, resp)
		p.updateLatency(time.Since(start).Milliseconds())
		return resp, nil
	}
	// 未启用 cache，直接调 LLM。
	resp, err := p.llm.Complete(ctx, prompt, LLMOptions{EnableCache: p.config.Enabled})
	if err != nil {
		return "", err
	}
	p.updateLatency(time.Since(start).Milliseconds())
	return resp, nil
}

// updateLatency 更新累计延迟并重算平均值。
func (p *PromptCacheOptimizer) updateLatency(latencyMs int64) {
	if p == nil {
		return
	}
	total := atomic.AddInt64(&p.totalLatencyMs, latencyMs)
	reqs := atomic.LoadInt64(&p.metrics.TotalRequests)
	if reqs > 0 {
		atomic.StoreInt64(&p.metrics.AvgFirstTokenMs, total/reqs)
	}
}

// Metrics 返回缓存指标快照。
//
// HitRatio = CacheHits / TotalRequests（TotalRequests=0 时为 0）。
func (p *PromptCacheOptimizer) Metrics() PromptCacheMetrics {
	if p == nil {
		return PromptCacheMetrics{}
	}
	hits := atomic.LoadInt64(&p.metrics.CacheHits)
	total := atomic.LoadInt64(&p.metrics.TotalRequests)
	ratio := 0.0
	if total > 0 {
		ratio = float64(hits) / float64(total)
	}
	return PromptCacheMetrics{
		TotalRequests:   total,
		CacheHits:       hits,
		CacheMisses:     atomic.LoadInt64(&p.metrics.CacheMisses),
		HitRatio:        ratio,
		CachedTokens:    atomic.LoadInt64(&p.metrics.CachedTokens),
		AvgFirstTokenMs: atomic.LoadInt64(&p.metrics.AvgFirstTokenMs),
	}
}

// hashPrefix 计算前缀的 SHA256 hash（前 16 字符）。
func hashPrefix(prefix string) string {
	h := sha256.Sum256([]byte(prefix))
	return hex.EncodeToString(h[:])[:16]
}

// 编译期断言：PromptCacheOptimizer 实现 LLMClient（包装 LLM）。
// 注意：PromptCacheOptimizer 不直接实现 LLMClient，因为它不实现
// CompleteWithStructuredOutput/CompleteWithLogprobs。
