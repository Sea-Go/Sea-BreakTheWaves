package svc

import (
	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/config"
)

type ServiceContext struct {
	Config config.Config
	Search *searchclient.Client
}

func NewServiceContext(c config.Config) *ServiceContext {
	if err := commonconfig.InitPath("etc/config.yaml"); err != nil {
		panic(err)
	}
	if err := infra.PostgresInit(); err != nil {
		panic(err)
	}
	if err := infra.SourcePostgresInit(); err != nil {
		panic(err)
	}
	return &ServiceContext{Config: c, Search: searchclient.NewRuntime()}
}
