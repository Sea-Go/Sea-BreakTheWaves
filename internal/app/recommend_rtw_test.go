package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/ridethewind"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/recommend"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

func TestRTWItemSourceConsumesWorkerSnapshotAndRevisionRefs(t *testing.T) {
	ref := artifacts.Reference([]byte("fixed-three-lane-index"))
	snapshot := ridethewind.SearchSnapshot{ModuleId: "module-1", ReleaseId: "release-1",
		Generation: 3, PublicationRevision: "7", ValidRevisionIds: []string{"revision-1"},
		Indexes: map[string]ridethewind.CitationObject{
			"dense":       {Key: ref.Key, Sha256: ref.SHA256},
			"sparse":      {Key: ref.Key, Sha256: ref.SHA256},
			"multivector": {Key: ref.Key, Sha256: ref.SHA256},
		}}
	objectHash := artifacts.Hash([]byte("source object"))
	revision := ridethewind.Revision{RevisionId: "revision-1", ModuleId: "module-1",
		EntityId: "article-1", Kind: "source", Title: "Actual metadata", MediaType: "text/markdown",
		ObjectKey: "sha256/" + objectHash, ContentHash: objectHash,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Content:   "private source body must not become item feature"}
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		var data any
		switch r.URL.Path {
		case "/internal/v1/knowledge/modules/module-1/search-snapshot":
			data = snapshot
		case "/internal/v1/knowledge/revisions/revision-1":
			data = revision
		default:
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data any    `json:"data"`
		}{200, "success", data})
	}))
	defer server.Close()
	client, err := ridethewind.New(httpclient.Config{BaseURL: server.URL, Token: "test-worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewRTWItemSource(client)
	if err != nil {
		t.Fatal(err)
	}
	release, err := recommend.BuildPool(context.Background(), source, "module-1")
	if err != nil || reads.Load() != 3 || release.Publication.ReleaseID != "release-1" ||
		release.Publication.Indexes["dense"].SHA256 != ref.SHA256 || len(release.Items) != 1 ||
		release.Items[0].ItemID != "article-1" || release.Items[0].RevisionID != "revision-1" {
		t.Fatalf("RTW Worker refs not consumed faithfully: %+v calls=%d err=%v", release, reads.Load(), err)
	}
	raw, err := json.Marshal(release)
	if err != nil || strings.Contains(string(raw), revision.Content) {
		t.Fatalf("source body leaked into item feature: %v", err)
	}
}
