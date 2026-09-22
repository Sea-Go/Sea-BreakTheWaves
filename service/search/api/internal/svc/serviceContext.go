package svc

import (
	searchservice "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchservice"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/config"
	"github.com/zeromicro/go-zero/zrpc"
)

type ServiceContext struct {
	Config        config.Config
	SearchService searchservice.SearchService
}

func NewServiceContext(c config.Config) *ServiceContext {
	conn := zrpc.MustNewClient(c.SearchRpc)
	return &ServiceContext{Config: c, SearchService: searchservice.NewSearchService(conn)}
}
