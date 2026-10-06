package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/processing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"io"
	"os"
)

func (s *Service) understand(ctx context.Context, revision, section string, start int64, title, text string) (processing.Understanding, error) {
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte(revision+section+fmt.Sprint(start)+s.config.UnderstandingModel)).String()
	raw, e := s.store.Computation(ctx, key)
	var out processing.Understanding
	if e == nil {
		e = json.Unmarshal([]byte(raw), &out)
		return out, e
	}
	if !errors.Is(e, item.ErrNotFound) {
		return out, e
	}
	out, e = s.analyzer.Understand(ctx, title, text)
	if e != nil {
		return out, e
	}
	data, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	e = s.store.SaveComputation(ctx, key, revision, "understanding", string(data))
	return out, e
}

func (s *Service) encoding(ctx context.Context, retrieval item.Retrieval, v item.Item, section string, offset int64, text string) ([]inference.Encoding, error) {
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte("encoding/"+v.RevisionID+v.ID+section+fmt.Sprint(offset)+retrieval.Model+retrieval.Space)).String()
	ref, e := s.store.Computation(ctx, key)
	if e == nil {
		r, e := s.objects.Open(ctx, ref, 0, -1)
		if e != nil {
			return nil, e
		}
		defer r.Close()
		var encoded []inference.Encoding
		e = json.NewDecoder(r).Decode(&encoded)
		return encoded, e
	}
	if !errors.Is(e, item.ErrNotFound) {
		return nil, e
	}
	encoded, e := s.encoder.Encode(ctx, retrieval.Model, retrieval.Space, []string{text})
	if e != nil {
		return nil, e
	}
	f, e := os.CreateTemp("", "btw-encoding-")
	if e != nil {
		return nil, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = json.NewEncoder(f).Encode(encoded); e != nil {
		return nil, e
	}
	n, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return nil, e
	}
	object := "computations/" + uuid.NewString() + ".json"
	if _, e = s.objects.Put(ctx, object, f, n, "application/json"); e != nil {
		return nil, e
	}
	if e = s.store.SaveComputation(ctx, key, v.RevisionID, "encoding", object); e != nil {
		return nil, e
	}
	return encoded, nil
}
