package milvus_search

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"sea/config"
	graphschema "sea/embedding/schema/graph"
	"sea/embedding/service"
	"sea/infra"
	"sea/zlog"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

// MilvusSearchInput 是 milvus_search 的输入参数。
type MilvusSearchInput struct {
	Mode      string `json:"mode" jsonschema:"description=检索模式,required,enum=coarse,enum=fine"`
	QueryText string `json:"query_text" jsonschema:"description=检索 query（通常由意图/记忆生成的短文本）"`
	TopK      int    `json:"topk" jsonschema:"description=返回条数,default=10"`
	Filter    string `json:"filter" jsonschema:"description=Milvus 表达式过滤（可选）"`
}

type MilvusHit struct {
	ID         string  `json:"id"` // coarse: article_id；fine: chunk_id
	ArticleID  string  `json:"article_id"`
	ChunkID    string  `json:"chunk_id,omitempty"`
	Similarity float32 `json:"similarity"`
	Source     string  `json:"source,omitempty"` // milvus | graph
}

type MilvusSearchOutput struct {
	ReturnedDocCount int         `json:"returned_doc_count"`
	Empty            bool        `json:"empty"`
	CoverageScore    float32     `json:"coverage_score"`
	Hits             []MilvusHit `json:"hits"`
	Collection       string      `json:"collection"`
	LatencyMs        int64       `json:"latency_ms"`
	GraphExpanded    bool        `json:"graph_expanded"`
	GraphAdded       int         `json:"graph_added"`
}

