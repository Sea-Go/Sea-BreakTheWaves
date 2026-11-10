// 知识模块检索 serving:DC 三路索引快照(打开/查询)+ RRF 融合 + 证据输出。
// 数据流:ridethewind GetCurrentSearchSnapshot(RTW 权威指针)→ 三路 LaneIndex Open
// → Snapshot.Search → RRF 融合 → Evidence(chunk 正文+locator+revision/release)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/dense"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/multivector"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/rrf"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/sparse"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
)

// Evidence 是一次检索的最终证据项:chunk 正文 + 稳定定位 + 版本身份。
type Evidence struct {
	ChunkID    string          `json:"chunk_id"`
	Locator    string          `json:"locator"`
	RevisionID string          `json:"revision_id"`
	ReleaseID  string          `json:"release_id"`
	Text       string          `json:"text"`
	Score      float64         `json:"score"`
	Lanes      []string        `json:"lanes"`
	Location   corpus.Location `json:"location"`
}

// candPair 跨路归一的候选(chunk 即定位与正文载体)。
type candPair struct {
	chunk corpus.Chunk
}

// Searcher 从 LaneIndex+shard 工件的自描述配置构建 lane 服务(请求时),
// 不依赖静态索引配置文件:DC 的 configuration_id 按注册动态签发,
// 静态文件必然过期(实测 bae3c088→598c09bd 漂移导致 Open 全拒)。
type Searcher struct {
	rtw     *ridethewind.Client
	dc      *datacenter.Client
	objects artifacts.Store
	maxTopK int
}

func NewSearcher(rtwBase, rtwToken, dcBase, dcToken, artifactDir string, maxTopK int) (*Searcher, error) {
	rtw, err := ridethewind.New(httpclient.Config{
		BaseURL: rtwBase, Token: rtwToken,
		HTTPClient:       &http.Client{Timeout: 20 * time.Second},
		MaxResponseBytes: 8 << 20,
	})
	if err != nil {
		return nil, fmt.Errorf("construct ridethewind client: %w", err)
	}
	dc, err := datacenter.New(httpclient.Config{
		BaseURL: dcBase, Token: dcToken,
		HTTPClient:       &http.Client{Timeout: 60 * time.Second},
		MaxResponseBytes: 16 << 20,
	})
	if err != nil {
		return nil, fmt.Errorf("construct datacenter client: %w", err)
	}
	objects, err := artifacts.NewLocal(artifactDir)
	if err != nil {
		return nil, fmt.Errorf("open artifact dir: %w", err)
	}
	return &Searcher{rtw: rtw, dc: dc, objects: objects, maxTopK: maxTopK}, nil
}

// loadLaneConfig 从 LaneIndex 的第一个 shard 工件读出构建时的 lane Config
// (含 DC 当次签发的 configuration_id)——索引工件自描述。
func loadLaneConfig[T any](ctx context.Context, objects artifacts.Store, ref corpus.Ref) (T, error) {
	var cfg T
	idxRaw, err := objects.Get(ctx, ref)
	if err != nil {
		return cfg, fmt.Errorf("load lane index: %w", err)
	}
	var idx corpus.LaneIndex
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		return cfg, fmt.Errorf("decode lane index: %w", err)
	}
	if len(idx.Shards) == 0 {
		return cfg, fmt.Errorf("lane index 无 shard")
	}
	shardRaw, err := objects.Get(ctx, idx.Shards[0].Artifact)
	if err != nil {
		return cfg, fmt.Errorf("load shard artifact: %w", err)
	}
	var shard struct {
		Config T `json:"config"`
	}
	if err := json.Unmarshal(shardRaw, &shard); err != nil {
		return cfg, fmt.Errorf("decode shard config: %w", err)
	}
	return shard.Config, nil
}

