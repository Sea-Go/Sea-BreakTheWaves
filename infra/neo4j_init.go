package infra

import (
	"context"
	"time"

	"sea/config"
	"sea/zlog"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"go.uber.org/zap"
)

var neo4jDriver neo4j.DriverWithContext

// Neo4j 返回全局 Neo4j driver（Neo4jInit 成功后可用）。
func Neo4j() neo4j.DriverWithContext {
	return neo4jDriver
}

// Neo4jInit 初始化 Neo4j driver。
// 若连接失败，返回 error；调用方可选择降级（GraphRAG 禁用）。
func Neo4jInit() error {
	cfg := config.Cfg
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	d, err := neo4j.NewDriverWithContext(
		cfg.Neo4j.Address,
		neo4j.BasicAuth(cfg.Neo4j.Username, cfg.Neo4j.Password, ""),
	)
	if err != nil {
		return err
	}

	if err := d.VerifyConnectivity(ctx); err != nil {
		_ = d.Close(ctx)
		return err
	}

	neo4jDriver = d
	zlog.L().Info("neo4j init success")
	return nil
}

func Neo4jClose(ctx context.Context) {
	if neo4jDriver == nil {
		return
	}
	if err := neo4jDriver.Close(ctx); err != nil {
		zlog.L().Warn("neo4j close failed", zap.Error(err))
	}
	neo4jDriver = nil
}
