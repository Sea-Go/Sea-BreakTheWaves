package recommend

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/gozero/internal/types"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func RecommendStreamHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RecommendReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		events, err := svcCtx.Recommend.StreamRecommend(r.Context(), recommendclient.RecommendRequest{
			TenantID: req.TenantId, RequestID: req.RequestId, Scenario: req.Scenario,
			Channel: req.Channel, User: recommendclient.UserIdentity{UserID: req.UserId}, Query: req.Query,
			TopK: int(req.TopK), PathMode: req.PathMode, Debug: req.Debug,
		})
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
		for event := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, event.RequestID)
			flusher.Flush()
		}
	}
}
