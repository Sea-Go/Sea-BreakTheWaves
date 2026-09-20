// Package cpu batch_test.go — BatchProcessor 单元测试（Task 13.3）。
//
// 用 stdlib 手写 stub（不引入 testify）覆盖：
//   - BatchRecall 分片 + 并行 + 结果合并
//   - BatchVector 分片 + 并行 + 结果合并
//   - BatchSimilarity 分片 + 并行 + 结果合并
//   - 单个用户失败跳过
//   - nil pool 时串行执行
//   - 默认 batchSize（<=0 → 32）
//
// stub 命名加 BP（BatchProcessor）前缀避免与已有 stub 冲突。
package cpu

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// ----------------------------------------------------------------------------
// stub 实现（BP 前缀 = BatchProcessor）
// ----------------------------------------------------------------------------

// stubBPRecaller 测试用 BatchRecaller stub。
type stubBPRecaller struct {
	mu         sync.Mutex
	calls      int64
	batches    [][]string
	err        error
	errOnBatch int // 在第 N 次调用（从 0 开始）返回 err
}

func (s *stubBPRecaller) RecallBatch(_ context.Context, keys []string) ([]any, error) {
	n := atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	s.batches = append(s.batches, append([]string(nil), keys...))
	s.mu.Unlock()
	if s.err != nil && int(n)-1 == s.errOnBatch {
		return nil, s.err
	}
	// 返回与 keys 等长的结果（每 key 一项，值为 key 自身）。
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out, nil
}

// stubBPVectorComputer 测试用 VectorComputer stub。
type stubBPVectorComputer struct {
	calls    int64
	err      error
	errOnIdx int
}

func (s *stubBPVectorComputer) ComputeBatch(_ context.Context, vectors [][]float32) ([][]float32, error) {
	n := atomic.AddInt64(&s.calls, 1)
	if s.err != nil && int(n)-1 == s.errOnIdx {
		return nil, s.err
	}
	// 返回每向量 ×2 的结果。
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
		doubled := make([]float32, len(v))
		for j, x := range v {
			doubled[j] = x * 2
		}
		out[i] = doubled
	}
	return out, nil
}

// stubBPSimilarityComputer 测试用 SimilarityComputer stub。
type stubBPSimilarityComputer struct {
	calls   int64
	errOnID string // 该 userID 返回 err
}

func (s *stubBPSimilarityComputer) ComputeSimilarity(_ context.Context, userID string) ([]Similarity, error) {
	atomic.AddInt64(&s.calls, 1)
	if s.errOnID != "" && userID == s.errOnID {
		return nil, errors.New("similarity compute failed")
	}
	return []Similarity{
		{UserID: userID + "-neighbor1", Score: 0.9},
		{UserID: userID + "-neighbor2", Score: 0.8},
	}, nil
}

// 编译期断言：stub 实现 interface。
var _ BatchRecaller = (*stubBPRecaller)(nil)
var _ VectorComputer = (*stubBPVectorComputer)(nil)
var _ SimilarityComputer = (*stubBPSimilarityComputer)(nil)

// ----------------------------------------------------------------------------
// BatchRecall 测试
// ----------------------------------------------------------------------------

// TestBatchRecall_Sharding 验证 keys 按 batchSize 分片。
func TestBatchRecall_Sharding(t *testing.T) {
	pool := NewWorkerPool(4)
	defer pool.Release()
	b := NewBatchProcessor(3, pool)
	keys := []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9", "k10"}
	r := &stubBPRecaller{}
	results, err := b.BatchRecall(context.Background(), keys, r)
	if err != nil {
		t.Fatalf("BatchRecall 错误: %v", err)
	}
	// 10 keys / batchSize 3 = 4 分片。
	if len(results) != 4 {
		t.Fatalf("results 分片数 = %d, 期望 4", len(results))
	}
	// 总 key 数应为 10。
	total := 0
	for _, shard := range results {
		total += len(shard)
	}
	if total != 10 {
		t.Errorf("总 key 数 = %d, 期望 10", total)
	}
	// 验证 recaller 被调用 4 次。
	if got := atomic.LoadInt64(&r.calls); got != 4 {
		t.Errorf("recaller.calls = %d, 期望 4", got)
	}
}

