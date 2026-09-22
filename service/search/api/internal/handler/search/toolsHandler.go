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
		resp, err := l.Tools()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, map[string]any{"tools": resp})
	}
}
