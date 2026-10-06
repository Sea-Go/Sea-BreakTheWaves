package items

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	mi "github.com/milvus-io/milvus/client/v2/index"
	mc "github.com/milvus-io/milvus/client/v2/milvusclient"
	"math"
	"sort"
	"strings"
)

type Milvus struct {
	Client          *mc.Client
	BatchSize       int
	QueryTokenLimit int
}

func collection(r item.Retrieval, lane string) string {
	switch lane {
	case "dense":
		return r.Dense
	case "sparse":
		return r.Sparse
	case "match":
		return r.Match
	default:
		return r.Multi
	}
}
func schema(name string, dimensions int, sparse bool) *entity.Schema {
	fields := []*entity.Field{entity.NewField().WithName("id").WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(512)}
	for _, name := range []string{"item_id", "root_id", "root_kind", "revision_id", "section_id"} {
		fields = append(fields, entity.NewField().WithName(name).WithDataType(entity.FieldTypeVarChar).WithMaxLength(256))
	}
	fields = append(fields, entity.NewField().WithName("start").WithDataType(entity.FieldTypeInt64), entity.NewField().WithName("end").WithDataType(entity.FieldTypeInt64))
	vector := entity.NewField().WithName("vector")
	if sparse {
		vector.WithDataType(entity.FieldTypeSparseVector)
	} else {
		vector.WithDataType(entity.FieldTypeFloatVector).WithDim(int64(dimensions))
	}
	fields = append(fields, vector)
	return &entity.Schema{CollectionName: name, AutoID: false, EnableDynamicField: false, Fields: fields}
}
func (m *Milvus) Ensure(ctx context.Context, r item.Retrieval) error {
	if m.Client == nil || r.Dimensions < 1 {
		return item.ErrUnavailable
	}
	for _, lane := range lanes(r) {
		name := collection(r, lane)
		if name == "" || strings.ContainsAny(name, " /.-") {
			return item.ErrInvalid
		}
		has, e := m.Client.HasCollection(ctx, mc.NewHasCollectionOption(name))
		if e != nil {
			return e
		}
		if !has {
			if e = m.Client.CreateCollection(ctx, mc.NewCreateCollectionOption(name, schema(name, dimensions(r, lane), lane == "sparse"))); e != nil {
				return e
			}
		}
		indexes, e := m.Client.ListIndexes(ctx, mc.NewListIndexOption(name))
		if e != nil {
			return e
		}
		if len(indexes) == 0 {
			var idx mi.Index = mi.NewDiskANNIndex(entity.IP)
			if lane == "sparse" {
				idx = mi.NewSparseInvertedIndex(entity.IP, 0)
			}
			task, e := m.Client.CreateIndex(ctx, mc.NewCreateIndexOption(name, "vector", idx).WithIndexName("vector"))
			if e != nil {
				return e
			}
			if e = task.Await(ctx); e != nil {
				return e
			}
		}
		task, e := m.Client.LoadCollection(ctx, mc.NewLoadCollectionOption(name))
		if e != nil {
			return e
		}
		if e = task.Await(ctx); e != nil {
			return e
		}
	}
	return nil
}
func sparseVector(v inference.Sparse) (entity.SparseEmbedding, error) {
	return entity.NewSliceSparseEmbedding(v.Indices, v.Values)
}
func (m *Milvus) Upsert(ctx context.Context, r item.Retrieval, records []Record) error {
	batch := m.BatchSize
	if batch < 1 {
		batch = 256
	}
	for _, lane := range lanes(r) {
		var ids, itemIDs, roots, kinds, revisions, sections []string
		var starts, ends []int64
		var vectors [][]float32
		var sparse []entity.SparseEmbedding
		flush := func() error {
			if len(ids) == 0 {
				return nil
			}
			q := mc.NewColumnBasedInsertOption(collection(r, lane)).WithVarcharColumn("id", ids).WithVarcharColumn("item_id", itemIDs).WithVarcharColumn("root_id", roots).WithVarcharColumn("root_kind", kinds).WithVarcharColumn("revision_id", revisions).WithVarcharColumn("section_id", sections).WithInt64Column("start", starts).WithInt64Column("end", ends)
			if lane == "sparse" {
				q.WithColumns(column.NewColumnSparseVectors("vector", sparse))
			} else {
				q.WithFloatVectorColumn("vector", dimensions(r, lane), vectors)
			}
			_, e := m.Client.Upsert(ctx, q)
			ids = nil
			itemIDs = nil
			roots = nil
			kinds = nil
			revisions = nil
			sections = nil
			starts = nil
			ends = nil
			vectors = nil
			sparse = nil
			return e
		}
		for _, rec := range records {
			count := 1
			if lane == "multi" {
				count = len(rec.Encoding.Multi)
			}
			for i := 0; i < count; i++ {
				id := rec.ID
				if lane == "multi" {
					id = fmt.Sprintf("%s/%d", id, i)
				}
				ids = append(ids, id)
				itemIDs = append(itemIDs, rec.ItemID)
				roots = append(roots, rec.RootID)
				kinds = append(kinds, rec.RootKind)
				revisions = append(revisions, rec.RevisionID)
				sections = append(sections, rec.Location.SectionID)
				starts = append(starts, rec.Location.Start)
				ends = append(ends, rec.Location.End)
				switch lane {
				case "sparse":
					v, e := sparseVector(rec.Encoding.Sparse)
					if e != nil {
						return e
					}
					sparse = append(sparse, v)
				case "match":
					if len(rec.Match) != r.MatchDimensions {
						return item.ErrInvalid
					}
					vectors = append(vectors, rec.Match)
				case "dense":
					if len(rec.Encoding.Dense) != r.Dimensions {
						return item.ErrInvalid
					}
					vectors = append(vectors, rec.Encoding.Dense)
				case "multi":
					if len(rec.Encoding.Multi[i]) != r.Dimensions {
						return item.ErrInvalid
					}
					vectors = append(vectors, rec.Encoding.Multi[i])
				}
				if len(ids) >= batch {
					if e := flush(); e != nil {
						return e
					}
				}
			}
		}
		if e := flush(); e != nil {
			return e
		}
	}
	return nil
}
func literal(v any) string { b, _ := json.Marshal(v); return string(b) }
func (m *Milvus) Search(ctx context.Context, r item.Retrieval, lane string, encoded inference.Encoding, kinds []string, k int) ([]Candidate, error) {
	if len(encoded.Segments) == 0 || k < 1 || len(kinds) == 0 {
		return nil, item.ErrInvalid
	}
	// The online multi-vector score is an ANN approximation over returned token neighbors.
	// Exact full MaxSim belongs to offline comparison; no whole-corpus matrix is loaded.
	vectors := []entity.Vector{}
	for _, seg := range encoded.Segments {
		switch lane {
		case "dense", "match":
			vectors = append(vectors, entity.FloatVector(seg.Dense))
		case "sparse":
			v, e := sparseVector(seg.Sparse)
			if e != nil {
				return nil, e
			}
			vectors = append(vectors, v)
		case "multi":
			for _, v := range seg.Multi {
				vectors = append(vectors, entity.FloatVector(v))
			}
		default:
			return nil, item.ErrInvalid
		}
	}
	if lane == "multi" && m.QueryTokenLimit > 0 && len(vectors) > m.QueryTokenLimit {
		return nil, fmt.Errorf("%w: query token budget exceeded", item.ErrInvalid)
	}
	filter := "root_kind in " + literal(kinds)
	byItem := map[string]Candidate{}
	sums := map[string]float64{}
	for _, v := range vectors {
		q := mc.NewSearchOption(collection(r, lane), k, []entity.Vector{v}).WithANNSField("vector").WithConsistencyLevel(entity.ClStrong).WithFilter(filter).WithOutputFields("item_id", "revision_id", "section_id", "start", "end")
		if lane == "sparse" {
			q.WithAnnParam(mi.NewSparseAnnParam())
		} else {
			q.WithAnnParam(mi.NewDiskAnnParam(max(k, 64)))
		}
		sets, e := m.Client.Search(ctx, q)
		if e != nil {
			return nil, e
		}
		if len(sets) != 1 {
			return nil, item.ErrInvalid
		}
		set := sets[0]
		if set.Err != nil {
			return nil, set.Err
		}
		tokenBest := map[string]Candidate{}
		for i := 0; i < set.ResultCount; i++ {
			id, e := set.GetColumn("item_id").GetAsString(i)
			if e != nil {
				return nil, e
			}
			rev, e := set.GetColumn("revision_id").GetAsString(i)
			if e != nil {
				return nil, e
			}
			section, e := set.GetColumn("section_id").GetAsString(i)
			if e != nil {
				return nil, e
			}
			start, e := set.GetColumn("start").GetAsInt64(i)
			if e != nil {
				return nil, e
			}
			end, e := set.GetColumn("end").GetAsInt64(i)
			if e != nil {
				return nil, e
			}
			score := float64(set.Scores[i])
			if math.IsNaN(score) || math.IsInf(score, 0) {
				return nil, item.ErrInvalid
			}
			key := id + ":" + rev
			c := Candidate{Approximate: lane == "multi", ItemID: id, RevisionID: rev, Score: score, Location: item.Location{RevisionID: rev, SectionID: section, Start: start, End: end}}
			if old, ok := tokenBest[key]; !ok || score > old.Score {
				tokenBest[key] = c
			}
		}
		for key, c := range tokenBest {
			if lane == "multi" {
				sums[key] += c.Score
			}
			if old, ok := byItem[key]; !ok || c.Score > old.Score {
				byItem[key] = c
			}
		}
	}
	out := make([]Candidate, 0, len(byItem))
	for key, c := range byItem {
		if lane == "multi" {
			c.Score = sums[key] / float64(len(vectors))
		}
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
func (m *Milvus) Delete(ctx context.Context, r item.Retrieval, root string) error {
	for _, lane := range lanes(r) {
		if _, e := m.Client.Delete(ctx, mc.NewDeleteOption(collection(r, lane)).WithExpr("root_id == "+literal(root))); e != nil {
			return e
		}
	}
	return nil
}

var _ Index = (*Milvus)(nil)

func lanes(r item.Retrieval) []string {
	out := []string{"dense", "sparse", "multi"}
	if r.Match != "" {
		out = append(out, "match")
	}
	return out
}
func dimensions(r item.Retrieval, lane string) int {
	if lane == "match" {
		return r.MatchDimensions
	}
	return r.Dimensions
}

func (m *Milvus) Prune(ctx context.Context, r item.Retrieval, revision string) error {
	for _, lane := range lanes(r) {
		if _, e := m.Client.Delete(ctx, mc.NewDeleteOption(collection(r, lane)).WithExpr("revision_id == "+literal(revision))); e != nil {
			return e
		}
	}
	return nil
}
