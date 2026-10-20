// ============================================================================
// search.go —— 档位化整篇检索（工程方案 §3.2 的档位×能力矩阵）。
//
//	fast     = dense + sparse 两路 + RRF（跳 multi：低延迟档）
//	balanced = 三路 + RRF
//	deep     = 三路 + RRF + 每路候选数放宽一倍（模拟"多取 top"）
//
// dev 近似声明：命中定位取文档第一个段落节点整段为 HitSpan（真实按段
// 打分定位属后续）；deep 的"双倍候选"是放宽的模拟，真实 deep 档（更宽
// 的召回 + 重排）属后续工作。
// ============================================================================

package retrieval

import (
	"context"
	"errors"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
)

// Tier 是检索档位。
type Tier string

// 档位常量（工程方案 §3.2）。
const (
	// TierFast 低延迟档：dense+sparse 两路，跳过 multi。
	TierFast Tier = "fast"
	// TierBalanced 均衡档：三路召回 + RRF 融合。
	TierBalanced Tier = "balanced"
	// TierDeep 深查档：三路 + RRF + 双倍路内候选（模拟放宽）。
	TierDeep Tier = "deep"
)

// ErrInvalidTier 标记未知档位。
var ErrInvalidTier = errors.New("retrieval: 未知检索档位")

// ErrEmptyQuery 标记查询表示三件套全空。
var ErrEmptyQuery = errors.New("retrieval: 查询表示三件套（dense/sparse/multi）全空")

// Request 是一次整篇检索的请求：查询 ID + 档位 + 查询表示三件套。
type Request struct {
	// QueryID 查询 ID（非空，透传进 EvidencePack）。
	QueryID string
	// Tier 检索档位（fast|balanced|deep）。
	Tier Tier
	// Dense 查询稠密向量（dense 路）。
	Dense []float32
	// Sparse 查询稀疏 impact（sparse 路）。
	Sparse map[uint32]float32
	// Multi 查询多向量 token 矩阵（multi 路；fast 档跳过）。
	Multi [][]float32
}

// Searcher 在只读 Store 上执行档位化检索。
type Searcher struct {
	store *Store
	// TopN 每路召回的截断条数（fast/balanced 档；deep 档取 2×TopN）。
	// <=0 时取 evidence.MaxCandidates。
	TopN int
}

// NewSearcher 基于 Store 构造检索器，TopN 默认 evidence.MaxCandidates。
func NewSearcher(s *Store) *Searcher {
	return &Searcher{store: s, TopN: evidence.MaxCandidates}
}

// Search 按档位执行三路召回、RRF 融合与证据组装，返回 EvidencePack。
// 候选文档缺 Tree/Source（未 AttachSource）会在证据组装阶段报错。
func (sh *Searcher) Search(ctx context.Context, req Request) (evidence.EvidencePack, error) {
	if err := ctx.Err(); err != nil {
		return evidence.EvidencePack{}, err
	}
	if req.QueryID == "" {
		return evidence.EvidencePack{}, fmt.Errorf("retrieval: query_id 不能为空")
	}
	if len(req.Dense) == 0 && len(req.Sparse) == 0 && len(req.Multi) == 0 {
		return evidence.EvidencePack{}, ErrEmptyQuery
	}

	// 每路候选截断：fast/balanced = TopN；deep = 2×TopN（模拟放宽）。
	laneTop := sh.TopN
	if laneTop <= 0 {
		laneTop = evidence.MaxCandidates
	}
	if req.Tier == TierDeep {
		laneTop *= 2
	}

	sn := sh.store.Snapshot()
	var lists [][]Scored
	switch req.Tier {
	case TierFast:
		// fast 档：dense+sparse 两路，刻意跳过 multi（§3.2 矩阵）。
		lists = append(lists, truncate(sn.Dense(req.Dense), laneTop))
		lists = append(lists, truncate(sn.Sparse(req.Sparse), laneTop))
	case TierBalanced, TierDeep:
		lists = append(lists, truncate(sn.Dense(req.Dense), laneTop))
		lists = append(lists, truncate(sn.Sparse(req.Sparse), laneTop))
		lists = append(lists, truncate(sn.Multi(req.Multi), laneTop))
	default:
		return evidence.EvidencePack{}, fmt.Errorf("%w: %q（合法值 fast|balanced|deep）", ErrInvalidTier, req.Tier)
	}

	fused := RRF(lists, KDefault)
	packTop := min(laneTop, evidence.MaxCandidates)
	if len(fused) > packTop {
		fused = fused[:packTop]
	}

	// 各路分数快照（截断后的路内分数；未参与的路记 0）。
	denseScores := scoreMap(lists[0])
	sparseScores := scoreMap(lists[1])
	var multiScores map[string]float32
	if len(lists) > 2 {
		multiScores = scoreMap(lists[2])
	}

	hits := make([]evidence.DocHit, 0, len(fused))
	for _, f := range fused {
		d, ok := sn.Doc(f.DocKey)
		if !ok {
			return evidence.EvidencePack{}, fmt.Errorf("retrieval: 融合候选 %s 不在 Store 中", f.DocKey)
		}
		span, err := firstParagraphSpan(d)
		if err != nil {
			return evidence.EvidencePack{}, fmt.Errorf("retrieval: 候选 %s: %w", f.DocKey, err)
		}
		hits = append(hits, evidence.DocHit{
			DocKey:     f.DocKey,
			RevisionID: d.Tree.RevisionID,
			RRFScore:   f.Score,
			Lanes: evidence.LaneScores{
				Dense:  denseScores[f.DocKey],
				Sparse: sparseScores[f.DocKey],
				Multi:  multiScores[f.DocKey],
			},
			Hits:   []evidence.HitSpan{span},
			Tree:   d.Tree,
			Source: d.Source,
		})
	}
	return evidence.BuildPack(req.QueryID, hits)
}

// truncate 截断已排序候选列表至前 n 条。
func truncate(list []Scored, n int) []Scored {
	if len(list) <= n {
		return list
	}
	return list[:n]
}

// scoreMap 把单路候选列表转为 doc_key → 分数映射。
func scoreMap(list []Scored) map[string]float32 {
	if len(list) == 0 {
		return map[string]float32{}
	}
	m := make(map[string]float32, len(list))
	for _, s := range list {
		m[s.DocKey] = s.Score
	}
	return m
}

// firstParagraphSpan 是 dev 近似的命中定位：取文档结构树的第一个段落
// 节点（文档序）整段 [CharStart, CharEnd) 为唯一 HitSpan。真实命中定位
// （按各路分数选最高分段并给多段证据）属后续工作。
func firstParagraphSpan(d LoadedDoc) (evidence.HitSpan, error) {
	for i := range d.Tree.Nodes {
		n := &d.Tree.Nodes[i]
		if n.Level == evidence.LevelParagraph {
			return evidence.HitSpan{CharStart: n.CharStart, CharEnd: n.CharEnd}, nil
		}
	}
	return evidence.HitSpan{}, fmt.Errorf("结构树无段落节点，无法构造 dev 命中区间")
}
