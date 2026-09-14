package multivector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
)

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
	TokenTopK int                     `json:"token_top_k"`
	ProbeTopK int                     `json:"probe_top_k"`
}

func (c Config) request(role string, input []representation.Input) representation.Request {
	e := c.Document
	if role == "query" {
		e = c.Query
	}
	return representation.Request{Model: e.Callpoint, ConfigurationID: e.ConfigurationID, OutputContract: representation.OutputContract(representation.TokenMatrix), ContractID: c.Contract.ID, Space: c.Space, Role: role, Input: input}
}
func (c Config) validate() error {
	if err := c.Contract.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if c.Contract.Kind != representation.TokenMatrix || c.BatchSize < 1 || c.BatchSize > representation.MaxBatch || c.TokenTopK < 1 || c.TokenTopK > 16384 || c.ProbeTopK < 1 || c.ProbeTopK > 64 || c.Document.PhysicalModel == "" || c.Query.PhysicalModel == "" {
		return ErrInvalid
	}
	for _, role := range []string{"document", "query"} {
		if err := c.request(role, []representation.Input{{ID: "check", Text: "check"}}).Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	return nil
}
func (c Config) matches(p corpus.Profile) bool {
	return p.Lane == "multivector" && p.Encoder == c.Document.PhysicalModel && p.Tokenizer == c.Contract.TokenizerID && p.Space == c.Space && p.Dimensions == c.Contract.Dimensions && p.Mask == "valid" && p.Aggregation == c.Contract.Aggregation
}

// Binding excludes execution budgets; the immutable model/space/score meaning
// must be identical at build, reopen and query time.
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
	Index            corpus.LaneIndex
	Ref              corpus.Ref
	Usage            representation.Usage // confirmed model use in this invocation
	StoredUsage      representation.Usage // persisted original use, never billed on resume
	EncodedChunks    int
	ReusedChunks     int
	UsageUnknown     bool
	Reused           bool
	ActiveTokenRows  int   `json:"active_token_rows"`
	MatrixValueBytes int64 `json:"matrix_value_bytes"`
}

func indexCost(s *Snapshot) (int, int64) {
	var valueBytes int64
	for _, r := range s.rows {
		valueBytes += int64(r.Matrix.Shape[0] * r.Matrix.Shape[1] * 8)
	}
	return len(s.tokens), valueBytes
}

type ProjectionError struct {
	IndexRef corpus.Ref
	Err      error
}

func (e *ProjectionError) Error() string {
	return fmt.Sprintf("multivector projection for %s: %v", e.IndexRef.SHA256, e.Err)
}
func (e *ProjectionError) Unwrap() error { return e.Err }

type row struct {
	Chunk      corpus.Chunk               `json:"chunk"`
	Matrix     representation.TokenValues `json:"matrix"`
	MatrixHash string                     `json:"matrix_hash"`
}
type tokenRow struct {
	ChunkID  string `json:"chunk_id"`
	Position int    `json:"position"`
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
	TokenRows     []tokenRow           `json:"token_rows"`
}

func matrixHash(v representation.TokenValues) string {
	raw, _ := json.Marshal(v)
	return artifacts.Hash(raw)
}
func tokenRows(rows []row) []tokenRow {
	result := []tokenRow{}
	for _, r := range rows {
		for i, valid := range r.Matrix.Mask {
			if valid {
				result = append(result, tokenRow{r.Chunk.ID, i})
			}
		}
	}
	return result
}

type indexedToken struct {
	chunkID  string
	position int
	vector   []float64
}
type hit struct {
	id           string
	backendScore float64
}
type searchStats struct {
	observedRows    int
	candidateChunks int
}
type backend interface {
	prepare(context.Context, *Snapshot) error
	load(context.Context, *Snapshot) error
	verify(context.Context, *Snapshot) error
	search(context.Context, *Snapshot, representation.TokenValues, map[string]bool, int) ([]hit, searchStats, error)
}
type Service struct {
	objects artifacts.Store
	encoder Encoder
	config  Config
	backend backend
	observe *telemetry.Bundle
}
type Option func(*Service) error

func WithTelemetry(b *telemetry.Bundle) Option {
	return func(s *Service) error {
		if b == nil {
			return ErrInvalid
		}
		s.observe = b
		return nil
	}
}

