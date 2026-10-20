package rrf

import (
	"math"
	"testing"
)

// RRF 手算例（k=60，rank 从 1 起）：
//
//	list1 = [A, B, C]，list2 = [B, A]
//	A = 1/(60+1) + 1/(60+2) = 1/61 + 1/62
//	B = 1/(60+2) + 1/(60+1) = 1/61 + 1/62（与 A 平局 → Key 字典序 A 前）
//	C = 1/(60+3)
func TestRRFHandComputed(t *testing.T) {
	list1 := []Entry{{"doc-a", 9}, {"doc-b", 8}, {"doc-c", 7}}
	list2 := []Entry{{"doc-b", 5}, {"doc-a", 4}}
	got := RRF([][]Entry{list1, list2}, 60)

	ab := 1.0/61 + 1.0/62
	c := 1.0 / 63
	want := []Fused{{"doc-a", ab}, {"doc-b", ab}, {"doc-c", c}}
	if len(got) != len(want) {
		t.Fatalf("融合候选数 %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Key != want[i].Key || math.Abs(got[i].Score-want[i].Score) > 1e-12 {
			t.Fatalf("RRF[%d]=%v want %v", i, got[i], want[i])
		}
	}
}

// 单列表退化为 1/(k+rank)；k<=0 回退 KDefault=60。
func TestRRFDefaultsAndEmpty(t *testing.T) {
	got := RRF([][]Entry{{{"x", 1}, {"y", 1}}}, 0) // k=0 → KDefault
	if len(got) != 2 {
		t.Fatalf("候选数 %d want 2", len(got))
	}
	if s := got[0].Score - 1.0/61; math.Abs(s) > 1e-12 {
		t.Fatalf("got[0]=%v want %v", got[0].Score, 1.0/61)
	}
	if s := got[1].Score - 1.0/62; math.Abs(s) > 1e-12 {
		t.Fatalf("got[1]=%v want %v", got[1].Score, 1.0/62)
	}
	if got := RRF(nil, KDefault); len(got) != 0 {
		t.Fatalf("空输入应返回空: %v", got)
	}
	if got := RRF([][]Entry{nil, {}}, KDefault); len(got) != 0 {
		t.Fatalf("空列表应返回空: %v", got)
	}
}

// 未排序的路内列表先按分数降序（平局 Key 升序）定名次，再融合。
func TestRRFNormalizesUnsortedLane(t *testing.T) {
	got := RRF([][]Entry{{{"a", 1}, {"b", 9}}}, KDefault) // b 分高应排第一
	if len(got) != 2 || got[0].Key != "b" {
		t.Fatalf("未按分数定名次: %v", got)
	}
	if s := got[0].Score - 1.0/61; math.Abs(s) > 1e-12 {
		t.Fatalf("got[0]=%v want 1/61", got[0].Score)
	}
}

// 同输入两次融合结果逐元素相等（确定性）。
func TestRRFDeterministic(t *testing.T) {
	lists := [][]Entry{
		{{"a", 3}, {"b", 2}, {"c", 1}},
		{{"c", 3}, {"a", 2}},
		{{"b", 3}},
	}
	first := RRF(lists, KDefault)
	for i := 0; i < 10; i++ {
		again := RRF(lists, KDefault)
		if len(first) != len(again) {
			t.Fatalf("第 %d 次结果长度漂移", i)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("第 %d 次结果漂移: [%d] %v vs %v", i, j, first[j], again[j])
			}
		}
	}
}
