package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/retrieval/sparse"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

const realNativeOwner = "Search native three-lane isolated acceptance only"

type realNativeRuntime struct {
	SchemaVersion string `json:"schema_version"`
	Owner         string `json:"owner"`
	Endpoint      string `json:"endpoint"`
	Engine        string `json:"engine"`
	MilvusLite    string `json:"milvus_lite"`
	BinarySHA256  string `json:"binary_sha256"`
	Directory     string `json:"directory"`
	ReleasePath   string `json:"release_path"`
}

type realNativeHNSW struct {
	M              int `json:"m"`
	EFConstruction int `json:"ef_construction"`
	EFSearch       int `json:"ef_search"`
}

type realNativeSettings struct {
	SchemaVersion string         `json:"schema_version"`
	Engine        string         `json:"engine"`
	Namespace     string         `json:"namespace"`
	Dense         realNativeHNSW `json:"dense"`
	MultiVector   realNativeHNSW `json:"multivector"`
}

// The native result is test-only and does not expand RTW's signed IndexManifest
// v1. Production content workers still need an authority-bound physical config
// receipt before READY; this isolated projection cannot certify that rollout.
type realNativeProjection struct {
	Status             string          `json:"status"`
	Endpoint           string          `json:"endpoint"`
	RuntimeSHA256      string          `json:"runtime_sha256"`
	EngineBinarySHA256 string          `json:"engine_binary_sha256"`
	Settings           json.RawMessage `json:"settings"`
	PhysicalQualified  bool            `json:"physical_qualified"`
}

type realNativeProjector struct {
	client  *milvusclient.Client
	dense   *dense.Service
	sparse  *sparse.Service
	multi   *multivector.Service
	runtime realNativeRuntime
	setting realNativeSettings
	rawSHA  string
	once    sync.Once
	stopErr error
}

func readRealNativeRuntime(path string) (realNativeRuntime, string, error) {
	var runtime realNativeRuntime
	if !filepath.IsAbs(path) {
		return runtime, "", errors.New("native runtime path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return runtime, "", errors.New("native runtime must be a private ordinary file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 || len(raw) > 4096 || json.Unmarshal(raw, &runtime) != nil {
		return realNativeRuntime{}, "", errors.New("native runtime JSON is unavailable")
	}
	host, port, err := net.SplitHostPort(runtime.Endpoint)
	if err != nil || host != "127.0.0.1" || port == "" ||
		runtime.SchemaVersion != "sea.search.native-lite-runtime.v1" ||
		runtime.Owner != realNativeOwner || runtime.Engine != "lite" ||
		runtime.MilvusLite != "3.2.1" || !artifacts.ValidHash(runtime.BinarySHA256) ||
		!filepath.IsAbs(runtime.Directory) ||
		runtime.ReleasePath != filepath.Join(runtime.Directory, "release") ||
		filepath.Dir(path) != runtime.Directory {
		return realNativeRuntime{}, "", errors.New("native runtime owner/version/binary/scope differs")
	}
	return runtime, artifacts.Hash(raw), nil
}

func newRealNativeProjector(ctx context.Context, runtimePath, buildID string,
	objects artifacts.Store, encoder *datacenter.RepresentationGate,
	dConfig dense.Config, sConfig sparse.Config,
	mConfig multivector.Config) (*realNativeProjector, error) {
	runtime, runtimeSHA, err := readRealNativeRuntime(runtimePath)
	if err != nil || objects == nil || encoder == nil || buildID == "" {
		return nil, errors.New("test native projection requires the fixed runtime, build and typed DC gate")
	}
	setting := realNativeSettings{SchemaVersion: "sea.search.native-milvus.v1",
		Engine: "lite", Namespace: "native_" + artifacts.Hash([]byte(buildID))[:12],
		Dense:       realNativeHNSW{M: 16, EFConstruction: 128, EFSearch: 64},
		MultiVector: realNativeHNSW{M: 16, EFConstruction: 128, EFSearch: 64}}
	client, err := milvusclient.New(ctx, &milvusclient.ClientConfig{Address: runtime.Endpoint})
	if err != nil {
		return nil, fmt.Errorf("connect isolated native Lite: %w", err)
	}
	closeClient := func() { _ = client.Close(context.Background()) }
	dLane, err := dense.NewMilvus(objects, encoder, dConfig, client,
		dense.MilvusConfig{Namespace: setting.Namespace, Engine: "lite",
			M: setting.Dense.M, EFConstruction: setting.Dense.EFConstruction,
			EFSearch: setting.Dense.EFSearch})
	if err != nil {
		closeClient()
		return nil, err
	}
	sLane, err := sparse.NewMilvus(objects, encoder, sConfig, client,
		sparse.MilvusConfig{Namespace: setting.Namespace, Engine: "native-lite"})
	if err != nil {
		closeClient()
		return nil, err
	}
	mLane, err := multivector.NewMilvus(objects, encoder, mConfig, client,
		multivector.MilvusConfig{Namespace: setting.Namespace, Engine: "lite",
			M:              setting.MultiVector.M,
			EFConstruction: setting.MultiVector.EFConstruction,
			EFSearch:       setting.MultiVector.EFSearch})
	if err != nil {
		closeClient()
		return nil, err
	}
	return &realNativeProjector{client: client, dense: dLane, sparse: sLane,
		multi: mLane, runtime: runtime, setting: setting, rawSHA: runtimeSHA}, nil
}

func (p *realNativeProjector) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p.stopErr = p.client.Close(ctx)
	})
	return p.stopErr
}

