package recommend

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/logic/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func RecommendStreamHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RecommendReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := recommend.NewRecommendStreamLogic(r.Context(), svcCtx)
		stream, err := l.RecommendStream(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.ErrorCtx(r.Context(), w, errors.New("streaming unsupported"))
			return
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				break
			}
			fmt.Fprintf(w, "event: recommend\ndata: %s\n\n", resp.String())
			flusher.Flush()
		}
	}
}
