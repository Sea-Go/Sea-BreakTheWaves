// Package sparse owns learned-token encoding and an independent inverted lane.
package sparse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
)

var ErrInvalid = errors.New("invalid sparse contract or artifact")
var ErrEmptyVector = errors.New("empty sparse representation is unsupported by H05")

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
	return representation.Request{Model: e.Callpoint, ConfigurationID: e.ConfigurationID, OutputContract: representation.OutputContract(representation.Sparse), ContractID: c.Contract.ID, Space: c.Space, Role: role, Input: inputs}
}
func (c Config) validate() error {
	if e := c.Contract.Validate(); e != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	if c.Contract.Kind != representation.Sparse || c.BatchSize < 1 || c.BatchSize > representation.MaxBatch || c.ProbeTopK < 1 || c.ProbeTopK > 64 {
		return ErrInvalid
	}
	for _, role := range []string{"document", "query"} {
		if e := c.request(role, []representation.Input{{ID: "check", Text: "check"}}).Validate(); e != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, e)
		}
	}
	if c.Document.PhysicalModel == "" || c.Query.PhysicalModel == "" {
		return ErrInvalid
	}
	return nil
}
func (c Config) matches(p corpus.Profile) bool {
	return p.Lane == "sparse" && p.Encoder == c.Document.PhysicalModel && p.Tokenizer == c.Contract.TokenizerID && p.Space == c.Space && p.Dimensions == c.Contract.Dimensions && p.Mask == "" && p.Aggregation == ""
}

// Binding excludes batch/probe sizes, which do not change encoded values.
type binding struct {
	Document Encoding                `json:"document"`
	Query    Encoding                `json:"query"`
	Contract representation.Contract `json:"contract"`
	Space    string                  `json:"space"`
}

func (c Config) binding() binding { return binding{c.Document, c.Query, c.Contract, c.Space} }

type BuildRequest struct {
	BuildID       string
	Generation    int64
	ChunkManifest corpus.Ref
	Profile       corpus.Profile
	ResumeIndex   *corpus.Ref
}
type BuildResult struct {
	Index         corpus.LaneIndex
	Ref           corpus.Ref
	Usage         representation.Usage
	EncodedChunks int
	StoredUsage   representation.Usage
	ReusedChunks  int
	UsageUnknown  bool
	Reused        bool
}
type row struct {
	Chunk      corpus.Chunk                `json:"chunk"`
	Vector     representation.SparseValues `json:"vector"`
	VectorHash string                      `json:"vector_hash"`
}
type entry struct {
	ChunkID string  `json:"chunk_id"`
	Weight  float64 `json:"weight"`
}
type posting struct {
	TokenID int     `json:"token_id"`
	Entries []entry `json:"entries"`
}
type shard struct {
	SchemaVersion int                  `json:"schema_version"`
	BuildID       string               `json:"build_id"`
	Generation    int64                `json:"generation"`
	ModuleID      string               `json:"module_id"`
	ReleaseID     string               `json:"release_id"`
	ChunkManifest corpus.Ref           `json:"chunk_manifest"`
	Profile       corpus.Profile       `json:"profile"`
	Binding       binding              `json:"binding"`
	Usage         representation.Usage `json:"usage"`
	Rows          []row                `json:"rows"`
	Postings      []posting            `json:"postings"`
}
type hit struct {
	ID    string
	Score float64
}
type backend interface {
	prepare(context.Context, *Snapshot) error
	verify(context.Context, *Snapshot) error
	search(context.Context, *Snapshot, representation.SparseValues, map[string]bool, int) ([]hit, int, error)
}
type Service struct {
	objects artifacts.Store
	encoder Encoder
	config  Config
	backend backend
}

