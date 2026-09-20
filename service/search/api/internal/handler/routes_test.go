package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	searchlogic "github.com/Sea-Go/Sea-BreakTheWaves/service/search/api/internal/logic"
	searchclient "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/searchclient"

	"github.com/gin-gonic/gin"
)

func TestSearchRouterHealthAndLegacyCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(searchlogic.NewSearchLogic(searchclient.New(searchclient.Dependencies{})))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
}

func TestSearchRouterToolRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(searchlogic.NewSearchLogic(searchclient.New(searchclient.Dependencies{})))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search/tools", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools status = %d", rec.Code)
	}
}
