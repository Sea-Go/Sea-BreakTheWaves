package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	asynclogic "github.com/Sea-Go/Sea-BreakTheWaves/service/async/api/internal/logic"

	"github.com/gin-gonic/gin"
)

func TestAsyncRouterHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(asynclogic.NewEventLogic())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
}

func TestAsyncRouterAcceptsEventBatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(asynclogic.NewEventLogic())
	body := strings.NewReader(`{"events":[{"event_id":"1"},{"event_id":"2"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/events", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":2`) {
		t.Fatalf("events response=%d body=%s", rec.Code, rec.Body.String())
	}
}
