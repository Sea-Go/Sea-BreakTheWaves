package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	asynclient "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/asyncclient"
	recommendlogic "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/api/internal/logic"
	recommendclient "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/recommendclient"

	"github.com/gin-gonic/gin"
)

func TestRecommendRouterHealthAndLegacyGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reco := recommendclient.New(recommendclient.Dependencies{})
	r := NewRouter(recommendlogic.NewRecommendLogic(reco, asynclient.New("")))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/reco/recommend", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("legacy status = %d", rec.Code)
	}
}

func TestRecommendRouterSkillRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reco := recommendclient.New(recommendclient.Dependencies{})
	r := NewRouter(recommendlogic.NewRecommendLogic(reco, asynclient.New("")))
	req := httptest.NewRequest(http.MethodGet, "/api/v2/admin/skills", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("skills status = %d", rec.Code)
	}
}