func (p *realNativeProjector) Receipt() *realNativeProjection {
	if p == nil {
		return nil
	}
	raw, _ := json.Marshal(p.setting)
	return &realNativeProjection{Status: "test_projection_before_RTW_READY",
		Endpoint: p.runtime.Endpoint, RuntimeSHA256: p.rawSHA,
		EngineBinarySHA256: p.runtime.BinarySHA256, Settings: raw,
		PhysicalQualified: false}
}

func (p *realNativeProjector) Project(ctx context.Context, lane string, exactRef corpus.Ref,
	buildID string, generation int64, chunkRef corpus.Ref, profile corpus.Profile,
	chunks corpus.ChunkManifest) (corpus.LaneIndex, corpus.Ref, []corpus.ProbeResult, error) {
	var empty corpus.LaneIndex
	if p == nil || ctx.Err() != nil || !artifacts.ValidHash(exactRef.SHA256) ||
		exactRef.Key != "sha256/"+exactRef.SHA256 || len(chunks.Chunks) < 4 {
		return empty, corpus.Ref{}, nil, errors.New("native test projection requires four fixed released chunks")
	}
	switch lane {
	case "dense":
		built, err := p.dense.Build(ctx, dense.BuildRequest{BuildID: buildID,
			Generation: generation, ChunkManifest: chunkRef, Profile: profile,
			ResumeIndex: &exactRef})
		if err != nil || built.Ref != exactRef || built.EncodedChunks != 0 ||
			built.Usage.TotalTokens != 0 {
			return empty, corpus.Ref{}, nil, errors.New("dense native projection changed immutable representation or reencoded")
		}
		probes, err := p.dense.VerifyAndProbe(ctx, built.Index, chunks, chunks.Chunks)
		if err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		if _, err := p.dense.Load(ctx, exactRef); err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		return built.Index, exactRef, probes, nil
	case "sparse":
		built, err := p.sparse.Build(ctx, sparse.BuildRequest{BuildID: buildID,
			Generation: generation, ChunkManifest: chunkRef, Profile: profile,
			ResumeIndex: &exactRef})
		if err != nil || built.Ref != exactRef || built.EncodedChunks != 0 ||
			built.Usage.TotalTokens != 0 {
			return empty, corpus.Ref{}, nil, errors.New("learned sparse native projection changed representation or reencoded")
		}
		probes, err := p.sparse.VerifyAndProbe(ctx, built.Index, chunks, chunks.Chunks)
		if err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		if _, err := p.sparse.Load(ctx, exactRef); err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		return built.Index, exactRef, probes, nil
	case "multivector":
		built, err := p.multi.Build(ctx, multivector.BuildRequest{BuildID: buildID,
			Generation: generation, ChunkManifest: chunkRef, Profile: profile,
			ResumeIndex: &exactRef})
		if err != nil || built.Ref != exactRef || built.EncodedChunks != 0 ||
			built.Usage.TotalTokens != 0 {
			return empty, corpus.Ref{}, nil, errors.New("token-matrix native projection changed representation or reencoded")
		}
		probes, err := p.multi.VerifyAndProbe(ctx, built.Index, chunks, chunks.Chunks)
		if err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		if _, err := p.multi.Load(ctx, exactRef); err != nil {
			return empty, corpus.Ref{}, nil, err
		}
		return built.Index, exactRef, probes, nil
	default:
		return empty, corpus.Ref{}, nil, errors.New("unknown native search lane")
	}
}
