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
	"gorm.io/gorm"
)

var pgDB *sql.DB
var pgORM *gorm.DB

func Postgres() *sql.DB {
	return pgDB
}

func PostgresGORM() *gorm.DB {
	return pgORM
}

func PostgresInit() error {
	orm, err := database.Open(context.Background(), database.Config{
		DSN:                    config.Cfg.Postgres.DSN,
		MaxOpenConns:           config.Cfg.Postgres.MaxOpenConns,
		MaxIdleConns:           config.Cfg.Postgres.MaxIdleConns,
		ConnMaxLifetimeSeconds: config.Cfg.Postgres.ConnMaxLifetimeSeconds,
	})
	if err != nil {
		zlog.L().Error("postgres connect failed", zap.Error(err))
		return err
	}
	sqlDB, err := orm.DB()
	if err != nil {
		zlog.L().Error("postgres sql handle failed", zap.Error(err))
		return err
	}
	ctx := context.Background()
	pgDB = sqlDB
	pgORM = orm

	if err := ensurePGSchema(ctx, orm); err != nil {
		return err
	}

	indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer indexCancel()
	if err := ensureKeywordSearchIndexes(indexCtx, orm); err != nil {
		zlog.L().Warn("ensure local keyword search indexes failed", zap.Error(err))
	}

	zlog.L().Info("postgres initialized")
	return nil
}
