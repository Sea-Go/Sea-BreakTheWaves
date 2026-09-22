package svc

import (
	asyncservice "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/asyncservice"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/config"
	"github.com/zeromicro/go-zero/zrpc"
)

type ServiceContext struct {
	Config       config.Config
	AsyncService asyncservice.AsyncService
}

func NewServiceContext(c config.Config) *ServiceContext {
	conn := zrpc.MustNewClient(c.AsyncRpc)
	return &ServiceContext{Config: c, AsyncService: asyncservice.NewAsyncService(conn)}
}
