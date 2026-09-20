package main

import (
	"context"
	"net/http"

	asynchandler "github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/handler"
	asynclogic "github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/logic"
	asyncsvc "github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	zlog "github.com/Sea-Go/Sea-BreakTheWaves/service/common/logx"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func main() {
	svc := asyncsvc.New()
	defer svc.Close()
	r := asynchandler.NewRouter(asynclogic.NewEventLogic())
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	srv := &http.Server{Addr: ":20741", Handler: r}
	go func() {
		zlog.L().Info("async admin http started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.L().Fatal("async admin http failed", zap.Error(err))
		}
	}()
	<-context.Background().Done()
	_ = srv.Shutdown(context.Background())
}
