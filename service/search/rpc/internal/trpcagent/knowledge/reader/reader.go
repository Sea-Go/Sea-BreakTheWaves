package reader

import (
	"context"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/chunk"
)

type Reader interface {
	ReadArticle(ctx context.Context, id string) (chunk.Article, error)
}
