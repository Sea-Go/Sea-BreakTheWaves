package content

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

// Prepare checks the configured indexes of this deployment. It does not
// publish a model or change a process-wide index selector.
func (s *Service) Prepare(ctx context.Context) error {
	if s.index == nil || s.config.Retrieval.Model == "" {
		return item.ErrUnavailable
	}
	if err := s.index.Ensure(ctx, s.config.Retrieval); err != nil {
		return err
	}
	if s.lexical != nil {
		if err := s.lexical.Ensure(ctx); err != nil {
			return err
		}
	}
	if s.graph != nil {
		if err := s.graph.Ensure(ctx); err != nil {
			return err
		}
	}
	return nil
}

// BuildIndexes copies visible content into this deployment's configured
// indexes, then replays content changes which occurred during the scan.
func (s *Service) BuildIndexes(ctx context.Context) (int64, error) {
	if s.encoder == nil {
		return 0, item.ErrUnavailable
	}
	if err := s.Prepare(ctx); err != nil {
		return 0, err
	}
	head, err := s.store.Head(ctx)
	if err != nil {
		return 0, err
	}
	cursor := head.Sequence
	after := ""
	for {
		roots, err := s.store.VisibleRoots(ctx, after, 100)
		if err != nil {
			return cursor, err
		}
		if len(roots) == 0 {
			break
		}
		for _, root := range roots {
			if err := s.indexRevision(ctx, root.Serving); err != nil {
				return cursor, err
			}
			if err := s.projectRevision(ctx, root.Serving); err != nil {
				return cursor, err
			}
			after = root.ID
		}
	}
	for {
		changes, err := s.store.Changes(ctx, cursor, 100)
		if err != nil {
			return cursor, err
		}
		if len(changes) == 0 {
			return cursor, nil
		}
		for _, change := range changes {
			if err := s.index.Delete(ctx, s.config.Retrieval, change.RootID); err != nil {
				return cursor, err
			}
			if !change.Deleted {
				current, err := s.store.Item(ctx, change.RootID)
				if err == nil {
					err = s.indexRevision(ctx, current.RevisionID)
					if err == nil {
						err = s.projectRevision(ctx, current.RevisionID)
					}
				}
				if err != nil && !errors.Is(err, item.ErrNotFound) {
					return cursor, err
				}
			}
			cursor = change.Sequence
		}
	}
}

func (s *Service) indexRevision(ctx context.Context, revision string) error {
	after := ""
	for {
		items, err := s.store.ItemsForRevision(ctx, revision, after, 100)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, value := range items {
			if value.ID == value.RootID {
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
						windowReader := bufio.NewReader(reader)
						offset := int64(0)
						for {
							window, next, err := readWindow(windowReader, s.config.WindowRunes, offset)
							if err != nil {
								reader.Close()
								return err
							}
							if window == "" {
								break
							}
							if err := s.encodeWindow(ctx, s.config.Retrieval, value, section.ID, offset, window); err != nil {
								reader.Close()
								return err
							}
							offset = next
						}
						reader.Close()
						ordinal = section.Order
					}
				}
			} else {
				text, err := s.Read(ctx, value.Location, int64(s.config.WindowRunes)*4)
				if err != nil {
					return err
				}
				if err := s.encodeWindow(ctx, s.config.Retrieval, value, value.Location.SectionID, value.Location.Start, text); err != nil {
					return err
				}
			}
			after = value.ID
		}
	}
}

func (s *Service) ExportSamples(ctx context.Context, out io.Writer) error {
	encoder := json.NewEncoder(out)
	after := ""
	for {
		rows, err := s.store.Samples(ctx, after, 500)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			var sample item.Sample
			if err := json.Unmarshal([]byte(row.Payload), &sample); err != nil {
				return err
			}
			if err := encoder.Encode(sample); err != nil {
				return err
			}
			after = row.ID
		}
	}
}
