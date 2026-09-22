// Package database owns application-managed PostgreSQL connections through
// GORM. Framework-owned storage, such as tRPC-Agent-Go session tables, has a
// separate lifecycle and must not use this package as its ORM owner.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm/schema"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Config struct {
	DSN                    string
	Schema                 string
	MaxOpenConns           int
	MaxIdleConns           int
	ConnMaxLifetimeSeconds int
	ReadOnly               bool
}

func validateIdentifier(schema string) error {
	if schema == "" {
		return nil
	}
	if strings.ContainsAny(schema, "\";.\t\r\n ") {
		return fmt.Errorf("invalid postgres schema identifier %q", schema)
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// Open returns an application-owned GORM handle. Callers own Close through
// sql.DB.
func Open(ctx context.Context, cfg Config) (*gorm.DB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	conn, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	if err := prepareSchema(ctx, conn, cfg.Schema); err != nil {
		return nil, err
	}
	return openPgx(ctx, *conn, cfg)
}

// prepareSchema creates the target schema and puts it in pgx runtime settings.
// Setting search_path on one pooled GORM connection is insufficient because a
// later statement may use another database/sql connection.
func prepareSchema(ctx context.Context, conn *pgx.ConnConfig, schema string) error {
	if schema == "" {
		return nil
	}
	if err := validateIdentifier(schema); err != nil {
		return err
	}
	quoted := quoteIdentifier(schema)
	adminDB := sql.OpenDB(stdlib.GetConnector(*conn))
	defer adminDB.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := adminDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	var exists bool
	if err := adminDB.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)", schema,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check postgres schema: %w", err)
	}
	if !exists {
		if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			return fmt.Errorf("create postgres schema: %w", err)
		}
	}
	setRuntimeParam(conn, "search_path", quoted+", public")
	return nil
}

func setRuntimeParam(conn *pgx.ConnConfig, key, value string) {
	runtimeParams := make(map[string]string, len(conn.RuntimeParams)+1)
	for existingKey, existingValue := range conn.RuntimeParams {
		runtimeParams[existingKey] = existingValue
	}
	conn.RuntimeParams = runtimeParams
	conn.RuntimeParams[key] = value
}

// AutoMigrate applies application-owned GORM models in one bounded
// transaction. Raw schema SQL must not be used for application tables.
func AutoMigrate(ctx context.Context, db *gorm.DB, models ...any) error {
	if db == nil {
		return errors.New("gorm database is required")
	}
	if len(models) == 0 {
		return errors.New("gorm models are required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Migrator().AutoMigrate(models...); err != nil {
			return err
		}
		return createForeignKeys(tx, models)
	})
}

// createForeignKeys installs declarative relationship constraints after every
// table exists. GORM otherwise emits child FKs while creating the parent table.
func createForeignKeys(tx *gorm.DB, models []any) error {
	cache := &sync.Map{}
	seen := make(map[string]struct{})
	for _, model := range models {
		modelSchema, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			return fmt.Errorf("parse gorm schema: %w", err)
		}
		for _, relation := range modelSchema.Relationships.BelongsTo {
			constraint := relation.ParseConstraint()
			if constraint == nil || constraint.Schema == nil {
				continue
			}
			key := constraint.Schema.Table + "." + constraint.GetName()
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			name := constraint.GetName()
			if tx.Migrator().HasConstraint(model, name) {
				continue
			}
			if err := tx.Migrator().CreateConstraint(model, name); err != nil {
				return fmt.Errorf("create foreign key %s: %w", key, err)
			}
		}
	}
	return nil
}

// OpenPgx opens a GORM handle from an already-parsed pgx connection
// configuration. It lets pgx-backed services use the same connection contract
// while keeping GORM as the application schema owner.
func OpenPgx(ctx context.Context, conn *pgx.ConnConfig, cfg Config) (*gorm.DB, error) {
	if conn == nil {
		return nil, errors.New("pgx connection configuration is required")
	}
	parsed := *conn
	if err := prepareSchema(ctx, &parsed, cfg.Schema); err != nil {
		return nil, err
	}
	return openPgx(ctx, parsed, cfg)
}

func openPgx(ctx context.Context, conn pgx.ConnConfig, cfg Config) (*gorm.DB, error) {
	if cfg.ReadOnly {
		setRuntimeParam(&conn, "default_transaction_read_only", "on")
	}
	sqlDB := sql.OpenDB(stdlib.GetConnector(conn))
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Warn),
		NowFunc:                                  func() time.Time { return time.Now().UTC() },
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open postgres with gorm: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetimeSeconds > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}
