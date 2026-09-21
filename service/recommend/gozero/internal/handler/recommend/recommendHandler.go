// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.2

package recommend

import (
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/logic/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func RecommendHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RecommendReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		l := recommend.NewRecommendLogic(r.Context(), svcCtx)
		resp, err := l.Recommend(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}