// TestBatchRecall_ResultsMerged 验证结果按分片顺序合并。
func TestBatchRecall_ResultsMerged(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Release()
	b := NewBatchProcessor(2, pool)
	keys := []string{"a", "b", "c", "d", "e"}
	r := &stubBPRecaller{}
	results, err := b.BatchRecall(context.Background(), keys, r)
	if err != nil {
		t.Fatalf("BatchRecall 错误: %v", err)
	}
	// 验证每分片结果与 keys 顺序对应。
	// 分片1: [a,b] → [a,b]
	// 分片2: [c,d] → [c,d]
	// 分片3: [e] → [e]
	if len(results) != 3 {
		t.Fatalf("results 分片数 = %d, 期望 3", len(results))
	}
	if results[0][0] != "a" || results[0][1] != "b" {
		t.Errorf("results[0] = %v, 期望 [a,b]", results[0])
	}
	if results[1][0] != "c" || results[1][1] != "d" {
		t.Errorf("results[1] = %v, 期望 [c,d]", results[1])
	}
	if results[2][0] != "e" {
		t.Errorf("results[2] = %v, 期望 [e]", results[2])
	}
}

// TestBatchRecall_PartialFailure 验证任一分片失败返回 error。
func TestBatchRecall_PartialFailure(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Release()
	b := NewBatchProcessor(2, pool)
	keys := []string{"a", "b", "c", "d"}
	r := &stubBPRecaller{
		err:        errors.New("batch failed"),
		errOnBatch: 1, // 第 2 个分片失败
	}
	_, err := b.BatchRecall(context.Background(), keys, r)
	if err == nil {
		t.Error("BatchRecall 应返回错误")
	}
}

// TestBatchRecall_NilPool 验证 nil pool 时串行执行。
func TestBatchRecall_NilPool(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	keys := []string{"a", "b", "c", "d"}
	r := &stubBPRecaller{}
	results, err := b.BatchRecall(context.Background(), keys, r)
	if err != nil {
		t.Fatalf("BatchRecall 错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results 分片数 = %d, 期望 2", len(results))
	}
	if got := atomic.LoadInt64(&r.calls); got != 2 {
		t.Errorf("recaller.calls = %d, 期望 2", got)
	}
}

// TestBatchRecall_EmptyKeys 验证空 keys 返回 nil。
func TestBatchRecall_EmptyKeys(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	r := &stubBPRecaller{}
	results, err := b.BatchRecall(context.Background(), nil, r)
	if err != nil {
		t.Fatalf("BatchRecall 错误: %v", err)
	}
	if results != nil {
		t.Errorf("results 应为 nil, 实际 %v", results)
	}
}

// TestBatchRecall_NilRecaller 验证 nil recaller 返回错误。
func TestBatchRecall_NilRecaller(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	_, err := b.BatchRecall(context.Background(), []string{"a"}, nil)
	if err == nil {
		t.Error("nil recaller 应返回错误")
	}
}

// TestBatchRecall_DefaultBatchSize 验证 batchSize<=0 时默认 32。
func TestBatchRecall_DefaultBatchSize(t *testing.T) {
	b := NewBatchProcessor(0, nil)
	if b.batchSize != 32 {
		t.Errorf("batchSize = %d, 期望 32", b.batchSize)
	}
}

// ----------------------------------------------------------------------------
// BatchVector 测试
// ----------------------------------------------------------------------------

