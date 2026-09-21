// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package async

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/logic/async"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func ProjectionStateHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := async.NewProjectionStateLogic(r.Context(), svcCtx)
		resp, err := l.ProjectionState()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
