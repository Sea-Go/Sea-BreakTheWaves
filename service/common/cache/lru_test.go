// Package cache lru_test.go — LRUCache 单元测试（Task 13.4）。
//
// 用 stdlib 手写 stub 覆盖：
//   - 基本 Get/Set/Delete/Len 操作
//   - LRU 淘汰策略（超容淘汰队尾）
//   - LRU 顺序（Get 后移动到队首，影响淘汰选择）
//   - 更新已存在 key（不增加条目数）
//   - 并发安全（-race 检测）
//   - 零容量（视为 1）
package cache

import (
	"fmt"
	"sync"
	"testing"
)

// TestLRUCache_BasicOperations 验证基本 Get/Set/Len 操作。
func TestLRUCache_BasicOperations(t *testing.T) {
	c := NewLRUCache(3)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%v, %v), 期望 (1, true)", v, ok)
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = (%v, %v), 期望 (2, true)", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Errorf("Get(c) = (%v, %v), 期望 (3, true)", v, ok)
	}
	if v, ok := c.Get("d"); ok {
		t.Errorf("Get(d) = (%v, %v), 期望 (nil, false)", v, ok)
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d, 期望 3", c.Len())
	}
}

// TestLRUCache_Eviction 验证超容淘汰队尾（LRU）。
func TestLRUCache_Eviction(t *testing.T) {
	c := NewLRUCache(2)
	c.Set("a", 1)
	c.Set("b", 2)
	// a, b 都在，容量 2。
	c.Set("c", 3)
	// a 应被淘汰（a 最久未使用）。
	if _, ok := c.Get("a"); ok {
		t.Error("a 应被淘汰")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = (%v, %v), 期望 (2, true)", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Errorf("Get(c) = (%v, %v), 期望 (3, true)", v, ok)
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, 期望 2", c.Len())
	}
}

// TestLRUCache_LRUOrder 验证 Get 后移动到队首影响淘汰选择。
func TestLRUCache_LRUOrder(t *testing.T) {
	c := NewLRUCache(3)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	// 访问 a，使 a 变为最近使用。
	c.Get("a")
	// 插入 d，应淘汰 b（最久未使用）。
	c.Set("d", 4)
	if _, ok := c.Get("b"); ok {
		t.Error("b 应被淘汰（a 被 Get 过，b 变为最久未使用）")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%v, %v), 期望 (1, true)", v, ok)
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Errorf("Get(c) = (%v, %v), 期望 (3, true)", v, ok)
	}
	if v, ok := c.Get("d"); !ok || v != 4 {
		t.Errorf("Get(d) = (%v, %v), 期望 (4, true)", v, ok)
	}
}

// TestLRUCache_UpdateExisting 验证更新已存在 key 不增加条目数。
func TestLRUCache_UpdateExisting(t *testing.T) {
	c := NewLRUCache(2)
	c.Set("a", 1)
	c.Set("a", 2) // 更新值
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Errorf("Get(a) = (%v, %v), 期望 (2, true)", v, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, 期望 1（更新不增加条目）", c.Len())
	}
	// 验证更新后仍可继续插入。
	c.Set("b", 3)
	if c.Len() != 2 {
		t.Errorf("Len = %d, 期望 2", c.Len())
	}
}

// TestLRUCache_Delete 验证 Delete 操作。
func TestLRUCache_Delete(t *testing.T) {
	c := NewLRUCache(3)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("Delete 后 Get(a) 应未命中")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = (%v, %v), 期望 (2, true)", v, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, 期望 1", c.Len())
	}
	// Delete 不存在的 key 无副作用。
	c.Delete("nonexistent")
	if c.Len() != 1 {
		t.Errorf("Delete nonexistent 后 Len = %d, 期望 1", c.Len())
	}
}

// TestLRUCache_ZeroCapacity 验证 capacity<=0 视为 1。
func TestLRUCache_ZeroCapacity(t *testing.T) {
	c := NewLRUCache(0) // 视为 1
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%v, %v), 期望 (1, true)", v, ok)
	}
	c.Set("b", 2)
	if _, ok := c.Get("a"); ok {
		t.Error("capacity=1 时 a 应被淘汰")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = (%v, %v), 期望 (2, true)", v, ok)
	}
}

// TestLRUCache_NegativeCapacity 验证负容量视为 1。
func TestLRUCache_NegativeCapacity(t *testing.T) {
	c := NewLRUCache(-5)
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%v, %v), 期望 (1, true)", v, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, 期望 1", c.Len())
	}
}

// TestLRUCache_Concurrent 验证并发读写线程安全。
func TestLRUCache_Concurrent(t *testing.T) {
	c := NewLRUCache(100)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", idx%50)
			c.Set(key, idx)
			c.Get(key)
			c.Delete(key)
		}(i)
	}
	wg.Wait()
}

// TestLRUCache_NilReceiver 验证 nil receiver 安全处理。
func TestLRUCache_NilReceiver(t *testing.T) {
	var c *LRUCache
	if v, ok := c.Get("a"); ok || v != nil {
		t.Errorf("nil Get 应返回 (nil, false), got (%v, %v)", v, ok)
	}
	c.Set("a", 1) // 不应 panic
	c.Delete("a") // 不应 panic
	if c.Len() != 0 {
		t.Errorf("nil Len 应返回 0, got %d", c.Len())
	}
}

// TestLRUCache_OverwriteNoEviction 验证更新已存在 key 时不触发淘汰。
func TestLRUCache_OverwriteNoEviction(t *testing.T) {
	c := NewLRUCache(2)
	c.Set("a", 1)
	c.Set("b", 2)
	// 更新 a（不增加条目，不应淘汰 b）。
	c.Set("a", 10)
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Errorf("Get(a) = (%v, %v), 期望 (10, true)", v, ok)
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) = (%v, %v), 期望 (2, true)（更新不应淘汰 b）", v, ok)
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, 期望 2", c.Len())
	}
}