// SearchEvidence 执行三路检索并 RRF 融合;topK 由服务端钳制。
func (s *Searcher) SearchEvidence(ctx context.Context, moduleID, query string, topK int) ([]Evidence, string, error) {
	if strings.TrimSpace(moduleID) == "" || strings.TrimSpace(query) == "" {
		return nil, "", fmt.Errorf("module_id 与 query 均必填")
	}
	if topK <= 0 || topK > s.maxTopK {
		topK = s.maxTopK
	}
	snapshot, err := s.rtw.GetCurrentSearchSnapshot(ctx, moduleID)
	if err != nil {
		return nil, "", fmt.Errorf("read current search snapshot: %w", err)
	}

	best := map[string]corpus.Chunk{}
	lanesOf := map[string][]string{}
	perLane := make([][]rrf.Entry, 0, 3)
	addLane := func(name string, pairs []candPair) {
		entries := make([]rrf.Entry, 0, len(pairs))
		for rank, p := range pairs {
			entries = append(entries, rrf.Entry{Key: p.chunk.ID, Score: float64(rank + 1)})
			if _, seen := best[p.chunk.ID]; !seen {
				best[p.chunk.ID] = p.chunk
			}
			lanesOf[p.chunk.ID] = append(lanesOf[p.chunk.ID], name)
		}
		perLane = append(perLane, entries)
	}

	if ref, ok := snapshot.Indexes["dense"]; ok {
		laneRef := corpus.Ref{Key: ref.Key, SHA256: ref.Sha256}
		cfg, err := loadLaneConfig[dense.Config](ctx, s.objects, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("dense config: %w", err)
		}
		svc, err := dense.New(s.objects, s.dc, cfg)
		if err != nil {
			return nil, "", fmt.Errorf("construct dense lane: %w", err)
		}
		res, err := svc.Search(ctx, dense.Query{IndexRef: laneRef, ModuleID: moduleID,
			ReleaseID: snapshot.ReleaseId, Generation: snapshot.Generation,
			ValidRevisionIDs: snapshot.ValidRevisionIds, Text: query, TopK: topK})
		if err != nil {
			return nil, "", fmt.Errorf("search dense lane: %w", err)
		}
		pairs := make([]candPair, 0, len(res.Candidates))
		for _, c := range res.Candidates {
			pairs = append(pairs, candPair{chunk: c.Chunk})
		}
		addLane("dense", pairs)
	}
	if ref, ok := snapshot.Indexes["sparse"]; ok {
		laneRef := corpus.Ref{Key: ref.Key, SHA256: ref.Sha256}
		cfg, err := loadLaneConfig[sparse.Config](ctx, s.objects, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("sparse config: %w", err)
		}
		svc, err := sparse.New(s.objects, s.dc, cfg)
		if err != nil {
			return nil, "", fmt.Errorf("construct sparse lane: %w", err)
		}
		res, err := svc.Search(ctx, sparse.Query{IndexRef: laneRef, ModuleID: moduleID,
			ReleaseID: snapshot.ReleaseId, Generation: snapshot.Generation,
			ValidRevisionIDs: snapshot.ValidRevisionIds, Text: query, TopK: topK})
		if err != nil {
			return nil, "", fmt.Errorf("search sparse lane: %w", err)
		}
		pairs := make([]candPair, 0, len(res.Candidates))
		for _, c := range res.Candidates {
			pairs = append(pairs, candPair{chunk: c.Chunk})
		}
		addLane("sparse", pairs)
	}
	if ref, ok := snapshot.Indexes["multivector"]; ok {
		laneRef := corpus.Ref{Key: ref.Key, SHA256: ref.Sha256}
		cfg, err := loadLaneConfig[multivector.Config](ctx, s.objects, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("multivector config: %w", err)
		}
		svc, err := multivector.New(s.objects, s.dc, cfg)
		if err != nil {
			return nil, "", fmt.Errorf("construct multivector lane: %w", err)
		}
		res, err := svc.Search(ctx, multivector.Query{IndexRef: laneRef, ModuleID: moduleID,
			ReleaseID: snapshot.ReleaseId, Generation: snapshot.Generation,
			ValidRevisionIDs: snapshot.ValidRevisionIds, Text: query, TopK: topK})
		if err != nil {
			return nil, "", fmt.Errorf("search multivector lane: %w", err)
		}
		pairs := make([]candPair, 0, len(res.Candidates))
		for _, c := range res.Candidates {
			pairs = append(pairs, candPair{chunk: c.Chunk})
		}
		addLane("multivector", pairs)
	}
	if len(perLane) == 0 {
		return nil, snapshot.ReleaseId, fmt.Errorf("当前发布版本没有任何可用索引")
	}

	fused := rrf.RRF(perLane, 60)
	out := make([]Evidence, 0, topK)
	for _, f := range fused {
		chunk, ok := best[f.Key]
		if !ok {
			continue
		}
		out = append(out, Evidence{
			ChunkID: chunk.ID, Locator: chunk.Location.Locator,
			RevisionID: chunk.RevisionID, ReleaseID: snapshot.ReleaseId,
			Text: chunk.Text, Score: f.Score, Lanes: lanesOf[f.Key],
			Location: chunk.Location,
		})
		if len(out) >= topK {
			break
		}
	}
	return out, snapshot.ReleaseId, nil
}
