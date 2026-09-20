package main

import (
	"context"
	"net/http"
	"time"

	zlog "github.com/Sea-Go/Sea-BreakTheWaves/service/common/logx"
	recommendhandler "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/handler"
	recommendsvc "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"

	"go.uber.org/zap"
)

func main() {
	svc := recommendsvc.New()
	defer svc.Close()
	srv := &http.Server{Addr: ":20721", Handler: recommendhandler.NewRouter(svc.Logic)}
	go func() {
		zlog.L().Info("recommend http started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.L().Fatal("recommend http failed", zap.Error(err))
		}
	}()
	<-svc.Signals
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
