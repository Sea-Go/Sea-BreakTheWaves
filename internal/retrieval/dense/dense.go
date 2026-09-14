// Package dense builds and queries one independently discoverable dense lane.
package dense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

var ErrInvalid = errors.New("invalid dense contract or artifact")

// Encoder is the existing DC typed model boundary, not an untyped vector API.
type Encoder interface {
	Represent(context.Context, representation.Request, representation.Contract, string) (representation.Response, error)
}
type Encoding struct {
	Callpoint       string `json:"callpoint"`
	ConfigurationID string `json:"configuration_id"`
	PhysicalModel   string `json:"physical_model"`
}
type Config struct {
	Document  Encoding                `json:"document"`
	Query     Encoding                `json:"query"`
	Contract  representation.Contract `json:"contract"`
	Space     string                  `json:"space"`
	BatchSize int                     `json:"batch_size"`
	ProbeTopK int                     `json:"probe_top_k"`
}

func (c Config) request(role string, inputs []representation.Input) representation.Request {
	e := c.Document
	if role == "query" {
		e = c.Query
	}
	return representation.Request{Model: e.Callpoint, ConfigurationID: e.ConfigurationID, OutputContract: representation.OutputContract(representation.Dense), ContractID: c.Contract.ID, Space: c.Space, Role: role, Input: inputs}
}
func (c Config) validate() error {
	if err := c.Contract.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if c.Contract.Kind != representation.Dense || c.BatchSize < 1 || c.BatchSize > representation.MaxBatch || c.ProbeTopK < 1 || c.ProbeTopK > 64 {
		return ErrInvalid
	}
	for _, role := range []string{"document", "query"} {
		if err := c.request(role, []representation.Input{{ID: "validation", Text: "validation"}}).Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if c.Document.PhysicalModel == "" || c.Query.PhysicalModel == "" {
		return ErrInvalid
	}
	return nil
}
func (c Config) profileMatches(p corpus.Profile) bool {
	return p.Lane == "dense" && p.Encoder == c.Document.PhysicalModel && p.Tokenizer == c.Contract.TokenizerID && p.Space == c.Space && p.Dimensions == c.Contract.Dimensions && p.Mask == "" && p.Aggregation == ""
}

type BuildRequest struct {
	BuildID       string
	Generation    int64
	ChunkManifest corpus.Ref
	Profile       corpus.Profile
	// ResumeIndex names a complete immutable encoding result from a previous
	// interrupted backend projection. It is fully verified before being reused.
	ResumeIndex *corpus.Ref
}

// ProjectionError preserves a complete encoding index for explicit retry.
// It does not grant READY; callers must still handle the error.
type ProjectionError struct {
	IndexRef corpus.Ref
	Err      error
}

func (e *ProjectionError) Error() string {
	return fmt.Sprintf("dense projection for %s: %v", e.IndexRef.SHA256, e.Err)
}
func (e *ProjectionError) Unwrap() error { return e.Err }

type BuildResult struct {
	Index         corpus.LaneIndex
	Ref           corpus.Ref
	Usage         representation.Usage
	EncodedChunks int
	Reused        bool
}
type row struct {
	Chunk      corpus.Chunk `json:"chunk"`
	Vector     []float64    `json:"vector"`
	VectorHash string       `json:"vector_hash"`
}
type shard struct {
	SchemaVersion int                  `json:"schema_version"`
	BuildID       string               `json:"build_id"`
	Generation    int64                `json:"generation"`
	ModuleID      string               `json:"module_id"`
	ReleaseID     string               `json:"release_id"`
	ChunkManifest corpus.Ref           `json:"chunk_manifest"`
	Profile       corpus.Profile       `json:"profile"`
	Config        Config               `json:"config"`
	Usage         representation.Usage `json:"usage"`
	Rows          []row                `json:"rows"`
}

// Backend is intentionally private to the lane. Only the two supported
// implementations can interpret immutable snapshots and raw vectors.
type backend interface {
	prepare(context.Context, *Snapshot) error
	verify(context.Context, *Snapshot) error
	load(context.Context, *Snapshot) error
	scoreKind(string) string
	search(context.Context, *Snapshot, []float64, map[string]bool, int) ([]hit, error)
}
type hit struct {
	ID    string
	Score float64
}

type Service struct {
	objects artifacts.Store
	encoder Encoder
	config  Config
	backend backend
}

// New uses the durable artifact store as a small-corpus exact reference index.
// The caller owns the store and encoder lifecycle.
func New(objects artifacts.Store, encoder Encoder, config Config) (*Service, error) {
	if objects == nil || encoder == nil {
		return nil, ErrInvalid
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Service{objects: objects, encoder: encoder, config: config, backend: exact{}}, nil
}

func load[T any](ctx context.Context, objects artifacts.Store, ref corpus.Ref) (T, error) {
	var value T
	raw, err := objects.Get(ctx, ref)
	if err != nil {
		return value, err
	}
	if !artifacts.ValidHash(ref.SHA256) || artifacts.Hash(raw) != ref.SHA256 {
		return value, ErrInvalid
	}
	if err = representation.Decode(raw, &value); err != nil {
		return value, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return value, nil
}
func put(ctx context.Context, objects artifacts.Store, value any) (corpus.Ref, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return corpus.Ref{}, err
	}
	return objects.Put(ctx, data)
}
func vectorHash(v []float64) string { b, _ := json.Marshal(v); return artifacts.Hash(b) }
func validVector(v []float64, c representation.Contract) bool {
	if len(v) != c.Dimensions {
		return false
	}
	sum := 0.0
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
		sum += x * x
	}
	return sum > 0 && !math.IsInf(sum, 0) && (c.Normalization != "l2" || math.Abs(sum-1) <= 1e-4)
}

func validateManifest(m corpus.ChunkManifest) error {
	if m.SchemaVersion != 1 || m.ModuleID == "" || m.ReleaseID == "" || !artifacts.ValidHash(m.InputManifestHash) || m.Profile == "" || len(m.Chunks) == 0 || len(m.Inputs) == 0 {
		return ErrInvalid
	}
	inputs := map[string]corpus.Input{}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, in := range m.Inputs {
		if in.RevisionID == "" || in.ContentID == "" || in.ChunkCount < 1 || !artifacts.ValidHash(in.Original.SHA256) {
			return ErrInvalid
		}
		if _, ok := inputs[in.RevisionID]; ok {
			return ErrInvalid
		}
		inputs[in.RevisionID] = in
	}
	for _, c := range m.Chunks {
		in, ok := inputs[c.RevisionID]
		encoding, _ := json.Marshal([]any{m.Profile, c.Text})
		if !ok || c.ID == "" || ids[c.ID] || !c.Required || c.Text == "" || c.TextHash != artifacts.Hash([]byte(c.Text)) || c.EncodingKey != artifacts.Hash(encoding) || c.Original != in.Original || c.ContentID != in.ContentID || c.SourceKind != in.SourceKind || c.Location.Locator == "" || c.Location.OriginalByteStart < 0 || c.Location.OriginalByteEnd <= c.Location.OriginalByteStart || c.Location.NormalizedRuneStart < 0 || c.Location.NormalizedRuneEnd <= c.Location.NormalizedRuneStart {
			return ErrInvalid
		}
		ids[c.ID] = true
		counts[c.RevisionID]++
	}
	for id, in := range inputs {
		if counts[id] != in.ChunkCount {
			return ErrInvalid
		}
	}
	return nil
}
func (s *Service) encode(ctx context.Context, role string, inputs []representation.Input) (representation.Response, error) {
	if err := ctx.Err(); err != nil {
		return representation.Response{}, err
	}
	q := s.config.request(role, inputs)
	if err := q.Validate(); err != nil {
		return representation.Response{}, err
	}
	e := s.config.Document
	if role == "query" {
		e = s.config.Query
	}
	response, err := s.encoder.Represent(ctx, q, s.config.Contract, e.PhysicalModel)
	if err != nil {
		return response, fmt.Errorf("dense %s encode: %w", role, err)
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := response.Validate(q, s.config.Contract, e.PhysicalModel); err != nil {
		return response, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return response, nil
}

func (s *Service) Build(ctx context.Context, q BuildRequest) (BuildResult, error) {
	if q.BuildID == "" || q.Generation <= 0 || !s.config.profileMatches(q.Profile) {
		return BuildResult{}, ErrInvalid
	}
	m, err := load[corpus.ChunkManifest](ctx, s.objects, q.ChunkManifest)
	if err != nil {
		return BuildResult{}, err
	}
	if err = validateManifest(m); err != nil {
		return BuildResult{}, err
	}
	index := corpus.LaneIndex{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, InputManifestHash: m.InputManifestHash, ChunkManifest: q.ChunkManifest, Profile: q.Profile}
	if q.ResumeIndex != nil {
		snapshot, err := s.Open(ctx, *q.ResumeIndex)
		if err != nil {
			return BuildResult{}, err
		}
		got := snapshot.index
		got.Shards = nil
		if !reflect.DeepEqual(got, index) {
			return BuildResult{}, fmt.Errorf("%w: resume binding differs", ErrInvalid)
		}
		if err = s.backend.prepare(ctx, snapshot); err != nil {
			return BuildResult{}, err
		}
		return BuildResult{Index: snapshot.index, Ref: *q.ResumeIndex, Usage: snapshot.usage, EncodedChunks: len(snapshot.rows), Reused: true}, nil
	}
	usage := representation.Usage{}
	for start := 0; start < len(m.Chunks); start += s.config.BatchSize {
		if err := ctx.Err(); err != nil {
			return BuildResult{}, err
		}
		chunks := m.Chunks[start:min(start+s.config.BatchSize, len(m.Chunks))]
		inputs := make([]representation.Input, len(chunks))
		for i, c := range chunks {
			inputs[i] = representation.Input{ID: c.ID, Text: c.Text}
		}
		response, err := s.encode(ctx, "document", inputs)
		if err != nil {
			return BuildResult{}, err
		}
		vectors := map[string][]float64{}
		for _, item := range response.Data {
			vectors[item.ID] = item.Dense.Values
		}
		part := shard{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, ChunkManifest: q.ChunkManifest, Profile: q.Profile, Config: s.config, Usage: *response.Usage, Rows: make([]row, 0, len(chunks))}
		chunkIDs := make([]string, 0, len(chunks))
		for _, c := range chunks {
			v := vectors[c.ID]
			part.Rows = append(part.Rows, row{Chunk: c, Vector: v, VectorHash: vectorHash(v)})
			chunkIDs = append(chunkIDs, c.ID)
		}
		ref, err := put(ctx, s.objects, part)
		if err != nil {
			return BuildResult{}, err
		}
		index.Shards = append(index.Shards, corpus.IndexShard{Artifact: ref, ChunkIDs: chunkIDs})
		usage.PromptTokens += response.Usage.PromptTokens
		usage.TotalTokens += response.Usage.TotalTokens
	}
	ref, err := put(ctx, s.objects, index)
	if err != nil {
		return BuildResult{}, err
	}
	snapshot, err := s.Open(ctx, ref)
	if err != nil {
		return BuildResult{}, err
	}
	if err = s.backend.prepare(ctx, snapshot); err != nil {
		return BuildResult{}, &ProjectionError{IndexRef: ref, Err: err}
	}
	return BuildResult{Index: index, Ref: ref, Usage: usage, EncodedChunks: len(m.Chunks)}, nil
}

// Snapshot is immutable after Open and may serve concurrent queries. Keep one
// snapshot per selected index; validity remains an explicit per-query input.
type Snapshot struct {
	service  *Service
	index    corpus.LaneIndex
	ref      corpus.Ref
	manifest corpus.ChunkManifest
	rows     map[string]row
	usage    representation.Usage
}

func (s *Service) Open(ctx context.Context, ref corpus.Ref) (*Snapshot, error) {
	index, err := load[corpus.LaneIndex](ctx, s.objects, ref)
	if err != nil {
		return nil, err
	}
	m, err := load[corpus.ChunkManifest](ctx, s.objects, index.ChunkManifest)
	if err != nil {
		return nil, err
	}
	return s.open(ctx, ref, index, m)
}

// Load opens an existing immutable index and loads its backend projection.
// It cannot create or repair missing rows; use Build with ResumeIndex for that.
func (s *Service) Load(ctx context.Context, ref corpus.Ref) (*Snapshot, error) {
	snapshot, err := s.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err = s.backend.load(ctx, snapshot); err != nil {
		return nil, err
	}
	if err = s.backend.verify(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Service) open(ctx context.Context, ref corpus.Ref, index corpus.LaneIndex, m corpus.ChunkManifest) (*Snapshot, error) {
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	if index.SchemaVersion != 1 || index.BuildID == "" || index.Generation <= 0 || index.InputManifestHash != m.InputManifestHash || !s.config.profileMatches(index.Profile) || len(index.Shards) == 0 {
		return nil, ErrInvalid
	}
	snapshot := &Snapshot{service: s, index: index, ref: ref, manifest: m, rows: map[string]row{}}
	expected := map[string]corpus.Chunk{}
	for _, c := range m.Chunks {
		expected[c.ID] = c
	}
	seen := map[string]bool{}
	for _, declared := range index.Shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[declared.Artifact.SHA256] || len(declared.ChunkIDs) == 0 {
			return nil, ErrInvalid
		}
		seen[declared.Artifact.SHA256] = true
		part, err := load[shard](ctx, s.objects, declared.Artifact)
		if err != nil {
			return nil, err
		}
		if part.SchemaVersion != 1 || part.BuildID != index.BuildID || part.Generation != index.Generation || part.ModuleID != m.ModuleID || part.ReleaseID != m.ReleaseID || part.ChunkManifest != index.ChunkManifest || part.Profile != index.Profile || part.Config != s.config || len(part.Rows) != len(declared.ChunkIDs) || part.Usage.PromptTokens < 0 || part.Usage.TotalTokens < part.Usage.PromptTokens {
			return nil, ErrInvalid
		}
		for i, r := range part.Rows {
			c, ok := expected[r.Chunk.ID]
			_, duplicate := snapshot.rows[r.Chunk.ID]
			if !ok || duplicate || r.Chunk != c || r.Chunk.ID != declared.ChunkIDs[i] || !validVector(r.Vector, s.config.Contract) || r.VectorHash != vectorHash(r.Vector) {
				return nil, ErrInvalid
			}
			snapshot.rows[r.Chunk.ID] = r
		}
		snapshot.usage.PromptTokens += part.Usage.PromptTokens
		snapshot.usage.TotalTokens += part.Usage.TotalTokens
	}
	if len(snapshot.rows) != len(expected) {
		return nil, fmt.Errorf("%w: incomplete dense coverage", ErrInvalid)
	}
	return snapshot, nil
}

type Query struct {
	IndexRef         corpus.Ref
	ModuleID         string
	ReleaseID        string
	Generation       int64
	ValidRevisionIDs []string
	Text             string
	TopK             int
}
type Candidate struct {
	Chunk            corpus.Chunk `json:"chunk"`
	ModuleID         string       `json:"module_id"`
	ReleaseID        string       `json:"release_id"`
	Generation       int64        `json:"generation"`
	Lane             string       `json:"lane"`
	IndexRef         corpus.Ref   `json:"index_ref"`
	Score            float64      `json:"score"`
	BackendScoreKind string       `json:"backend_score_kind"`
	BackendScore     float64      `json:"backend_score"`
	Rank             int          `json:"rank"`
}
type Result struct {
	Candidates []Candidate          `json:"candidates"`
	Usage      representation.Usage `json:"usage"`
}

func (s *Service) Search(ctx context.Context, q Query) (Result, error) {
	snapshot, err := s.Open(ctx, q.IndexRef)
	if err != nil {
		return Result{}, err
	}
	return snapshot.Search(ctx, q)
}
func (s *Snapshot) Search(ctx context.Context, q Query) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if q.IndexRef != s.ref || q.ModuleID != s.manifest.ModuleID || q.ReleaseID != s.manifest.ReleaseID || q.Generation != s.index.Generation || q.TopK < 1 || q.TopK > 1000 || q.Text == "" {
		return Result{}, ErrInvalid
	}
	valid := map[string]bool{}
	for _, id := range q.ValidRevisionIDs {
		if id == "" || valid[id] {
			return Result{}, ErrInvalid
		}
		valid[id] = true
	}
	allowed := map[string]bool{}
	for id, r := range s.rows {
		if valid[r.Chunk.RevisionID] {
			allowed[id] = true
		}
	}
	result := Result{Candidates: []Candidate{}}
	if len(allowed) == 0 {
		return result, nil
	}
	response, err := s.service.encode(ctx, "query", []representation.Input{{ID: "query", Text: q.Text}})
	if err != nil {
		return result, err
	}
	v := response.Data[0].Dense.Values
	result.Usage = *response.Usage
	hits, err := s.service.backend.search(ctx, s, v, allowed, q.TopK)
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, h := range hits {
		r, ok := s.rows[h.ID]
		if !ok || !allowed[h.ID] || seen[h.ID] || math.IsNaN(h.Score) || math.IsInf(h.Score, 0) {
			return Result{}, fmt.Errorf("%w: backend returned foreign/invalid candidate", ErrInvalid)
		}
		seen[h.ID] = true
		score, err := similarity(v, r.Vector, s.service.config.Contract.Metric)
		if err != nil {
			return Result{}, err
		}
		result.Candidates = append(result.Candidates, Candidate{Chunk: r.Chunk, ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "dense", IndexRef: s.ref, Score: score, BackendScore: h.Score, BackendScoreKind: s.service.backend.scoreKind(s.service.config.Contract.Metric)})
	}
	if len(result.Candidates) > q.TopK {
		return Result{}, ErrInvalid
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		a, b := result.Candidates[i], result.Candidates[j]
		if a.Score == b.Score {
			return a.Chunk.ID < b.Chunk.ID
		}
		return a.Score > b.Score
	})
	for i := range result.Candidates {
		result.Candidates[i].Rank = i + 1
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) VerifyAndProbe(ctx context.Context, index corpus.LaneIndex, m corpus.ChunkManifest, probes []corpus.Chunk) ([]corpus.ProbeResult, error) {
	// Compare the supplied manifest to the fixed hashed object, even when this
	// method is invoked independently of the content reconciler.
	stored, err := load[corpus.ChunkManifest](ctx, s.objects, index.ChunkManifest)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(stored, m) {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.open(ctx, artifacts.Reference(raw), index, m)
	if err != nil {
		return nil, err
	}
	if err := s.backend.load(ctx, snapshot); err != nil {
		return nil, err
	}
	if err := s.backend.verify(ctx, snapshot); err != nil {
		return nil, err
	}
	revisions := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		revisions = append(revisions, in.RevisionID)
	}
	results := make([]corpus.ProbeResult, 0, len(probes))
	for _, probe := range probes {
		r, exists := snapshot.rows[probe.ID]
		if !exists || r.Chunk != probe {
			return nil, ErrInvalid
		}
		result, err := snapshot.Search(ctx, Query{IndexRef: snapshot.ref, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, Generation: index.Generation, ValidRevisionIDs: revisions, Text: probe.Text, TopK: s.config.ProbeTopK})
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(result.Candidates))
		for _, c := range result.Candidates {
			ids = append(ids, c.Chunk.ID)
		}
		results = append(results, corpus.ProbeResult{QueryChunkID: probe.ID, CandidateIDs: ids})
	}
	return results, nil
}
