package reader

import (
	"context"

	"sea/service/common/chunk"
)

type Reader interface {
	ReadArticle(ctx context.Context, id string) (chunk.Article, error)
}
