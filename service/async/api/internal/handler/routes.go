package handler

import (
	"io"
	"net/http"

	asynclogic "sea/service/async/api/internal/logic"
	"sea/service/common/response"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(events *asynclogic.EventLogic) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) { response.OK(c, gin.H{"status": "ok", "service": "async"}) })

	r.POST("/internal/events", func(c *gin.Context) {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 10<<20))
		if err != nil {
			response.Fail(c, http.StatusBadRequest, http.StatusBadRequest, err.Error(), "")
			return
		}
		resp, err := events.Submit(c.Request.Context(), c.GetHeader("Content-Type"), body)
		if err != nil {
			response.Fail(c, http.StatusBadRequest, http.StatusBadRequest, err.Error(), "")
			return
		}
		response.OK(c, resp)
	})
	r.GET("/internal/processing/state", func(c *gin.Context) { response.OK(c, gin.H{"state": "healthy"}) })
	r.GET("/internal/projections/state", func(c *gin.Context) { response.OK(c, gin.H{"state": "healthy"}) })
	return r
}
