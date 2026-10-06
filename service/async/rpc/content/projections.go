package content

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/elastic"
)

// projectRevision writes external read projections before PostgreSQL serves a
// revision. Retry is idempotent; stale rows are hidden by candidate validation.
func (s *Service) projectRevision(ctx context.Context, revision string) error {
	if s.lexical == nil && s.graph == nil {
		return nil
	}
	var root item.Item
	after := ""
	for {
		rows, err := s.store.ItemsForRevision(ctx, revision, after, 100)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, v := range rows {
			if v.ID == v.RootID {
				root = v
			}
			after = v.ID
		}
	}
	if root.ID == "" {
		return fmt.Errorf("source has no root item for projection")
	}
	if s.graph != nil {
		if err := s.graph.Upsert(ctx, []item.Item{root}, nil); err != nil {
			return err
		}
	}
	if s.lexical != nil {
		ordinal := 0
		for {
			sections, err := s.store.Sections(ctx, revision, ordinal, 100)
			if err != nil {
				return err
			}
			if len(sections) == 0 {
				break
			}
			for _, section := range sections {
				reader, err := s.objects.Open(ctx, section.ObjectKey, 0, -1)
				if err != nil {
					return err
				}
				maxBytes := s.config.MaxLexicalSectionBytes
				if maxBytes <= 0 {
					maxBytes = 64 << 20
				}
				raw, readErr := io.ReadAll(io.LimitReader(reader, maxBytes+1))
				closeErr := reader.Close()
				if readErr != nil {
					return readErr
				}
				if closeErr != nil {
					return closeErr
				}
				if int64(len(raw)) > maxBytes {
					return fmt.Errorf("%w: natural section exceeds lexical indexing budget", item.ErrInvalid)
				}
				document := elastic.Document{ID: revision + ":" + section.ID, ItemID: root.ID, RootID: root.ID, RootKind: root.RootKind, Kind: root.Kind, RevisionID: revision, SectionID: section.ID, Start: 0, End: section.Runes, Title: section.Title, Summary: root.Summary, Body: string(raw), Language: root.Language}
				if err := s.lexical.Upsert(ctx, document); err != nil {
					return err
				}
				ordinal = section.Order
			}
		}
	}
	after = ""
	for {
		rows, err := s.store.ItemsForRevision(ctx, revision, after, 100)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		objects := make([]item.Item, 0, len(rows))
		for _, v := range rows {
			after = v.ID
			if v.ID == v.RootID {
				continue
			}
			objects = append(objects, v)
			if s.lexical != nil {
				text, err := s.readProjectionQuote(ctx, v.Location)
				if err != nil {
					return err
				}
				doc := elastic.Document{ID: revision + ":" + v.ID, ItemID: v.ID, RootID: v.RootID, RootKind: v.RootKind, Kind: v.Kind, RevisionID: revision, SectionID: v.Location.SectionID, Start: v.Location.Start, End: v.Location.End, Title: v.Title, Summary: v.Summary, Body: text, Language: v.Language}
				if err := s.lexical.Upsert(ctx, doc); err != nil {
					return err
				}
			}
		}
		if s.graph != nil && len(objects) > 0 {
			if err := s.graph.Upsert(ctx, objects, nil); err != nil {
				return err
			}
		}
	}
	if s.graph != nil {
		after = ""
		for {
			relations, next, err := s.store.RelationsForRevision(ctx, revision, after, 100)
			if err != nil {
				return err
			}
			if len(relations) == 0 {
				break
			}
			if err := s.graph.Upsert(ctx, nil, relations); err != nil {
				return err
			}
			after = next
		}
	}
	if s.lexical != nil {
		return s.lexical.Refresh(ctx)
	}
	return nil
}

// readProjectionQuote reads a newly processed revision directly. Public reads
// require serving visibility, which is intentionally not yet set at this stage.
func (s *Service) readProjectionQuote(ctx context.Context, loc item.Location) (string, error) {
	if loc.SectionID == "" || loc.Start < 0 || loc.End <= loc.Start {
		return "", item.ErrInvalid
	}
	section, err := s.store.Section(ctx, loc.RevisionID, loc.SectionID)
	if err != nil {
		return "", err
	}
	reader, err := s.objects.Open(ctx, section.ObjectKey, 0, -1)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	stream := bufio.NewReader(reader)
	var out strings.Builder
	for n := int64(0); n < loc.End; n++ {
		r, _, err := stream.ReadRune()
		if errors.Is(err, io.EOF) {
			return "", io.ErrUnexpectedEOF
		}
		if err != nil {
			return "", err
		}
		if n >= loc.Start {
			out.WriteRune(r)
		}
		if out.Len() > 1<<20 {
			return "", fmt.Errorf("%w: semantic quote exceeds lexical budget", item.ErrInvalid)
		}
	}
	return out.String(), nil
}
