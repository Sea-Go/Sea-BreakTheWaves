// Code scaffolded by goctl. Safe to edit.
package content

import (
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/logic/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/zeromicro/go-zero/rest/httpx"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func DownloadSourceHandler(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.SourceFileRequest
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, fmt.Errorf("%w: %v", item.ErrInvalid, err))
			return
		}
		start, length := int64(0), int64(-1)
		rangeValue := r.Header.Get("Range")
		if rangeValue != "" {
			v := strings.TrimPrefix(rangeValue, "bytes=")
			parts := strings.Split(v, "-")
			if v == rangeValue || len(parts) != 2 || strings.Contains(v, ",") {
				http.Error(w, "single byte range required", 416)
				return
			}
			var err error
			start, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil || start < 0 {
				http.Error(w, "invalid range", 416)
				return
			}
			if parts[1] != "" {
				end, err := strconv.ParseInt(parts[1], 10, 64)
				if err != nil || end < start {
					http.Error(w, "invalid range", 416)
					return
				}
				length = end - start + 1
			}
		}
		reader, rev, err := content.NewDownloadSourceLogic(r.Context(), s).DownloadSource(req.Revision, start, length)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		defer reader.Close()
		remaining := rev.Bytes - start
		if length < 0 || length > remaining {
			length = remaining
		}
		w.Header().Set("Content-Type", rev.MediaType)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if rangeValue != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, rev.Bytes))
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = io.CopyN(w, reader, length)
	}
}
