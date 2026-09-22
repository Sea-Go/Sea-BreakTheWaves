// Command usermodel-schema initializes the async usermodel PostgreSQL schema
// with GORM AutoMigrate. It is an explicit deployment fixture, not part of the
// normal worker startup path.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("usermodel.schema.failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	runFlag := flag.Bool("run", false, "explicitly enable schema initialization")
	dsnEnv := flag.String("dsn-env", "", "environment variable holding the PostgreSQL DSN")
	schema := flag.String("schema", "public", "target PostgreSQL schema")
	flag.Parse()
	if !*runFlag || *dsnEnv == "" {
		return errors.New("--run and --dsn-env are required")
	}
	dsn := os.Getenv(*dsnEnv)
	if dsn == "" {
		return fmt.Errorf("environment variable %s is empty", *dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, database.Config{DSN: dsn, Schema: *schema, MaxOpenConns: 1})
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql handle: %w", err)
	}
	defer sqlDB.Close()
	if err := usermodel.MigrateSchema(ctx, db); err != nil {
		return fmt.Errorf("migrate usermodel schema: %w", err)
	}
	return nil
}
