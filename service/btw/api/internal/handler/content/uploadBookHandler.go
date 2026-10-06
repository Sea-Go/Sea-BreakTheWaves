// Code scaffolded by goctl. Safe to edit.
package content

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/logic/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
	"net/http"
)

// UploadBookHandler accepts a raw file body; metadata is in the query string.
func UploadBookHandler(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		req := types.BookUploadRequest{RequestId: q.Get("request_id"), Filename: q.Get("filename"), Title: q.Get("title")}
		out, err := content.NewUploadBookLogic(r.Context(), s).UploadBook(&req, http.MaxBytesReader(w, r.Body, s.Config.Limits.UploadBytes))
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.WriteJsonCtx(r.Context(), w, http.StatusAccepted, out)
	}
}
