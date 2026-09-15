package wikiqualitysource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRTWFixedFactSetPrivateHTTPCarriesOriginalAndSeparatePayloadJCS(t *testing.T) {
	event, original := staticFactSetGolden(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/internal/v1/knowledge/wiki-fact-sets/events/"+event.EventID ||
			r.Header.Get("Authorization") != "Bearer fixture-worker-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
			"event_id":            event.EventID,
			"event_json":          string(original),
			"event_raw_sha256":    factSetGoldenRawSHA,
			"event_jcs_sha256":    factSetGoldenEventJCS,
			"fact_set_jcs_sha256": factSetGoldenPayloadJCS,
		}})
	}))
	defer server.Close()
	authority, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := authority.ReadFactSetEvent(context.Background(), event.EventID)
	if err != nil || !bytes.Equal(proof.EventJSON, original) ||
		proof.EventRawSHA256 != factSetGoldenRawSHA ||
		proof.EventJCSSHA256 != factSetGoldenEventJCS ||
		proof.FactSetJCSSHA256 != factSetGoldenPayloadJCS {
		t.Fatalf("RTW private FactSet bytes/payload hash changed: %+v %v", proof, err)
	}
	if _, err := authority.ReadFactSetEvent(context.Background(), "../../invalid"); !errors.Is(err, ErrContract) {
		t.Fatal("unsafe RTW private FactSet EventID path allowed")
	}
}

func TestRTWFixedFactSetPrivateHTTPRejectsRedirectAndForgedPayload(t *testing.T) {
	event, original := staticFactSetGolden(t)
	for _, scenario := range []struct {
		name     string
		redirect bool
		badSHA   bool
	}{
		{name: "redirect", redirect: true},
		{name: "wrong payload JCS SHA", badSHA: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario.redirect {
					http.Redirect(w, r, "/elsewhere", http.StatusFound)
					return
				}
				payloadSHA := factSetGoldenPayloadJCS
				if scenario.badSHA {
					payloadSHA = strings.Repeat("0", 64)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]string{
					"event_id":            event.EventID,
					"event_json":          string(original),
					"event_raw_sha256":    factSetGoldenRawSHA,
					"event_jcs_sha256":    factSetGoldenEventJCS,
					"fact_set_jcs_sha256": payloadSHA,
				}})
			}))
			defer server.Close()
			authority, err := NewHTTPAuthority(server.URL, "fixture-worker-token")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := authority.ReadFactSetEvent(context.Background(), event.EventID); !errors.Is(err, ErrContract) {
				t.Fatalf("RTW private FactSet redirect or forged payload SHA accepted: %v", err)
			}
		})
	}
}
