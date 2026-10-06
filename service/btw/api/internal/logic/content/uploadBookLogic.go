// Code scaffolded by goctl. Safe to edit.
package content

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"io"
	"path/filepath"
	"strings"
	"time"
)

type UploadBookLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUploadBookLogic(ctx context.Context, s *svc.ServiceContext) *UploadBookLogic {
	return &UploadBookLogic{ctx, s}
}
func (l *UploadBookLogic) UploadBook(req *types.BookUploadRequest, body io.Reader) (*types.Admission, error) {
	if req.RequestId == "" || req.Filename == "" {
		return nil, item.ErrInvalid
	}
	media := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(req.Filename)) {
	case ".epub":
		media = "application/epub+zip"
	case ".txt":
		media = "text/plain"
	case ".md", ".markdown":
		media = "text/markdown"
	}
	title := req.Title
	if title == "" {
		title = filepath.Base(req.Filename)
	}
	v, err := l.svcCtx.Content.Accept(l.ctx, item.ContentEvent{Producer: "book-upload", EventID: req.RequestId, SourceID: req.RequestId, Revision: 1, Operation: "upsert", Kind: "book", Title: title, MediaType: media, OccurredAt: time.Now().UTC()}, body)
	if err != nil {
		return nil, err
	}
	return &types.Admission{SourceId: v.SourceID, ItemId: v.ItemID, RevisionId: v.RevisionID, JobId: v.JobID, Duplicate: v.Duplicate, Obsolete: v.Obsolete}, nil
}
