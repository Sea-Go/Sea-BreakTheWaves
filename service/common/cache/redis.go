// Package cache redis.go — L2 Redis 缓存抽象（Task 13.4）。
//
// 该文件实现 RedisCache：基于 RedisClient interface 抽象 Redis 客户端，
// 不直接 import go-redis，避免外部依赖。提供 Get/Set/Delete 与 ErrCacheMiss。
//
// 职责：
//   - 通过 RedisClient interface 抽象 Redis 客户端（Get/Set/Del）
//   - Get 时若 client 返回空字符串则返回 ErrCacheMiss
//   - 提供 NoopRedisClient 用于离线测试
//
// 二开扩展点：实现 RedisClient interface 接入 go-redis / redigo / 自研 Redis 客户端。
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrCacheMiss 缓存未命中错误。
//
// RedisCache.Get 在 client 返回空字符串（或 NoopRedisClient 直接返回）时返回该错误。
// TieredCache.Get 在 L1/L2 均未命中时返回该错误。
var ErrCacheMiss = errors.New("cache: miss")

// RedisClient Redis 客户端抽象 interface。
//
// 职责：抽象 go-redis / redigo / 自研客户端的 Get/Set/Del 操作，
// 使 RedisCache 不直接依赖具体 Redis 库。
//
// 二开扩展点：实现该 interface 接入实际 Redis 客户端。
type RedisClient interface {
	// Get 获取 key 的值。
	// 不存在时返回空串 + nil error（由 RedisCache 转换为 ErrCacheMiss）。
	// 真实错误（如网络故障）返回非 nil error。
	Get(ctx context.Context, key string) (string, error)
	// Set 设置 key 的值，带 TTL。
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	// Del 删除 key（可变参数）。
	Del(ctx context.Context, keys ...string) error
}

// RedisCache L2 Redis 缓存。
//
// 字段语义：
//   - client：RedisClient 实现（不可为 nil）
//   - ttl：默认 TTL（Set 时使用，<=0 视为永不过期）
type RedisCache struct {
	// client Redis 客户端实现。
	client RedisClient
	// ttl 默认生存时间。
	ttl time.Duration
}

// NewRedisCache 构造 RedisCache。
// client RedisClient 实现（可为 nil，此时 Get 返回 ErrCacheMiss，Set/Del 空操作）；ttl 默认 TTL（<=0 视为永不过期）。
// 返回 *RedisCache。
func NewRedisCache(client RedisClient, ttl time.Duration) *RedisCache {
	return &RedisCache{
		client: client,
		ttl:    ttl,
	}
}

// Get 获取缓存值。
//
// client 返回空串时返回 ErrCacheMiss；client 返回错误时透传。
func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	if c == nil || c.client == nil {
		return "", ErrCacheMiss
	}
	val, err := c.client.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if val == "" {
		return "", ErrCacheMiss
	}
	return val, nil
}

// Set 设置缓存值（使用构造时指定的 TTL）。
func (c *RedisCache) Set(ctx context.Context, key string, value string) error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Set(ctx, key, value, c.ttl)
}

// Delete 删除缓存条目。
func (c *RedisCache) Delete(ctx context.Context, keys ...string) error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Del(ctx, keys...)
}

// NoopRedisClient 空操作 Redis 客户端，用于离线测试。
//
// Get 永远返回 ErrCacheMiss；Set/Del 空操作返回 nil。
// 实现 RedisClient interface，可直接传入 NewRedisCache。
type NoopRedisClient struct{}

// Get 返回 ErrCacheMiss（表示 key 不存在）。
func (NoopRedisClient) Get(_ context.Context, _ string) (string, error) {
	return "", ErrCacheMiss
}

// Set 空操作，返回 nil。
func (NoopRedisClient) Set(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

// Del 空操作，返回 nil。
func (NoopRedisClient) Del(_ context.Context, _ ...string) error {
	return nil
}
