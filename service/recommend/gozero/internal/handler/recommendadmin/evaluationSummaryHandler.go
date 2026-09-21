package recommendadmin

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/logic/recommendadmin"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func EvaluationSummaryHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := recommendadmin.NewEvaluationSummaryLogic(r.Context(), svcCtx)
		resp, err := l.EvaluationSummary()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}
