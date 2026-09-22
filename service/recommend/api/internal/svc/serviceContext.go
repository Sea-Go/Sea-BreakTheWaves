package svc

import (
	recommendservice "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendservice"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/config"
	"github.com/zeromicro/go-zero/zrpc"
)

type ServiceContext struct {
	Config           config.Config
	RecommendService recommendservice.RecommendService
}

func NewServiceContext(c config.Config) *ServiceContext {
	conn := zrpc.MustNewClient(c.RecommendRpc)
	return &ServiceContext{Config: c, RecommendService: recommendservice.NewRecommendService(conn)}
}
