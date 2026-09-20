package handler

import (
	"errors"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/middleware"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/response"
	searchlogic "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/logic"
	searchtypes "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(logic *searchlogic.SearchLogic) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), middleware.TraceMiddleware())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) { response.OK(c, gin.H{"status": "ok", "service": "search"}) })

	r.POST("/api/v1/search", func(c *gin.Context) {
		var req searchtypes.ContentSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.Search(c.Request.Context(), req)
		if err != nil {
			response.Fail(c, http.StatusInternalServerError, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		response.OK(c, resp)
	})

	r.POST("/api/v1/search/title", func(c *gin.Context) {
		var req searchtypes.StructuredSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.SearchTitle(c.Request.Context(), req)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, searchtypes.ErrSourceMetadataUnavailable) {
				status = http.StatusServiceUnavailable
			}
			response.Fail(c, status, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		response.OK(c, resp)
	})

	r.POST("/api/v1/search/authors", func(c *gin.Context) {
		var req searchtypes.StructuredSearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.SearchAuthors(c.Request.Context(), req)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, searchtypes.ErrSourceMetadataUnavailable) {
				status = http.StatusServiceUnavailable
			}
			response.Fail(c, status, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		response.OK(c, resp)
	})

	r.POST("/api/v1/onboarding/questionnaire", func(c *gin.Context) {
		var req searchtypes.OnboardingQuestionnaireRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, http.StatusBadRequest, middleware.ErrInvalidArgs, err.Error(), "")
			return
		}
		resp, err := logic.Onboarding(c.Request.Context(), req)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, searchtypes.ErrOnboardingMemoryUnavailable):
				status = http.StatusServiceUnavailable
			case errors.Is(err, searchtypes.ErrInvalidOnboardingAnswer):
				status = http.StatusBadRequest
			}
			response.Fail(c, status, middleware.StatusError, err.Error(), resp.TraceID)
			return
		}
		response.OK(c, resp)
	})

	r.GET("/api/v1/search/tools", func(c *gin.Context) {
		response.OK(c, gin.H{"tools": logic.Tools()})
	})
	return r
}
