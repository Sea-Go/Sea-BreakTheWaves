package engine

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/recommendation"
)

func New(search recommendation.Search, r item.Reader) *recommendation.Service {
	return &recommendation.Service{Search: search, Reader: r}
}