// New uses a persistent exact token-row index for small corpus and numerical
// truth. Build never reads the active release pointer; the caller pins a manifest.
func New(objects artifacts.Store, encoder Encoder, c Config, options ...Option) (*Service, error) {
	if objects == nil || encoder == nil {
		return nil, ErrInvalid
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	s := &Service{objects: objects, encoder: encoder, config: c, backend: exact{}}
	for _, option := range options {
		if option == nil {
			return nil, ErrInvalid
		}
		if err := option(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func load[T any](ctx context.Context, objects artifacts.Store, ref corpus.Ref) (T, error) {
	var value T
	raw, err := objects.Get(ctx, ref)
	if err != nil {
		return value, err
	}
	if artifacts.Reference(raw) != ref {
		return value, ErrInvalid
	}
	if err := representation.Decode(raw, &value); err != nil {
		return value, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return value, nil
}
func put(ctx context.Context, objects artifacts.Store, value any) (corpus.Ref, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return corpus.Ref{}, err
	}
	return objects.Put(ctx, raw)
}
func validateManifest(m corpus.ChunkManifest) error {
	if m.SchemaVersion != 1 || m.ModuleID == "" || m.ReleaseID == "" || !artifacts.ValidHash(m.InputManifestHash) || m.Profile == "" || len(m.Inputs) == 0 || len(m.Chunks) == 0 {
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
func (s *Service) encode(ctx context.Context, role string, input []representation.Input) (out representation.Response, err error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	q := s.config.request(role, input)
	if err := q.Validate(); err != nil {
		return out, err
	}
	e := s.config.Document
	if role == "query" {
		e = s.config.Query
	}
	ctx, stage, stageErr := s.begin(ctx, "multivector.encode", slog.String("role", role), slog.Int("batch_size", len(input)), slog.String("configuration_id", e.ConfigurationID), slog.String("representation_contract_id", q.ContractID), slog.String("representation_space", q.Space))
	if stageErr != nil {
		return out, stageErr
	}
	defer func() {
		tokens := int64(0)
		if out.Usage != nil {
			tokens = out.Usage.PromptTokens
		}
		end(ctx, stage, err, slog.Int64("prompt_tokens", tokens))
	}()
	r, err := s.encoder.Represent(ctx, q, s.config.Contract, e.PhysicalModel)
	if err != nil {
		return r, fmt.Errorf("multivector %s encode: %w", role, err)
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := r.Validate(q, s.config.Contract, e.PhysicalModel); err != nil {
		return r, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, item := range r.Data {
		if err := ValidateMatrix(*item.TokenMatrix, s.config.Contract); err != nil {
			return r, err
		}
	}
	return r, nil
}
func (s *Service) project(ctx context.Context, snap *Snapshot) (err error) {
	ctx, stage, e := s.begin(ctx, "multivector.project", slog.String("build_id", snap.index.BuildID), slog.Int64("generation", snap.index.Generation), slog.Int("token_rows", len(snap.tokens)))
	if e != nil {
		return e
	}
	defer func() { end(ctx, stage, err) }()
	return s.backend.prepare(ctx, snap)
}
func (s *Service) begin(ctx context.Context, event string, attrs ...slog.Attr) (context.Context, *telemetry.Stage, error) {
	if s.observe == nil {
		return ctx, nil, nil
	}
	return s.observe.Begin(ctx, "multivector", event, attrs...)
}
func end(ctx context.Context, stage *telemetry.Stage, err error, attrs ...slog.Attr) {
	if stage == nil {
		return
	}
	if err != nil {
		outcome, code := "failed", "MULTIVECTOR_FAILURE"
		switch {
		case errors.Is(err, context.Canceled):
			outcome, code = "cancelled", "CANCELLED"
		case errors.Is(err, context.DeadlineExceeded):
			outcome, code = "timed_out", "DEADLINE_EXCEEDED"
		case errors.Is(err, ErrInvalid):
			outcome, code = "rejected", "INVALID_CONTRACT_OR_ARTIFACT"
		}
		stage.End(ctx, outcome, code, err, attrs...)
		return
	}
	stage.End(ctx, "succeeded", "", nil, attrs...)
}

func (s *Service) Build(ctx context.Context, q BuildRequest) (out BuildResult, err error) {
	ctx, stage, e := s.begin(ctx, "multivector.build", slog.String("build_id", q.BuildID), slog.Int64("generation", q.Generation))
	if e != nil {
		return out, e
	}
	defer func() {
		end(ctx, stage, err, slog.Int("encoded_chunks", out.EncodedChunks), slog.Int("reused_chunks", out.ReusedChunks), slog.Int64("prompt_tokens", out.Usage.PromptTokens))
	}()
	if q.BuildID == "" || q.Generation < 1 || !s.config.matches(q.Profile) {
		return out, ErrInvalid
	}
	m, e := load[corpus.ChunkManifest](ctx, s.objects, q.ChunkManifest)
	if e != nil {
		return out, e
	}
	if e := validateManifest(m); e != nil {
		return out, e
	}
	idx := corpus.LaneIndex{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, InputManifestHash: m.InputManifestHash, ChunkManifest: q.ChunkManifest, Profile: q.Profile}
	if q.ResumeIndex != nil {
		snap, e := s.Open(ctx, *q.ResumeIndex)
		if e != nil {
			return out, e
		}
		got := snap.index
		got.Shards = nil
		if !reflect.DeepEqual(got, idx) {
			return out, ErrInvalid
		}
		rows, bytes := indexCost(snap)
		out = BuildResult{Index: snap.index, Ref: *q.ResumeIndex, StoredUsage: snap.usage, ReusedChunks: len(snap.rows), Reused: true, ActiveTokenRows: rows, MatrixValueBytes: bytes}
		if e := s.project(ctx, snap); e != nil {
			return out, &ProjectionError{IndexRef: *q.ResumeIndex, Err: e}
		}
		return out, nil
	}
	usage := representation.Usage{}
	encoded := 0
	for start := 0; start < len(m.Chunks); start += s.config.BatchSize {
		if e := ctx.Err(); e != nil {
			return BuildResult{Usage: usage, EncodedChunks: encoded}, e
		}
		group := m.Chunks[start:min(start+s.config.BatchSize, len(m.Chunks))]
		input := make([]representation.Input, len(group))
		for i, c := range group {
			input[i] = representation.Input{ID: c.ID, Text: c.Text}
		}
		r, e := s.encode(ctx, "document", input)
		if e != nil {
			return BuildResult{Usage: usage, EncodedChunks: encoded, UsageUnknown: true}, e
		}
		usage.PromptTokens += r.Usage.PromptTokens
		usage.TotalTokens += r.Usage.TotalTokens
		encoded += len(group)
		values := map[string]representation.TokenValues{}
		for _, item := range r.Data {
			values[item.ID] = *item.TokenMatrix
		}
		part := shard{SchemaVersion: 1, BuildID: q.BuildID, Generation: q.Generation, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, ChunkManifest: q.ChunkManifest, Profile: q.Profile, Binding: s.config.binding(), Usage: *r.Usage, Rows: make([]row, 0, len(group))}
		ids := make([]string, 0, len(group))
		for _, c := range group {
			v := values[c.ID]
			part.Rows = append(part.Rows, row{c, v, matrixHash(v)})
			ids = append(ids, c.ID)
		}
		part.TokenRows = tokenRows(part.Rows)
		ref, e := put(ctx, s.objects, part)
		if e != nil {
			return BuildResult{Usage: usage, EncodedChunks: encoded}, e
		}
		idx.Shards = append(idx.Shards, corpus.IndexShard{Artifact: ref, ChunkIDs: ids})
	}
	ref, e := put(ctx, s.objects, idx)
	if e != nil {
		return BuildResult{Usage: usage, EncodedChunks: encoded}, e
	}
	out = BuildResult{Index: idx, Ref: ref, Usage: usage, StoredUsage: usage, EncodedChunks: encoded}
	snap, e := s.Open(ctx, ref)
	if e != nil {
		return out, e
	}
	out.ActiveTokenRows, out.MatrixValueBytes = indexCost(snap)
	if e = s.project(ctx, snap); e != nil {
		return out, &ProjectionError{IndexRef: ref, Err: e}
	}
	return out, nil
}

type Snapshot struct {
	service  *Service
	index    corpus.LaneIndex
	ref      corpus.Ref
	manifest corpus.ChunkManifest
	rows     map[string]row
	tokens   []indexedToken
	usage    representation.Usage
}

func (s *Service) Open(ctx context.Context, ref corpus.Ref) (*Snapshot, error) {
	idx, err := load[corpus.LaneIndex](ctx, s.objects, ref)
	if err != nil {
		return nil, err
	}
	m, err := load[corpus.ChunkManifest](ctx, s.objects, idx.ChunkManifest)
	if err != nil {
		return nil, err
	}
	return s.open(ctx, ref, idx, m)
}
func (s *Service) Load(ctx context.Context, ref corpus.Ref) (snap *Snapshot, err error) {
	ctx, stage, e := s.begin(ctx, "multivector.load", slog.String("index_hash", ref.SHA256))
	if e != nil {
		return nil, e
	}
	defer func() { end(ctx, stage, err) }()
	snap, err = s.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := s.backend.load(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.backend.verify(ctx, snap); err != nil {
		return nil, err
	}
	return snap, nil
}
func (s *Service) open(ctx context.Context, ref corpus.Ref, idx corpus.LaneIndex, m corpus.ChunkManifest) (*Snapshot, error) {
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	if idx.SchemaVersion != 1 || idx.BuildID == "" || idx.Generation < 1 || idx.InputManifestHash != m.InputManifestHash || !s.config.matches(idx.Profile) || len(idx.Shards) == 0 {
		return nil, ErrInvalid
	}
	snap := &Snapshot{service: s, index: idx, ref: ref, manifest: m, rows: map[string]row{}}
	expected := map[string]corpus.Chunk{}
	for _, c := range m.Chunks {
		expected[c.ID] = c
	}
	seenShards := map[string]bool{}
	for _, declared := range idx.Shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seenShards[declared.Artifact.SHA256] || len(declared.ChunkIDs) == 0 {
			return nil, ErrInvalid
		}
		seenShards[declared.Artifact.SHA256] = true
		part, err := load[shard](ctx, s.objects, declared.Artifact)
		if err != nil {
			return nil, err
		}
		if part.SchemaVersion != 1 || part.BuildID != idx.BuildID || part.Generation != idx.Generation || part.ModuleID != m.ModuleID || part.ReleaseID != m.ReleaseID || part.ChunkManifest != idx.ChunkManifest || part.Profile != idx.Profile || part.Binding != s.config.binding() || len(part.Rows) != len(declared.ChunkIDs) || part.Usage.PromptTokens < 0 || part.Usage.TotalTokens < part.Usage.PromptTokens {
			return nil, ErrInvalid
		}
		if !reflect.DeepEqual(part.TokenRows, tokenRows(part.Rows)) {
			return nil, fmt.Errorf("%w: token rows differ from matrices", ErrInvalid)
		}
		for i, r := range part.Rows {
			want, ok := expected[r.Chunk.ID]
			_, duplicate := snap.rows[r.Chunk.ID]
			if !ok || duplicate || want != r.Chunk || declared.ChunkIDs[i] != r.Chunk.ID || matrixHash(r.Matrix) != r.MatrixHash {
				return nil, ErrInvalid
			}
			if err := ValidateMatrix(r.Matrix, s.config.Contract); err != nil {
				return nil, err
			}
			snap.rows[r.Chunk.ID] = r
		}
		for _, token := range part.TokenRows {
			snap.tokens = append(snap.tokens, indexedToken{token.ChunkID, token.Position, snap.rows[token.ChunkID].Matrix.Values[token.Position]})
		}
		snap.usage.PromptTokens += part.Usage.PromptTokens
		snap.usage.TotalTokens += part.Usage.TotalTokens
	}
	if len(snap.rows) != len(expected) || len(snap.tokens) == 0 {
		return nil, fmt.Errorf("%w: incomplete token-matrix coverage", ErrInvalid)
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
	TokenTopK        int // 0 selects configured budget; cannot exceed it
}
type Candidate struct {
	Chunk             corpus.Chunk `json:"chunk"`
	ModuleID          string       `json:"module_id"`
	ReleaseID         string       `json:"release_id"`
	Generation        int64        `json:"generation"`
	Lane              string       `json:"lane"`
	IndexRef          corpus.Ref   `json:"index_ref"`
	Score             float64      `json:"score"`
	Aggregation       string       `json:"aggregation"`
	BackendTokenScore float64      `json:"backend_token_score"`
	Rank              int          `json:"rank"`
}
type Cost struct {
	// Exact observes every compared row; native ANN exposes returned rows only.
	BackendTokenRowsObserved int   `json:"backend_token_rows_observed"`
	CandidateChunks          int   `json:"candidate_chunks"`
	ScoredMatrixValueBytes   int64 `json:"scored_matrix_value_bytes"`
	ExactDotProducts         int64 `json:"exact_dot_products"`
	ValidQueryTokens         int   `json:"valid_query_tokens"`
	PerTokenBudget           int   `json:"per_token_budget"`
}
type Result struct {
	Candidates []Candidate          `json:"candidates"`
	Usage      representation.Usage `json:"usage"`
	Cost       Cost                 `json:"cost"`
}

func (s *Service) Search(ctx context.Context, q Query) (Result, error) {
	snap, err := s.Open(ctx, q.IndexRef)
	if err != nil {
		return Result{}, err
	}
	return snap.Search(ctx, q)
}
func (s *Snapshot) Search(ctx context.Context, q Query) (out Result, err error) {
	return s.search(ctx, q, false)
}
func (s *Snapshot) search(ctx context.Context, q Query, verify bool) (out Result, err error) {
	ctx, stage, e := s.service.begin(ctx, "multivector.search", slog.String("module_id", q.ModuleID), slog.String("release_id", q.ReleaseID), slog.Int64("generation", q.Generation))
	if e != nil {
		return out, e
	}
	defer func() {
		end(ctx, stage, err, slog.Int("candidate_chunks", out.Cost.CandidateChunks), slog.Int("returned_chunks", len(out.Candidates)), slog.Int("backend_token_rows_observed", out.Cost.BackendTokenRowsObserved))
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if q.IndexRef != s.ref || q.ModuleID != s.manifest.ModuleID || q.ReleaseID != s.manifest.ReleaseID || q.Generation != s.index.Generation || q.TopK < 1 || q.TopK > 1000 || strings.TrimSpace(q.Text) == "" || q.TokenTopK < 0 || q.TokenTopK > s.service.config.TokenTopK {
		return out, ErrInvalid
	}
	valid := map[string]bool{}
	for _, id := range q.ValidRevisionIDs {
		if id == "" || valid[id] {
			return out, ErrInvalid
		}
		valid[id] = true
	}
	allowed := map[string]bool{}
	for id, r := range s.rows {
		if valid[r.Chunk.RevisionID] {
			allowed[id] = true
		}
	}
	out.Candidates = []Candidate{}
	if len(allowed) == 0 {
		return out, nil
	}
	budget := q.TokenTopK
	if budget == 0 {
		budget = s.service.config.TokenTopK
	}
	out.Cost.PerTokenBudget = budget
	r, e := s.service.encode(ctx, "query", []representation.Input{{ID: "query", Text: q.Text}})
	if e != nil {
		return out, e
	}
	matrix := *r.Data[0].TokenMatrix
	out.Usage = *r.Usage
	for _, v := range matrix.Mask {
		if v {
			out.Cost.ValidQueryTokens++
		}
	}
	hits, stats, e := s.service.backend.search(ctx, s, matrix, allowed, budget)
	if e != nil {
		return out, e
	}
	out.Cost.BackendTokenRowsObserved = stats.observedRows
	out.Cost.CandidateChunks = stats.candidateChunks
	seen := map[string]bool{}
	for _, h := range hits {
		item, exists := s.rows[h.id]
		if !exists || !allowed[h.id] || seen[h.id] || math.IsNaN(h.backendScore) || math.IsInf(h.backendScore, 0) {
			return Result{}, ErrInvalid
		}
		seen[h.id] = true
		score, e := MaxSim(matrix, item.Matrix, s.service.config.Contract)
		if e != nil {
			return Result{}, e
		}
		out.Cost.ExactDotProducts += int64(out.Cost.ValidQueryTokens * validCount(item.Matrix.Mask))
		out.Cost.ScoredMatrixValueBytes += int64(item.Matrix.Shape[0] * item.Matrix.Shape[1] * 8)
		out.Candidates = append(out.Candidates, Candidate{Chunk: item.Chunk, ModuleID: q.ModuleID, ReleaseID: q.ReleaseID, Generation: q.Generation, Lane: "multivector", IndexRef: s.ref, Score: score, Aggregation: s.service.config.Contract.Aggregation, BackendTokenScore: h.backendScore})
	}
	if len(out.Candidates) != stats.candidateChunks {
		return Result{}, ErrInvalid
	}
	sort.Slice(out.Candidates, func(i, j int) bool {
		if out.Candidates[i].Score == out.Candidates[j].Score {
			return out.Candidates[i].Chunk.ID < out.Candidates[j].Chunk.ID
		}
		return out.Candidates[i].Score > out.Candidates[j].Score
	})
	if len(out.Candidates) > q.TopK {
		out.Candidates = out.Candidates[:q.TopK]
	}
	if verify {
		truth, e := s.exactTop(ctx, matrix, allowed, q.TopK)
		if e != nil {
			return Result{}, e
		}
		if e := compareBoundary(out.Candidates, truth); e != nil {
			return Result{}, e
		}
	}
	for i := range out.Candidates {
		out.Candidates[i].Rank = i + 1
	}
	return out, ctx.Err()
}
func validCount(mask []bool) int {
	n := 0
	for _, yes := range mask {
		if yes {
			n++
		}
	}
	return n
}
func (s *Snapshot) exactTop(ctx context.Context, query representation.TokenValues, allowed map[string]bool, k int) ([]hit, error) {
	out := make([]hit, 0, len(allowed))
	for id := range allowed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		score, err := MaxSim(query, s.rows[id].Matrix, s.service.config.Contract)
		if err != nil {
			return nil, err
		}
		out = append(out, hit{id, score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].backendScore == out[j].backendScore {
			return out[i].id < out[j].id
		}
		return out[i].backendScore > out[j].backendScore
	})
	return out[:min(k, len(out))], nil
}
func compareBoundary(candidates []Candidate, truth []hit) error {
	if len(candidates) != len(truth) {
		return fmt.Errorf("%w: token candidates miss exact top-k cardinality", ErrInvalid)
	}
	if len(truth) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Chunk.ID] = true
	}
	threshold := truth[len(truth)-1].backendScore
	tolerance := 1e-8 * math.Max(1, math.Abs(threshold))
	for _, h := range truth {
		if h.backendScore > threshold+tolerance && !seen[h.id] {
			return fmt.Errorf("%w: independent token candidates miss strict MaxSim top-k", ErrInvalid)
		}
	}
	for _, c := range candidates {
		if c.Score < threshold-tolerance {
			return fmt.Errorf("%w: candidate below exact MaxSim top-k boundary", ErrInvalid)
		}
	}
	return nil
}

// VerifyAndProbe is the content LaneVerifier. It checks fixed artifacts and the
// actual backend, then compares each probe to full-corpus MaxSim truth.
func (s *Service) VerifyAndProbe(ctx context.Context, idx corpus.LaneIndex, m corpus.ChunkManifest, probes []corpus.Chunk) (results []corpus.ProbeResult, err error) {
	ctx, stage, e := s.begin(ctx, "multivector.probe", slog.String("build_id", idx.BuildID), slog.Int64("generation", idx.Generation), slog.Int("probe_count", len(probes)))
	if e != nil {
		return nil, e
	}
	defer func() { end(ctx, stage, err, slog.Int("verified_probes", len(results))) }()
	actual, err := load[corpus.ChunkManifest](ctx, s.objects, idx.ChunkManifest)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(actual, m) || len(probes) == 0 {
		return nil, ErrInvalid
	}
	raw, _ := json.Marshal(idx)
	snap, err := s.Open(ctx, artifacts.Reference(raw))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(snap.index, idx) {
		return nil, ErrInvalid
	}
	if err := s.backend.load(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.backend.verify(ctx, snap); err != nil {
		return nil, err
	}
	revisions := make([]string, 0, len(m.Inputs))
	for _, in := range m.Inputs {
		revisions = append(revisions, in.RevisionID)
	}
	result := make([]corpus.ProbeResult, 0, len(probes))
	seen := map[string]bool{}
	for _, probe := range probes {
		item, ok := snap.rows[probe.ID]
		if !ok || item.Chunk != probe || seen[probe.ID] {
			return nil, ErrInvalid
		}
		seen[probe.ID] = true
		got, err := snap.search(ctx, Query{IndexRef: snap.ref, ModuleID: m.ModuleID, ReleaseID: m.ReleaseID, Generation: idx.Generation, ValidRevisionIDs: revisions, Text: probe.Text, TopK: s.config.ProbeTopK}, true)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(got.Candidates))
		for _, c := range got.Candidates {
			ids = append(ids, c.Chunk.ID)
		}
		result = append(result, corpus.ProbeResult{QueryChunkID: probe.ID, CandidateIDs: ids})
	}
	return result, nil
}