func New(objects artifacts.Store, encoder Encoder, config Config) (*Service, error) {
	if objects == nil || encoder == nil {
		return nil, ErrInvalid
	}
	if e := config.validate(); e != nil {
		return nil, e
	}
	return &Service{objects, encoder, config, inverted{}}, nil
}
func load[T any](ctx context.Context, objects artifacts.Store, ref corpus.Ref) (T, error) {
	var v T
	raw, e := objects.Get(ctx, ref)
	if e != nil {
		return v, e
	}
	if ref != artifacts.Reference(raw) {
		return v, ErrInvalid
	}
	if e = representation.Decode(raw, &v); e != nil {
		return v, fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	return v, nil
}
func put(ctx context.Context, objects artifacts.Store, v any) (corpus.Ref, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return corpus.Ref{}, e
	}
	return objects.Put(ctx, b)
}
func vectorHash(v representation.SparseValues) string {
	b, _ := json.Marshal(v)
	return artifacts.Hash(b)
}
func validateVector(v representation.SparseValues, contract representation.Contract) error {
	if len(v.Indices) == 0 {
		return ErrEmptyVector
	}
	if len(v.Indices) != len(v.Weights) || len(v.Indices) > contract.MaxNonzero {
		return ErrInvalid
	}
	last := -1
	norm := 0.0
	for i, id := range v.Indices {
		w := v.Weights[i]
		if id <= last || id >= contract.Dimensions || w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			return ErrInvalid
		}
		last = id
		norm += w * w
	}
	if math.IsInf(norm, 0) || math.IsNaN(norm) || norm == 0 || (contract.Normalization == "l2" && math.Abs(norm-1) > 1e-4) {
		return ErrInvalid
	}
	return nil
}
func postings(rows []row) []posting {
	byToken := map[int][]entry{}
	for _, r := range rows {
		for i, token := range r.Vector.Indices {
			byToken[token] = append(byToken[token], entry{r.Chunk.ID, r.Vector.Weights[i]})
		}
	}
	ids := make([]int, 0, len(byToken))
	for id := range byToken {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	result := make([]posting, 0, len(ids))
	for _, id := range ids {
		es := byToken[id]
		sort.Slice(es, func(i, j int) bool { return es[i].ChunkID < es[j].ChunkID })
		result = append(result, posting{id, es})
	}
	return result
}
func validateManifest(m corpus.ChunkManifest) error {
	if m.SchemaVersion != 1 || m.ModuleID == "" || m.ReleaseID == "" || !artifacts.ValidHash(m.InputManifestHash) || m.Profile == "" || len(m.Chunks) == 0 || len(m.Inputs) == 0 {
		return ErrInvalid
	}
	inputs := map[string]corpus.Input{}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, in := range m.Inputs {
		if in.RevisionID == "" || in.ContentID == "" || in.ChunkCount < 1 || in.Original.Key != "sha256/"+in.Original.SHA256 || !artifacts.ValidHash(in.Original.SHA256) || (in.SourceKind != "source" && in.SourceKind != "wiki") {
			return ErrInvalid
		}
		if _, ok := inputs[in.RevisionID]; ok {
			return ErrInvalid
		}
		inputs[in.RevisionID] = in
	}
	for _, c := range m.Chunks {
		in, ok := inputs[c.RevisionID]
		encoded, _ := json.Marshal([]any{m.Profile, c.Text})
		if !ok || c.ID == "" || seen[c.ID] || !c.Required || strings.TrimSpace(c.Text) == "" || c.TextHash != artifacts.Hash([]byte(c.Text)) || c.EncodingKey != artifacts.Hash(encoded) || c.Original != in.Original || c.ContentID != in.ContentID || c.SourceKind != in.SourceKind || c.Location.Locator == "" || c.Location.OriginalByteStart < 0 || c.Location.OriginalByteEnd <= c.Location.OriginalByteStart || c.Location.NormalizedRuneStart < 0 || c.Location.NormalizedRuneEnd <= c.Location.NormalizedRuneStart {
			return ErrInvalid
		}
		seen[c.ID] = true
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
	if e := ctx.Err(); e != nil {
		return representation.Response{}, e
	}
	q := s.config.request(role, inputs)
	if e := q.Validate(); e != nil {
		return representation.Response{}, e
	}
	cfg := s.config.Document
	if role == "query" {
		cfg = s.config.Query
	}
	v, e := s.encoder.Represent(ctx, q, s.config.Contract, cfg.PhysicalModel)
	if e != nil {
		return v, fmt.Errorf("sparse %s encode: %w", role, e)
	}
	if e = ctx.Err(); e != nil {
		return v, e
	}
	if e = v.Validate(q, s.config.Contract, cfg.PhysicalModel); e != nil {
		return v, fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	for _, item := range v.Data {
		if e = validateVector(*item.Sparse, s.config.Contract); e != nil {
			return v, e
		}
	}
	return v, nil
}
func (s *Service) Build(ctx context.Context, q BuildRequest) (BuildResult, error) {
	if q.BuildID == "" || q.Generation < 1 || !s.config.matches(q.Profile) {
		return BuildResult{}, ErrInvalid
	}
	m, e := load[corpus.ChunkManifest](ctx, s.objects, q.ChunkManifest)
	if e != nil {
		return BuildResult{}, e
	}
	if e = validateManifest(m); e != nil {
		return BuildResult{}, e
	}
	idx := corpus.LaneIndex{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, InputManifestHash: m.InputManifestHash, ChunkManifest: q.ChunkManifest, Profile: q.Profile}
	if q.ResumeIndex != nil {
		snap, e := s.Open(ctx, *q.ResumeIndex)
		if e != nil {
			return BuildResult{}, e
		}
		binding := snap.index
		binding.Shards = nil
		if !reflect.DeepEqual(binding, idx) {
			return BuildResult{}, ErrInvalid
		}
		out := BuildResult{Index: snap.index, Ref: *q.ResumeIndex, StoredUsage: snap.usage, ReusedChunks: len(snap.rows), Reused: true}
		return out, s.backend.prepare(ctx, snap)
	}
	usage := representation.Usage{}
	encodedCount := 0
	for start := 0; start < len(m.Chunks); start += s.config.BatchSize {
		group := m.Chunks[start:min(start+s.config.BatchSize, len(m.Chunks))]
		inputs := make([]representation.Input, len(group))
		for i, c := range group {
			inputs[i] = representation.Input{ID: c.ID, Text: c.Text}
		}
		response, e := s.encode(ctx, "document", inputs)
		if e != nil {
			return BuildResult{Usage: usage, EncodedChunks: encodedCount, UsageUnknown: true}, e
		}
		usage.PromptTokens += response.Usage.PromptTokens
		usage.TotalTokens += response.Usage.TotalTokens
		encodedCount += len(group)
		values := map[string]representation.SparseValues{}
		for _, item := range response.Data {
			values[item.ID] = *item.Sparse
		}
		part := shard{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, ChunkManifest: q.ChunkManifest, Profile: q.Profile, Binding: s.config.binding(), Usage: *response.Usage, Rows: make([]row, 0, len(group))}
		ids := make([]string, 0, len(group))
		for _, c := range group {
			v := values[c.ID]
			part.Rows = append(part.Rows, row{c, v, vectorHash(v)})
			ids = append(ids, c.ID)
		}
		part.Postings = postings(part.Rows)
		ref, e := put(ctx, s.objects, part)
		if e != nil {
			return BuildResult{Usage: usage, EncodedChunks: encodedCount}, e
		}
		idx.Shards = append(idx.Shards, corpus.IndexShard{Artifact: ref, ChunkIDs: ids})
	}
	ref, e := put(ctx, s.objects, idx)
	if e != nil {
		return BuildResult{Usage: usage, EncodedChunks: encodedCount}, e
	}
	snap, e := s.Open(ctx, ref)
	if e != nil {
		return BuildResult{Usage: usage, EncodedChunks: encodedCount}, e
	}
	result := BuildResult{Index: idx, Ref: ref, Usage: usage, StoredUsage: usage, EncodedChunks: len(m.Chunks)}
	return result, s.backend.prepare(ctx, snap)
}

type Snapshot struct {
	service  *Service
	index    corpus.LaneIndex
	ref      corpus.Ref
	manifest corpus.ChunkManifest
	rows     map[string]row
	postings map[int][]entry
	usage    representation.Usage
}

func (s *Service) Open(ctx context.Context, ref corpus.Ref) (*Snapshot, error) {
	idx, e := load[corpus.LaneIndex](ctx, s.objects, ref)
	if e != nil {
		return nil, e
	}
	m, e := load[corpus.ChunkManifest](ctx, s.objects, idx.ChunkManifest)
	if e != nil {
		return nil, e
	}
	return s.open(ctx, ref, idx, m)
}
func (s *Service) open(ctx context.Context, ref corpus.Ref, idx corpus.LaneIndex, m corpus.ChunkManifest) (*Snapshot, error) {
	if e := validateManifest(m); e != nil {
		return nil, e
	}
	if idx.SchemaVersion != 1 || idx.BuildID == "" || idx.Generation < 1 || idx.InputManifestHash != m.InputManifestHash || !s.config.matches(idx.Profile) || len(idx.Shards) == 0 {
		return nil, ErrInvalid
	}
	snap := &Snapshot{service: s, index: idx, ref: ref, manifest: m, rows: map[string]row{}, postings: map[int][]entry{}}
	expected := map[string]corpus.Chunk{}
	for _, c := range m.Chunks {
		expected[c.ID] = c
	}
	shards := map[string]bool{}
	for _, declared := range idx.Shards {
		if shards[declared.Artifact.SHA256] || len(declared.ChunkIDs) == 0 {
			return nil, ErrInvalid
		}
		shards[declared.Artifact.SHA256] = true
		part, e := load[shard](ctx, s.objects, declared.Artifact)
		if e != nil {
			return nil, e
		}
		if part.SchemaVersion != 1 || part.BuildID != idx.BuildID || part.Generation != idx.Generation || part.ModuleID != m.ModuleID || part.ReleaseID != m.ReleaseID || part.ChunkManifest != idx.ChunkManifest || part.Profile != idx.Profile || part.Binding != s.config.binding() || len(part.Rows) != len(declared.ChunkIDs) || part.Usage.PromptTokens < 0 || part.Usage.TotalTokens < part.Usage.PromptTokens {
			return nil, ErrInvalid
		}
		for i, r := range part.Rows {
			want, ok := expected[r.Chunk.ID]
			if _, exists := snap.rows[r.Chunk.ID]; !ok || exists || want != r.Chunk || declared.ChunkIDs[i] != r.Chunk.ID || vectorHash(r.Vector) != r.VectorHash {
				return nil, ErrInvalid
			}
			if e := validateVector(r.Vector, s.config.Contract); e != nil {
				return nil, e
			}
			snap.rows[r.Chunk.ID] = r
		}
		if !reflect.DeepEqual(part.Postings, postings(part.Rows)) {
			return nil, fmt.Errorf("%w: inverted postings differ from fixed vectors", ErrInvalid)
		}
		for _, p := range part.Postings {
			snap.postings[p.TokenID] = append(snap.postings[p.TokenID], p.Entries...)
		}
		snap.usage.PromptTokens += part.Usage.PromptTokens
		snap.usage.TotalTokens += part.Usage.TotalTokens
	}
	if len(snap.rows) != len(expected) {
		return nil, fmt.Errorf("%w: incomplete sparse coverage", ErrInvalid)
	}
	return snap, nil
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
	Chunk        corpus.Chunk `json:"chunk"`
	ModuleID     string       `json:"module_id"`
	ReleaseID    string       `json:"release_id"`
	Generation   int64        `json:"generation"`
	Lane         string       `json:"lane"`
	IndexRef     corpus.Ref   `json:"index_ref"`
	Score        float64      `json:"score"`
	BackendScore float64      `json:"backend_score"`
	Rank         int          `json:"rank"`
}
type Result struct {
	Candidates       []Candidate          `json:"candidates"`
	Usage            representation.Usage `json:"usage"`
	ExaminedPostings *int                 `json:"examined_postings,omitempty"`
}

func (s *Service) Search(ctx context.Context, q Query) (Result, error) {
	snap, e := s.Open(ctx, q.IndexRef)
	if e != nil {
		return Result{}, e
	}
	return snap.Search(ctx, q)
}
func (s *Snapshot) Search(ctx context.Context, q Query) (Result, error) {
	return s.search(ctx, q, false)
}
func (s *Snapshot) search(ctx context.Context, q Query, verify bool) (Result, error) {
	result := Result{Candidates: []Candidate{}}
	if e := ctx.Err(); e != nil {
		return result, e
	}
	if q.IndexRef != s.ref || q.ModuleID != s.manifest.ModuleID || q.ReleaseID != s.manifest.ReleaseID || q.Generation != s.index.Generation || q.TopK < 1 || q.TopK > 1000 || strings.TrimSpace(q.Text) == "" {
		return result, ErrInvalid
	}
	valid := map[string]bool{}
	for _, id := range q.ValidRevisionIDs {
		if id == "" || valid[id] {
			return result, ErrInvalid
		}
		valid[id] = true
	}
	allowed := map[string]bool{}
	for id, r := range s.rows {
		if valid[r.Chunk.RevisionID] {
			allowed[id] = true
		}
	}
	if len(allowed) == 0 {
		return result, nil
	}
	response, e := s.service.encode(ctx, "query", []representation.Input{{ID: "query", Text: q.Text}})
	if e != nil {
		return result, e
	}
	v := *response.Data[0].Sparse
	result.Usage = *response.Usage
	hits, examined, e := s.service.backend.search(ctx, s, v, allowed, q.TopK)
	if examined >= 0 {
		result.ExaminedPostings = &examined
	}
	if e != nil {
		return result, e
	}
	seen := map[string]bool{}
	for _, h := range hits {
		r, ok := s.rows[h.ID]
		if !ok || !allowed[h.ID] || seen[h.ID] || math.IsNaN(h.Score) || math.IsInf(h.Score, 0) {
			return Result{}, ErrInvalid
		}
		seen[h.ID] = true
		score, e := Dot(v, r.Vector)
		if e != nil {
			return Result{}, e
		}
		if score <= 0 {
			continue
		}
		result.Candidates = append(result.Candidates, Candidate{Chunk: r.Chunk, ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "sparse", IndexRef: s.ref, Score: score, BackendScore: h.Score})
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
	if verify {
		truth, _, err := (inverted{}).search(ctx, s, v, allowed, q.TopK)
		if err != nil {
			return Result{}, err
		}
		if len(truth) != len(result.Candidates) {
			return Result{}, fmt.Errorf("%w: sparse candidate coverage differs from exact IP", ErrInvalid)
		}
		if len(truth) > 0 {
			threshold := truth[len(truth)-1].Score
			tolerance := 1e-6 * math.Max(1, math.Abs(threshold))
			for _, hit := range truth {
				if hit.Score > threshold+tolerance && !seen[hit.ID] {
					return Result{}, fmt.Errorf("%w: sparse backend missed a strict top candidate", ErrInvalid)
				}
			}
			for _, candidate := range result.Candidates {
				if candidate.Score < threshold-tolerance {
					return Result{}, fmt.Errorf("%w: sparse backend returned below exact top-k boundary", ErrInvalid)
				}
			}
		}
	}
	for i := range result.Candidates {
		result.Candidates[i].Rank = i + 1
	}
	return result, ctx.Err()
}
func (s *Service) VerifyAndProbe(ctx context.Context, idx corpus.LaneIndex, m corpus.ChunkManifest, probes []corpus.Chunk) ([]corpus.ProbeResult, error) {
	actual, e := load[corpus.ChunkManifest](ctx, s.objects, idx.ChunkManifest)
	if e != nil {
		return nil, e
	}
	if !reflect.DeepEqual(actual, m) || len(probes) == 0 {
		return nil, ErrInvalid
	}
	b, e := json.Marshal(idx)
	if e != nil {
		return nil, e
	}
	snap, e := s.open(ctx, artifacts.Reference(b), idx, m)
	if e != nil {
		return nil, e
	}
	if e = s.backend.verify(ctx, snap); e != nil {
		return nil, e
	}
	valid := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		valid = append(valid, in.RevisionID)
	}
	results := make([]corpus.ProbeResult, 0, len(probes))
	seen := map[string]bool{}
	for _, probe := range probes {
		r, ok := snap.rows[probe.ID]
		if !ok || r.Chunk != probe || seen[probe.ID] {
			return nil, ErrInvalid
		}
		seen[probe.ID] = true
		found, e := snap.search(ctx, Query{IndexRef: snap.ref, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, Generation: idx.Generation, ValidRevisionIDs: valid, Text: probe.Text, TopK: s.config.ProbeTopK}, true)
		if e != nil {
			return nil, e
		}
		ids := make([]string, 0, len(found.Candidates))
		for _, c := range found.Candidates {
			ids = append(ids, c.Chunk.ID)
		}
		results = append(results, corpus.ProbeResult{QueryChunkID: probe.ID, CandidateIDs: ids})
	}
	return results, nil
}
