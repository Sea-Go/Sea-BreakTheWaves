package engine

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/agentx"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/graph"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/search"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func New(r item.Reader, e inference.Encoder, i idx.Index, ranker inference.Ranker, scenarios map[string]item.Scenario, m model.Model, c agentx.Config, retrieval item.Retrieval, lexical *elastic.Client, contentGraph *graph.Client, planning ...model.Model) (*search.Service, error) {
	return search.New(r, e, i, ranker, scenarios, m, c, retrieval, lexical, contentGraph, planning...)
}
