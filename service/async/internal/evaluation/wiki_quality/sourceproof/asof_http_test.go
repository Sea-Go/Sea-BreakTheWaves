package sourceproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRTWSourceVersionWorkerHTTPFeedsDCCutoffAlignment(t *testing.T) {
	reader, pinned, dc, candidate := alignedVersionFixture(t)
	response, err := json.Marshal(struct {
		Code int                       `json:"code"`
		Msg  string                    `json:"msg"`
		Data RTWSourceVersionCandidate `json:"data"`
	}{http.StatusOK, "ok", candidate})
	if err != nil {
		t.Fatal(err)
	}
	currentResponse := response
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != rtwVersionWitnessPath ||
			r.Header.Get("Authorization") != "Bearer fixture-worker" ||
			r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			ModuleID            string `json:"module_id"`
			PageID              string `json:"page_id"`
			FactSetRevisionID   string `json:"fact_set_revision_id"`
			WikiRevisionID      string `json:"wiki_revision_id"`
			SourceScopeRevision string `json:"source_scope_revision"`
			SourceVersion       int64  `json:"source_version"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.ModuleID != pinned.ModuleID ||
			body.PageID != pinned.PageID || body.FactSetRevisionID != pinned.FactSetRevisionID ||
			body.WikiRevisionID != pinned.WikiRevisionID ||
			body.SourceScopeRevision != pinned.SourceScopeRevision || body.SourceVersion != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(currentResponse)
	}))
	defer server.Close()
	worker, err := NewHTTPSourceVersionReader(server.URL, "fixture-worker")
	if err != nil {
		t.Fatal(err)
	}
	aligned, err := reader.ReadDCCurrentAlignment(context.Background(), pinned, dc, worker)
	if err != nil || requests != 1 || aligned.SourceVersion != 4 ||
		aligned.CutoffOffset != 4 || !aligned.SourceAvailable ||
		aligned.QualityState != "not_evaluable" {
		t.Fatalf("Worker RTW V→DC ACK C private HTTP did not qualify source: %+v %v requests=%d", aligned, err, requests)
	}
	for _, trial := range []struct {
		name string
		body []byte
	}{
		{"duplicate_source_version", bytes.Replace(response,
			[]byte(`"source_version":4`), []byte(`"source_version":4,"source_version":5`), 1)},
		{"unknown_tenant", bytes.Replace(response,
			[]byte(`"code":200`), []byte(`"code":200,"tenant_id":"invented"`), 1)},
		{"malformed_payload", []byte(`{"code":200,"data":`)},
	} {
		t.Run(trial.name, func(t *testing.T) {
			currentResponse = trial.body
			if _, err := worker.ReadWikiQualitySourceVersionCandidate(context.Background(), pinned, 4); !errors.Is(err, ErrDCCutoffAlignment) {
				t.Fatalf("bad RTW Worker witness %s crossed strict source version boundary: %v", trial.name, err)
			}
		})
	}
	if _, err := NewHTTPSourceVersionReader(server.URL, "fixture-worker\nforged"); !errors.Is(err, ErrDCCutoffAlignment) {
		t.Fatalf("invalid Worker bearer entered RTW source version reader: %v", err)
	}
}

func TestRTWSourceVersionReaderRejectsRedirectBeforeAnotherWorkerOrigin(t *testing.T) {
	_, pinned, _, _ := alignedVersionFixture(t)
	secondary := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("RTW Worker bearer followed a redirect to another origin")
	}))
	defer secondary.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secondary.URL, http.StatusFound)
	}))
	defer primary.Close()
	worker, err := NewHTTPSourceVersionReader(primary.URL, "fixture-worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ReadWikiQualitySourceVersionCandidate(context.Background(), pinned, 4); !errors.Is(err, ErrDCCutoffAlignment) {
		t.Fatalf("RTW private Worker source version followed a redirect: %v", err)
	}
}
