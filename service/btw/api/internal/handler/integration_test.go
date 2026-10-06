package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/content"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/blob"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/database"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/settings"
	"github.com/google/uuid"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/router"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type queryStub struct {
	mu       sync.Mutex
	requests []item.Request
	fail     bool
	wait     bool
	done     chan struct{}
}

func (s *queryStub) Close() error                                 { return nil }
func (s *queryStub) Scenario(item.Request) (item.Scenario, error) { return item.Scenario{}, nil }
func (s *queryStub) Candidates(ctx context.Context, r item.Request) (item.Result, error) {
	return s.Search(ctx, r)
}
func (s *queryStub) Search(_ context.Context, r item.Request) (item.Result, error) {
	if r.Ontology == nil {
		return item.Result{}, item.ErrInvalid
	}
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()
	return item.Result{RequestID: r.RequestID, Status: "ok", Hits: []item.Hit{}, Citations: []item.Citation{}, Path: []string{"contract_fixture"}}, nil
}
func (s *queryStub) Generate(ctx context.Context, r item.Request, _ bool, emit func(item.StreamEvent) error) (item.Result, error) {
	out, e := s.Search(ctx, r)
	if e != nil {
		return out, e
	}
	if emit != nil {
		if e = emit(item.StreamEvent{Type: "delta", RequestID: r.RequestID, Delta: "part"}); e != nil {
			return out, e
		}
	}
	if s.wait {
		<-ctx.Done()
		if s.done != nil {
			close(s.done)
		}
		return out, ctx.Err()
	}
	if s.fail {
		return out, errors.New("provider failure")
	}
	out.Answer = "complete"
	return out, nil
}
func (s *queryStub) Recommend(ctx context.Context, r item.Request, emit func(item.StreamEvent) error) (item.Result, error) {
	if emit != nil {
		return s.Generate(ctx, r, true, emit)
	}
	return s.Search(ctx, r)
}
func apiFixture(t *testing.T, q *queryStub, withStore bool) (*httptest.Server, *svc.ServiceContext) {
	t.Helper()
	c := config.Config{RestConf: rest.RestConf{Host: "127.0.0.1", Port: 20731}, Config: settings.Config{Limits: settings.Limits{UploadBytes: 32 << 20, ReadBytes: 1 << 20, QueryBytes: 1 << 20}}}
	ctx := &svc.ServiceContext{Config: c, Search: q, Recommend: q}
	if withStore {
		dsn := os.Getenv("BTW_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("requires isolated PostgreSQL")
		}
		schema := "btwhttp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		db, e := database.Open(context.Background(), database.Config{DSN: dsn, Schema: schema})
		if e != nil {
			t.Fatal(e)
		}
		sql, _ := db.DB()
		t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); sql.Close() })
		objects, e := blob.NewLocal(t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		ctx.Content, e = content.New(db, objects, nil, nil, nil, content.Config{MaxUploadBytes: 32 << 20, MaxUncompressedBytes: 64 << 20, WindowRunes: 1024, LeaseSeconds: 30, MaxAttempts: 3})
		if e != nil {
			t.Fatal(e)
		}
		if e = ctx.Content.Migrate(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	server, e := rest.NewServer(c.RestConf)
	if e != nil {
		t.Fatal(e)
	}
	RegisterHandlers(server, ctx)
	mux := router.NewRouter()
	for _, route := range server.Routes() {
		if e = mux.Handle(route.Method, route.Path, ctx.Middleware(route.Handler)); e != nil {
			t.Fatal(e)
		}
	}
	httpx.SetErrorHandler(func(e error) (int, any) {
		status := 500
		if errors.Is(e, item.ErrInvalid) {
			status = 400
		}
		if errors.Is(e, item.ErrNotFound) {
			status = 404
		}
		return status, map[string]string{"error": e.Error()}
	})
	web := httptest.NewServer(mux)
	t.Cleanup(func() { web.Close(); ctx.Close() })
	return web, ctx
}
func body() string {
	return `{"request_id":"r1","scenario":"search","query":"rare term","search_level":"fast","limit":2,"ontology":{"user_id":"u1","nodes":[{"id":"n","type":"interest","label":"books","attributes":{"weight":2}}],"relations":[{"from":"u1","to":"n","type":"likes"}],"attributes":{"goal":"learn"},"vector":{"space":"external","values":[1,2]}}}`
}
func TestGeneratedRouteContractAndFinalSSE(t *testing.T) {
	q := &queryStub{}
	server, _ := apiFixture(t, q, false)
	resp, e := http.Post(server.URL+"/api/search", "application/json", strings.NewReader(body()))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("search status %d %s", resp.StatusCode, data)
	}
	q.mu.Lock()
	got := q.requests[0]
	q.mu.Unlock()
	weightJSON, _ := json.Marshal(got.Ontology.Nodes[0].Attributes["weight"])
	if got.Ontology.Vector.Space != "external" || string(weightJSON) != "2" || got.Ontology.Relations[0].Type != "likes" || got.Limit != 2 || got.SearchLevel != "fast" {
		t.Fatalf("lost contract: %+v", got)
	}
	resp, e = http.Post(server.URL+"/api/answer/stream", "application/json", strings.NewReader(body()))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), "event: delta") || !strings.Contains(string(raw), "event: final") || !strings.Contains(string(raw), `"answer":"complete"`) {
		t.Fatalf("invalid terminal stream %s", raw)
	}
	resp, e = http.Post(server.URL+"/api/search", "application/json", strings.NewReader(`{"request_id":"r1"}`))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("invalid request returned %d", resp.StatusCode)
	}
}
func TestStreamErrorAndClientCancellation(t *testing.T) {
	q := &queryStub{fail: true}
	server, _ := apiFixture(t, q, false)
	resp, e := http.Post(server.URL+"/api/answer/stream", "application/json", strings.NewReader(body()))
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), "event: error") || strings.Contains(string(raw), "event: final") {
		t.Fatalf("failure stream %s", raw)
	}
	waiting := &queryStub{wait: true, done: make(chan struct{})}
	server, _ = apiFixture(t, waiting, false)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/answer/stream", strings.NewReader(body()))
	req.Header.Set("Content-Type", "application/json")
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatal("no first event")
	}
	cancel()
	resp.Body.Close()
	select {
	case <-waiting.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not reach generation")
	}
}
func TestRawBookUploadBeyondOldLimitAndRange(t *testing.T) {
	server, ctx := apiFixture(t, &queryStub{}, true)
	raw := bytes.Repeat([]byte("z"), 17<<20)
	response, e := http.Post(server.URL+"/api/books?request_id=large-http&filename=book.txt", "application/octet-stream", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	var admission item.Admission
	if e = json.NewDecoder(response.Body).Decode(&admission); e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 202 || admission.RevisionID == "" {
		t.Fatalf("upload %d %+v", response.StatusCode, admission)
	}
	r, _ := http.NewRequest("GET", server.URL+"/api/sources/"+admission.RevisionID+"/file", nil)
	r.Header.Set("Range", "bytes=16777216-16777231")
	response, e = http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 206 || len(data) != 16 {
		t.Fatalf("range %d %d", response.StatusCode, len(data))
	}
	job, e := ctx.Content.Job(context.Background(), admission.JobID)
	if e != nil || job.State != "pending" {
		t.Fatalf("upload pretended processing complete %+v %v", job, e)
	}
}
