// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package search

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/logic/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/gozero/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func SearchTitleHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.StructuredSearchReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		l := search.NewSearchTitleLogic(r.Context(), svcCtx)
		resp, err := l.SearchTitle(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
