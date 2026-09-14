package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

// realIndexFixture is an opt-in cross-repository acceptance contract. Its
// runtime file is created by DataCenter's real BGE-M3 control-plane test and
// contains only disposable local credentials. It must remain outside Git.
type realIndexFixture struct {
	DCRuntime       string     `json:"dc_runtime"`
	ArtifactDir     string     `json:"artifact_dir"`
	ChunkManifest   corpus.Ref `json:"chunk_manifest"`
	BuildID         string     `json:"build_id"`
	ReleaseID       string     `json:"release_id"`
	Generation      int64      `json:"generation"`
	ResultPath      string     `json:"result_path"`
	ExpectedQuote   string     `json:"expected_quote"`
	ExpectedChunkID string     `json:"expected_chunk_id"`
}

type realIndexResult struct {
	IndexManifest corpus.Ref            `json:"index_manifest"`
	Indexes       map[string]corpus.Ref `json:"indexes"`
	ChunkCount    int                   `json:"chunk_count"`
}

type realIndexLanes struct {
	dense  *dense.Service
	sparse *sparse.Service
	multi  *multivector.Service
	result realIndexResult
}

type realDCRuntime struct {
	Endpoint       string `json:"endpoint"`
	Token          string `json:"access_token"`
	Configurations map[string]struct {
		Callpoint       string `json:"callpoint"`
		ConfigurationID string `json:"configuration_id"`
		Profile         struct {
			Model    string                  `json:"model"`
			Space    string                  `json:"representation_space"`
			Contract representation.Contract `json:"representation_contract"`
		} `json:"profile"`
	} `json:"configurations"`
}

