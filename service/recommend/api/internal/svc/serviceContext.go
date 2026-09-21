package svc

import (
	"context"

	recommendmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/recommend"
	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/config"
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
	if err := database.Exec(ctx, infra.PostgresGORM(), recommendmigration.SQL); err != nil {
		panic(err)
	}
	if err := database.Exec(ctx, infra.PostgresGORM(), recommendmigration.PairSQL); err != nil {
		panic(err)
	}

	client := recommendclient.NewRuntime("internal/trpcagent/skill")
	return &ServiceContext{Config: c, Recommend: client}
}
