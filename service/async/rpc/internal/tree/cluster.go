package tree

import (
	"math"
	"sort"
)

// DefaultStopSize is the member cap used when Cluster is called with a
// non-positive stopSize.
const DefaultStopSize = 8

// DocVec pairs a document key with its dense embedding. The encoder that
// produced Vec is identified by encoder_id on the tree, not per vector.
type DocVec struct {
	DocKey string
	Vec    []float32
}

// Cluster deterministically partitions docs with agglomerative clustering:
// cosine distance, average linkage, greedy best-pair merges. Determinism
// rules, in order:
//
//  1. Docs are first sorted by DocKey (stable on input position), so input
//     order never influences the result.
//  2. Among admissible pairs the pair with the highest average cosine
//     similarity wins; ties go to the lexicographically smallest cluster
//     index pair (i < j).
//  3. Average-linkage similarities are maintained with a fixed weighted
//     update formula, so float accumulation order never varies between runs.
//
// Stopping rule: a merge is admissible only while the merged cluster stays
// within stopSize members. Clustering stops when no admissible pair remains
// (every cluster is at the cap already) or when a single cluster is left.
// Every returned group therefore has at most stopSize members. Groups hold
// indices into the original docs slice, members ascending, and groups are
// ordered by their smallest member index. Pure Go, no third-party deps.
func Cluster(docs []DocVec, stopSize int) [][]int {
	if len(docs) == 0 {
		return nil
	}
	if stopSize <= 0 {
		stopSize = DefaultStopSize
	}
	sortedIdx := make([]int, len(docs))
	for i := range sortedIdx {
		sortedIdx[i] = i
	}
	sort.SliceStable(sortedIdx, func(x, y int) bool {
		return docs[sortedIdx[x]].DocKey < docs[sortedIdx[y]].DocKey
	})
	vecs := make([][]float32, len(docs))
	for p, orig := range sortedIdx {
		vecs[p] = docs[orig].Vec
	}

	clusters := make([][]int, len(vecs))
	for i := range clusters {
		clusters[i] = []int{i}
	}
	// sim[a][b] is the average-linkage cosine similarity between clusters
	// a and b, maintained exactly through the weighted merge update.
	sim := make([][]float64, len(vecs))
	for i := range sim {
		sim[i] = make([]float64, len(vecs))
		for j := 0; j < i; j++ {
			s := cosine(vecs[i], vecs[j])
			sim[i][j] = s
			sim[j][i] = s
		}
	}
	for len(clusters) > 1 {
		bestA, bestB := -1, -1
		best := math.Inf(-1)
		for a := 0; a < len(clusters); a++ {
			for b := a + 1; b < len(clusters); b++ {
				if len(clusters[a])+len(clusters[b]) > stopSize {
					continue
				}
				if sim[a][b] > best {
					best, bestA, bestB = sim[a][b], a, b
				}
			}
		}
		if bestA < 0 {
			break // no admissible merge: every cluster is at the cap
		}
		a, b := bestA, bestB
		wa, wb := float64(len(clusters[a])), float64(len(clusters[b]))
		for c := 0; c < len(clusters); c++ {
			if c == a || c == b {
				continue
			}
			sim[a][c] = (wa*sim[a][c] + wb*sim[b][c]) / (wa + wb)
			sim[c][a] = sim[a][c]
		}
		merged := make([]int, 0, len(clusters[a])+len(clusters[b]))
		merged = append(merged, clusters[a]...)
		merged = append(merged, clusters[b]...)
		sort.Ints(merged)
		clusters[a] = merged
		clusters = append(clusters[:b], clusters[b+1:]...)
		sim = append(sim[:b], sim[b+1:]...)
		for r := range sim {
			sim[r] = append(sim[r][:b], sim[r][b+1:]...)
		}
	}

	groups := make([][]int, 0, len(clusters))
	for _, cl := range clusters {
		g := make([]int, 0, len(cl))
		for _, p := range cl {
			g = append(g, sortedIdx[p])
		}
		sort.Ints(g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(x, y int) bool { return groups[x][0] < groups[y][0] })
	return groups
}

// cosine returns the cosine similarity of a and b in float64. Zero vectors
// have undefined direction and score 0 against everything. Vectors of
// different lengths compare over their shared prefix; callers that need a
// dimension guarantee validate it before clustering.
func cosine(a, b []float32) float64 {
	m := len(a)
	if len(b) < m {
		m = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < m; i++ {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func finiteVec(v []float32) bool {
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
	}
	return true
}
