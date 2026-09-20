package svc

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	cfg "sea/service/common/config"
	"sea/service/common/infra"
	metrics "sea/service/common/metricx"
	searchlogic "sea/service/search/api/internal/logic"
	searchclient "sea/service/search/rpc/searchclient"
)

type ServiceContext struct {
	Search       *searchclient.Client
	Logic        *searchlogic.SearchLogic
	ShutdownOTel func(context.Context) error
	Signals      chan os.Signal
}

func New() *ServiceContext {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	metrics.InitMetrics(signals, &cfg.Cfg)
	shutdown, err := infra.OtelInit()
	if err != nil {
		panic(err)
	}
	client := searchclient.NewRuntime()
	return &ServiceContext{Search: client, Logic: searchlogic.NewSearchLogic(client), ShutdownOTel: shutdown, Signals: signals}
}

func (s *ServiceContext) Close() {
	infra.Neo4jClose(context.Background())
	if s.ShutdownOTel != nil {
		_ = s.ShutdownOTel(context.Background())
	}
}
