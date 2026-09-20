package dense

import (
	"context"
	"math"
	"sort"
)

type exact struct{}

func (exact) prepare(ctx context.Context, _ *Snapshot) error { return ctx.Err() }
func (exact) search(ctx context.Context, s *Snapshot, q []float64, allowed map[string]bool, k int) ([]hit, error) {
	hits := make([]hit, 0, len(allowed))
	for id := range allowed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		score, err := similarity(q, s.rows[id].Vector, s.service.config.Contract.Metric)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit{id, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].ID < hits[j].ID
		}
		return hits[i].Score > hits[j].Score
	})
	return hits[:min(k, len(hits))], nil
}
func similarity(a, b []float64, metric string) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, ErrInvalid
	}
	dot, aa, bb := 0.0, 0.0, 0.0
	for i, x := range a {
		dot += x * b[i]
		aa += x * x
		bb += b[i] * b[i]
	}
	if aa <= 0 || bb <= 0 || math.IsInf(aa, 0) || math.IsInf(bb, 0) || math.IsNaN(dot) || math.IsInf(dot, 0) {
		return 0, ErrInvalid
	}
	switch metric {
	case "dot":
		return dot, nil
	case "cosine":
		return dot / math.Sqrt(aa) / math.Sqrt(bb), nil
	default:
		return 0, ErrInvalid
	}
}

func (exact) verify(ctx context.Context, _ *Snapshot) error { return ctx.Err() }

func (exact) scoreKind(metric string) string {
	if metric == "dot" {
		return "dot_product"
	}
	return "cosine_similarity"
}

func (exact) load(ctx context.Context, _ *Snapshot) error { return ctx.Err() }
