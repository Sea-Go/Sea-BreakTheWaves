package retrieval

import (
	"math"
	"testing"
)

// 测试语料：3 个文档，三路表示全部手工构造、期望值手算。
func threeDocStore(t *testing.T) Snapshot {
	t.Helper()
	store, err := NewStore(map[string]LoadedDoc{
		"doc-a": {
			Dense:  []float32{1, 0},
			Sparse: map[uint32]float32{7: 3},
			Multi:  [][]float32{{1, 0}, {0, 1}},
		},
		"doc-b": {
			Dense:  []float32{1, 1},
			Sparse: map[uint32]float32{7: 1, 9: 2},
			Multi:  [][]float32{{0.6, 0.8}},
		},
		"doc-c": {
			Dense:  []float32{-1, 0},         // 与查询反平行：余弦 -1，不构成候选
			Sparse: map[uint32]float32{5: 9}, // 无公共 term：内积 0
			Multi:  [][]float32{{-1, 0}},     // MaxSim 为负：不构成候选
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store.Snapshot()
}

// Dense 手算（query=[1,0]）：
//
//	doc-a cos=1；doc-b cos=1/√2≈0.70711；doc-c cos=-1（剔除）。
func TestRecallDenseHandComputed(t *testing.T) {
	sn := threeDocStore(t)
	got := sn.Dense([]float32{1, 0})
	want := []Scored{{"doc-a", 1}, {"doc-b", float32(1 / math.Sqrt2)}}
	if len(got) != len(want) {
		t.Fatalf("dense 候选数 %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].DocKey != want[i].DocKey || math.Abs(float64(got[i].Score-want[i].Score)) > 1e-6 {
			t.Fatalf("dense[%d]=%v want %v", i, got[i], want[i])
		}
	}
	// 维度不匹配的查询（3 维）对全部 2 维文档无候选。
	if got := sn.Dense([]float32{1, 0, 0}); len(got) != 0 {
		t.Fatalf("维度不匹配应无候选: %v", got)
	}
	// 零向量查询无候选。
	if got := sn.Dense([]float32{0, 0}); len(got) != 0 {
		t.Fatalf("零向量查询应无候选: %v", got)
	}
	// 空查询无候选。
	if got := sn.Dense(nil); len(got) != 0 {
		t.Fatalf("空查询应无候选: %v", got)
	}
}

// Sparse 手算（query={7:2, 9:1}，impact 内积）：
//
//	doc-a = 2×3 = 6；doc-b = 2×1 + 1×2 = 4；doc-c 无公共 term = 0（剔除）。
func TestRecallSparseHandComputed(t *testing.T) {
	sn := threeDocStore(t)
	got := sn.Sparse(map[uint32]float32{7: 2, 9: 1})
	want := []Scored{{"doc-a", 6}, {"doc-b", 4}}
	if len(got) != len(want) {
		t.Fatalf("sparse 候选数 %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].DocKey != want[i].DocKey || got[i].Score != want[i].Score {
			t.Fatalf("sparse[%d]=%v want %v", i, got[i], want[i])
		}
	}
	if got := sn.Sparse(map[uint32]float32{}); len(got) != 0 {
		t.Fatalf("空查询应无候选: %v", got)
	}
	if got := sn.Sparse(map[uint32]float32{42: 1}); len(got) != 0 {
		t.Fatalf("无交集查询应无候选: %v", got)
	}
}

// Multi 手算（exact MaxSim，query tokens = [[1,0],[0,1]]）：
//
//	doc-a = max(1,0)+max(0,1) = 1+1 = 2；
//	doc-b = max(0.6)+max(0.8) = 0.6+0.8 = 1.4；
//	doc-c = max(-1)+max(0) = -1+0 = -1 ≤ 0（剔除）。
func TestRecallMultiHandComputed(t *testing.T) {
	sn := threeDocStore(t)
	got := sn.Multi([][]float32{{1, 0}, {0, 1}})
	want := []Scored{{"doc-a", 2}, {"doc-b", 1.4}}
	if len(got) != len(want) {
		t.Fatalf("multi 候选数 %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].DocKey != want[i].DocKey || math.Abs(float64(got[i].Score-want[i].Score)) > 1e-6 {
			t.Fatalf("multi[%d]=%v want %v", i, got[i], want[i])
		}
	}
	// 查询行宽不一致：拒绝（nil）。
	if got := sn.Multi([][]float32{{1, 0}, {1}}); got != nil {
		t.Fatalf("行宽不一致的查询应返回 nil: %v", got)
	}
	if got := sn.Multi(nil); len(got) != 0 {
		t.Fatalf("空查询应无候选: %v", got)
	}
	// 全零查询行（零范数）：无候选。
	if got := sn.Multi([][]float32{{0, 0}}); len(got) != 0 {
		t.Fatalf("零范数查询行应无候选: %v", got)
	}
}

// 平局确定性：同分候选按 doc_key 字典序。
func TestRecallTieBreakDeterministic(t *testing.T) {
	store, err := NewStore(map[string]LoadedDoc{
		"zz": {Dense: []float32{1, 0}},
		"aa": {Dense: []float32{1, 0}},
		"mm": {Dense: []float32{1, 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sn := store.Snapshot()
	got := sn.Dense([]float32{1, 0})
	wantKeys := []string{"aa", "mm", "zz"}
	if len(got) != 3 {
		t.Fatalf("候选数 %d want 3", len(got))
	}
	for i, k := range wantKeys {
		if got[i].DocKey != k {
			t.Fatalf("平局顺序 got[%d]=%s want %s", i, got[i].DocKey, k)
		}
	}
}
