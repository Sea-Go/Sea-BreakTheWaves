package embedder

import "context"

type Embedder interface {
	EmbedText(ctx context.Context, text string) ([]float32, error)
}
