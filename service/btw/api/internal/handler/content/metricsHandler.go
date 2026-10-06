// Code scaffolded by goctl. Safe to edit.
package content

import (
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/logic/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/zeromicro/go-zero/rest/httpx"
	"net/http"
)

func MetricsHandler(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler, err := content.NewMetricsLogic(r.Context(), s).Metrics()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		handler.ServeHTTP(w, r)
	}
}
