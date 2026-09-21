// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package async

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/logic/async"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func EventsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.EventBatchReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		l := async.NewEventsLogic(r.Context(), svcCtx)
		resp, err := l.Events(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
