package svc

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	asyncworker "sea/service/async/rpc/worker"
	"sea/service/common/config"
	"sea/service/common/infra"
)

type ServiceContext struct {
	StopKafka    func() error
	Cancel       context.CancelFunc
	ShutdownOTel func(context.Context) error
	Signals      chan os.Signal
}

func New() *ServiceContext {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	if err := config.Init(); err != nil {
		panic(err)
	}
	shutdown, err := infra.OtelInit()
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := asyncworker.Start(ctx)
	if err != nil {
		cancel()
		panic(err)
	}
	return &ServiceContext{StopKafka: stop, Cancel: cancel, ShutdownOTel: shutdown, Signals: signals}
}

func (s *ServiceContext) Close() {
	if s.Cancel != nil {
		s.Cancel()
	}
	if s.StopKafka != nil {
		_ = s.StopKafka()
	}
	infra.Neo4jClose(context.Background())
	if s.ShutdownOTel != nil {
		_ = s.ShutdownOTel(context.Background())
	}
}
