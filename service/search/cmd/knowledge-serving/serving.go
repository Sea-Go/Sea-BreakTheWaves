// 知识模块检索 serving:DC 三路索引快照(打开/查询)+ RRF 融合 + 证据输出。
// 数据流:ridethewind GetCurrentSearchSnapshot(RTW 权威指针)→ 三路 LaneIndex Open
// → Snapshot.Search → RRF 融合 → Evidence(chunk 正文+locator+revision/release)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

// indexSettings 与 cmd/worker 的索引配置文件同构(/etc/sea-btw/search-index-config.json)。
type indexSettings struct {
	Dense       dense.Config       `json:"dense"`
	Sparse      sparse.Config      `json:"sparse"`
	MultiVector multivector.Config `json:"multivector"`
}

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

// Searcher 持有三路 lane 服务;快照每次调用现取(RTW 指针为权威,
// 不缓存 LaneIndex 以避免切版本后读到旧索引)。
type Searcher struct {
	rtw     *ridethewind.Client
	dense   *dense.Service
	sparse  *sparse.Service
	multi   *multivector.Service
	maxTopK int
}

func NewSearcher(rtwBase, rtwToken, dcBase, dcToken, artifactDir, indexConfigFile string, maxTopK int) (*Searcher, error) {
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
	var settings indexSettings
	raw, err := os.ReadFile(indexConfigFile)
	if err != nil {
		return nil, fmt.Errorf("read index settings: %w", err)
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("decode index settings: %w", err)
	}
	denseLane, err := dense.New(objects, dc, settings.Dense)
	if err != nil {
		return nil, fmt.Errorf("construct dense lane: %w", err)
	}
	sparseLane, err := sparse.New(objects, dc, settings.Sparse)
	if err != nil {
		return nil, fmt.Errorf("construct sparse lane: %w", err)
	}
	multiLane, err := multivector.New(objects, dc, settings.MultiVector)
	if err != nil {
		return nil, fmt.Errorf("construct multivector lane: %w", err)
	}
	return &Searcher{rtw: rtw, dense: denseLane, sparse: sparseLane, multi: multiLane, maxTopK: maxTopK}, nil
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
		_, err := s.dense.Open(ctx, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("open dense lane: %w", err)
		}
		res, err := s.dense.Search(ctx, dense.Query{IndexRef: laneRef, ModuleID: moduleID,
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
		_, err := s.sparse.Open(ctx, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("open sparse lane: %w", err)
		}
		res, err := s.sparse.Search(ctx, sparse.Query{IndexRef: laneRef, ModuleID: moduleID,
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
		_, err := s.multi.Open(ctx, laneRef)
		if err != nil {
			return nil, "", fmt.Errorf("open multivector lane: %w", err)
		}
		res, err := s.multi.Search(ctx, multivector.Query{IndexRef: laneRef, ModuleID: moduleID,
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
