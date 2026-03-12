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
	// 1) 配置与日志
	if err := config.Init(); err != nil {
		panic(err)
	}
	if err := zlog.InitLogger(config.Cfg.Log.Path, config.Cfg.Log.Level, config.Cfg.Log.ServiceName); err != nil {
		panic(err)
	}

	zlog.L().Info("服务启动中...")

	// 2) 基础设施初始化（全部来自 config.yaml）
	if err := infra.PostgresInit(); err != nil {
		zlog.L().Fatal("Postgres 初始化失败", zap.Error(err))
	}
	if err := infra.MilvusInit(); err != nil {
		zlog.L().Fatal("Milvus 初始化失败", zap.Error(err))
	}
	// Neo4j 用于 GraphRAG；连接失败不阻断主链路（降级为纯 Milvus）。
	if err := infra.Neo4jInit(); err != nil {
		zlog.L().Warn("Neo4j 初始化失败（GraphRAG 将自动禁用）", zap.Error(err))
	}

	// Kafka Consumer 用于接收 Sea-RideTheWind 发送的文章数据
	if err := kafka.Init(); err != nil {
		zlog.L().Warn("Kafka Consumer 初始化失败", zap.Error(err))
	}

	shutdownOtel, err := infra.OtelInit()
	if err != nil {
		zlog.L().Fatal("Otel 初始化失败", zap.Error(err))
	}

	// 3) Repo
	db := infra.Postgres()
	articleRepo := storage.NewArticleRepo(db)
	poolRepo := storage.NewPoolRepo(db)
	memoryRepo := storage.NewMemoryRepo(db)
	historyRepo := storage.NewUserHistoryRepo(db)
	memoryChunkRepo := storage.NewMemoryChunkRepo(db)

	// 4) 退出信号（同时用于 metrics.InitMetrics 暴露）
	signChan := make(chan os.Signal, 1)
	signal.Notify(signChan, syscall.SIGINT, syscall.SIGTERM)

	// 5) metrics 暴露：按你要求的方式在 main 中初始化
	metrics.InitMetrics(signChan, &config.Cfg)

	// 6) skills 注册（集中到 app）
	reg := skillsys.NewRegistry()
	skillsys.RegisterSkills(reg, articleRepo, poolRepo, memoryRepo, historyRepo, memoryChunkRepo)

	// 6.1) 启动 Kafka Consumer（消费 Sea-RideTheWind 发送的文章数据）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := kafka.Start(ctx, createKafkaMessageHandler(reg)); err != nil {
		zlog.L().Error("Kafka Consumer 启动失败", zap.Error(err))
	}

	// 7) Agent
	aiClient := infra.NewAIClient()
	recoAgent := agent.NewRecoAgent(aiClient, reg, poolRepo, memoryRepo, memoryChunkRepo)
	contentSearchAgent := agent.NewContentSearchAgent(aiClient, reg, articleRepo)

	// 8) HTTP（路由/中间件/metrics endpoint 全部在 router 内注册）
	r := router.NewRouter(reg, recoAgent, contentSearchAgent)
	srv := &http.Server{
		Addr:    config.Cfg.Services.HTTPAddr + ":" + config.Cfg.Services.HTTPPort,
		Handler: r,
	}

	go func() {
		zlog.L().Info("HTTP 服务已启动", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.L().Fatal("HTTP 服务启动失败", zap.Error(err))
		}
	}()

	// 9) 优雅退出
	<-signChan
	zlog.L().Info("收到退出信号，开始停止服务")

	if err := srv.Shutdown(context.Background()); err != nil {
		zlog.L().Error("服务停止失败", zap.Error(err))
	} else {
		zlog.L().Info("HTTP 服务已停止")
	}

	infra.Neo4jClose(context.Background())

	if shutdownOtel != nil {
		_ = shutdownOtel(context.Background())
	}

	_ = kafka.Close()

	zlog.L().Info("服务已停止")
}
