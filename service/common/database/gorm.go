// Package database owns application-managed PostgreSQL connections through
// GORM. Framework-owned storage, such as tRPC-Agent-Go session tables, has a
// separate lifecycle and must not use this package as its ORM owner.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	parts := strings.Split(schema, ".")
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "\";") {
			return fmt.Errorf("invalid postgres schema identifier %q", schema)
		}
	}
	return nil
}

// Open returns an application-owned GORM handle. Callers own Close through
// sql.DB.
func Open(ctx context.Context, cfg Config) (*gorm.DB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("postgres DSN is required")
	}
	if err := validateIdentifier(cfg.Schema); err != nil {
		return nil, err
	}
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Warn),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres with gorm: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get gorm sql database: %w", err)
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
	if cfg.ReadOnly {
		if err := db.Exec("SET default_transaction_read_only = on").Error; err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("set postgres read only: %w", err)
		}
	}
	if err := UseSchema(db, cfg.Schema); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// UseSchema creates the application schema when needed and places it first on
// search_path. The built-in public schema remains as a fallback.
func UseSchema(db *gorm.DB, schema string) error {
	if schema == "" {
		return nil
	}
	if err := validateIdentifier(schema); err != nil {
		return err
	}
	quoted := quoteIdentifier(schema)
	if err := db.Exec(fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoted)).Error; err != nil {
		return fmt.Errorf("create postgres schema: %w", err)
	}
	if err := db.Exec(fmt.Sprintf("SET search_path = %s, public", quoted)).Error; err != nil {
		return fmt.Errorf("select postgres schema: %w", err)
	}
	return nil
}

func quoteIdentifier(value string) string {
	parts := strings.Split(value, ".")
	for i := range parts {
		parts[i] = `"` + strings.ReplaceAll(parts[i], `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// Exec applies an embedded, application-owned SQL migration through GORM.
func Exec(ctx context.Context, db *gorm.DB, migrationSQL string) error {
	if strings.TrimSpace(migrationSQL) == "" {
		return errors.New("migration SQL is empty")
	}
	return db.WithContext(ctx).Exec(migrationSQL).Error
}

func placeholder() bool { return true }
