package chunking

import "github.com/Sea-Go/Sea-BreakTheWaves/service/common/chunk"

type Chunker interface {
	Chunk(article chunk.Article) ([]chunk.Chunk, error)
}
