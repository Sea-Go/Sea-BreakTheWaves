package vectorstore

import "context"

type Candidate struct {
	ID         string  `json:"id"`
	ArticleID  string  `json:"article_id"`
	Score      float32 `json:"score"`
	Collection string  `json:"collection"`
}

type Filter struct {
	ArticleIDs []string
	Expression string
	Limit      int
}

type Store interface {
	Search(ctx context.Context, collection string, vector []float32, filter Filter) ([]Candidate, error)
}
