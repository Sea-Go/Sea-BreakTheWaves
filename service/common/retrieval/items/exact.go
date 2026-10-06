package items

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"sort"
	"sync"
)

// Exact is a small-corpus numerical reference for tests/benchmarks, never a serving fallback.
type Exact struct {
	mu   sync.RWMutex
	rows map[string]Record
}

func NewExact() *Exact { return &Exact{rows: map[string]Record{}} }
func (x *Exact) Ensure(_ context.Context, _ item.Retrieval) error { return nil }
func (x *Exact) Upsert(ctx context.Context, r item.Retrieval, rows []Record) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	for _, v := range rows {
		x.rows[v.ID] = v
	}
	return nil
}
func (x *Exact) Delete(_ context.Context, r item.Retrieval, root string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for key, v := range x.rows {
		if v.RootID == root {
			delete(x.rows, key)
		}
	}
	return nil
}
func Dot(a, b []float32) float64 {
	var sum float64
	for i := 0; i < min(len(a), len(b)); i++ {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
func SparseDot(a, b inference.Sparse) float64 {
	i, j := 0, 0
	sum := 0.
	for i < len(a.Indices) && j < len(b.Indices) {
		if a.Indices[i] < b.Indices[j] {
			i++
		} else if a.Indices[i] > b.Indices[j] {
			j++
		} else {
			sum += float64(a.Values[i]) * float64(b.Values[j])
			i++
			j++
		}
	}
	return sum
}
func MaxSim(a, b [][]float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	sum := 0.
	for _, q := range a {
		best := Dot(q, b[0])
		for _, d := range b[1:] {
			best = max(best, Dot(q, d))
		}
		sum += best
	}
	return sum / float64(len(a))
}
func (x *Exact) Search(ctx context.Context, r item.Retrieval, lane string, q inference.Encoding, kinds []string, k int) ([]Candidate, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	allowed := map[string]bool{}
	for _, kind := range kinds {
		allowed[kind] = true
	}
	merged := map[string]Candidate{}
	for _, row := range x.rows {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if !allowed[row.RootKind] {
			continue
		}
		best := -1e300
		for _, seg := range q.Segments {
			var score float64
			switch lane {
			case "match":
				score = Dot(seg.Dense, row.Match)
			case "dense":
				score = Dot(seg.Dense, row.Encoding.Dense)
			case "sparse":
				score = SparseDot(seg.Sparse, row.Encoding.Sparse)
			case "multi":
				score = MaxSim(seg.Multi, row.Encoding.Multi)
			default:
				return nil, item.ErrInvalid
			}
			best = max(best, score)
		}
		key := row.ItemID + ":" + row.RevisionID
		if old, ok := merged[key]; !ok || best > old.Score {
			merged[key] = Candidate{ItemID: row.ItemID, RevisionID: row.RevisionID, Score: best, Location: row.Location}
		}
	}
	out := make([]Candidate, 0, len(merged))
	for _, c := range merged {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func (x *Exact) Prune(_ context.Context, r item.Retrieval, revision string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for id, v := range x.rows {
		if v.RevisionID == revision {
			delete(x.rows, id)
		}
	}
	return nil
}
