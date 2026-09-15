package sourceproof

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRTWHistoricalFactSetUsesExplicitAdminRevisionGET(t *testing.T) {
	fixture, req := sourceReaderFixture(t)
	history := fixture.RTW.(fixtureRTW).history
	path := "/v1/knowledge/modules/" + req.ModuleID + "/wiki-pages/" +
		req.PageID + "/fact-set-revisions/" + req.FactSetRevisionID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path ||
			r.Header.Get("Authorization") != "Bearer fixture-admin-jwt" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var body map[string]any
		raw, err := json.Marshal(history.Record)
		if err == nil {
			err = json.Unmarshal(raw, &body)
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body["fact_set_jcs_sha256"] = history.FactSetJCSSHA256
		body["event_id"] = history.EventID
		body["event_raw_sha256"] = history.EventRawSHA256
		body["event_jcs_sha256"] = history.EventJCSSHA256
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "", "data": body})
	}))
	defer server.Close()
	httpReader, err := NewRTWHTTPReader(server.URL, "fixture-worker-token",
		"fixture-admin-jwt")
	if err != nil {
		t.Fatal(err)
	}
	read, err := httpReader.ReadHistoricalFactSet(context.Background(), req.ModuleID,
		req.PageID, req.FactSetRevisionID, req.WikiRevisionID)
	if err != nil || read.EventID != history.EventID ||
		read.Record.SourceScopeRevision != req.SourceScopeRevision ||
		read.FactSetJCSSHA256 != history.FactSetJCSSHA256 {
		t.Fatalf("RTW explicit historical revision HTTP lost original hashes: %+v %v", read, err)
	}
	if _, err := httpReader.ReadHistoricalFactSet(context.Background(), req.ModuleID,
		req.PageID, req.FactSetRevisionID, "wrong-wiki"); !errors.Is(err, ErrHistoricalAuthority) {
		t.Fatalf("current/substituted Wiki target passed historical GET: %v", err)
	}
	if _, err := NewRTWHTTPReader(server.URL, "fixture-worker-token",
		"fixture-admin-jwt\nsmuggled"); !errors.Is(err, ErrHistoricalAuthority) {
		t.Fatalf("admin bearer with control character accepted: %v", err)
	}
	if _, err := httpReader.ReadHistoricalFactSet(context.Background(), req.ModuleID,
		"page/other", req.FactSetRevisionID, req.WikiRevisionID); !errors.Is(err, ErrHistoricalAuthority) {
		t.Fatalf("unbounded page path accepted: %v", err)
	}
	if !strings.HasPrefix(path, "/v1/knowledge/modules/") {
		t.Fatal("historical FactSet GET did not use RTW Admin read surface")
	}
}
