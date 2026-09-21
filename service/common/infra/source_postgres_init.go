package infra

import (
	"context"
	"database/sql"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/logx"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

var sourcePgDB *sql.DB

func SourcePostgres() *sql.DB {
	return sourcePgDB
}

func SourcePostgresInit() error {
	orm, err := database.Open(context.Background(), database.Config{
		DSN:                    config.Cfg.SourcePostgres.DSN(),
		MaxOpenConns:           config.Cfg.SourcePostgres.MaxOpenConns,
		MaxIdleConns:           config.Cfg.SourcePostgres.MaxIdleConns,
		ConnMaxLifetimeSeconds: config.Cfg.SourcePostgres.ConnMaxLifetimeSeconds,
	})
	if err != nil {
		zlog.L().Error("source postgres connect failed", zap.Error(err))
		return err
	}
	sqlDB, err := orm.DB()
	if err != nil {
		zlog.L().Error("source postgres sql handle failed", zap.Error(err))
		return err
	}
	sourcePgDB = sqlDB

	indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer indexCancel()
	if err := ensureSourceKeywordSearchIndexes(indexCtx, orm); err != nil {
		zlog.L().Warn("ensure source keyword search indexes failed", zap.Error(err))
	}

	zlog.L().Info("source postgres initialized")
	return nil
}
