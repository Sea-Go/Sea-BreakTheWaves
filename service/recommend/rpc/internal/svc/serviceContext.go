package svc

import (
	"context"

	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	recommendstorage "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/config"
)

type ServiceContext struct {
	Config    config.Config
	Recommend *recommendclient.Client
}

func NewServiceContext(c config.Config) *ServiceContext {
	if err := commonconfig.InitPath("etc/config.yaml"); err != nil {
		panic(err)
	}
	if err := infra.PostgresInit(); err != nil {
		panic(err)
	}
	ctx := context.Background()
	if err := recommendstorage.MigrateSchema(ctx, infra.PostgresGORM()); err != nil {
		panic(err)
	}
	return &ServiceContext{Config: c, Recommend: recommendclient.NewRuntime("internal/trpcagent/skill")}
}
