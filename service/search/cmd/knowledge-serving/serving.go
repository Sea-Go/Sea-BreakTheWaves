// 知识模块检索 serving:DC 三路索引快照(打开/查询)+ RRF 融合 + 证据输出。
// 数据流:ridethewind GetCurrentSearchSnapshot(RTW 权威指针)→ 三路 LaneIndex Open
// → Snapshot.Search → RRF 融合 → Evidence(chunk 正文+locator+revision/release)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Searcher 每次检索时从自动刷新的索引配置文件读取当前 lane 配置:
// DC/BGE 运行时的 configuration_id 是会话句柄(~30 分钟轮换,文件由
// sea-bge-publish-config 周期重写),查询编码必须用当前句柄;
// 索引工件的语义校验(物理模型/契约/空间)由 lane 包的 semanticConfigEqual 放行。
type Searcher struct {
	rtw             *ridethewind.Client
	rtwBase         string
	rtwToken        string
	dc              *datacenter.Client
	objects         artifacts.Store
	maxTopK         int
	indexConfigFile string
}

// indexSettings 与 cmd/worker 的索引配置文件同构(/etc/sea-btw/search-index-config.json)。
type indexSettings struct {
	Dense       dense.Config       `json:"dense"`
	Sparse      sparse.Config      `json:"sparse"`
	MultiVector multivector.Config `json:"multivector"`
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
	if _, err := readIndexSettings(indexConfigFile); err != nil {
		return nil, err
	}
	return &Searcher{rtw: rtw, rtwBase: rtwBase, rtwToken: rtwToken, dc: dc, objects: objects, indexConfigFile: indexConfigFile, maxTopK: maxTopK}, nil
}

// readIndexSettings 每次调用现读文件——句柄每 ~30 分钟轮换,不缓存。
func readIndexSettings(path string) (indexSettings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return indexSettings{}, fmt.Errorf("read index settings: %w", err)
	}
	var s indexSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return indexSettings{}, fmt.Errorf("decode index settings: %w", err)
	}
	return s, nil
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
	settings, err := readIndexSettings(s.indexConfigFile)
	if err != nil {
		return nil, "", err
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
		svc, err := dense.New(s.objects, s.dc, settings.Dense)
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
		svc, err := sparse.New(s.objects, s.dc, settings.Sparse)
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
		svc, err := multivector.New(s.objects, s.dc, settings.MultiVector)
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

// ModuleSummary 是已发布知识模块的最小公开描述(模块发现用)。
type ModuleSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
}

// ListModules 列出 RTW 全部已发布知识模块(公开读,无需管理凭据)。
func (s *Searcher) ListModules(ctx context.Context) ([]ModuleSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.rtwBase+"/v1/knowledge/modules", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.rtwToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rtw modules status %d", resp.StatusCode)
	}
	var parsed struct {
		Data struct {
			Items []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Category string `json:"category"`
				Release  string `json:"release"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, err
	}
	out := []ModuleSummary{}
	for _, item := range parsed.Data.Items {
		if item.Release == "" {
			continue // 未发布模块不进入检索消费
		}
		out = append(out, ModuleSummary{ID: item.ID, Title: item.Title, Category: item.Category})
	}
	return out, nil
}
