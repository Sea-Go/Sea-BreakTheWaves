package parity

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
)

const parityCallpoint = "frozen-bge-m3-parity"
const parityDocumentConfiguration = "11111111-1111-4111-8111-111111111111"
const parityQueryConfiguration = "22222222-2222-4222-8222-222222222222"

// hex of UTF-8 segments, separated by '!', is injective and keeps the tuple's
// lexicographic order (including prefix strings). It is also a valid DC input
// ID, unlike NUL-delimited tuples. Reject overlong IDs rather than colliding.
func composite(parts ...string) (string, error) {
	if len(parts) == 0 {
		return "", ErrArtifact
	}
	value := ""
	for i, part := range parts {
		if part == "" {
			return "", ErrArtifact
		}
		if i > 0 {
			value += "!"
		}
		value += hex.EncodeToString([]byte(part))
	}
	if len(value) > 256 {
		return "", ErrArtifact
	}
	return value, nil
}

type frozenEncoder struct {
	profile map[string]LaneProfile
	docs    map[string]Representations
	queries map[string]Representations // exact text; repeated text must match
}

func (e frozenEncoder) Represent(_ context.Context, q representation.Request,
	c representation.Contract, model string) (representation.Response, error) {
	lane := map[representation.Kind]string{representation.Dense: "dense", representation.Sparse: "sparse",
		representation.TokenMatrix: "token_matrix"}[c.Kind]
	p, ok := e.profile[lane]
	if !ok || model != p.Model || q.Space != p.RepresentationSpace || q.ContractID != c.ID ||
		q.OutputContract != p.OutputContract || c != p.RepresentationContract {
		return representation.Response{}, ErrArtifact
	}
	response := representation.Response{Model: model, ConfigurationID: q.ConfigurationID,
		OutputContract: q.OutputContract, ContractID: q.ContractID, Space: q.Space,
		Role: q.Role, TokenizerID: c.TokenizerID, VocabularyID: c.VocabularyID,
		Usage: &representation.Usage{}} // frozen replay is not a new model call
	for i := len(q.Input) - 1; i >= 0; i-- {
		input := q.Input[i]
		var v Representations
		if q.Role == "document" {
			v, ok = e.docs[input.ID]
		} else {
			v, ok = e.queries[input.Text]
		}
		if !ok {
			return representation.Response{}, ErrArtifact
		}
		item := representation.Item{ID: input.ID}
		switch c.Kind {
		case representation.Dense:
			item.Dense = v.Dense
		case representation.Sparse:
			item.Sparse = v.Sparse
		case representation.TokenMatrix:
			item.TokenMatrix = v.TokenMatrix
		default:
			return representation.Response{}, ErrArtifact
		}
		response.Data = append(response.Data, item)
	}
	return response, nil
}

type Hit struct {
	DocumentID       string  `json:"document_id"`
	DocumentRevision string  `json:"document_revision"`
	ChunkID          string  `json:"chunk_id"`
	Score            float64 `json:"score"`
}

type ExactResults struct {
	Dense         map[string][]Hit
	Sparse        map[string][]Hit
	TokenMatrix   map[string][]Hit
	FullTokenRows int
	ChunkCount    int
	QueryCount    int
}

func (f Frozen) buildCorpus(ctx context.Context, objects artifacts.Store) (corpus.ChunkManifest, corpus.Ref,
	map[string]ChunkRow, map[string]string, error) {
	m := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "h10b-synthetic", ReleaseID: f.ManifestSHA256,
		InputManifestHash: f.Manifest.DatasetManifestSHA256, Profile: "bge-m3-three-lane-parity-v1",
		ParserVersion: "frozen-h10b-v1", ChunkerVersion: "frozen-chunks-v1", ChunkSize: 4096}
	lookup := map[string]ChunkRow{}
	keyToRevision := map[string]string{}
	for _, row := range f.Chunks {
		id, err := composite(row.DocumentID, row.DocumentRevision, row.ChunkID)
		if err != nil {
			return m, corpus.Ref{}, nil, nil, err
		}
		original, err := objects.Put(ctx, []byte("frozen-h10b-original:"+id))
		if err != nil {
			return m, corpus.Ref{}, nil, nil, err
		}
		m.Inputs = append(m.Inputs, corpus.Input{RevisionID: id,
			ContentID: row.DocumentID, SourceKind: "wiki", Original: original, ChunkCount: 1})
		encoding, _ := json.Marshal([]any{m.Profile, row.Text})
		m.Chunks = append(m.Chunks, corpus.Chunk{ID: id, RevisionID: id,
			ContentID: row.DocumentID, SourceKind: "wiki", Original: original,
			Location: corpus.Location{Locator: "frozen:chunk", OriginalByteStart: 0,
				OriginalByteEnd: len(row.Text), NormalizedRuneStart: 0,
				NormalizedRuneEnd: utf8.RuneCountInString(row.Text)},
			Text: row.Text, TextHash: artifacts.Hash([]byte(row.Text)),
			EncodingKey: artifacts.Hash(encoding), Required: true})
		if _, exists := lookup[id]; exists || keyToRevision[row.ChunkKey] != "" {
			return m, corpus.Ref{}, nil, nil, ErrArtifact
		}
		lookup[id] = row
		keyToRevision[row.ChunkKey] = id
	}
	data, err := json.Marshal(m)
	if err != nil {
		return m, corpus.Ref{}, nil, nil, err
	}
	ref, err := objects.Put(ctx, data)
	return m, ref, lookup, keyToRevision, err
}

