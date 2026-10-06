package recommendation

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

type Search interface {
	Candidates(context.Context, item.Request) (item.Result, error)
	Search(context.Context, item.Request) (item.Result, error)
	Generate(context.Context, item.Request, bool, func(item.StreamEvent) error) (item.Result, error)
	Scenario(item.Request) (item.Scenario, error)
}
type Service struct {
	Search Search
	Reader item.Reader
}

func (s *Service) Recommend(ctx context.Context, req item.Request, emit func(item.StreamEvent) error) (item.Result, error) {
	cfg, e := s.Search.Scenario(req)
	if e != nil {
		return item.Result{}, e
	}
	if cfg.Delivery == "plan" {
		return s.Search.Generate(ctx, req, true, emit)
	}
	if cfg.Delivery != "list" || emit != nil {
		return item.Result{}, item.ErrInvalid
	}
	out, e := s.Search.Candidates(ctx, req)
	if e != nil {
		return out, e
	}
	// Website lists represent root articles, even when a semantic child produced the match.
	ids := []string{}
	for _, h := range out.Hits {
		ids = append(ids, h.Item.RootID)
	}
	roots, e := s.Reader.Resolve(ctx, ids, cfg.Kinds)
	if e != nil {
		return out, e
	}
	seen := map[string]bool{}
	hits := []item.Hit{}
	for _, h := range out.Hits {
		root, ok := roots[h.Item.RootID]
		if !ok || root.RevisionID != h.Item.RevisionID {
			continue
		}
		key := root.DuplicateID
		if key == "" {
			key = root.ID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		root.Location = h.Item.Location
		h.Item = root
		hits = append(hits, h)
	}
	limit := req.Limit
	if limit == 0 {
		limit = cfg.Limit
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out.Hits = hits
	out.Path = append(out.Path, "list_dedup")
	return out, nil
}
