package handler

import (
	"net/http"

	recommend "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/handler/recommend"
	recommendadmin "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/handler/recommendadmin"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/svc"
	"github.com/zeromicro/go-zero/rest"
)

func RegisterHandlers(server *rest.Server, serverCtx *svc.ServiceContext) {
	server.AddRoutes([]rest.Route{
		{Method: http.MethodPost, Path: "/recommend", Handler: recommend.RecommendHandler(serverCtx)},
		{Method: http.MethodPost, Path: "/recommend/stream", Handler: recommend.RecommendStreamHandler(serverCtx)},
		{Method: http.MethodPost, Path: "/events", Handler: recommend.EventsHandler(serverCtx)},
		{Method: http.MethodGet, Path: "/admin/skills", Handler: recommend.SkillListHandler(serverCtx)},
	}, rest.WithPrefix("/api/v2"))
	server.AddRoutes([]rest.Route{
		{Method: http.MethodGet, Path: "/reco/evaluation/summary", Handler: recommendadmin.EvaluationSummaryHandler(serverCtx)},
	}, rest.WithPrefix("/api/v1/admin"))
}