// RunExact loads frozen values through each lane's existing Encoder seam and
// exercises Build→Search. It does not perform another model call or use ANN.
func RunExact(ctx context.Context, frozen Frozen, artifactDir string,
	candidates map[string][]string) (ExactResults, error) {
	var out ExactResults
	objects, err := artifacts.NewLocal(artifactDir)
	if err != nil {
		return out, err
	}
	m, manifestRef, lookup, keyToRevision, err := frozen.buildCorpus(ctx, objects)
	if err != nil {
		return out, err
	}
	encoder := frozenEncoder{profile: frozen.Manifest.Lanes, docs: map[string]Representations{},
		queries: map[string]Representations{}}
	for id, row := range lookup {
		encoder.docs[id] = row.Representations
		for _, active := range row.Representations.TokenMatrix.Mask {
			if active {
				out.FullTokenRows++
			}
		}
	}
	if out.FullTokenRows < 1 || out.FullTokenRows > 16384 {
		return out, fmt.Errorf("%w: full-token exact reference exceeds lane budget", ErrArtifact)
	}
	for _, row := range frozen.Queries {
		encoder.queries[row.Text] = row.Representations
	}
	profile := func(name string) LaneProfile { return frozen.Manifest.Lanes[name] }
	encoding := func(p LaneProfile, configuration string) dense.Encoding {
		return dense.Encoding{Callpoint: parityCallpoint, ConfigurationID: configuration, PhysicalModel: p.Model}
	}
	denseProfile := profile("dense")
	denseCfg := dense.Config{Document: encoding(denseProfile, parityDocumentConfiguration),
		Query: encoding(denseProfile, parityQueryConfiguration), Contract: denseProfile.RepresentationContract,
		Space: denseProfile.RepresentationSpace, BatchSize: denseProfile.MaxBatch,
		ProbeTopK: min(4, len(m.Chunks))}
	denseService, err := dense.New(objects, encoder, denseCfg)
	if err != nil {
		return out, err
	}
	denseBuilt, err := denseService.Build(ctx, dense.BuildRequest{BuildID: "frozen-bge-dense", Generation: 1,
		ChunkManifest: manifestRef, Profile: corpus.Profile{Lane: "dense", Encoder: denseProfile.Model,
			Tokenizer: denseProfile.RepresentationContract.TokenizerID, Space: denseProfile.RepresentationSpace,
			Dimensions: denseProfile.RepresentationContract.Dimensions}})
	if err != nil {
		return out, fmt.Errorf("build existing dense exact: %w", err)
	}
	sparseProfile := profile("sparse")
	sparseCfg := sparse.Config{Document: sparse.Encoding{Callpoint: parityCallpoint,
		ConfigurationID: parityDocumentConfiguration, PhysicalModel: sparseProfile.Model},
		Query: sparse.Encoding{Callpoint: parityCallpoint, ConfigurationID: parityQueryConfiguration,
			PhysicalModel: sparseProfile.Model}, Contract: sparseProfile.RepresentationContract,
		Space: sparseProfile.RepresentationSpace, BatchSize: sparseProfile.MaxBatch,
		ProbeTopK: min(4, len(m.Chunks))}
	sparseService, err := sparse.New(objects, encoder, sparseCfg)
	if err != nil {
		return out, err
	}
	sparseBuilt, err := sparseService.Build(ctx, sparse.BuildRequest{BuildID: "frozen-bge-sparse", Generation: 1,
		ChunkManifest: manifestRef, Profile: corpus.Profile{Lane: "sparse", Encoder: sparseProfile.Model,
			Tokenizer: sparseProfile.RepresentationContract.TokenizerID, Space: sparseProfile.RepresentationSpace,
			Dimensions: sparseProfile.RepresentationContract.Dimensions}})
	if err != nil {
		return out, fmt.Errorf("build existing sparse inverted: %w", err)
	}
	multiProfile := profile("token_matrix")
	multiCfg := multivector.Config{Document: multivector.Encoding{Callpoint: parityCallpoint,
		ConfigurationID: parityDocumentConfiguration, PhysicalModel: multiProfile.Model},
		Query: multivector.Encoding{Callpoint: parityCallpoint, ConfigurationID: parityQueryConfiguration,
			PhysicalModel: multiProfile.Model}, Contract: multiProfile.RepresentationContract,
		Space: multiProfile.RepresentationSpace, BatchSize: multiProfile.MaxBatch,
		TokenTopK: out.FullTokenRows, ProbeTopK: min(4, len(m.Chunks))}
	multiService, err := multivector.New(objects, encoder, multiCfg)
	if err != nil {
		return out, err
	}
	multiBuilt, err := multiService.Build(ctx, multivector.BuildRequest{BuildID: "frozen-bge-multivector", Generation: 1,
		ChunkManifest: manifestRef, Profile: corpus.Profile{Lane: "multivector", Encoder: multiProfile.Model,
			Tokenizer: multiProfile.RepresentationContract.TokenizerID, Space: multiProfile.RepresentationSpace,
			Dimensions: multiProfile.RepresentationContract.Dimensions, Mask: "valid",
			Aggregation: multiProfile.RepresentationContract.Aggregation}})
	if err != nil {
		return out, fmt.Errorf("build existing multivector exact: %w", err)
	}
	mapDense := func(candidates []dense.Candidate) ([]Hit, error) {
		result := make([]Hit, 0, len(candidates))
		for _, candidate := range candidates {
			row, ok := lookup[candidate.Chunk.ID]
			if !ok {
				return nil, ErrArtifact
			}
			result = append(result, Hit{row.DocumentID, row.DocumentRevision, row.ChunkID, candidate.Score})
		}
		return result, nil
	}
	mapSparse := func(candidates []sparse.Candidate) ([]Hit, error) {
		result := make([]Hit, 0, len(candidates))
		for _, candidate := range candidates {
			row, ok := lookup[candidate.Chunk.ID]
			if !ok {
				return nil, ErrArtifact
			}
			result = append(result, Hit{row.DocumentID, row.DocumentRevision, row.ChunkID, candidate.Score})
		}
		return result, nil
	}
	mapMulti := func(candidates []multivector.Candidate) ([]Hit, error) {
		result := make([]Hit, 0, len(candidates))
		for _, candidate := range candidates {
			row, ok := lookup[candidate.Chunk.ID]
			if !ok {
				return nil, ErrArtifact
			}
			result = append(result, Hit{row.DocumentID, row.DocumentRevision, row.ChunkID, candidate.Score})
		}
		return result, nil
	}
	if len(keyToRevision) == 0 || len(m.Chunks) > 1000 {
		return out, ErrArtifact
	}
	out.Dense, out.Sparse, out.TokenMatrix = map[string][]Hit{}, map[string][]Hit{}, map[string][]Hit{}
	for _, query := range frozen.Queries {
		allowedKeys, ok := candidates[query.QueryID]
		if !ok {
			continue // the scorer pins one H10.b split, not every encoded query
		}
		revisions := make([]string, 0, len(allowedKeys))
		seen := map[string]bool{}
		for _, key := range allowedKeys {
			id, exists := keyToRevision[key]
			if !exists || seen[id] {
				return out, fmt.Errorf("%w: scorer candidate not in frozen chunks", ErrArtifact)
			}
			seen[id] = true
			revisions = append(revisions, id)
		}
		sort.Strings(revisions)
		d, err := denseService.Search(ctx, dense.Query{IndexRef: denseBuilt.Ref, ModuleID: m.ModuleID,
			ReleaseID: m.ReleaseID, Generation: 1, ValidRevisionIDs: revisions,
			Text: query.Text, TopK: len(m.Chunks)})
		if err != nil {
			return out, fmt.Errorf("search existing dense exact: %w", err)
		}
		out.Dense[query.QueryID], err = mapDense(d.Candidates)
		if err != nil {
			return out, err
		}
		s, err := sparseService.Search(ctx, sparse.Query{IndexRef: sparseBuilt.Ref, ModuleID: m.ModuleID,
			ReleaseID: m.ReleaseID, Generation: 1, ValidRevisionIDs: revisions,
			Text: query.Text, TopK: len(m.Chunks)})
		if err != nil {
			return out, fmt.Errorf("search existing sparse inverted: %w", err)
		}
		out.Sparse[query.QueryID], err = mapSparse(s.Candidates)
		if err != nil {
			return out, err
		}
		mv, err := multiService.Search(ctx, multivector.Query{IndexRef: multiBuilt.Ref, ModuleID: m.ModuleID,
			ReleaseID: m.ReleaseID, Generation: 1, ValidRevisionIDs: revisions,
			Text: query.Text, TopK: len(m.Chunks), TokenTopK: out.FullTokenRows})
		if err != nil {
			return out, fmt.Errorf("search existing multivector exact: %w", err)
		}
		out.TokenMatrix[query.QueryID], err = mapMulti(mv.Candidates)
		if err != nil {
			return out, err
		}
	}
	if len(out.Dense) != len(candidates) {
		return out, ErrArtifact
	}
	out.ChunkCount, out.QueryCount = len(m.Chunks), len(candidates)
	return out, nil
}
