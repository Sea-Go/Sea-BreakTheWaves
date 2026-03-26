package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"sea/agent"
	"sea/config"
	"sea/infra"
	"sea/kafka"
	"sea/metrics"
	"sea/router"
	"sea/skillsys"
	"sea/storage"
	"sea/zlog"

	"go.uber.org/zap"
)

func main() {
	if err := config.Init(); err != nil {
		panic(err)
	}
	if err := zlog.InitLogger(config.Cfg.Log.Path, config.Cfg.Log.Level, config.Cfg.Log.ServiceName); err != nil {
		panic(err)
	}

	zlog.L().Info("service starting")

	if err := infra.PostgresInit(); err != nil {
		zlog.L().Fatal("postgres init failed", zap.Error(err))
	}
	if err := infra.MilvusInit(); err != nil {
		zlog.L().Fatal("milvus init failed", zap.Error(err))
	}
	if err := infra.Neo4jInit(); err != nil {
		zlog.L().Warn("neo4j init failed, continue with milvus only", zap.Error(err))
	}
	if err := kafka.Init(); err != nil {
		zlog.L().Warn("kafka init failed", zap.Error(err))
	}

	shutdownOtel, err := infra.OtelInit()
	if err != nil {
		zlog.L().Fatal("otel init failed", zap.Error(err))
	}

	db := infra.Postgres()
	articleRepo := storage.NewArticleRepo(db)
	poolRepo := storage.NewPoolRepo(db)
	memoryRepo := storage.NewMemoryRepo(db)
	historyRepo := storage.NewUserHistoryRepo(db)
	memoryChunkRepo := storage.NewMemoryChunkRepo(db)

	signChan := make(chan os.Signal, 1)
	signal.Notify(signChan, syscall.SIGINT, syscall.SIGTERM)
	metrics.InitMetrics(signChan, &config.Cfg)

	reg := skillsys.NewRegistry()
	skillsys.RegisterSkills(reg, articleRepo, poolRepo, memoryRepo, historyRepo, memoryChunkRepo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := kafka.Start(ctx, createKafkaMessageHandler(reg, articleRepo)); err != nil {
		zlog.L().Error("kafka consumer start failed", zap.Error(err))
	}
	if err := kafka.StartRetry(ctx, createKafkaRetryHandler(reg, articleRepo)); err != nil {
		zlog.L().Error("kafka retry consumer start failed", zap.Error(err))
	}

	aiClient := infra.NewAIClient()
	recoAgent := agent.NewRecoAgent(aiClient, reg, poolRepo, memoryRepo, memoryChunkRepo)
	contentSearchAgent := agent.NewContentSearchAgent(aiClient, reg, articleRepo)

	r := router.NewRouter(reg, recoAgent, contentSearchAgent)
	srv := &http.Server{
		Addr:    config.Cfg.Services.HTTPAddr + ":" + config.Cfg.Services.HTTPPort,
		Handler: r,
	}

	go func() {
		zlog.L().Info("http server started", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.L().Fatal("http server start failed", zap.Error(err))
		}
	}()

	<-signChan
	zlog.L().Info("shutdown signal received")

	if err := srv.Shutdown(context.Background()); err != nil {
		zlog.L().Error("http server shutdown failed", zap.Error(err))
	}

	infra.Neo4jClose(context.Background())
	if shutdownOtel != nil {
		_ = shutdownOtel(context.Background())
	}
	_ = kafka.Close()

	zlog.L().Info("service stopped")
}
