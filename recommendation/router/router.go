package router

import (
	"errors"
	"net/http"

	"sea/agent"
	recommendationv2 "sea/internal/recommendationv2"
	"sea/middleware"
	searchsvc "sea/service"
	"sea/skillsys"
	"sea/zlog"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func NewRouter(
	reg *skillsys.Registry,
	recoV2 *recommendationv2.RecommendationService,
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
		Fail(c, http.StatusGone, middleware.StatusError, "legacy /api/v1/reco/recommend is deprecated; use /api/v2/recommend", "")
	})

	r.POST("/api/v2/recommend", func(c *gin.Context) {
		var req recommendationv2.RecommendRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}

		resp, err := recoV2.Recommend(c.Request.Context(), req)
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		OK(c, resp)
	})

	r.POST("/api/v2/events", func(c *gin.Context) {
		var req recommendationv2.EventBatchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		OK(c, recoV2.RecordEvents(c.Request.Context(), req))
	})

	r.GET("/api/v2/admin/obs/summary", func(c *gin.Context) {
		OK(c, recoV2.Summary())
	})

	r.GET("/api/v2/admin/obs/traces", func(c *gin.Context) {
		var req recommendationv2.TraceQueryRequest
		if err := c.ShouldBindQuery(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		OK(c, recoV2.Trace(req))
	})

	r.GET("/api/v2/admin/skills", func(c *gin.Context) {
		OK(c, gin.H{"skills": recoV2.ListSkills()})
	})

	r.POST("/api/v2/recommend/stream", func(c *gin.Context) {
		var req recommendationv2.RecommendRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		events, err := recoV2.StreamRecommend(c.Request.Context(), req)
		if err != nil {
			Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		for event := range events {
			c.SSEvent(event.Type, event)
			c.Writer.Flush()
		}
	})

	r.POST("/api/v1/reco/events", func(c *gin.Context) {
		Fail(c, http.StatusGone, middleware.StatusError, "legacy /api/v1/reco/events is deprecated; use /api/v2/events", "")
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
