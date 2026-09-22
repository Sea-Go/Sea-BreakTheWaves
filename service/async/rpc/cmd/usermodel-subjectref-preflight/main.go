package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/usermodel/preflight"
	"github.com/jackc/pgx/v5"
)

func main() { os.Exit(run()) }

func run() int {
	runFlag := flag.Bool("run", false, "explicitly enable a read-only audit")
	dsnEnv := flag.String("dsn-env", "", "name of an environment variable containing the PostgreSQL DSN")
	schema := flag.String("schema", "public", "schema holding the frozen usermodel v1 tables")
	mode := flag.String("mode", "audit", "audit or catalog (isolated fixture inventory)")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With(
		"service", "btw-subjectref-preflight", "component", "usermodel", "source", "cli")
	if !*runFlag || *dsnEnv == "" || (*mode != "audit" && *mode != "catalog") {
		logger.Error("preflight.configuration.rejected", "outcome", "rejected", "reason", "explicit run, dsn-env and valid mode required")
		return 2
	}
	dsn := os.Getenv(*dsnEnv)
	if dsn == "" {
		logger.Error("preflight.configuration.rejected", "outcome", "rejected", "reason", "named DSN variable is empty")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		logger.Error("preflight.connection.rejected", "outcome", "failed", "reason", "DSN could not be parsed")
		return 2
	}
	// The session remains read-only even if an audit query escapes its explicit
	// repeatable-read transaction. No migration or row write is supported.
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "30000"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		logger.Error("preflight.connection.rejected", "outcome", "failed", "reason", "PostgreSQL connection failed")
		return 2
	}
	defer conn.Close(context.Background())
	if *mode == "catalog" {
		catalog, err := preflight.Inspect(ctx, conn, *schema)
		if err != nil {
			logger.Error("preflight.catalog.failed", "outcome", "failed")
			return 2
		}
		if err := json.NewEncoder(os.Stdout).Encode(catalog); err != nil {
			logger.Error("preflight.catalog.output.failed", "outcome", "failed")
			return 2
		}
		logger.Info("preflight.catalog.completed", "outcome", "completed", "tables", len(catalog.Tables))
		return 0
	}
	observer := func(event preflight.Observation) {
		logger.Info(event.Stage, "outcome", event.Outcome, "table", event.Table, "count", event.Count)
	}
	report, err := preflight.Run(ctx, conn, *schema, observer)
	if err != nil {
		logger.Error("preflight.audit.failed", "outcome", "failed", "reason", err.Error())
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		logger.Error("preflight.audit.output.failed", "outcome", "failed")
		return 2
	}
	if report.L1+report.L2 > 0 {
		return 1
	}
	return 0
}
