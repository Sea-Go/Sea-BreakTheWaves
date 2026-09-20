// Package cpu batch.go — 批处理（Task 13.3）。
//
// 该文件实现 BatchProcessor：用 Pool 并行执行批量召回 / 批量向量计算 /
// 批量 CF 相似度计算，按 batchSize 分片并行，结果按顺序合并。
//
// 职责：
//   - BatchRecall：将 keys 按 batchSize 分片，并行调用 BatchRecaller.RecallBatch
//     结果按分片顺序合并为 [][]any
//   - BatchVector：将 vectors 按 batchSize 分片，并行调用 VectorComputer.ComputeBatch
//     结果按分片顺序合并为 [][]float32
//   - BatchSimilarity：将 userIDs 按 batchSize 分片，并行调用 SimilarityComputer.ComputeSimilarity
//     结果聚合为 map[string][]Similarity
//
// 二开扩展点：
//   - 替换 Pool：实现 Pool interface 接入 ants/goroutine pool 等第三方池
//   - 替换 BatchRecaller/VectorComputer/SimilarityComputer：实现对应 interface
//
// 不直接 import ants / automaxprocs / trpc-agent-go，所有依赖通过 interface 注入。
package cpu

import (
	"context"
	"errors"
	"sync"
)

// BatchRecaller 批量召回 interface。
//
// 二开扩展点：实现该 interface 接入自研批量召回器（Milvus 批量查询等）。
type BatchRecaller interface {
	// RecallBatch 批量召回。
	// ctx 上下文；keys 待召回的 key 列表。
	// 返回与 keys 等长的结果切片（每 key 一项）与 error。
	RecallBatch(ctx context.Context, keys []string) ([]any, error)
}

// VectorComputer 向量批量计算 interface。
//
// 二开扩展点：实现该 interface 接入自研向量计算引擎（GPU/batch embedding）。
type VectorComputer interface {
	// ComputeBatch 批量计算向量。
	// ctx 上下文；vectors 待计算的向量列表。
	// 返回与 vectors 等长的结果向量切片与 error。
	ComputeBatch(ctx context.Context, vectors [][]float32) ([][]float32, error)
}

// SimilarityComputer 相似度批量计算 interface。
//
// 二开扩展点：实现该 interface 接入自研 CF 引擎（user_cf / item_cf）。
type SimilarityComputer interface {
	// ComputeSimilarity 计算单个用户的相似度列表。
	// ctx 上下文；userID 用户 ID。
	// 返回与该用户最相似的邻居列表与 error。
	ComputeSimilarity(ctx context.Context, userID string) ([]Similarity, error)
}

// Similarity 相似度条目。
//
// 字段语义：
//   - UserID：邻居用户 ID
//   - Score：相似度分数（0-1）
type Similarity struct {
	// UserID 邻居用户 ID。
	UserID string
	// Score 相似度分数。
	Score float64
}

// BatchProcessor 批处理器。
//
// 字段语义：
//   - batchSize：每批分片大小（<=0 时默认 32）
//   - pool：协程池（用于并行执行分片；可为 nil，nil 时串行执行）
//
// 二开扩展点：通过 NewBatchProcessor(batchSize, pool) 注入自研 Pool 与分片大小。
type BatchProcessor struct {
	batchSize int
	pool      Pool
}

// NewBatchProcessor 构造 BatchProcessor。
// batchSize 每批分片大小（<=0 时默认 32）；
// pool 协程池（可为 nil，nil 时串行执行）。
// 返回 *BatchProcessor。
func NewBatchProcessor(batchSize int, pool Pool) *BatchProcessor {
	if batchSize <= 0 {
		batchSize = 32
	}
	return &BatchProcessor{
		batchSize: batchSize,
		pool:      pool,
	}
}

