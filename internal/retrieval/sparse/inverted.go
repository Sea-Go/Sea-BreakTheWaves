package sparse

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"math"
	"sort"
)

// Dot is the precise sorted-sparse inner product reference. Inputs must be
// nonempty, finite, positive, unique and ordered; no frequency aggregation occurs.
func Dot(a, b representation.SparseValues) (float64, error) {
	for _, v := range []representation.SparseValues{a, b} {
		if len(v.Indices) == 0 {
			return 0, ErrEmptyVector
		}
		if len(v.Indices) != len(v.Weights) {
			return 0, ErrInvalid
		}
		last := -1
		for i, id := range v.Indices {
			if id <= last || v.Weights[i] <= 0 || math.IsNaN(v.Weights[i]) || math.IsInf(v.Weights[i], 0) {
				return 0, ErrInvalid
			}
			last = id
		}
	}
	sum := 0.0
	for i, j := 0, 0; i < len(a.Indices) && j < len(b.Indices); {
		switch {
		case a.Indices[i] < b.Indices[j]:
			i++
		case a.Indices[i] > b.Indices[j]:
			j++
		default:
			sum += a.Weights[i] * b.Weights[j]
			i++
			j++
		}
	}
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return 0, ErrInvalid
	}
	return sum, nil
}

type inverted struct{}

func (inverted) prepare(ctx context.Context, _ *Snapshot) error { return ctx.Err() }
func (inverted) verify(ctx context.Context, _ *Snapshot) error  { return ctx.Err() }
func (inverted) search(ctx context.Context, s *Snapshot, q representation.SparseValues, allowed map[string]bool, k int) ([]hit, int, error) {
	scores := map[string]float64{}
	examined := 0
	for i, term := range q.Indices {
		for _, p := range s.postings[term] {
			if e := ctx.Err(); e != nil {
				return nil, examined, e
			}
			examined++
			if allowed[p.ChunkID] {
				scores[p.ChunkID] += q.Weights[i] * p.Weight
			}
		}
	}
	out := make([]hit, 0, len(scores))
	for id, score := range scores {
		if math.IsInf(score, 0) || math.IsNaN(score) {
			return nil, examined, ErrInvalid
		}
		if score > 0 {
			out = append(out, hit{id, score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	return out[:min(k, len(out))], examined, nil
}
