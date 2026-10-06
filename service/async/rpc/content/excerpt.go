package content

import (
	"bufio"
	"context"
	"errors"
	"html"
	"io"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
)

// FindExcerpt verifies a displayed lexical fragment against the immutable
// normalized section and returns its actual Unicode code-point range.
func (s *Service) FindExcerpt(ctx context.Context, loc item.Location, fragment string, maxBytes int64) (item.Location, string, error) {
	if loc.RevisionID == "" || loc.SectionID == "" || maxBytes < 1 {
		return item.Location{}, "", item.ErrInvalid
	}
	if err := s.store.Readable(ctx, loc.RevisionID); err != nil {
		return item.Location{}, "", err
	}
	fragment = html.UnescapeString(strings.ReplaceAll(strings.ReplaceAll(fragment, "<em>", ""), "</em>", ""))
	fragment = strings.TrimSpace(strings.Trim(fragment, "…"))
	target := []rune(fragment)
	if len(target) == 0 || int64(len(fragment)) > maxBytes {
		return item.Location{}, "", item.ErrInvalid
	}
	section, err := s.store.Section(ctx, loc.RevisionID, loc.SectionID)
	if err != nil {
		return item.Location{}, "", err
	}
	reader, err := s.objects.Open(ctx, section.ObjectKey, 0, -1)
	if err != nil {
		return item.Location{}, "", err
	}
	defer reader.Close()
	prefix := make([]int, len(target))
	for i, j := 1, 0; i < len(target); i++ {
		for j > 0 && target[i] != target[j] {
			j = prefix[j-1]
		}
		if target[i] == target[j] {
			j++
		}
		prefix[i] = j
	}
	matched := 0
	position := int64(0)
	stream := bufio.NewReader(reader)
	for {
		if position%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return item.Location{}, "", err
			}
		}
		r, _, err := stream.ReadRune()
		if errors.Is(err, io.EOF) {
			return item.Location{}, "", item.ErrNotFound
		}
		if err != nil {
			return item.Location{}, "", err
		}
		for matched > 0 && r != target[matched] {
			matched = prefix[matched-1]
		}
		if r == target[matched] {
			matched++
		}
		if matched == len(target) {
			found := item.Location{RevisionID: loc.RevisionID, SectionID: loc.SectionID, Start: position + 1 - int64(len(target)), End: position + 1}
			return found, fragment, nil
		}
		position++
	}
}
