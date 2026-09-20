package multivector

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// MilvusConfig keeps this lane in its own immutable token-row collection.
// The SDK client is borrowed; the caller owns its lifecycle.
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

func NewMilvus(objects artifacts.Store, encoder Encoder, c Config, client *milvusclient.Client, cfg MilvusConfig, options ...Option) (*Service, error) {
	if cfg.Engine == "" {
		cfg.Engine = "milvus"
	}
	if client == nil || (cfg.Engine != "milvus" && cfg.Engine != "lite") || !namespacePattern.MatchString(cfg.Namespace) || cfg.M < 2 || cfg.M > 2048 || cfg.EFConstruction < cfg.M || cfg.EFConstruction > 65536 || cfg.EFSearch < 1 || cfg.EFSearch > 65536 {
		return nil, ErrInvalid
	}
	s, err := New(objects, encoder, c, options...)
	if err != nil {
		return nil, err
	}
	s.backend = &milvus{client: client, config: cfg}
	return s, nil
}
func (m *milvus) identity(s *Snapshot) (string, string) {
	raw, _ := json.Marshal(struct {
		IndexHash  string       `json:"index_hash"`
		Parameters MilvusConfig `json:"parameters"`
		Metric     string       `json:"metric"`
	}{s.ref.SHA256, m.config, "IP"})
	return "sea_multi_" + m.config.Namespace + "_" + artifacts.Hash(raw), "sea.multivector.tokens.v1:" + string(raw)
}
func (m *milvus) schema(s *Snapshot) *entity.Schema {
	name, description := m.identity(s)
	return &entity.Schema{CollectionName: name, Description: description, AutoID: false, EnableDynamicField: false, Fields: []*entity.Field{
		entity.NewField().WithName("token_id").WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(64),
		entity.NewField().WithName("chunk_id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(256),
		entity.NewField().WithName("revision_id").WithDataType(entity.FieldTypeVarChar).WithMaxLength(256),
		entity.NewField().WithName("index_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("matrix_hash").WithDataType(entity.FieldTypeVarChar).WithMaxLength(64),
		entity.NewField().WithName("position").WithDataType(entity.FieldTypeInt64),
		entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(s.service.config.Contract.Dimensions)),
	}}
}
func (m *milvus) validateCollection(ctx context.Context, s *Snapshot) error {
	name, description := m.identity(s)
	collection, err := m.client.DescribeCollection(ctx, milvusclient.NewDescribeCollectionOption(name))
	if err != nil {
		return err
	}
	if collection.Schema == nil || collection.Name != name || (collection.Schema.Description != description && !(m.config.Engine == "lite" && collection.Schema.Description == "")) || collection.Schema.AutoID || collection.Schema.EnableDynamicField || len(collection.Schema.Fields) != 7 {
		return fmt.Errorf("%w: Milvus token collection identity differs", ErrInvalid)
	}
	fields := map[string]*entity.Field{}
	for _, field := range collection.Schema.Fields {
		fields[field.Name] = field
	}
	for _, want := range m.schema(s).Fields {
		got := fields[want.Name]
		if got == nil || got.DataType != want.DataType || got.PrimaryKey != want.PrimaryKey || got.AutoID || got.Nullable || !reflect.DeepEqual(got.TypeParams, want.TypeParams) {
			return fmt.Errorf("%w: Milvus token field %s differs", ErrInvalid, want.Name)
		}
	}
	desc, err := m.client.DescribeIndex(ctx, milvusclient.NewDescribeIndexOption(name, "vector"))
	if err != nil {
		return err
	}
	if desc.Index == nil {
		return ErrInvalid
	}
	params := desc.Params()
	if nested := params["params"]; nested != "" {
		var settings struct {
			M  int `json:"M"`
			EF int `json:"efConstruction"`
		}
		if err := json.Unmarshal([]byte(nested), &settings); err != nil {
			return ErrInvalid
		}
		if flat := params["M"]; flat != "" && flat != strconv.Itoa(settings.M) {
			return ErrInvalid
		}
		if flat := params["efConstruction"]; flat != "" && flat != strconv.Itoa(settings.EF) {
			return ErrInvalid
		}
		params["M"] = strconv.Itoa(settings.M)
		params["efConstruction"] = strconv.Itoa(settings.EF)
	}
	if params["index_type"] != "HNSW" || params["metric_type"] != "IP" || params["M"] != strconv.Itoa(m.config.M) || params["efConstruction"] != strconv.Itoa(m.config.EFConstruction) {
		return fmt.Errorf("%w: Milvus token HNSW/IP parameters differ", ErrInvalid)
	}
	return nil
}
func float32Vector(v []float64) ([]float32, error) {
	out := make([]float32, len(v))
	var norm float64
	for i, x := range v {
		out[i] = float32(x)
		if math.IsNaN(float64(out[i])) || math.IsInf(float64(out[i]), 0) {
			return nil, ErrInvalid
		}
		norm += float64(out[i]) * float64(out[i])
	}
	if norm == 0 || math.IsInf(norm, 0) {
		return nil, fmt.Errorf("%w: token vector cannot be represented in float32", ErrInvalid)
	}
	return out, nil
}
func tokenID(chunkID string, position int) string {
	return artifacts.Hash([]byte(chunkID + ":" + strconv.Itoa(position)))
}
func (m *milvus) prepare(ctx context.Context, s *Snapshot) error {
	name, _ := m.identity(s)
	for _, token := range s.tokens {
		if _, err := float32Vector(token.vector); err != nil {
			return err
		}
	}
	exists, err := m.client.HasCollection(ctx, milvusclient.NewHasCollectionOption(name))
	if err != nil {
		return err
	}
	if !exists {
		indexOption := milvusclient.NewCreateIndexOption(name, "vector", index.NewHNSWIndex(entity.IP, m.config.M, m.config.EFConstruction)).WithIndexName("vector")
		nested, _ := json.Marshal(map[string]int{"M": m.config.M, "efConstruction": m.config.EFConstruction})
		indexOption.WithExtraParam("params", string(nested))
		if err := m.client.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(name, m.schema(s)).WithConsistencyLevel(entity.ClStrong).WithIndexOptions(indexOption)); err != nil {
			return err
		}
	}
	if err := m.validateCollection(ctx, s); err != nil {
		return err
	}
	// Upsert is idempotent for a fixed index hash; token IDs encode chunk/position.
	for start := 0; start < len(s.tokens); start += 128 {
		if err := ctx.Err(); err != nil {
			return err
		}
		group := s.tokens[start:min(start+128, len(s.tokens))]
		ids, chunks, revisions, indexHashes, matrixHashes := make([]string, 0, len(group)), make([]string, 0, len(group)), make([]string, 0, len(group)), make([]string, 0, len(group)), make([]string, 0, len(group))
		positions := make([]int64, 0, len(group))
		vectors := make([][]float32, 0, len(group))
		for _, token := range group {
			r := s.rows[token.chunkID]
			v, _ := float32Vector(token.vector)
			ids = append(ids, tokenID(token.chunkID, token.position))
			chunks = append(chunks, token.chunkID)
			revisions = append(revisions, r.Chunk.RevisionID)
			indexHashes = append(indexHashes, s.ref.SHA256)
			matrixHashes = append(matrixHashes, r.MatrixHash)
			positions = append(positions, int64(token.position))
			vectors = append(vectors, v)
		}
		option := milvusclient.NewColumnBasedInsertOption(name).WithVarcharColumn("token_id", ids).WithVarcharColumn("chunk_id", chunks).WithVarcharColumn("revision_id", revisions).WithVarcharColumn("index_hash", indexHashes).WithVarcharColumn("matrix_hash", matrixHashes).WithInt64Column("position", positions).WithFloatVectorColumn("vector", s.service.config.Contract.Dimensions, vectors)
		if _, err := m.client.Upsert(ctx, option); err != nil {
			return err
		}
	}
	flush, err := m.client.Flush(ctx, milvusclient.NewFlushOption(name))
	if err != nil {
		return err
	}
	if err := flush.Await(ctx); err != nil {
		return err
	}
	load, err := m.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		return err
	}
	if err := load.Await(ctx); err != nil {
		return err
	}
	return m.verify(ctx, s)
}
func (m *milvus) load(ctx context.Context, s *Snapshot) error {
	name, _ := m.identity(s)
	if err := m.validateCollection(ctx, s); err != nil {
		return err
	}
	load, err := m.client.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(name))
	if err != nil {
		return err
	}
	return load.Await(ctx)
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
	if total != int64(len(s.tokens)) {
		return fmt.Errorf("%w: Milvus token row count differs", ErrInvalid)
	}
	for start := 0; start < len(s.tokens); start += 128 {
		group := s.tokens[start:min(start+128, len(s.tokens))]
		ids := make([]string, 0, len(group))
		want := map[string]indexedToken{}
		for _, token := range group {
			id := tokenID(token.chunkID, token.position)
			ids = append(ids, id)
			want[id] = token
		}
		result, err := m.client.Query(ctx, milvusclient.NewQueryOption(name).WithFilter("token_id in "+jsonLiteral(ids)+" and index_hash == "+jsonLiteral(s.ref.SHA256)).WithOutputFields("token_id", "chunk_id", "revision_id", "matrix_hash", "position", "vector").WithConsistencyLevel(entity.ClStrong).WithLimit(len(ids)))
		if err != nil {
			return err
		}
		if result.Err != nil {
			return result.Err
		}
		if result.Len() != len(ids) {
			return fmt.Errorf("%w: Milvus token rows missing", ErrInvalid)
		}
		seen := map[string]bool{}
		for i := 0; i < result.Len(); i++ {
			for _, field := range []string{"token_id", "chunk_id", "revision_id", "matrix_hash", "position", "vector"} {
				if result.GetColumn(field) == nil {
					return ErrInvalid
				}
			}
			id, err := result.GetColumn("token_id").GetAsString(i)
			if err != nil {
				return err
			}
			token, ok := want[id]
			if !ok || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			chunk, err := result.GetColumn("chunk_id").GetAsString(i)
			if err != nil {
				return err
			}
			revision, err := result.GetColumn("revision_id").GetAsString(i)
			if err != nil {
				return err
			}
			hash, err := result.GetColumn("matrix_hash").GetAsString(i)
			if err != nil {
				return err
			}
			position, err := result.GetColumn("position").GetAsInt64(i)
			if err != nil {
				return err
			}
			value, err := result.GetColumn("vector").Get(i)
			if err != nil {
				return err
			}
			vector, ok := value.(entity.FloatVector)
			expected, _ := float32Vector(token.vector)
			if !ok || chunk != token.chunkID || revision != s.rows[token.chunkID].Chunk.RevisionID || hash != s.rows[token.chunkID].MatrixHash || position != int64(token.position) || !reflect.DeepEqual([]float32(vector), expected) {
				return fmt.Errorf("%w: Milvus token row differs", ErrInvalid)
			}
		}
	}
	return nil
}
func (m *milvus) search(ctx context.Context, s *Snapshot, q representation.TokenValues, allowed map[string]bool, perTokenK int) ([]hit, searchStats, error) {
	name, _ := m.identity(s)
	revisions := map[string]bool{}
	for id := range allowed {
		revisions[s.rows[id].Chunk.RevisionID] = true
	}
	valid := make([]string, 0, len(revisions))
	for id := range revisions {
		valid = append(valid, id)
	}
	sort.Strings(valid)
	best := map[string]float64{}
	stats := searchStats{}
	for i, vector := range q.Values {
		if !q.Mask[i] {
			continue
		}
		encoded, err := float32Vector(vector)
		if err != nil {
			return nil, stats, err
		}
		option := milvusclient.NewSearchOption(name, perTokenK, []entity.Vector{entity.FloatVector(encoded)}).WithANNSField("vector").WithSearchParam("metric_type", "IP").WithAnnParam(index.NewHNSWAnnParam(max(perTokenK, m.config.EFSearch))).WithConsistencyLevel(entity.ClStrong).WithFilter("index_hash == "+jsonLiteral(s.ref.SHA256)+" and revision_id in "+jsonLiteral(valid)).WithOutputFields("chunk_id", "revision_id", "matrix_hash", "position")
		found, err := m.client.Search(ctx, option)
		if err != nil {
			return nil, stats, err
		}
		if len(found) != 1 || found[0].Err != nil {
			return nil, stats, ErrInvalid
		}
		result := found[0]
		if result.IDs == nil || result.IDs.Len() != result.ResultCount || len(result.Scores) != result.ResultCount {
			return nil, stats, ErrInvalid
		}
		stats.observedRows += result.ResultCount
		for j := 0; j < result.ResultCount; j++ {
			id, err := result.IDs.GetAsString(j)
			if err != nil {
				return nil, stats, err
			}
			for _, field := range []string{"chunk_id", "revision_id", "matrix_hash", "position"} {
				if result.GetColumn(field) == nil {
					return nil, stats, ErrInvalid
				}
			}
			chunk, err := result.GetColumn("chunk_id").GetAsString(j)
			if err != nil {
				return nil, stats, err
			}
			r, ok := s.rows[chunk]
			if !ok || !allowed[chunk] {
				return nil, stats, ErrInvalid
			}
			revision, err := result.GetColumn("revision_id").GetAsString(j)
			if err != nil {
				return nil, stats, err
			}
			hash, err := result.GetColumn("matrix_hash").GetAsString(j)
			if err != nil {
				return nil, stats, err
			}
			position, err := result.GetColumn("position").GetAsInt64(j)
			if err != nil {
				return nil, stats, err
			}
			if position < 0 || position >= int64(len(r.Matrix.Mask)) || !r.Matrix.Mask[position] || id != tokenID(chunk, int(position)) || revision != r.Chunk.RevisionID || hash != r.MatrixHash {
				return nil, stats, ErrInvalid
			}
			projection, _ := float32Vector(r.Matrix.Values[position])
			calculated := 0.0
			for k, x := range encoded {
				calculated += float64(x) * float64(projection[k])
			}
			score := float64(result.Scores[j])
			if math.IsNaN(score) || math.IsInf(score, 0) || math.Abs(score-calculated) > 1e-4*math.Max(1, math.Abs(calculated)) {
				return nil, stats, fmt.Errorf("%w: Milvus token IP score differs", ErrInvalid)
			}
			if old, ok := best[chunk]; !ok || score > old {
				best[chunk] = score
			}
		}
	}
	out := make([]hit, 0, len(best))
	for id, score := range best {
		out = append(out, hit{id, score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].backendScore == out[j].backendScore {
			return out[i].id < out[j].id
		}
		return out[i].backendScore > out[j].backendScore
	})
	stats.candidateChunks = len(out)
	return out, stats, nil
}
func jsonLiteral(v any) string { raw, _ := json.Marshal(v); return string(raw) }
