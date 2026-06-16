package router

import (
	"errors"
	"net/http"

	"sea/agent"
	"sea/middleware"
	searchsvc "sea/service"
	"sea/skillsys"
	rectypes "sea/type"
	"sea/zlog"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func NewRouter(
	reg *skillsys.Registry,
	reco *agent.RecoAgent,
	contentSearch *agent.ContentSearchAgent,
	titleSearch *searchsvc.ArticleTitleSearchService,
	authorSearch *searchsvc.AuthorNameSearchService,
	onboardingQuestionnaire *searchsvc.OnboardingQuestionnaireService,
	recoEvaluation *searchsvc.RecoEvaluationService,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.TraceMiddleware())

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) {
		OK(c, gin.H{"status": "ok"})
	})

	r.POST("/api/v1/docs/ingest", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		_, out, err := reg.Invoke(c.Request.Context(), "doc_ingest", body)
		if err != nil {
			zlog.L().Error("document ingest failed", zap.Error(err))
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
			return
		}

		OK(c, out)
	})

	r.POST("/api/v1/reco/recommend", func(c *gin.Context) {
		var req agent.RecommendRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := reco.Recommend(c.Request.Context(), req)
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		if recoEvaluation != nil {
			if err := recoEvaluation.RecordRecommendation(c.Request.Context(), req, resp); err != nil {
				zlog.L().Warn("record recommendation evaluation failed", zap.Error(err))
			}
		}
		OK(c, resp)
	})

	r.POST("/api/v1/reco/events", func(c *gin.Context) {
		var req struct {
			Events []rectypes.RecoEventLog `json:"events"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		if recoEvaluation == nil {
			OK(c, gin.H{"accepted": 0})
			return
		}

		events := make([]rectypes.RecoEventLog, 0, len(req.Events))
		events = append(events, req.Events...)
		resp, err := recoEvaluation.RecordEvents(c.Request.Context(), rectypes.RecoEventRequest{Events: events})
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
			return
		}
		OK(c, resp)
	})

	r.GET("/api/v1/admin/reco/evaluation/summary", func(c *gin.Context) {
		if recoEvaluation == nil {
			OK(c, gin.H{"metric_values": []any{}})
			return
		}
		summary, err := recoEvaluation.Summary(
			c.Request.Context(),
			c.DefaultQuery("surface", "dashboard_recommend"),
			c.DefaultQuery("window", "24h"),
		)
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
			return
		}
		OK(c, summary)
	})

	r.POST("/api/v1/search", func(c *gin.Context) {
		var req agent.ContentSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := contentSearch.Search(c.Request.Context(), req)
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		OK(c, resp)
	})

	r.POST("/api/v1/search/title", func(c *gin.Context) {
		var req searchsvc.StructuredSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := titleSearch.Search(c.Request.Context(), req)
		if err != nil {
			if errors.Is(err, searchsvc.ErrSourceMetadataUnavailable) {
				Fail(c, http.StatusServiceUnavailable, middleware.StatusError, err.Error(), resp.TraceID)
				return
			}
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		OK(c, resp)
	})

	r.POST("/api/v1/search/authors", func(c *gin.Context) {
		var req searchsvc.StructuredSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := authorSearch.Search(c.Request.Context(), req)
		if err != nil {
			if errors.Is(err, searchsvc.ErrSourceMetadataUnavailable) {
				Fail(c, http.StatusServiceUnavailable, middleware.StatusError, err.Error(), resp.TraceID)
				return
			}
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		OK(c, resp)
	})

	r.POST("/api/v1/onboarding/questionnaire", func(c *gin.Context) {
		var req searchsvc.OnboardingQuestionnaireRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := onboardingQuestionnaire.Submit(c.Request.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, searchsvc.ErrOnboardingMemoryUnavailable):
				Fail(c, http.StatusServiceUnavailable, middleware.StatusError, err.Error(), resp.TraceID)
			case errors.Is(err, searchsvc.ErrInvalidOnboardingAnswer):
				Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), resp.TraceID)
			default:
				Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			}
			return
		}
		OK(c, resp)
	})

	r.GET("/api/v1/tools", func(c *gin.Context) {
		OK(c, gin.H{"tools": reg.List()})
	})

	return r
}
