// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package recommendadmin

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/logic/recommendadmin"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func EvaluationSummaryHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := recommendadmin.NewEvaluationSummaryLogic(r.Context(), svcCtx)
		err := l.EvaluationSummary()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.Ok(w)
		}
	}
}