func buildRTWRealThreeLane(ctx context.Context, fixture realIndexFixture) (realIndexLanes, error) {
	if fixture.DCRuntime == "" || fixture.ArtifactDir == "" || fixture.BuildID == "" || fixture.ReleaseID == "" ||
		fixture.Generation < 1 || fixture.ResultPath == "" || fixture.ExpectedQuote == "" || fixture.ExpectedChunkID == "" {
		return realIndexLanes{}, errors.New("incomplete actual three-lane build fixture")
	}
	raw, err := os.ReadFile(fixture.DCRuntime)
	if err != nil {
		return realIndexLanes{}, err
	}
	var runtime realDCRuntime
	if err := json.Unmarshal(raw, &runtime); err != nil || runtime.Endpoint == "" || runtime.Token == "" || len(runtime.Configurations) != 3 {
		return realIndexLanes{}, errors.New("incomplete disposable DataCenter BGE runtime")
	}
	dc, err := datacenter.New(httpclient.Config{BaseURL: runtime.Endpoint, Token: runtime.Token})
	if err != nil {
		return realIndexLanes{}, err
	}
	representations, err := datacenter.NewRepresentationGate(dc, 1) // locked local BGE provider accepts one in-flight call
	if err != nil {
		return realIndexLanes{}, err
	}
	objects, err := artifacts.NewLocal(fixture.ArtifactDir)
	if err != nil {
		return realIndexLanes{}, err
	}
	manifestRaw, err := objects.Get(ctx, fixture.ChunkManifest)
	if err != nil {
		return realIndexLanes{}, err
	}
	var chunks corpus.ChunkManifest
	if err := representation.Decode(manifestRaw, &chunks); err != nil || chunks.ReleaseID != fixture.ReleaseID || len(chunks.Chunks) < 2 {
		return realIndexLanes{}, errors.New("RTW fixed chunk manifest differs from build fixture")
	}
	var expected bool
	for _, chunk := range chunks.Chunks {
		if chunk.ID == fixture.ExpectedChunkID && chunk.Text == fixture.ExpectedQuote {
			expected = true
		}
	}
	if !expected {
		return realIndexLanes{}, errors.New("expected product source absent from RTW fixed chunks")
	}
	makeEncoding := func(kind string) (string, string, string, representation.Contract, string, error) {
		wire, ok := runtime.Configurations[kind]
		if !ok || wire.Callpoint == "" || wire.ConfigurationID == "" || wire.Profile.Model == "" ||
			wire.Profile.Space == "" || wire.Profile.Contract.Kind != representation.Kind(kind) {
			return "", "", "", representation.Contract{}, "", fmt.Errorf("incomplete DC %s configuration", kind)
		}
		return wire.Callpoint, wire.ConfigurationID, wire.Profile.Model, wire.Profile.Contract, wire.Profile.Space, nil
	}
	dCall, dID, dModel, dContract, dSpace, err := makeEncoding("dense")
	if err != nil {
		return realIndexLanes{}, err
	}
	sCall, sID, sModel, sContract, sSpace, err := makeEncoding("sparse")
	if err != nil {
		return realIndexLanes{}, err
	}
	mCall, mID, mModel, mContract, mSpace, err := makeEncoding("token_matrix")
	if err != nil {
		return realIndexLanes{}, err
	}
	dConfig := dense.Config{Document: dense.Encoding{Callpoint: dCall, ConfigurationID: dID, PhysicalModel: dModel},
		Query:    dense.Encoding{Callpoint: dCall, ConfigurationID: dID, PhysicalModel: dModel},
		Contract: dContract, Space: dSpace, BatchSize: 2, ProbeTopK: 2}
	sConfig := sparse.Config{Document: sparse.Encoding{Callpoint: sCall, ConfigurationID: sID, PhysicalModel: sModel},
		Query:    sparse.Encoding{Callpoint: sCall, ConfigurationID: sID, PhysicalModel: sModel},
		Contract: sContract, Space: sSpace, BatchSize: 2, ProbeTopK: 2}
	mConfig := multivector.Config{Document: multivector.Encoding{Callpoint: mCall, ConfigurationID: mID, PhysicalModel: mModel},
		Query:    multivector.Encoding{Callpoint: mCall, ConfigurationID: mID, PhysicalModel: mModel},
		Contract: mContract, Space: mSpace, BatchSize: 2, TokenTopK: 8, ProbeTopK: 2}
	dLane, err := dense.New(objects, representations, dConfig)
	if err != nil {
		return realIndexLanes{}, err
	}
	sLane, err := sparse.New(objects, representations, sConfig)
	if err != nil {
		return realIndexLanes{}, err
	}
	mLane, err := multivector.New(objects, representations, mConfig)
	if err != nil {
		return realIndexLanes{}, err
	}
	profiles := map[string]corpus.Profile{
		"dense":  {Lane: "dense", Encoder: dModel, Tokenizer: dContract.TokenizerID, Space: dSpace, Dimensions: dContract.Dimensions},
		"sparse": {Lane: "sparse", Encoder: sModel, Tokenizer: sContract.TokenizerID, Space: sSpace, Dimensions: sContract.Dimensions},
		"multivector": {Lane: "multivector", Encoder: mModel, Tokenizer: mContract.TokenizerID, Space: mSpace,
			Dimensions: mContract.Dimensions, Mask: "valid", Aggregation: mContract.Aggregation},
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	index := corpus.IndexManifest{SchemaVersion: 1, BuildID: fixture.BuildID, ReleaseID: fixture.ReleaseID,
		Generation: fixture.Generation, InputManifestHash: chunks.InputManifestHash, ChunkManifest: fixture.ChunkManifest,
		ChunkCount: int64(len(chunks.Chunks))}
	refs := make(map[string]corpus.Ref, 3)
	for _, lane := range []string{"dense", "sparse", "multivector"} {
		var built corpus.LaneIndex
		var ref corpus.Ref
		var probes []corpus.ProbeResult
		switch lane {
		case "dense":
			b, e := dLane.Build(deadline, dense.BuildRequest{BuildID: fixture.BuildID, Generation: fixture.Generation,
				ChunkManifest: fixture.ChunkManifest, Profile: profiles[lane]})
			if e != nil {
				return realIndexLanes{}, fmt.Errorf("dense build: %w", e)
			}
			built, ref = b.Index, b.Ref
			probes, err = dLane.VerifyAndProbe(deadline, built, chunks, chunks.Chunks)
		case "sparse":
			b, e := sLane.Build(deadline, sparse.BuildRequest{BuildID: fixture.BuildID, Generation: fixture.Generation,
				ChunkManifest: fixture.ChunkManifest, Profile: profiles[lane]})
			if e != nil {
				return realIndexLanes{}, fmt.Errorf("sparse build: %w", e)
			}
			built, ref = b.Index, b.Ref
			probes, err = sLane.VerifyAndProbe(deadline, built, chunks, chunks.Chunks)
		case "multivector":
			b, e := mLane.Build(deadline, multivector.BuildRequest{BuildID: fixture.BuildID, Generation: fixture.Generation,
				ChunkManifest: fixture.ChunkManifest, Profile: profiles[lane]})
			if e != nil {
				return realIndexLanes{}, fmt.Errorf("multivector build: %w", e)
			}
			built, ref = b.Index, b.Ref
			probes, err = mLane.VerifyAndProbe(deadline, built, chunks, chunks.Chunks)
		}
		if err != nil {
			return realIndexLanes{}, fmt.Errorf("%s probe: %w", lane, err)
		}
		if len(probes) != len(chunks.Chunks) || len(built.Shards) == 0 {
			return realIndexLanes{}, fmt.Errorf("%s incomplete probe or shards", lane)
		}
		for i, probe := range probes {
			if probe.QueryChunkID != chunks.Chunks[i].ID || len(probe.CandidateIDs) == 0 || probe.CandidateIDs[0] != chunks.Chunks[i].ID {
				return realIndexLanes{}, fmt.Errorf("%s self probe mismatch for %s", lane, chunks.Chunks[i].ID)
			}
		}
		probeRef, e := putRealIndex(deadline, objects, corpus.LaneProbe{SchemaVersion: 1, Lane: lane,
			Index: ref, ChunkManifest: fixture.ChunkManifest, Results: probes})
		if e != nil {
			return realIndexLanes{}, e
		}
		index.Lanes = append(index.Lanes, corpus.LaneManifest{Profile: profiles[lane], Artifact: ref,
			ChunkCount: int64(len(chunks.Chunks)), Shards: len(built.Shards), ProbePassed: true, Probe: probeRef})
		refs[lane] = ref
	}
	indexRef, err := putRealIndex(deadline, objects, index)
	if err != nil {
		return realIndexLanes{}, err
	}
	result := realIndexResult{IndexManifest: indexRef, Indexes: refs, ChunkCount: len(chunks.Chunks)}
	raw, err = json.Marshal(result)
	if err != nil {
		return realIndexLanes{}, err
	}
	if err = os.WriteFile(fixture.ResultPath, raw, 0600); err != nil {
		return realIndexLanes{}, err
	}
	return realIndexLanes{dense: dLane, sparse: sLane, multi: mLane, result: result}, nil
}

func putRealIndex(ctx context.Context, objects artifacts.Store, value any) (corpus.Ref, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return corpus.Ref{}, err
	}
	return objects.Put(ctx, raw)
}
