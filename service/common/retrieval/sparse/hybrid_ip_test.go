package sparse

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
)

// Lite 3.2.1's sparse inverted TopK uses BM25 and ranks B before A for this
// length trap. The frozen learned-weight postings must enumerate the actual
// query intersection and rank by IP before TopK, including after a fresh Load.
type hybridTrapEncoder struct{}

func (hybridTrapEncoder) Represent(ctx context.Context, req representation.Request,
	contract representation.Contract, model string) (representation.Response, error) {
	if err := ctx.Err(); err != nil {
		return representation.Response{}, err
	}
	values := map[string]representation.SparseValues{
		"A-long":     {Indices: []int{1, 2}, Weights: []float64{10, 100}},
		"B-short":    {Indices: []int{1}, Weights: []float64{9}},
		"trap-query": {Indices: []int{1}, Weights: []float64{1}},
	}
	response := representation.Response{Model: model, ConfigurationID: req.ConfigurationID,
		OutputContract: req.OutputContract, ContractID: req.ContractID, Space: req.Space,
		Role: req.Role, TokenizerID: contract.TokenizerID,
		VocabularyID: contract.VocabularyID,
		Usage: &representation.Usage{PromptTokens: int64(len(req.Input)),
			TotalTokens: int64(len(req.Input))}}
	for i := len(req.Input) - 1; i >= 0; i-- {
		input := req.Input[i]
		vector, ok := values[input.Text]
		if !ok {
			return representation.Response{}, ErrInvalid
		}
		response.Data = append(response.Data, representation.Item{ID: input.ID, Sparse: &vector})
	}
	return response, nil
}

func TestFrozenLearnedPostingIPKeepsTrueTopKBeforeBM25LengthTrap(t *testing.T) {
	objects, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfig()
	manifest := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: "module-trap",
		ReleaseID: "release-trap", InputManifestHash: artifacts.Hash([]byte("trap-input")),
		Profile: "trap-chunk-v1", ParserVersion: "sea.paragraph.v1",
		ChunkerVersion: "trpc.fixed.v1.8.1", ChunkSize: 512, Overlap: 0}
	for _, row := range []struct{ id, text string }{{"A", "A-long"}, {"B", "B-short"}} {
		original := artifacts.Reference([]byte(row.text))
		encoding, err := json.Marshal([]any{manifest.Profile, row.text})
		if err != nil {
			t.Fatal(err)
		}
		manifest.Inputs = append(manifest.Inputs, corpus.Input{RevisionID: "r" + row.id,
			ContentID: row.id, SourceKind: "source", Original: original, ChunkCount: 1})
		manifest.Chunks = append(manifest.Chunks, corpus.Chunk{ID: row.id,
			RevisionID: "r" + row.id, ContentID: row.id, SourceKind: "source",
			Original: original, Location: corpus.Location{Locator: "paragraph:1",
				OriginalByteEnd: len(row.text), NormalizedRuneEnd: len(row.text)},
			Text: row.text, TextHash: artifacts.Hash([]byte(row.text)),
			EncodingKey: artifacts.Hash(encoding), Required: true})
	}
	ref, err := put(context.Background(), objects, manifest)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(objects, hybridTrapEncoder{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	built, err := service.Build(context.Background(), BuildRequest{BuildID: "trap-build",
		Generation: 1, ChunkManifest: ref, Profile: profile(cfg)})
	if err != nil || len(built.Index.Shards) == 0 {
		t.Fatalf("frozen learned sparse posting Build failed: %+v %v", built, err)
	}
	loaded, err := service.Load(context.Background(), built.Ref)
	if err != nil {
		t.Fatalf("published sparse shard/Posting Load failed: %v", err)
	}
	query := Query{IndexRef: built.Ref, ModuleID: manifest.ModuleID,
		ReleaseID: manifest.ReleaseID, Generation: 1,
		ValidRevisionIDs: []string{"rA", "rB"}, Text: "trap-query", TopK: 1}
	first, err := loaded.Search(context.Background(), query)
	if err != nil || len(first.Candidates) != 1 ||
		first.Candidates[0].Chunk.ID != "A" || first.Candidates[0].Score != 10 ||
		first.ExaminedPostings == nil || *first.ExaminedPostings != 2 {
		t.Fatalf("learned IP lost A before TopK or scanned unrelated token2: %+v %v", first, err)
	}
	query.TopK = 2
	full, err := loaded.Search(context.Background(), query)
	if err != nil || len(full.Candidates) != 2 ||
		full.Candidates[0].Chunk.ID != "A" || full.Candidates[0].Score != 10 ||
		full.Candidates[1].Chunk.ID != "B" || full.Candidates[1].Score != 9 {
		t.Fatalf("frozen postings changed true learned-IP order on reload: %+v %v", full, err)
	}
}
