package content

import (
	"context"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/catalog"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	"io"
	"strings"
	"time"
)

// GCOriginals only examines this store's generated original keys, with a commit grace period.
func (s *Service) GCOriginals(ctx context.Context, age time.Duration, apply bool, out io.Writer) error {
	if age < time.Hour {
		return fmt.Errorf("orphan grace must be at least one hour")
	}
	store, ok := s.objects.(blob.Janitor)
	if !ok {
		return item.ErrUnavailable
	}
	before := time.Now().Add(-age)
	return store.List(ctx, "originals/", func(v blob.Object) error {
		if !v.Modified.Before(before) {
			return nil
		}
		if _, e := uuid.Parse(strings.TrimPrefix(v.Key, "originals/")); e != nil {
			return nil
		}
		var count int64
		if e := s.store.DB.WithContext(ctx).Model(&catalog.Revision{}).Where("file_key=?", v.Key).Count(&count).Error; e != nil {
			return e
		}
		if count != 0 {
			return nil
		}
		action := "would_delete"
		if apply {
			if e := store.Delete(ctx, v.Key); e != nil {
				return e
			}
			action = "deleted"
		}
		_, e := fmt.Fprintf(out, "%s %s %d\n", action, v.Key, v.Size)
		return e
	})
}
