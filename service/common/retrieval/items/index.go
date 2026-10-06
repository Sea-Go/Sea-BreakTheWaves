// Package items indexes retrieval representations belonging to whole items.
package items

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

type Record struct {
	Match      []float32
	ID         string
	ItemID     string
	RootID     string
	RootKind   string
	RevisionID string
	Location   item.Location
	Encoding   inference.Segment
}
type Candidate struct {
	Approximate bool
	ItemID      string
	RevisionID  string
	Score       float64
	Location    item.Location
}
type Index interface {
	Ensure(context.Context, item.Retrieval) error
	Upsert(context.Context, item.Retrieval, []Record) error
	Search(context.Context, item.Retrieval, string, inference.Encoding, []string, int) ([]Candidate, error)
	Delete(context.Context, item.Retrieval, string) error
	Prune(context.Context, item.Retrieval, string) error
}
