package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
)

func fixedIndexSettings() (indexSettings, []ridethewind.RetrievalProfile) {
	const physicalModel = "fixture_model"
	const tokenizer = "tokens_v1"
	encoding := func(callpoint, configurationID string) dense.Encoding {
		return dense.Encoding{Callpoint: callpoint, ConfigurationID: configurationID, PhysicalModel: physicalModel}
	}
	settings := indexSettings{
		Dense: dense.Config{Document: encoding("dense_document", "11111111-1111-4111-8111-111111111111"),
			Query: encoding("dense_query", "22222222-2222-4222-8222-222222222222"),
			Contract: representation.Contract{ID: "dense_v1", Kind: representation.Dense, Dimensions: 2,
				TokenizerID: tokenizer, Normalization: "none", Metric: "dot", Aggregation: "none"},
			Space: "dense_space", BatchSize: 8, ProbeTopK: 2},
		Sparse: sparse.Config{Document: sparse.Encoding{Callpoint: "sparse_document", ConfigurationID: "33333333-3333-4333-8333-333333333333", PhysicalModel: physicalModel},
			Query: sparse.Encoding{Callpoint: "sparse_query", ConfigurationID: "44444444-4444-4444-8444-444444444444", PhysicalModel: physicalModel},
			Contract: representation.Contract{ID: "sparse_v1", Kind: representation.Sparse, Dimensions: 100,
				TokenizerID: tokenizer, VocabularyID: "vocab_v1", Normalization: "none", Metric: "dot", Aggregation: "sum", MaxNonzero: 2},
			Space: "sparse_space", BatchSize: 8, ProbeTopK: 2},
		MultiVector: multivector.Config{Document: multivector.Encoding{Callpoint: "multi_document", ConfigurationID: "55555555-5555-4555-8555-555555555555", PhysicalModel: physicalModel},
			Query: multivector.Encoding{Callpoint: "multi_query", ConfigurationID: "66666666-6666-4666-8666-666666666666", PhysicalModel: physicalModel},
			Contract: representation.Contract{ID: "multi_v1", Kind: representation.TokenMatrix, Dimensions: 2,
				TokenizerID: tokenizer, Normalization: "none", Metric: "maxsim", Aggregation: "sum_maxsim", MaxTokens: 4},
			Space: "multi_space", BatchSize: 8, TokenTopK: 4, ProbeTopK: 2},
	}
	profiles := []ridethewind.RetrievalProfile{
		{Lane: "dense", Encoder: physicalModel, Tokenizer: tokenizer, Space: settings.Dense.Space, Dimensions: 2},
		{Lane: "sparse", Encoder: physicalModel, Tokenizer: tokenizer, Space: settings.Sparse.Space, Dimensions: 100},
		{Lane: "multivector", Encoder: physicalModel, Tokenizer: tokenizer, Space: settings.MultiVector.Space,
			Dimensions: 2, Mask: "valid", Aggregation: "sum_maxsim"},
	}
	return settings, profiles
}

func TestReadIndexSettingsRejectsUnknownAndTrailingFields(t *testing.T) {
	settings, _ := fixedIndexSettings()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.json")
	for _, tc := range []struct {
		name string
		raw  []byte
		ok   bool
	}{
		{"valid", raw, true},
		{"trailing", append(append([]byte(nil), raw...), []byte(` {}`)...), false},
		{"unknown", append([]byte(`{"unknown":true,`), raw[1:]...), false},
		{"oversized", make([]byte, 64*1024+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readIndexSettings(path)
			if (err == nil) != tc.ok {
				t.Fatalf("settings validity=%v err=%v", tc.ok, err)
			}
		})
	}
}
