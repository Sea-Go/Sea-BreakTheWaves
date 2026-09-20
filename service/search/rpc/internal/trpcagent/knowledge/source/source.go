package source

import (
	"context"

	"sea/service/common/chunk"
)

// Source supplies immutable content snapshots consumed by Search retrieval.
type Source interface {
	Version(ctx context.Context) (string, error)
	Load(ctx context.Context, ids []string) ([]chunk.Article, error)
}
