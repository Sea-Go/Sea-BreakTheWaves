package main

import (
	"context"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	zlog "github.com/Sea-Go/Sea-BreakTheWaves/service/common/logx"
	searchhandler "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/handler"
	searchsvc "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"

	"go.uber.org/zap"
)

func main() {
	if err := config.Init(); err != nil {
		panic(err)
	}
	if err := zlog.InitLogger(config.Cfg.Log.Path, config.Cfg.Log.Level, "SeaSearchAPI"); err != nil {
		panic(err)
	}
	svc := searchsvc.New()
	defer svc.Close()

	srv := &http.Server{Addr: config.Cfg.Services.HTTPAddr + ":20731", Handler: searchhandler.NewRouter(svc.Logic)}
	go func() {
		zlog.L().Info("search http started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.L().Fatal("search http failed", zap.Error(err))
		}
	}()
	<-svc.Signals
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
