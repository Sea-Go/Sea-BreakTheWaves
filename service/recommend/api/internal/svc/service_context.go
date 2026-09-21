package svc

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	recommendmigration "github.com/Sea-Go/Sea-BreakTheWaves/migrations/recommend"
	asynclient "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/asyncclient"
	commonconfig "github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	metrics "github.com/Sea-Go/Sea-BreakTheWaves/service/common/metricx"
	recommendlogic "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/logic"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"
)

type ServiceContext struct {
	Recommend    *recommendclient.Client
	Logic        *recommendlogic.RecommendLogic
	Events       *asynclient.Client
	ShutdownOTel func(context.Context) error
	Signals      chan os.Signal
}

func New() *ServiceContext {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	if err := initRecommendConfig(); err != nil {
		panic(err)
	}
	if err := infra.PostgresInit(); err != nil {
		panic(err)
	}
	if err := database.Exec(context.Background(), infra.PostgresORM(), recommendmigration.SQL); err != nil {
		panic(err)
	}
	if err := database.Exec(context.Background(), infra.PostgresORM(), recommendmigration.PairSQL); err != nil {
		panic(err)
	}
	metrics.InitMetrics(signals, &commonconfig.Cfg)
	shutdown, err := infra.OtelInit()
	if err != nil {
		panic(err)
	}
	client := recommendclient.NewRuntime("service/recommend/rpc/internal/trpcagent/skill")
	return &ServiceContext{
		Recommend: client, Logic: recommendlogic.NewRecommendLogic(client, asynclient.New("http://127.0.0.1:20741")),
		Events:       asynclient.New("http://127.0.0.1:20741"),
		ShutdownOTel: shutdown,
		Signals:      signals,
	}
}

func (s *ServiceContext) Close() {
	infra.Neo4jClose(context.Background())
	if s.ShutdownOTel != nil {
		_ = s.ShutdownOTel(context.Background())
	}
}

func initRecommendConfig() error {
	if explicit := os.Getenv("RECOMMEND_CONFIG_PATH"); explicit != "" {
		return commonconfig.InitPath(explicit)
	}

	candidates := []string{
		"service/recommend/api/etc/config.yaml",
		"etc/config.yaml",
		"config.yaml",
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return commonconfig.InitPath(path)
		}
	}
	return commonconfig.Init()
}
