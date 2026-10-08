// Package rrf 是 Reciprocal Rank Fusion 的共享实现（唯一实现）。
//
// 此前搜索侧（search/internal/retrieval/fusion.go）与推荐侧
// （recommend/internal/model/recall/content_recaller.go）各有一份私有
// RRF；Milvus Go SDK 亦提供 NewRRFReranker（服务端形态）。2026-10-08
// 复用审计后将 RRF 上提为本共享包：搜索侧经薄适配消费，推荐侧与 Milvus
// 适配器后续可指向同一实现。
package rrf

import "sort"

// KDefault 是 RRF 的默认平滑常数（业界惯例值 60；与 Milvus RRFReranker
// 默认值一致）。
const KDefault = 60

// Entry 是单路召回列表中的一个候选：稳定键 + 该路分数（分数仅用于路内
// 排序，融合只用名次）。
type Entry struct {
	Key   string
	Score float64
}

// Fused 是融合后的一个候选：键 + RRF 累计分。
type Fused struct {
	Key   string
	Score float64
}

// RRF 融合多路候选列表：score = Σ_ranks 1/(k + rank)。rank 为该文档在
// 此路的位次（1 起，未出现不贡献）。k<=0 时取 KDefault。输出按 RRF 分数
// 降序，平局按 Key 字典序升序（保证同输入同输出）。空列表或全空输入返回
// nil。
func RRF(rankLists [][]Entry, k int) []Fused {
	if len(rankLists) == 0 {
		return nil
	}
	if k <= 0 {
		k = KDefault
	}
	type acc struct {
		score float64
	}
	byKey := make(map[string]*acc, 64)
	for _, list := range rankLists {
		// 路内先归一为名次：调用方传入的列表若未排序，此处按分数降序
		// （平局 Key 升序）确定名次，语义与"位次"一致。
		ordered := normalize(list)
		for rank, e := range ordered {
			a, ok := byKey[e.Key]
			if !ok {
				a = &acc{}
				byKey[e.Key] = a
			}
			a.score += 1.0 / float64(k+rank+1)
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	out := make([]Fused, 0, len(byKey))
	for key, a := range byKey {
		out = append(out, Fused{Key: key, Score: a.score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// normalize 把一路候选排成"名次序"：分数降序、平局 Key 字典序升序。
func normalize(list []Entry) []Entry {
	ordered := append([]Entry(nil), list...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		return ordered[i].Key < ordered[j].Key
	})
	return ordered
}