// BatchRecall 批量召回（按 batchSize 分片并行）。
//
// 流程：
//  1. 将 keys 按 batchSize 切分为多个分片
//  2. 每个分片调用 BatchRecaller.RecallBatch（通过 pool 并行）
//  3. 结果按分片顺序合并为 [][]any（外层为分片，内层为分片内 keys 的结果）
//  4. 任一分片失败返回 error（错误不隔离，整体失败）
//
// ctx 取消时未启动的分片跳过；已启动的分片结果仍合并返回。
func (b *BatchProcessor) BatchRecall(ctx context.Context, keys []string, recaller BatchRecaller) ([][]any, error) {
	if b == nil {
		return nil, errors.New("batch processor: nil receiver")
	}
	if recaller == nil {
		return nil, errors.New("batch processor: nil recaller")
	}
	if len(keys) == 0 {
		return nil, nil
	}
	// 切分分片。
	shards := shardStrings(keys, b.batchSize)
	results := make([][]any, len(shards))
	errs := make([]error, len(shards))
	b.parallel(len(shards), func(idx int) {
		shard := shards[idx]
		res, err := recaller.RecallBatch(ctx, shard)
		if err != nil {
			errs[idx] = err
			return
		}
		results[idx] = res
	})
	// 任一分片失败返回 error（取第一个）。
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// BatchVector 批量向量计算（按 batchSize 分片并行）。
//
// 流程：
//  1. 将 vectors 按 batchSize 切分为多个分片
//  2. 每个分片调用 VectorComputer.ComputeBatch（通过 pool 并行）
//  3. 结果按分片顺序合并为 [][]float32
//  4. 任一分片失败返回 error
func (b *BatchProcessor) BatchVector(ctx context.Context, vectors [][]float32, compute VectorComputer) ([][]float32, error) {
	if b == nil {
		return nil, errors.New("batch processor: nil receiver")
	}
	if compute == nil {
		return nil, errors.New("batch processor: nil vector computer")
	}
	if len(vectors) == 0 {
		return nil, nil
	}
	shards := shardVectors(vectors, b.batchSize)
	results := make([][]float32, 0, len(vectors))
	resultShards := make([][][]float32, len(shards))
	errs := make([]error, len(shards))
	b.parallel(len(shards), func(idx int) {
		shard := shards[idx]
		res, err := compute.ComputeBatch(ctx, shard)
		if err != nil {
			errs[idx] = err
			return
		}
		resultShards[idx] = res
	})
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	// 合并所有分片结果。
	for _, shard := range resultShards {
		results = append(results, shard...)
	}
	return results, nil
}

// BatchSimilarity 批量 CF 相似度（按 batchSize 分片并行）。
//
// 流程：
//  1. 将 userIDs 按 batchSize 切分为多个分片
//  2. 每个分片中逐个调用 SimilarityComputer.ComputeSimilarity（通过 pool 并行分片）
//  3. 结果聚合为 map[string][]Similarity（key=userID）
//  4. 单个用户失败跳过（不阻断其他用户）
func (b *BatchProcessor) BatchSimilarity(ctx context.Context, userIDs []string, sim SimilarityComputer) (map[string][]Similarity, error) {
	if b == nil {
		return nil, errors.New("batch processor: nil receiver")
	}
	if sim == nil {
		return nil, errors.New("batch processor: nil similarity computer")
	}
	if len(userIDs) == 0 {
		return map[string][]Similarity{}, nil
	}
	shards := shardStrings(userIDs, b.batchSize)
	shardResults := make([]map[string][]Similarity, len(shards))
	b.parallel(len(shards), func(idx int) {
		shard := shards[idx]
		m := make(map[string][]Similarity, len(shard))
		for _, uid := range shard {
			sims, err := sim.ComputeSimilarity(ctx, uid)
			if err != nil {
				// 单个用户失败跳过。
				continue
			}
			m[uid] = sims
		}
		shardResults[idx] = m
	})
	// 合并所有分片结果到统一 map。
	out := make(map[string][]Similarity, len(userIDs))
	for _, shard := range shardResults {
		for k, v := range shard {
			out[k] = v
		}
	}
	return out, nil
}

// parallel 并行执行 n 个任务，每个任务通过 pool.Submit 提交（pool 为 nil 时串行）。
//
// 任务签名为 func(idx int)，idx 为任务索引（0..n-1）。
// 内部用 sync.WaitGroup 等待所有任务完成。
func (b *BatchProcessor) parallel(n int, task func(idx int)) {
	if n <= 0 {
		return
	}
	if b.pool == nil {
		// 串行执行。
		for i := 0; i < n; i++ {
			task(i)
		}
		return
	}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		idx := i
		err := b.pool.Submit(func() {
			defer wg.Done()
			task(idx)
		})
		if err != nil {
			// pool 提交失败，回退到当前 goroutine 同步执行。
			defer wg.Done()
			task(idx)
		}
	}
	wg.Wait()
}

// shardStrings 将 keys 按 size 切分为多个分片。
//
// 例：keys 长度 10、size 3 → 4 个分片（3,3,3,1）。
func shardStrings(keys []string, size int) [][]string {
	if size <= 0 {
		size = 32
	}
	if len(keys) == 0 {
		return nil
	}
	n := (len(keys) + size - 1) / size
	shards := make([][]string, 0, n)
	for i := 0; i < len(keys); i += size {
		end := i + size
		if end > len(keys) {
			end = len(keys)
		}
		shards = append(shards, keys[i:end])
	}
	return shards
}

// shardVectors 将 vectors 按 size 切分为多个分片。
func shardVectors(vectors [][]float32, size int) [][][]float32 {
	if size <= 0 {
		size = 32
	}
	if len(vectors) == 0 {
		return nil
	}
	n := (len(vectors) + size - 1) / size
	shards := make([][][]float32, 0, n)
	for i := 0; i < len(vectors); i += size {
		end := i + size
		if end > len(vectors) {
			end = len(vectors)
		}
		shards = append(shards, vectors[i:end])
	}
	return shards
}
