package communitysource

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed coverage_schema.sql
var coverageSchema string

func InitializeCoverage(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return ErrContract
	}
	_, err := db.Exec(ctx, coverageSchema)
	return err
}
