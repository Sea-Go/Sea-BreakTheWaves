package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	recommendationv2 "sea/internal/recommendationv2"
)

func TestV2RecommendAndSummaryRoutes(t *testing.T) {
	r := NewRouter(nil, recommendationv2.NewRecommendationService(nil), nil, nil, nil, nil, nil)
	body, _ := json.Marshal(recommendationv2.RecommendRequest{
		TenantID: "tenant-a",
		User:     recommendationv2.UserIdentity{UserID: "u1"},
		TopK:     2,
		PathMode: recommendationv2.PathFast,
		Debug:    true,
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/recommend", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v2/admin/obs/summary", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("summary status = %d, body = %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v2/admin/obs/traces?user_id=u1&skill=recall.hybrid", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("traces status = %d, body = %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("recall.hybrid")) {
		t.Fatalf("traces response missing recall.hybrid: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v2/admin/skills", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("skills status = %d, body = %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("recall.hybrid")) {
		t.Fatalf("skills response missing recall.hybrid: %s", w.Body.String())
	}
}

func TestV2RecommendStreamRouteEmitsSSEEvents(t *testing.T) {
	r := NewRouter(nil, recommendationv2.NewRecommendationService(nil), nil, nil, nil, nil, nil)
	body, _ := json.Marshal(recommendationv2.RecommendRequest{
		TenantID: "tenant-a",
		User:     recommendationv2.UserIdentity{UserID: "u1"},
		TopK:     1,
		PathMode: recommendationv2.PathFast,
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/recommend/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	bodyText := w.Body.String()
	for _, want := range []string{"event:run_started", "event:step_started", "event:step_finished", "event:run_finished"} {
		if !bytes.Contains([]byte(bodyText), []byte(want)) {
			t.Fatalf("stream body missing %q: %s", want, bodyText)
		}
	}
}

func TestLegacyRecommendationEndpointsAreGone(t *testing.T) {
	r := NewRouter(nil, recommendationv2.NewRecommendationService(nil), nil, nil, nil, nil, nil)
	for _, path := range []string{"/api/v1/reco/recommend", "/api/v1/reco/events"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusGone {
			t.Fatalf("%s status = %d, want %d", path, w.Code, http.StatusGone)
		}
	}
}
