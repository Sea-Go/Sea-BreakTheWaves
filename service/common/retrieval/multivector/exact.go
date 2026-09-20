package multivector

import (
	"context"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
)

// exact is a persistent-artifact-backed token-row reference index. It scans
// only this lane's rows; no dense/sparse candidate enters the search.
type exact struct{}

func (exact) prepare(ctx context.Context, _ *Snapshot) error { return ctx.Err() }
func (exact) load(ctx context.Context, _ *Snapshot) error    { return ctx.Err() }
func (exact) verify(ctx context.Context, _ *Snapshot) error  { return ctx.Err() }

func (exact) search(ctx context.Context, s *Snapshot, q representation.TokenValues, allowed map[string]bool, perTokenK int) ([]hit, searchStats, error) {
	type scoredToken struct {
		chunkID  string
		position int
		score    float64
	}
	var stats searchStats
	best := map[string]float64{}
	for i, vector := range q.Values {
		if !q.Mask[i] {
			continue
		}
		rows := make([]scoredToken, 0, len(s.tokens))
		for _, token := range s.tokens {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
			if !allowed[token.chunkID] {
				continue
			}
			score, err := dot(vector, token.vector)
			if err != nil {
				return nil, stats, err
			}
			stats.observedRows++
			rows = append(rows, scoredToken{token.chunkID, token.position, score})
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].score == rows[j].score {
				if rows[i].chunkID == rows[j].chunkID {
					return rows[i].position < rows[j].position
				}
				return rows[i].chunkID < rows[j].chunkID
			}
			return rows[i].score > rows[j].score
		})
		for _, row := range rows[:min(perTokenK, len(rows))] {
			if old, ok := best[row.chunkID]; !ok || row.score > old {
				best[row.chunkID] = row.score
			}
		}
	}
	result := make([]hit, 0, len(best))
	for id, score := range best {
		result = append(result, hit{id, score})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].backendScore == result[j].backendScore {
			return result[i].id < result[j].id
		}
		return result[i].backendScore > result[j].backendScore
	})
	stats.candidateChunks = len(result)
	return result, stats, nil
}
