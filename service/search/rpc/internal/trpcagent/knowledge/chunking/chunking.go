package chunking

import "sea/service/common/chunk"

type Chunker interface {
	Chunk(article chunk.Article) ([]chunk.Chunk, error)
}
