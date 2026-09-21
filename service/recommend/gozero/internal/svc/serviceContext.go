package svc

import (
	"os"

	asynclient "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/asyncclient"
	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/config"
)

type ServiceContext struct {
	Config    config.Config
	Recommend *recommendclient.Client
	Events    *asynclient.Client
}

func NewServiceContext(c config.Config) *ServiceContext {
	configPath := os.Getenv("RECOMMEND_CONFIG_PATH")
	if configPath == "" {
		configPath = "service/recommend/api/etc/config.yaml"
	}
	if err := commonconfig.InitPath(configPath); err != nil {
		panic(err)
	}
	if err := infra.PostgresInit(); err != nil {
		panic(err)
	}
	return &ServiceContext{
		Config:    c,
		Recommend: recommendclient.NewRuntime("service/recommend/rpc/internal/trpcagent/skill"),
		Events:    asynclient.New("http://127.0.0.1:20741"),
	}
}
