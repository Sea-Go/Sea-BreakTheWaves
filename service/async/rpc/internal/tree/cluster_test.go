package tree

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// axisDocs returns n docs whose vectors all point along the given 2-D axis,
// so within-group cosine similarity is 1 and across-group is 0.
func axisDocs(prefix string, n int, x, y float32) []DocVec {
	docs := make([]DocVec, 0, n)
	for i := 1; i <= n; i++ {
		docs = append(docs, DocVec{
			DocKey: fmt.Sprintf("%s%d", prefix, i),
			Vec:    []float32{x, y},
		})
	}
	return docs
}

func groupsToKeys(docs []DocVec, groups [][]int) [][]string {
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		keys := make([]string, 0, len(g))
		for _, idx := range g {
			keys = append(keys, docs[idx].DocKey)
		}
		sort.Strings(keys)
		out = append(out, keys)
	}
	sort.Slice(out, func(x, y int) bool { return out[x][0] < out[y][0] })
	return out
}

func TestClusterDeterministicSameInput(t *testing.T) {
	docs := append(axisDocs("a", 5, 1, 0), axisDocs("b", 5, 0, 1)...)
	first := Cluster(docs, 4)
	second := Cluster(docs, 4)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same input clustered differently:\n%v\n%v", first, second)
	}
	want := [][]int{{0, 1, 2, 3}, {4, 9}, {5, 6, 7, 8}}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("groups = %v, want %v", first, want)
	}
}

func TestClusterShuffleInvariant(t *testing.T) {
	docs := append(axisDocs("a", 5, 1, 0), axisDocs("b", 5, 0, 1)...)
	want := [][]string{
		{"a1", "a2", "a3", "a4"},
		{"a5", "b5"},
		{"b1", "b2", "b3", "b4"},
	}
	permutations := [][]int{
		{9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
		{4, 9, 0, 5, 1, 6, 2, 7, 3, 8},
		{0, 5, 1, 6, 2, 7, 3, 8, 4, 9},
	}
	for _, perm := range permutations {
		shuffled := make([]DocVec, 0, len(docs))
		for _, idx := range perm {
			shuffled = append(shuffled, docs[idx])
		}
		got := groupsToKeys(shuffled, Cluster(shuffled, 4))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %v produced %v, want %v", perm, got, want)
		}
	}
}

func TestClusterStopRule(t *testing.T) {
	t.Run("cap bounds every group", func(t *testing.T) {
		docs := axisDocs("k", 20, 1, 0)
		for _, stop := range []int{8, 0} { // 0 falls back to the default of 8
			groups := Cluster(docs, stop)
			sizes := []int{}
			for _, g := range groups {
				sizes = append(sizes, len(g))
				if len(g) > 8 {
					t.Fatalf("stop %d produced group of %d", stop, len(g))
				}
			}
			if !reflect.DeepEqual(sizes, []int{8, 8, 4}) {
				t.Fatalf("stop %d sizes = %v, want [8 8 4]", stop, sizes)
			}
		}
	})
	t.Run("stop size one keeps singletons", func(t *testing.T) {
		docs := axisDocs("a", 5, 1, 0)
		groups := Cluster(docs, 1)
		if len(groups) != 5 {
			t.Fatalf("groups = %d, want 5 singletons", len(groups))
		}
		for _, g := range groups {
			if len(g) != 1 {
				t.Fatalf("expected singletons, got %v", g)
			}
		}
	})
	t.Run("small input collapses to one cluster", func(t *testing.T) {
		docs := axisDocs("a", 3, 1, 0)
		groups := Cluster(docs, 8)
		want := [][]int{{0, 1, 2}}
		if !reflect.DeepEqual(groups, want) {
			t.Fatalf("groups = %v, want %v", groups, want)
		}
	})
	t.Run("empty input yields no groups", func(t *testing.T) {
		if got := Cluster(nil, 8); got != nil {
			t.Fatalf("groups = %v, want nil", got)
		}
	})
}

func TestClusterZeroVectorScoresZero(t *testing.T) {
	docs := []DocVec{
		{DocKey: "z1", Vec: []float32{0, 0}},
		{DocKey: "z2", Vec: []float32{0, 0}},
		{DocKey: "a1", Vec: []float32{1, 0}},
	}
	groups := Cluster(docs, 8)
	// z1/z2 similarity is 0, same as cross-group pairs; the lexicographic
	// tie-break merges (z1,z2) first, then (z1z2,a1) is admissible at cap 8.
	want := [][]int{{0, 1, 2}}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups = %v, want %v", groups, want)
	}
}
