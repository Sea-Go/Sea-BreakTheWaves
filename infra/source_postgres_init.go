package infra

import (
	"context"
	"database/sql"
	"time"

	"sea/config"
	"sea/zlog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
)

var sourcePgDB *sql.DB

func SourcePostgres() *sql.DB {
	return sourcePgDB
}

func SourcePostgresInit() error {
	db, err := sql.Open("pgx", config.Cfg.SourcePostgres.DSN())
	if err != nil {
		zlog.L().Error("source postgres connect failed", zap.Error(err))
		return err
	}

	if config.Cfg.SourcePostgres.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.Cfg.SourcePostgres.MaxOpenConns)
	}
	if config.Cfg.SourcePostgres.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.Cfg.SourcePostgres.MaxIdleConns)
	}
	if config.Cfg.SourcePostgres.ConnMaxLifetimeSeconds > 0 {
		db.SetConnMaxLifetime(time.Duration(config.Cfg.SourcePostgres.ConnMaxLifetimeSeconds) * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		zlog.L().Error("source postgres ping failed", zap.Error(err))
		return err
	}

	sourcePgDB = db
	zlog.L().Info("source postgres initialized")
	return nil
}
