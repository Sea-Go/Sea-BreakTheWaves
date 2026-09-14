package dense

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// MilvusConfig is explicit: no discovery of production services or collections.
// Namespace separates operators; the full immutable index digest and HNSW
// settings further distinguish every collection. No API drops a collection.
type MilvusConfig struct {
	Namespace      string `json:"namespace"`
	Engine         string `json:"engine"`
	M              int    `json:"m"`
	EFConstruction int    `json:"ef_construction"`
	EFSearch       int    `json:"ef_search"`
}
type milvus struct {
	client *milvusclient.Client
	config MilvusConfig
}

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// NewMilvus borrows an explicit SDK client; its creator must Close the client.
// Construction is side-effect free. Build creates only its digest-bound
// collection, and Search never creates, repairs or silently rebuilds one.
func NewMilvus(objects artifacts.Store, encoder Encoder, config Config, client *milvusclient.Client, options MilvusConfig) (*Service, error) {
	if options.Engine == "" {
		options.Engine = "milvus"
	}
	if (options.Engine != "milvus" && options.Engine != "lite") || client == nil || !namespacePattern.MatchString(options.Namespace) || options.M < 2 || options.M > 2048 || options.EFConstruction < options.M || options.EFConstruction > 65536 || options.EFSearch < 1 || options.EFSearch > 65536 {
		return nil, ErrInvalid
	}
	s, err := New(objects, encoder, config)
	if err != nil {
		return nil, err
	}
	s.backend = &milvus{client: client, config: options}
	return s, nil
}
func (m *milvus) identity(s *Snapshot) (string, string) {
	raw, _ := json.Marshal(struct {
		IndexHash  string       `json:"index_hash"`
		Parameters MilvusConfig `json:"parameters"`
		Metric     string       `json:"metric"`
	}{s.ref.SHA256, m.config, s.service.config.Contract.Metric})
	hash := artifacts.Hash(raw)
	return "sea_dense_" + m.config.Namespace + "_" + hash, "sea.dense.milvus.v1:" + string(raw)
}
func (m *milvus) metric(s *Snapshot) entity.MetricType {
	if s.service.config.Contract.Metric == "dot" {
		return entity.IP
	}
	return entity.COSINE
}
func (m *milvus) schema(s *Snapshot) *entity.Schema {
	name, description := m.identity(s)
	return &entity.Schema{CollectionName: name, Description: description, AutoID: false, EnableDynamicField: false, Fields: []*entity.Field{
		entity.NewField().WithName("chunk_id").WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(256),
		entity.NewField().WithName("revision_id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(256),
		entity.NewField().WithName("index_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("vector_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(s.service.config.Contract.Dimensions)),
	}}
}
func (m *milvus) validateCollection(ctx context.Context, s *Snapshot) error {
	name, description := m.identity(s)
	collection, err := m.client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(name))
	if err != nil {
		return err
	}
	if collection.Schema == nil || collection.Name != name || (collection.Schema.Description != description && !(m.config.Engine == "lite" && collection.Schema.Description == "")) || collection.Schema.AutoID || collection.Schema.EnableDynamicField || len(collection.Schema.Fields) != 5 {
		return fmt.Errorf("%w: Milvus collection identity differs", ErrInvalid)
	}
	fields := map[string]*entity.Field{}
	for _, field := range collection.Schema.Fields {
		fields[field.Name] = field
	}
	for _, wanted := range m.schema(s).Fields {
		got := fields[wanted.Name]
		if got == nil || got.DataType != wanted.DataType || got.PrimaryKey != wanted.PrimaryKey || got.AutoID || got.Nullable || !reflect.DeepEqual(got.TypeParams, wanted.TypeParams) {
			return fmt.Errorf("%w: Milvus field %s differs", ErrInvalid, wanted.Name)
		}
	}
	desc, err := m.client.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(name, "vector"))
	if err != nil {
		return err
	}
	if desc.Index == nil {
		return fmt.Errorf("%w: missing Milvus index", ErrInvalid)
	}
	params := desc.Params()
	if nested := params["params"]; nested != "" {
		var declared struct {
			M  int `json:"M"`
			EF int `json:"efConstruction"`
		}
		if err := json.Unmarshal([]byte(nested), &declared); err != nil {
			return ErrInvalid
		}
		if flat := params["M"]; flat != "" && flat != strconv.Itoa(declared.M) {
			return ErrInvalid
		}
		if flat := params["efConstruction"]; flat != "" && flat != strconv.Itoa(declared.EF) {
			return ErrInvalid
		}
		params["M"] = strconv.Itoa(declared.M)
		params["efConstruction"] = strconv.Itoa(declared.EF)
	}
	if params["index_type"] != "HNSW" || params["metric_type"] != string(m.metric(s)) || params["M"] != strconv.Itoa(m.config.M) || params["efConstruction"] != strconv.Itoa(m.config.EFConstruction) {
		return fmt.Errorf("%w: Milvus HNSW parameters differ", ErrInvalid)
	}
	return nil
}
func toFloat32(v []float64) ([]float32, error) {
	result := make([]float32, len(v))
	norm := 0.0
	for i, x := range v {
		result[i] = float32(x)
		if math.IsInf(float64(result[i]), 0) || math.IsNaN(float64(result[i])) {
			return nil, ErrInvalid
		}
		norm += float64(result[i]) * float64(result[i])
	}
	if norm == 0 {
		return nil, fmt.Errorf("%w: vector underflows float32", ErrInvalid)
	}
	return result, nil
}
func (m *milvus) prepare(ctx context.Context, s *Snapshot) error {
	name, _ := m.identity(s)
	// Validate conversion for the entire input before any collection write.
	for _, r := range s.rows {
		if _, err := toFloat32(r.Vector); err != nil {
			return err
		}
	}
	exists, err := m.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if !exists {
		indexOption := milvusclient.NewCreateIndexOption(name, "vector", index.NewHNSWIndex(m.metric(s), m.config.M, m.config.EFConstruction)).WithIndexName("vector")
		nested, _ := json.Marshal(map[string]int{"M": m.config.M, "efConstruction": m.config.EFConstruction})
		indexOption.WithExtraParam("params", string(nested))
		option := milvusclient.NewCreateCollectionOption(name, m.schema(s)).WithConsistencyLevel(entity.ClStrong).WithIndexOptions(indexOption)
		if err = m.client.CreateCollection(ctx, option); err != nil {
			return err
		}
	}
	if err = m.validateCollection(ctx, s); err != nil {
		return err
	}
	keys := make([]string, 0, len(s.rows))
	for id := range s.rows {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for start := 0; start < len(keys); start += s.service.config.BatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids := keys[start:min(start+s.service.config.BatchSize, len(keys))]
		revisions, hashes, vectorHashes := make([]string, 0, len(ids)), make([]string, 0, len(ids)), make([]string, 0, len(ids))
		vectors := make([][]float32, 0, len(ids))
		for _, id := range ids {
			r := s.rows[id]
			v, _ := toFloat32(r.Vector)
			vectors = append(vectors, v)
			revisions = append(revisions, r.Chunk.RevisionID)
			hashes = append(hashes, s.ref.SHA256)
			vectorHashes = append(vectorHashes, r.VectorHash)
		}
		option := milvusclient.NewColumnBasedInsertOption(name).WithVarcharColumn("chunk_id", ids).WithVarcharColumn("revision_id", revisions).WithVarcharColumn("index_hash", hashes).WithVarcharColumn("vector_hash", vectorHashes).WithFloatVectorColumn("vector", s.service.config.Contract.Dimensions, vectors)
		// Upsert retries write the same immutable rows, never an alias/current head.
		if _, err = m.client.Upsert(ctx, option); err != nil {
			return err
		}
	}
	flush, err := m.client.Flush(ctx, milvusclient.NewFlushOption(name))
	if err != nil {
		return err
	}
	if err = flush.Await(ctx); err != nil {
		return err
	}
	load, err := m.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		return err
	}
	if err = load.Await(ctx); err != nil {
		return err
	}
	return m.verify(ctx, s)
}
func (m *milvus) verify(ctx context.Context, s *Snapshot) error {
	if err := m.validateCollection(ctx, s); err != nil {
		return err
	}
	name, _ := m.identity(s)
	count, err := m.client.Query(ctx, milvusclient.NewQueryOption(name).WithOutputFields("count(*)").WithConsistencyLevel(entity.ClStrong))
	if err != nil {
		return err
	}
	if count.Err != nil {
		return count.Err
	}
	if count.Len() != 1 || count.GetColumn("count(*)") == nil {
		return ErrInvalid
	}
	total, err := count.GetColumn("count(*)").GetAsInt64(0)
	if err != nil {
		return err
	}
	if total != int64(len(s.rows)) {
		return fmt.Errorf("%w: Milvus total coverage differs", ErrInvalid)
	}
	keys := make([]string, 0, len(s.rows))
	for id := range s.rows {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for start := 0; start < len(keys); start += 128 {
		ids := keys[start:min(start+128, len(keys))]
		wanted := map[string]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		result, err := m.client.Query(ctx, milvusclient.NewQueryOption(name).WithFilter("chunk_id in "+jsonLiteral(ids)+" and index_hash == "+jsonLiteral(s.ref.SHA256)).WithOutputFields("chunk_id", "revision_id", "vector_hash", "vector").WithConsistencyLevel(entity.ClStrong).WithLimit(len(ids)))
		if err != nil {
			return err
		}
		if result.Err != nil {
			return result.Err
		}
		if result.Len() != len(ids) {
			return fmt.Errorf("%w: Milvus missing projected rows", ErrInvalid)
		}
		seen := map[string]bool{}
		for i := 0; i < result.Len(); i++ {
			columns := []string{"chunk_id", "revision_id", "vector_hash", "vector"}
			for _, field := range columns {
				if result.GetColumn(field) == nil {
					return ErrInvalid
				}
			}
			id, err := result.GetColumn("chunk_id").GetAsString(i)
			if err != nil {
				return err
			}
			r, ok := s.rows[id]
			if !ok || !wanted[id] || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			revision, err := result.GetColumn("revision_id").GetAsString(i)
			if err != nil {
				return err
			}
			hash, err := result.GetColumn("vector_hash").GetAsString(i)
			if err != nil {
				return err
			}
			value, err := result.GetColumn("vector").Get(i)
			if err != nil {
				return err
			}
			vector, ok := value.(entity.FloatVector)
			expected, _ := toFloat32(r.Vector)
			if !ok || revision != r.Chunk.RevisionID || hash != r.VectorHash || !reflect.DeepEqual([]float32(vector), expected) {
				return fmt.Errorf("%w: Milvus projection row differs", ErrInvalid)
			}
		}
	}
	return nil
}
func (m *milvus) search(ctx context.Context, s *Snapshot, q []float64, allowed map[string]bool, k int) ([]hit, error) {
	vector, err := toFloat32(q)
	if err != nil {
		return nil, err
	}
	name, _ := m.identity(s)
	// Valid revisions are encoded as a pre-ANN filter. The fixed collection and
	// index hash pin module/release/generation, with a second local candidate check.
	revisions := map[string]bool{}
	for id := range allowed {
		revisions[s.rows[id].Chunk.RevisionID] = true
	}
	valid := make([]string, 0, len(revisions))
	for id := range revisions {
		valid = append(valid, id)
	}
	sort.Strings(valid)
	option := milvusclient.NewSearchOption(name, k, []entity.Vector{entity.FloatVector(vector)}).WithANNSField("vector").WithSearchParam("metric_type", string(m.metric(s))).WithAnnParam(index.NewHNSWAnnParam(max(k, m.config.EFSearch))).WithConsistencyLevel(entity.ClStrong).WithFilter("index_hash == "+jsonLiteral(s.ref.SHA256)+" and revision_id in "+jsonLiteral(valid)).WithOutputFields("chunk_id", "revision_id", "vector_hash")
	result, err := m.client.Search(ctx, option)
	if err != nil {
		return nil, err
	}
	if len(result) != 1 || result[0].Err != nil {
		return nil, fmt.Errorf("%w: invalid Milvus query result", ErrInvalid)
	}
	found := result[0]
	if found.IDs == nil || found.IDs.Len() != found.ResultCount || len(found.Scores) != found.ResultCount {
		return nil, ErrInvalid
	}
	hits := make([]hit, 0, found.ResultCount)
	for i := 0; i < found.ResultCount; i++ {
		id, err := found.IDs.GetAsString(i)
		if err != nil {
			return nil, err
		}
		r, ok := s.rows[id]
		if !ok || !allowed[id] {
			return nil, ErrInvalid
		}
		for _, field := range []string{"revision_id", "vector_hash"} {
			if found.GetColumn(field) == nil {
				return nil, ErrInvalid
			}
		}
		revision, err := found.GetColumn("revision_id").GetAsString(i)
		if err != nil {
			return nil, err
		}
		hash, err := found.GetColumn("vector_hash").GetAsString(i)
		if err != nil {
			return nil, err
		}
		if revision != r.Chunk.RevisionID || hash != r.VectorHash {
			return nil, ErrInvalid
		}
		hits = append(hits, hit{id, float64(found.Scores[i])})
	}
	return hits, nil
}

// Literals are JSON encoded; field names and operators are fixed by this lane.
// This equivalent Milvus expression also works on engines without templates.
func jsonLiteral(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func (m *milvus) scoreKind(metric string) string { return exact{}.scoreKind(metric) }

func (m *milvus) load(ctx context.Context, s *Snapshot) error {
	name, _ := m.identity(s)
	if err := m.validateCollection(ctx, s); err != nil {
		return err
	}
	task, err := m.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		return err
	}
	return task.Await(ctx)
}
