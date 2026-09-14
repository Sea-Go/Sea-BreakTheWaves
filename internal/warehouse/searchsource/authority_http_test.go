package searchsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAuthorityReadsOnlyMatchingRTWFrozenEvent(t *testing.T) {
	event, raw, sha, _ := fixtureEvent(t, fixtureJudgment())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path !=
			"/internal/v1/knowledge/search-judgments/events/"+event.EventID ||
			r.Header.Get("Authorization") != "Bearer fixture-worker-token" {
			t.Errorf("wrong RTW private request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"event_id":%q,"event_json":%q,"event_sha256":%q}}`,
			event.EventID, string(raw), sha)
	}))
	defer server.Close()
	authority, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	got, gotSHA, err := authority.ReadJudgmentEvent(context.Background(), event.EventID)
	if err != nil || string(got) != string(raw) || gotSHA != sha {
		t.Fatalf("RTW original bytes changed: %s %s %v", got, gotSHA, err)
	}
	if _, _, err := authority.ReadJudgmentEvent(context.Background(), "../other"); !errors.Is(err, ErrContract) {
		t.Fatalf("unsafe event ID accepted: %v", err)
	}
}

func TestHTTPAuthorityDoesNotForwardBearerOnRedirect(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()
	authority, err := NewHTTPAuthority(source.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.ReadJudgmentEvent(context.Background(), "event-1"); !errors.Is(err, ErrContract) || redirected {
		t.Fatalf("authority redirect followed: %v destination=%t", err, redirected)
	}
	for _, address := range []string{"", "file:///tmp/rtw", "https://user:pass@example.test/", "https://example.test/other"} {
		if _, err := NewHTTPAuthority(address, "token"); !errors.Is(err, ErrContract) {
			t.Fatalf("unsupported authority URL %q: %v", address, err)
		}
	}
	if _, err := NewHTTPAuthority(source.URL, strings.TrimSpace("")); !errors.Is(err, ErrContract) {
		t.Fatalf("empty RTW worker token accepted: %v", err)
	}
}
