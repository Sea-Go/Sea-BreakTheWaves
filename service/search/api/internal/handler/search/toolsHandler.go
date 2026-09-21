// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package search

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/logic/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func ToolsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := search.NewToolsLogic(r.Context(), svcCtx)
		err := l.Tools()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.Ok(w)
		}
	}
}
