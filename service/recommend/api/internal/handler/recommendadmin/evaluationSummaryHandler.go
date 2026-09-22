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
		resp, err := l.EvaluationSummary(r.FormValue("surface"), r.FormValue("window"))
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}
}
