package content

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	idx "github.com/Sea-Go/Sea-BreakTheWaves/service/common/retrieval/items"
	"github.com/google/uuid"
)

// ExactMulti compares query vectors against one ANN-nominated source window.
// It makes no claim to score every token in the complete source.
func (s *Service) ExactMulti(ctx context.Context, value item.Item, loc item.Location, query [][]float32) (float64, error) {
	if len(query) == 0 || loc.SectionID == "" || loc.RevisionID != value.RevisionID {
		return 0, item.ErrInvalid
	}
	if err := s.store.Readable(ctx, value.RevisionID); err != nil {
		return 0, err
	}
	base := loc.Start
	if value.ID == value.RootID {
		width := int64(s.config.WindowRunes)
		if width < 1 {
			return 0, item.ErrInvalid
		}
		base = loc.Start / width * width
	}
	selection := s.config.Retrieval
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("encoding/"+value.RevisionID+value.ID+loc.SectionID+fmt.Sprint(base)+selection.Model+selection.Space)).String()
	object, err := s.store.Computation(ctx, key)
	if err != nil {
		return 0, err
	}
	reader, err := s.objects.Open(ctx, object, 0, -1)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	var encoded []inference.Encoding
	if err = json.NewDecoder(reader).Decode(&encoded); err != nil {
		return 0, err
	}
	if len(encoded) != 1 {
		return 0, item.ErrInvalid
	}
	for _, part := range encoded[0].Segments {
		if base+part.Start == loc.Start && len(part.Multi) > 0 {
			return idx.MaxSim(query, part.Multi), nil
		}
	}
	return 0, item.ErrNotFound
}
