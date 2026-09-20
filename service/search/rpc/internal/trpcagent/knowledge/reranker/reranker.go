package reranker

import "context"

type Request struct {
	Query    string
	DocIDs   []string
	Contents []string
	TopK     int
}

type Hit struct {
	ID    string
	Score float64
}

type Reranker interface {
	Rerank(ctx context.Context, req Request) ([]Hit, error)
}
