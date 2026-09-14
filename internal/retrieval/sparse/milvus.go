package sparse

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

type MilvusConfig struct {
	Namespace string `json:"namespace"`
	Engine    string `json:"engine"`
}
type milvus struct {
	client *milvusclient.Client
	config MilvusConfig
}

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func NewMilvus(objects artifacts.Store, encoder Encoder, cfg Config, client *milvusclient.Client, options MilvusConfig) (*Service, error) {
	if options.Engine == "" {
		options.Engine = "milvus"
	}
	if client == nil || !namespacePattern.MatchString(options.Namespace) || (options.Engine != "milvus" && options.Engine != "native-lite") {
		return nil, ErrInvalid
	}
	s, e := New(objects, encoder, cfg)
	if e != nil {
		return nil, e
	}
	s.backend = &milvus{client, options}
	return s, nil
}
func (m *milvus) identity(s *Snapshot) (string, string) {
	b, _ := json.Marshal(struct {
		Index     corpusIdentity `json:"index"`
		Engine    string         `json:"engine"`
		Metric    string         `json:"metric"`
		DropRatio float64        `json:"drop_ratio"`
	}{corpusIdentity{s.ref.SHA256, m.config.Namespace}, m.config.Engine, "IP", 0})
	hash := artifacts.Hash(b)
	return "sea_sparse_" + m.config.Namespace + "_" + hash, "sea.sparse.milvus.v1:" + string(b)
}

type corpusIdentity struct {
	Hash      string `json:"hash"`
	Namespace string `json:"namespace"`
}

