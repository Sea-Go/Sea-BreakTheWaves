// Package cache lru.go — L1 LRU 内存缓存（Task 13.4）。
//
// 该文件实现 LRUCache：基于 map + 双向链表的固定容量 LRU 缓存，
// 线程安全（sync.RWMutex），Get 时移动到队首，Set 时超容淘汰队尾。
//
// 职责：
//   - Get/Set/Delete/Len 基本操作
//   - 线程安全（sync.RWMutex）
//   - LRU 淘汰策略（队首 = MRU，队尾 = LRU）
//
// 二开扩展点：通过 NewLRUCache(capacity) 配置容量；如需替换 L1 实现，
// 可在 TieredCache 中注入自研 LRUCache 同接口实现。
package cache

import (
	"container/list"
	"sync"
)

// LRUCache L1 内存 LRU 缓存。
//
// 基于 map + container/list 双向链表实现，O(1) Get/Set/Delete。
// 线程安全：sync.RWMutex 保护并发读写。
//
// 字段语义：
//   - capacity：容量上限（<=0 视为 1）
//   - items：key -> *list.Element（element.Value 为 *cacheEntry）
//   - ll：双向链表，队首为最近使用（MRU），队尾为最久未使用（LRU）
//   - mu：读写锁
type LRUCache struct {
	// capacity 容量上限。
	capacity int
	// items key -> list.Element 映射。
	items map[string]*list.Element
	// ll 双向链表（队首 = MRU，队尾 = LRU）。
	ll *list.List
	// mu 读写锁保护并发访问。
	mu sync.RWMutex
}

// cacheEntry LRU 缓存条目。
//
// 字段语义：
//   - key：缓存键（用于淘汰时反向查找 map）
//   - value：缓存值
type cacheEntry struct {
	// key 缓存键（用于淘汰时反向查找 map）。
	key string
	// value 缓存值。
	value any
}

// NewLRUCache 构造 LRUCache。
// capacity 容量上限（<=0 视为 1）。
// 返回 *LRUCache。
func NewLRUCache(capacity int) *LRUCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &LRUCache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		ll:       list.New(),
	}
}

// Get 获取缓存值。
//
// 命中时将条目移动到队首（最近使用）；未命中返回 (nil, false)。
func (c *LRUCache) Get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(elem)
	return elem.Value.(*cacheEntry).value, true
}

// Set 设置缓存值。
//
// 若 key 已存在，更新值并移动到队首；否则新增到队首。
// 超容时淘汰队尾（最久未使用）。
func (c *LRUCache) Set(key string, value any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.ll.MoveToFront(elem)
		elem.Value.(*cacheEntry).value = value
		return
	}
	elem := c.ll.PushFront(&cacheEntry{key: key, value: value})
	c.items[key] = elem
	if c.ll.Len() > c.capacity {
		// 淘汰队尾（最久未使用）。
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

// Delete 删除缓存条目。
//
// key 不存在时无操作。
func (c *LRUCache) Delete(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.ll.Remove(elem)
		delete(c.items, key)
	}
}

// Len 返回当前缓存条目数。
func (c *LRUCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ll.Len()
}
