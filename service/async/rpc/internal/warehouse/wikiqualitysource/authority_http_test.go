package wikiqualitysource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikiQualityWorkerOriginalEventHTTPReceipt(t *testing.T) {
	batch, _, authority := testBatch(t)
	event := batch.Events[1].Event
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/internal/v1/knowledge/wiki-quality/events/quality-event-2" ||
			r.Header.Get("Authorization") != "Bearer fixture-worker-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		encoded, _ := canonical(authority.raw)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
			"event_id":         event.EventID,
			"event_json":       string(authority.raw),
			"event_raw_sha256": authority.sha,
			"event_jcs_sha256": digest(encoded),
		}})
	}))
	defer server.Close()
	a, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	raw, sha, err := a.ReadQualityEvent(context.Background(), event.EventID)
	if err != nil || string(raw) != string(authority.raw) || sha != authority.sha ||
		proveOriginalEvent(event, raw, sha, batch.Events[1].InputHash) != nil {
		t.Fatalf("RTW private original bytes or JCS lost: %v", err)
	}
	for _, invalid := range []string{"http://worker/path", "http://user:pass@worker", "http://worker?token=x"} {
		if _, err := NewHTTPAuthority(invalid, "fixture-worker-token"); !errors.Is(err, ErrContract) {
			t.Fatalf("invalid worker endpoint allowed: %s %v", invalid, err)
		}
	}
	if _, err := NewHTTPAuthority(server.URL, "worker\nheader"); !errors.Is(err, ErrContract) {
		t.Fatal("unsafe worker bearer allowed")
	}
	if _, _, err := a.ReadQualityEvent(context.Background(), "../../wrong"); !errors.Is(err, ErrContract) {
		t.Fatal("invalid event path allowed")
	}
}

func TestWikiQualityWorkerHTTPRejectsRedirectAndForgedOriginalReceipt(t *testing.T) {
	batch, _, authority := testBatch(t)
	for _, scenario := range []struct {
		name     string
		redirect bool
		wrongSHA bool
		trailing bool
	}{
		{name: "redirect", redirect: true},
		{name: "forged jcs", wrongSHA: true},
		{name: "trailing body", trailing: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario.redirect {
					http.Redirect(w, r, "/other", http.StatusFound)
					return
				}
				encoded, _ := canonical(authority.raw)
				jcsSHA := digest(encoded)
				if scenario.wrongSHA {
					jcsSHA = strings.Repeat("0", 64)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
					"event_id":         batch.Events[1].Event.EventID,
					"event_json":       string(authority.raw),
					"event_raw_sha256": authority.sha,
					"event_jcs_sha256": jcsSHA,
				}})
				if scenario.trailing {
					_, _ = w.Write([]byte(`{"extra":true}`))
				}
			}))
			defer server.Close()
			a, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := a.ReadQualityEvent(context.Background(), batch.Events[1].Event.EventID); !errors.Is(err, ErrContract) {
				t.Fatalf("forged RTW worker receipt accepted: %v", err)
			}
		})
	}
}
