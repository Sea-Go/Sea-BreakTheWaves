package retriever

import "context"

type Request struct {
	Query        string
	Vector       []float32
	TopK         int
	Collection   string
	ArticleScope []string
}

type Hit struct {
	ID        string
	ArticleID string
	Score     float64
}

type Retriever interface {
	Recall(ctx context.Context, req Request) ([]Hit, error)
}
