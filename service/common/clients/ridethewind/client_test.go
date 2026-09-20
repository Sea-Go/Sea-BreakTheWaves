package ridethewind

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
)

func TestWorkerBoundary(t *testing.T) {
	var routes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker" {
			t.Error("worker token missing")
		}
		routes = append(routes, r.URL.Path)
		var data any
		switch r.URL.Path {
		case "/internal/v1/knowledge/revisions/r":
			data = Revision{RevisionId: "r", Content: "immutable body", ContentHash: "hash"}
		case "/internal/v1/knowledge/releases/l":
			data = Release{ReleaseId: "l", SourceRevisionIds: []string{"r"}, ManifestHash: "fixed"}
		case "/internal/v1/knowledge/builds/b/claim":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["BuildId"]; ok {
				t.Error("Go path field leaked into JSON")
			}
			if body["generation"] != float64(3) || body["lease_epoch"] != float64(2) {
				t.Error("fence fields changed")
			}
			data = Build{BuildId: "b", State: "BUILDING", Generation: 3, LeaseEpoch: 2}
		case "/internal/v1/knowledge/builds/b/results":
			w.WriteHeader(409)
			return
		case "/internal/v1/knowledge/compiles/c/results":
			data = Compile{CompileId: "c", State: "ACCEPTED", RevisionId: "r", ResultHash: "result"}
		case "/internal/v1/knowledge/revisions/wrong":
			data = Revision{RevisionId: "different"}
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": data})
	}))
	defer server.Close()
	c, _ := New(httpclient.Config{BaseURL: server.URL, Token: "worker"})
	if r, e := c.GetRevision(context.Background(), "r"); e != nil || r.Content != "immutable body" {
		t.Fatalf("revision: %+v %v", r, e)
	}
	if _, e := c.GetRelease(context.Background(), "l"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.ClaimBuild(context.Background(), ClaimBuildReq{BuildId: "b", Generation: 3, LeaseEpoch: 2}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.AcceptBuild(context.Background(), AcceptBuildReq{BuildId: "b"}); e == nil {
		t.Fatal("lost conflict")
	}
	if _, e := c.AcceptCompile(context.Background(), AcceptCompileReq{CompileId: "c"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.GetRevision(context.Background(), "wrong"); e == nil {
		t.Fatal("accepted wrong revision")
	}
	if len(routes) != 6 {
		t.Fatal("unexpected retry", routes)
	}
}
func TestWorkerCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	c, _ := New(httpclient.Config{BaseURL: s.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, e := c.GetRevision(ctx, "r"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", e)
	}
}

func TestClaimRejectsMismatchedFence(t *testing.T) {
	for _, field := range []string{"generation", "attempt", "lease", "cancel", "hash", "state", "expiry"} {
		t.Run(field, func(t *testing.T) {
			q := ClaimBuildReq{BuildId: "b", Generation: 3, AttemptId: "a", LeaseEpoch: 2, CancelVersion: 1, ManifestHash: "hash", LeaseExpiresAt: "2026-09-14T12:00:00Z"}
			v := Build{BuildId: q.BuildId, Generation: q.Generation, AttemptId: q.AttemptId, LeaseEpoch: q.LeaseEpoch, CancelVersion: q.CancelVersion, ManifestHash: q.ManifestHash, State: "BUILDING", LeaseExpiresAt: q.LeaseExpiresAt}
			switch field {
			case "generation":
				v.Generation++
			case "attempt":
				v.AttemptId = "other"
			case "lease":
				v.LeaseEpoch++
			case "cancel":
				v.CancelVersion++
			case "hash":
				v.ManifestHash = "other"
			case "state":
				v.State = "READY"
			case "expiry":
				v.LeaseExpiresAt = "other"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "ok", "data": v})
			}))
			defer server.Close()
			c, _ := New(httpclient.Config{BaseURL: server.URL})
			if _, e := c.ClaimBuild(context.Background(), q); e == nil {
				t.Fatalf("accepted wrong %s receipt", field)
			}
		})
	}
}

func TestClaimAcceptsRTWAllocatedBuildFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ClaimBuildReq
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.LeaseEpoch != 0 ||
			request.AttemptId != "dc-attempt" {
			t.Errorf("RTW allocation request changed: %+v %v", request, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "msg": "success", "data": Build{
			BuildId: "build", Generation: 2, AttemptId: "dc-attempt", LeaseEpoch: 7,
			ManifestHash: "manifest", State: "BUILDING", LeaseExpiresAt: "lease"}})
	}))
	defer server.Close()
	client, err := New(httpclient.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := client.ClaimBuild(context.Background(), ClaimBuildReq{BuildId: "build",
		Generation: 2, AttemptId: "dc-attempt", LeaseEpoch: 0, ManifestHash: "manifest",
		LeaseExpiresAt: "lease"})
	if err != nil || claim.LeaseEpoch != 7 {
		t.Fatalf("client rejected RTW-issued build epoch: %+v %v", claim, err)
	}
}
