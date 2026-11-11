package recall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

func knowledgeFixtureServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestKnowledgeRecallerReturnsRealUnits(t *testing.T) {
	payload := `{"nodes":[
		{"props":{"node_id":"U-tcp","type":"unit","title":"TCP 连接管理","revision_id":2}},
		{"props":{"node_id":"U-congestion","type":"unit","title":"拥塞控制","revision_id":2}}
	],"edges":[]}`
	server := knowledgeFixtureServer(t, 200, payload)
	defer server.Close()
	recaller := NewKnowledgeRecaller(server.URL, "tok", "course:test", 10)
	result, err := recaller.Recall(context.Background(), domain.RecallRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "knowledge" || len(result.Candidates) != 2 {
		t.Fatalf("结果不符: %+v", result)
	}
	first := result.Candidates[0]
	if first.ArticleID != "U-tcp" || first.Source != "knowledge" {
		t.Fatalf("首候选择身份不符: %+v", first)
	}
	if first.Extra["knowledge_module"] != true || first.Extra["revision_id"] == nil {
		t.Fatalf("可观测元数据缺失: %+v", first.Extra)
	}
	if first.Score <= result.Candidates[1].Score {
		t.Fatal("召回分应按序递减")
	}
}

func TestKnowledgeRecallerDegradesIndependently(t *testing.T) {
	server := knowledgeFixtureServer(t, 503, `{"error":"down"}`)
	defer server.Close()
	recaller := NewKnowledgeRecaller(server.URL, "tok", "course:test", 10)
	result, err := recaller.Recall(context.Background(), domain.RecallRequest{})
	if err == nil {
		t.Fatal("服务端错误应返回 error(由 Hybrid 独立降级)")
	}
	if len(result.Candidates) != 0 {
		t.Fatal("失败时不应产出候选")
	}
}

func TestKnowledgeRecallerSendsKeywordFromIntent(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("鉴权头缺失")
		}
		_, _ = w.Write([]byte(`{"nodes":[],"edges":[]}`))
	}))
	defer server.Close()
	recaller := NewKnowledgeRecaller(server.URL, "tok", "course:test", 10)
	intent := &domain.Intent{Signals: map[string]string{"query": "三次握手"}}
	if _, err := recaller.Recall(context.Background(), domain.RecallRequest{Intent: intent}); err != nil {
		t.Fatal(err)
	}
	if received["keyword"] != "三次握手" || received["node_type"] != "unit" {
		t.Fatalf("请求未携带意图检索词: %+v", received)
	}
}
