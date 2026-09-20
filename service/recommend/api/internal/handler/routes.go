package handler

import (
	"io"
	"net/http"

	"sea/service/common/middleware"
	"sea/service/common/response"
	recommendlogic "sea/service/recommend/api/internal/logic"
	recommendtypes "sea/service/recommend/api/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(logic *recommendlogic.RecommendLogic) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.TraceMiddleware())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) { response.OK(c, gin.H{"status": "ok", "service": "recommend"}) })
	r.POST("/api/v1/reco/recommend", func(c *gin.Context) {
		response.Fail(c, http.StatusGone, middleware.StatusError, "legacy /api/v1/reco/recommend is deprecated; use /api/v2/recommend", "")
	})
	r.POST("/api/v1/reco/events", func(c *gin.Context) {
		response.Fail(c, http.StatusGone, middleware.StatusError, "legacy /api/v1/reco/events is deprecated; use /api/v2/events", "")
	})
	r.POST("/api/v2/recommend", func(c *gin.Context) {
		var req recommendtypes.RecommendRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.Recommend(c.Request.Context(), req)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		response.OK(c, resp)
	})
	r.POST("/api/v2/events", func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 10<<20))
		if err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.SubmitEvents(c.Request.Context(), c.GetHeader("Content-Type"), body)
		if err != nil {
			response.Fail(c, http.StatusBadGateway, middleware.StatusError, err.Error(), "")
			return
		}
		response.OK(c, resp)
	})
	r.GET("/api/v2/admin/obs/summary", func(c *gin.Context) { response.OK(c, logic.Summary()) })
	r.GET("/api/v2/admin/obs/traces", func(c *gin.Context) {
		var req recommendtypes.TraceQueryRequest
		if err := c.ShouldBindQuery(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		response.OK(c, logic.Trace(req))
	})
	r.GET("/api/v2/admin/skills", func(c *gin.Context) { response.OK(c, gin.H{"skills": logic.Skills()}) })
	r.POST("/api/v2/recommend/stream", func(c *gin.Context) {
		var req recommendtypes.RecommendRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		events, err := logic.Stream(c.Request.Context(), req)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
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
	r.GET("/api/v1/admin/reco/evaluation/summary", func(c *gin.Context) {
		resp, err := logic.Evaluation(c.Request.Context(), c.DefaultQuery("surface", "dashboard_recommend"), c.DefaultQuery("window", "24h"))
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), "")
			return
		}
		response.OK(c, resp)
	})
	return r
}