func (m *milvus) schema(s *Snapshot) *entity.Schema {
	name, desc := m.identity(s)
	return &entity.Schema{CollectionName: name, Description: desc, AutoID: false, EnableDynamicField: false, Fields: []*entity.Field{
		entity.NewField().WithName("chunk_id").WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(256),
		entity.NewField().WithName("revision_id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(256),
		entity.NewField().WithName("index_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("vector_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("vector").WithDataType(entity.FieldTypeSparseVector),
	}}
}
func (m *milvus) validate(ctx context.Context, s *Snapshot) error {
	name, desc := m.identity(s)
	got, e := m.client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(name))
	if e != nil {
		return e
	}
	if got.Schema == nil || got.Name != name || got.Schema.AutoID || got.Schema.EnableDynamicField || len(got.Schema.Fields) != 5 || (got.Schema.Description != desc && !(m.config.Engine == "native-lite" && got.Schema.Description == "")) {
		return fmt.Errorf("%w: sparse collection binding differs", ErrInvalid)
	}
	fields := map[string]*entity.Field{}
	for _, f := range got.Schema.Fields {
		fields[f.Name] = f
	}
	for _, want := range m.schema(s).Fields {
		f := fields[want.Name]
		if f == nil || f.DataType != want.DataType || f.PrimaryKey != want.PrimaryKey || f.AutoID || f.Nullable || !maps.Equal(f.TypeParams, want.TypeParams) {
			return fmt.Errorf("%w: sparse field %s differs", ErrInvalid, want.Name)
		}
	}
	descIndex, e := m.client.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(name, "vector"))
	if e != nil {
		return e
	}
	if descIndex.Index == nil {
		return ErrInvalid
	}
	params := descIndex.Params()
	if params["index_type"] != "SPARSE_INVERTED_INDEX" || params["metric_type"] != "IP" {
		return fmt.Errorf("%w: sparse inverted IP index required", ErrInvalid)
	}
	if drop := params["drop_ratio_build"]; drop != "" && drop != "0" && drop != "0.0" {
		return fmt.Errorf("%w: dropped sparse values forbidden", ErrInvalid)
	}
	if nested := params["params"]; nested != "" {
		var p map[string]json.RawMessage
		if json.Unmarshal([]byte(nested), &p) != nil {
			return ErrInvalid
		}
		if raw, ok := p["drop_ratio_build"]; ok {
			var ratio float64
			if json.Unmarshal(raw, &ratio) != nil || ratio != 0 {
				return ErrInvalid
			}
		}
	}
	return nil
}
func toMilvus(v representation.SparseValues) (entity.SparseEmbedding, error) {
	pos := make([]uint32, len(v.Indices))
	weights := make([]float32, len(v.Weights))
	for i, id := range v.Indices {
		if id < 0 || uint64(id) > math.MaxUint32 || v.Weights[i] <= 0 {
			return nil, ErrInvalid
		}
		pos[i] = uint32(id)
		weights[i] = float32(v.Weights[i])
		if math.IsInf(float64(weights[i]), 0) || math.IsNaN(float64(weights[i])) || weights[i] <= 0 {
			return nil, fmt.Errorf("%w: sparse weight not representable as float32", ErrInvalid)
		}
	}
	return entity.NewSliceSparseEmbedding(pos, weights)
}
func keys(s *Snapshot) []string {
	ids := make([]string, 0, len(s.rows))
	for id := range s.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (m *milvus) prepare(ctx context.Context, s *Snapshot) error {
	for _, r := range s.rows {
		if _, e := toMilvus(r.Vector); e != nil {
			return e
		}
	}
	name, _ := m.identity(s)
	exists, e := m.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if e != nil {
		return e
	}
	if !exists {
		idx := milvusclient.NewCreateIndexOption(name, "vector", index.NewSparseInvertedIndex(entity.IP, 0)).WithIndexName("vector")
		idx.WithExtraParam("params", `{"drop_ratio_build":0}`)
		q := milvusclient.NewCreateCollectionOption(name, m.schema(s)).WithConsistencyLevel(entity.ClStrong).WithIndexOptions(idx)
		if e = m.client.CreateCollection(ctx, q); e != nil {
			return e
		}
	}
	if e = m.validate(ctx, s); e != nil {
		return e
	}
	ids := keys(s)
	for start := 0; start < len(ids); start += s.service.config.BatchSize {
		group := ids[start:min(start+s.service.config.BatchSize, len(ids))]
		revisions, hashes, vectorHashes := []string{}, []string{}, []string{}
		vectors := []entity.SparseEmbedding{}
		for _, id := range group {
			r := s.rows[id]
			v, _ := toMilvus(r.Vector)
			vectors = append(vectors, v)
			revisions = append(revisions, r.Chunk.RevisionID)
			hashes = append(hashes, s.ref.SHA256)
			vectorHashes = append(vectorHashes, r.VectorHash)
		}
		q := milvusclient.NewColumnBasedInsertOption(name).WithVarcharColumn("chunk_id", group).WithVarcharColumn("revision_id", revisions).WithVarcharColumn("index_hash", hashes).WithVarcharColumn("vector_hash", vectorHashes).WithColumns(column.NewColumnSparseVectors("vector", vectors))
		if _, e = m.client.Upsert(ctx, q); e != nil {
			return e
		}
	}
	flush, e := m.client.Flush(ctx, milvusclient.NewFlushOption(name))
	if e != nil {
		return e
	}
	if e = flush.Await(ctx); e != nil {
		return e
	}
	return m.load(ctx, s)
}
func (m *milvus) load(ctx context.Context, s *Snapshot) error {
	if e := m.validate(ctx, s); e != nil {
		return e
	}
	name, _ := m.identity(s)
	task, e := m.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if e != nil {
		return e
	}
	if e = task.Await(ctx); e != nil {
		return e
	}
	return m.verify(ctx, s)
}

// Load reopens and validates a fixed projection after restart. It never encodes,
// upserts or repairs data; only Build/ResumeIndex may write a projection.
func (s *Service) Load(ctx context.Context, ref corpus.Ref) (*Snapshot, error) {
	snap, e := s.Open(ctx, ref)
	if e != nil {
		return nil, e
	}
	if backend, ok := s.backend.(*milvus); ok {
		e = backend.load(ctx, snap)
	} else {
		e = s.backend.verify(ctx, snap)
	}
	return snap, e
}
func (m *milvus) verify(ctx context.Context, s *Snapshot) error {
	if e := m.validate(ctx, s); e != nil {
		return e
	}
	name, _ := m.identity(s)
	ids := keys(s)
	count, e := m.client.Query(ctx, milvusclient.NewQueryOption(name).WithFilter("chunk_id != \"\"").WithOutputFields("count(*)").WithConsistencyLevel(entity.ClStrong))
	if e != nil {
		return e
	}
	if count.Err != nil {
		return count.Err
	}
	c := count.GetColumn("count(*)")
	if c == nil {
		return ErrInvalid
	}
	n, e := c.GetAsInt64(0)
	if e != nil || int(n) != len(ids) {
		return fmt.Errorf("%w: sparse projection count differs", ErrInvalid)
	}
	for start := 0; start < len(ids); start += 128 {
		group := ids[start:min(start+128, len(ids))]
		out, e := m.client.Query(ctx, milvusclient.NewQueryOption(name).WithFilter("chunk_id in "+literal(group)).WithOutputFields("chunk_id", "revision_id", "index_hash", "vector_hash", "vector").WithConsistencyLevel(entity.ClStrong).WithLimit(len(group)))
		if e != nil {
			return e
		}
		if out.Err != nil {
			return out.Err
		}
		if out.Len() != len(group) {
			return fmt.Errorf("%w: missing sparse rows", ErrInvalid)
		}
		seen := map[string]bool{}
		for i := 0; i < out.Len(); i++ {
			for _, f := range []string{"chunk_id", "revision_id", "index_hash", "vector_hash", "vector"} {
				if out.GetColumn(f) == nil {
					return ErrInvalid
				}
			}
			id, e := out.GetColumn("chunk_id").GetAsString(i)
			if e != nil {
				return e
			}
			r, ok := s.rows[id]
			if !ok || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			for f, want := range map[string]string{"revision_id": r.Chunk.RevisionID, "index_hash": s.ref.SHA256, "vector_hash": r.VectorHash} {
				got, e := out.GetColumn(f).GetAsString(i)
				if e != nil || got != want {
					return fmt.Errorf("%w: sparse row metadata differs", ErrInvalid)
				}
			}
			raw, e := out.GetColumn("vector").Get(i)
			if e != nil {
				return e
			}
			v, ok := raw.(entity.SparseEmbedding)
			if !ok || v.Len() != len(r.Vector.Indices) {
				return ErrInvalid
			}
			for j, want := range r.Vector.Indices {
				position, weight, ok := v.Get(j)
				if !ok || int(position) != want || weight != float32(r.Vector.Weights[j]) {
					return fmt.Errorf("%w: sparse projected values differ", ErrInvalid)
				}
			}
		}
	}
	return nil
}
func (m *milvus) search(ctx context.Context, s *Snapshot, q representation.SparseValues, allowed map[string]bool, k int) ([]hit, int, error) {
	vector, e := toMilvus(q)
	if e != nil {
		return nil, 0, e
	}
	name, _ := m.identity(s)
	revs := map[string]bool{}
	for id := range allowed {
		revs[s.rows[id].Chunk.RevisionID] = true
	}
	valid := []string{}
	for id := range revs {
		valid = append(valid, id)
	}
	sort.Strings(valid)
	params := index.NewSparseAnnParam()
	params.WithDropRatio(0)
	option := milvusclient.NewSearchOption(name, k, []entity.Vector{vector}).WithANNSField("vector").WithAnnParam(params).WithSearchParam("metric_type", "IP").WithConsistencyLevel(entity.ClStrong).WithFilter("index_hash == "+literal(s.ref.SHA256)+" and revision_id in "+literal(valid)).WithOutputFields("revision_id", "index_hash", "vector_hash")
	responses, e := m.client.Search(ctx, option)
	if e != nil {
		return nil, 0, e
	}
	if len(responses) != 1 || responses[0].Err != nil {
		return nil, 0, ErrInvalid
	}
	out := responses[0]
	if out.IDs == nil || out.IDs.Len() != out.ResultCount || len(out.Scores) != out.ResultCount {
		return nil, 0, ErrInvalid
	}
	hits := make([]hit, 0, out.ResultCount)
	for i := 0; i < out.ResultCount; i++ {
		id, e := out.IDs.GetAsString(i)
		if e != nil {
			return nil, 0, e
		}
		r, ok := s.rows[id]
		if !ok || !allowed[id] {
			return nil, 0, ErrInvalid
		}
		for f, want := range map[string]string{"revision_id": r.Chunk.RevisionID, "index_hash": s.ref.SHA256, "vector_hash": r.VectorHash} {
			col := out.GetColumn(f)
			if col == nil {
				return nil, 0, ErrInvalid
			}
			v, e := col.GetAsString(i)
			if e != nil || v != want {
				return nil, 0, ErrInvalid
			}
		}
		expected, e := Dot(asFloat32(q), asFloat32(r.Vector))
		if e != nil {
			return nil, 0, e
		}
		score := float64(out.Scores[i])
		if math.IsNaN(score) || math.IsInf(score, 0) || math.Abs(expected-score) > 1e-5*math.Max(1, math.Abs(expected)) {
			return nil, 0, fmt.Errorf("%w: backend score is not sparse inner product", ErrInvalid)
		}
		if score > 0 {
			hits = append(hits, hit{id, score})
		}
	}
	return hits, -1, nil
}
func asFloat32(v representation.SparseValues) representation.SparseValues {
	out := representation.SparseValues{Indices: append([]int(nil), v.Indices...), Weights: make([]float64, len(v.Weights))}
	for i, x := range v.Weights {
		out.Weights[i] = float64(float32(x))
	}
	return out
}
func literal(v any) string { b, _ := json.Marshal(v); return string(b) }