func New() tool.CallableTool {
	return function.NewFunctionTool(
		func(ctx context.Context, args MilvusSearchInput) (MilvusSearchOutput, error) {
			args.Mode = strings.ToLower(strings.TrimSpace(args.Mode))
			if args.TopK <= 0 {
				args.TopK = 10
			}

			collection := ""
			switch args.Mode {
			case "coarse":
				collection = config.Cfg.Milvus.Collections.Coarse
			case "fine":
				collection = config.Cfg.Milvus.Collections.Fine
			default:
				return MilvusSearchOutput{}, errors.New("mode 必须是 coarse 或 fine")
			}

			q := strings.TrimSpace(args.QueryText)
			if q == "" {
				return MilvusSearchOutput{}, errors.New("query_text 不能为空（当前 demo 只支持文本 query）")
			}

			opCtx, opSpan := zlog.StartSpan(ctx, "retrieval.completed")
			var (
				retCount        int
				cov             float32
				graphExpanded   bool
				graphAdded      int
				lat             int64
				requestedTopK   = args.TopK
				empty           bool
				decisionSignals = map[string]any{}

				embedMs        int64
				milvusSearchMs int64
				graphExpandMs  int64
			)
			defer func() {
				status := zlog.StatusOK
				// err is captured by closure
				decisionSignals["empty_retrieval"] = empty
				decisionSignals["embed_ms"] = embedMs
				decisionSignals["milvus_search_ms"] = milvusSearchMs
				decisionSignals["graphrag_expand_ms"] = graphExpandMs

				opSpan.End(status, nil,
					zap.Any("retrieval", map[string]any{
						"source":             collection,
						"query_count":        1,
						"requested_topk":     requestedTopK,
						"returned_doc_count": retCount,
						"coverage_score":     cov,
						"empty":              empty,
						"graph_expanded":     graphExpanded,
						"graph_added":        graphAdded,
						"latency_ms":         lat,
						"embed_ms":           embedMs,
						"milvus_search_ms":   milvusSearchMs,
						"graphrag_expand_ms": graphExpandMs,
					}),
					zap.Any("decision", map[string]any{
						"type":         "retrieve",
						"chosen":       "vector",
						"reason_codes": []string{"NEED_GROUNDING"},
						"signals":      decisionSignals,
					}),
				)
			}()

			start := time.Now()

			// 1) embedding
			stEmbed := time.Now()
			vec, e := service.TextVector(opCtx, q)
			embedMs = time.Since(stEmbed).Milliseconds()
			if e != nil {
				return MilvusSearchOutput{}, e
			}

			cli := infra.Milvus()
			if cli == nil {
				return MilvusSearchOutput{}, errors.New("Milvus 客户端未初始化")
			}

			opt := milvusclient.NewSearchOption(
				collection,
				args.TopK,
				[]entity.Vector{entity.FloatVector(vec)},
			).WithANNSField("vector")

			if strings.TrimSpace(args.Filter) != "" {
				opt = opt.WithFilter(args.Filter)
			}

			// 2) Milvus search
			tr := otel.Tracer("sea/milvus")
			searchCtx, span := tr.Start(opCtx, "milvus.search")
			span.SetAttributes(
				attribute.String("milvus.collection", collection),
				attribute.Int("milvus.topk", args.TopK),
				attribute.String("milvus.mode", args.Mode),
			)
			if strings.TrimSpace(args.Filter) != "" {
				span.SetAttributes(attribute.String("milvus.filter", args.Filter))
			}

			stSearch := time.Now()
			rs, se := cli.Search(searchCtx, opt)
			milvusSearchMs = time.Since(stSearch).Milliseconds()
			if se != nil {
				span.RecordError(se)
				span.SetStatus(codes.Error, se.Error())
				span.End()
				return MilvusSearchOutput{}, se
			}
			span.SetStatus(codes.Ok, "")
			span.End()

			var hits []MilvusHit
			if len(rs) > 0 {
				set := rs[0]
				for i := 0; i < set.ResultCount; i++ {
					id, _ := set.IDs.GetAsString(i)
					sim := set.Scores[i]

					h := MilvusHit{ID: id, Similarity: sim, Source: "milvus"}
					if args.Mode == "coarse" {
						h.ArticleID = id
					} else {
						h.ChunkID = id
						if idx := strings.Index(id, "#"); idx > 0 {
							h.ArticleID = id[:idx]
						}
					}
					hits = append(hits, h)
				}
			}

			// 3) fine 精召回：GraphRAG 扩展候选
			if args.Mode == "fine" {
				if drv := infra.Neo4j(); drv != nil {
					store := graphschema.NewStore(drv)
					if store != nil {
						trg := otel.Tracer("sea/graphrag")
						gctx, gspan := trg.Start(opCtx, "graphrag.expand")
						gspan.SetAttributes(attribute.Int("graphrag.seed_count", len(hits)))

						stGraph := time.Now()
						added, merged := expandWithGraphRAG(gctx, store, hits, args.TopK)
						graphExpandMs = time.Since(stGraph).Milliseconds()

						graphAdded = added
						graphExpanded = added > 0
						hits = merged

						gspan.SetAttributes(attribute.Int("graphrag.added", added))
						gspan.SetStatus(codes.Ok, "")
						gspan.End()
					}
				}
			}

			var coverageSum float32
			for _, h := range hits {
				coverageSum += h.Similarity
			}

			retCount = len(hits)
			empty = retCount == 0
			if retCount > 0 {
				cov = coverageSum / float32(retCount)
			}

			lat = time.Since(start).Milliseconds()
			return MilvusSearchOutput{
				ReturnedDocCount: retCount,
				Empty:            empty,
				CoverageScore:    cov,
				Hits:             hits,
				Collection:       collection,
				LatencyMs:        lat,
				GraphExpanded:    graphExpanded,
				GraphAdded:       graphAdded,
			}, nil
		},
		function.WithName("milvus_search"),
		function.WithDescription("在 Milvus 中进行向量检索（支持 coarse/fine 两个集合）。fine 模式会在 Neo4j 可用时用 GraphRAG（SIMILAR/NEXT）扩展候选。"),
	)
}

func expandWithGraphRAG(ctx context.Context, store *graphschema.Store, seeds []MilvusHit, topK int) (added int, merged []MilvusHit) {
	if len(seeds) == 0 {
		return 0, seeds
	}

	m := map[string]MilvusHit{}
	for _, h := range seeds {
		cid := strings.TrimSpace(h.ChunkID)
		if cid == "" {
			continue
		}
		m[cid] = h
	}

	for _, seed := range seeds {
		seedCID := strings.TrimSpace(seed.ChunkID)
		if seedCID == "" {
			continue
		}
		nbs, err := store.GetNeighbors(ctx, seedCID, 8)
		if err != nil {
			continue
		}
		for _, nb := range nbs {
			cid := strings.TrimSpace(nb.ChunkID)
			if cid == "" {
				continue
			}

			sim := seed.Similarity * float32(nb.Weight)
			old, ok := m[cid]
			if !ok {
				added++
			}

			if !ok || sim > old.Similarity {
				h := MilvusHit{ID: cid, ChunkID: cid, Similarity: sim, Source: "graph"}
				if idx := strings.Index(cid, "#"); idx > 0 {
					h.ArticleID = cid[:idx]
				}
				m[cid] = h
			}
		}
	}

	merged = make([]MilvusHit, 0, len(m))
	for _, h := range m {
		merged = append(merged, h)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Similarity > merged[j].Similarity })

	if topK > 0 && len(merged) > topK {
		merged = merged[:topK]
	}
	return added, merged
}