// TestBatchVector_ShardingAndMerge 验证向量分片与合并。
func TestBatchVector_ShardingAndMerge(t *testing.T) {
	pool := NewWorkerPool(4)
	defer pool.Release()
	b := NewBatchProcessor(3, pool)
	vectors := [][]float32{
		{1, 2},
		{3, 4},
		{5, 6},
		{7, 8},
		{9, 10},
	}
	c := &stubBPVectorComputer{}
	results, err := b.BatchVector(context.Background(), vectors, c)
	if err != nil {
		t.Fatalf("BatchVector 错误: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("results 长度 = %d, 期望 5", len(results))
	}
	// 验证每向量 ×2。
	expected := [][]float32{{2, 4}, {6, 8}, {10, 12}, {14, 16}, {18, 20}}
	for i, r := range results {
		if len(r) != 2 {
			t.Errorf("results[%d] 长度 = %d, 期望 2", i, len(r))
			continue
		}
		for j, v := range r {
			if v != expected[i][j] {
				t.Errorf("results[%d][%d] = %v, 期望 %v", i, j, v, expected[i][j])
			}
		}
	}
}

// TestBatchVector_PartialFailure 验证任一分片失败返回 error。
func TestBatchVector_PartialFailure(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Release()
	b := NewBatchProcessor(2, pool)
	vectors := [][]float32{{1}, {2}, {3}, {4}}
	c := &stubBPVectorComputer{
		err:      errors.New("compute failed"),
		errOnIdx: 0,
	}
	_, err := b.BatchVector(context.Background(), vectors, c)
	if err == nil {
		t.Error("BatchVector 应返回错误")
	}
}

// TestBatchVector_NilPool 验证 nil pool 时串行执行。
func TestBatchVector_NilPool(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	vectors := [][]float32{{1, 2}, {3, 4}}
	c := &stubBPVectorComputer{}
	results, err := b.BatchVector(context.Background(), vectors, c)
	if err != nil {
		t.Fatalf("BatchVector 错误: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results 长度 = %d, 期望 2", len(results))
	}
}

// TestBatchVector_EmptyVectors 验证空 vectors 返回 nil。
func TestBatchVector_EmptyVectors(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	c := &stubBPVectorComputer{}
	results, err := b.BatchVector(context.Background(), nil, c)
	if err != nil {
		t.Fatalf("BatchVector 错误: %v", err)
	}
	if results != nil {
		t.Errorf("results 应为 nil, 实际 %v", results)
	}
}

// ----------------------------------------------------------------------------
// BatchSimilarity 测试
// ----------------------------------------------------------------------------

// TestBatchSimilarity_ShardingAndMerge 验证 CF 相似度分片与合并。
func TestBatchSimilarity_ShardingAndMerge(t *testing.T) {
	pool := NewWorkerPool(4)
	defer pool.Release()
	b := NewBatchProcessor(2, pool)
	userIDs := []string{"u1", "u2", "u3", "u4", "u5"}
	c := &stubBPSimilarityComputer{}
	results, err := b.BatchSimilarity(context.Background(), userIDs, c)
	if err != nil {
		t.Fatalf("BatchSimilarity 错误: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("results 长度 = %d, 期望 5", len(results))
	}
	// 验证每个用户有 2 个邻居。
	for _, uid := range userIDs {
		sims, ok := results[uid]
		if !ok {
			t.Errorf("results[%q] 不存在", uid)
			continue
		}
		if len(sims) != 2 {
			t.Errorf("results[%q] 邻居数 = %d, 期望 2", uid, len(sims))
		}
	}
}

// TestBatchSimilarity_PartialFailure 验证单个用户失败跳过。
func TestBatchSimilarity_PartialFailure(t *testing.T) {
	pool := NewWorkerPool(2)
	defer pool.Release()
	b := NewBatchProcessor(2, pool)
	userIDs := []string{"u1", "u2", "u3"}
	c := &stubBPSimilarityComputer{errOnID: "u2"}
	results, err := b.BatchSimilarity(context.Background(), userIDs, c)
	if err != nil {
		t.Fatalf("BatchSimilarity 错误应为 nil（单个用户失败跳过）, 实际: %v", err)
	}
	// u1 和 u3 应有结果。
	if _, ok := results["u1"]; !ok {
		t.Error("results[u1] 应存在")
	}
	if _, ok := results["u3"]; !ok {
		t.Error("results[u3] 应存在")
	}
	// u2 应不存在（失败跳过）。
	if _, ok := results["u2"]; ok {
		t.Error("results[u2] 应不存在（失败跳过）")
	}
}

// TestBatchSimilarity_NilPool 验证 nil pool 时串行执行。
func TestBatchSimilarity_NilPool(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	userIDs := []string{"u1", "u2", "u3"}
	c := &stubBPSimilarityComputer{}
	results, err := b.BatchSimilarity(context.Background(), userIDs, c)
	if err != nil {
		t.Fatalf("BatchSimilarity 错误: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results 长度 = %d, 期望 3", len(results))
	}
}

// TestBatchSimilarity_EmptyUserIDs 验证空 userIDs 返回空 map。
func TestBatchSimilarity_EmptyUserIDs(t *testing.T) {
	b := NewBatchProcessor(2, nil)
	c := &stubBPSimilarityComputer{}
	results, err := b.BatchSimilarity(context.Background(), nil, c)
	if err != nil {
		t.Fatalf("BatchSimilarity 错误: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results 长度 = %d, 期望 0", len(results))
	}
}

// ----------------------------------------------------------------------------
// 辅助函数测试
// ----------------------------------------------------------------------------

// TestShardStrings 验证 shardStrings 分片正确。
func TestShardStrings(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e"}
	shards := shardStrings(keys, 2)
	if len(shards) != 3 {
		t.Fatalf("shards 长度 = %d, 期望 3", len(shards))
	}
	if len(shards[0]) != 2 || len(shards[1]) != 2 || len(shards[2]) != 1 {
		t.Errorf("分片大小不正确: %d %d %d", len(shards[0]), len(shards[1]), len(shards[2]))
	}
}

// TestShardStrings_DefaultSize 验证 size<=0 时默认 32。
func TestShardStrings_DefaultSize(t *testing.T) {
	keys := make([]string, 35)
	for i := range keys {
		keys[i] = "k"
	}
	shards := shardStrings(keys, 0)
	// 35 keys / 32 = 2 分片。
	if len(shards) != 2 {
		t.Fatalf("shards 长度 = %d, 期望 2", len(shards))
	}
}

// TestShardVectors 验证 shardVectors 分片正确。
func TestShardVectors(t *testing.T) {
	vectors := [][]float32{{1}, {2}, {3}, {4}}
	shards := shardVectors(vectors, 2)
	if len(shards) != 2 {
		t.Fatalf("shards 长度 = %d, 期望 2", len(shards))
	}
	if len(shards[0]) != 2 || len(shards[1]) != 2 {
		t.Errorf("分片大小不正确: %d %d", len(shards[0]), len(shards[1]))
	}
}

// TestSimilarity_ZeroValue 验证零值安全。
func TestSimilarity_ZeroValue(t *testing.T) {
	var s Similarity
	if s.UserID != "" || s.Score != 0 {
		t.Errorf("零值 Similarity 不正确: %+v", s)
	}
}

// TestBatchProcessor_NilReceiver 验证 nil receiver 安全处理。
func TestBatchProcessor_NilReceiver(t *testing.T) {
	var b *BatchProcessor
	_, err := b.BatchRecall(context.Background(), []string{"a"}, &stubBPRecaller{})
	if err == nil {
		t.Error("nil receiver BatchRecall 应返回错误")
	}
	_, err = b.BatchVector(context.Background(), [][]float32{{1}}, &stubBPVectorComputer{})
	if err == nil {
		t.Error("nil receiver BatchVector 应返回错误")
	}
	_, err = b.BatchSimilarity(context.Background(), []string{"u"}, &stubBPSimilarityComputer{})
	if err == nil {
		t.Error("nil receiver BatchSimilarity 应返回错误")
	}
}
