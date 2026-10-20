package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/pipeline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// seedsRelPath 是从本包目录到仓库根种子集的相对路径（Go 工具链忽略
// testdata 通配，需显式路径；与 search_demo 测试同口径）。
const seedsRelPath = "../../../../../testdata/index/seeds"

// newTestHandler 装载冻结种子集并装配出带完整中间件链（CORS + 日志）
// 的处理器：httptest 直接驱动真实路由，不经网络。
func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	corpus, err := devseed.LoadCorpus(filepath.Clean(seedsRelPath))
	if err != nil {
		t.Fatalf("装载种子集: %v", err)
	}
	p, err := pipeline.NewDefaultPipeline(corpus.Store)
	if err != nil {
		t.Fatalf("装配管线: %v", err)
	}
	return newAPIServer(p).handler()
}

// doJSON 发一次请求并返回响应与全部正文。
func doJSON(t *testing.T, h http.Handler, method, path, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	return res, string(b)
}

// topKeys 解码 JSON 对象的顶层键集合（校验响应形态）。
func topKeys(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v；正文: %s", err, body)
	}
	return m
}

// 正例：summary 交付——200 + C-3 形态五字段，答案带 [1] 角标与
// #cit-1 锚点，引用回指候选文档。
func TestSearchSummaryDelivery(t *testing.T) {
	h := newTestHandler(t)
	res, body := doJSON(t, h, http.MethodPost, "/api/v1/search",
		`{"query":"海洋观测中观测网络建设的核心要点有哪些？","tier":"fast","delivery":"summary"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d；正文: %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	keys := topKeys(t, body)
	for _, k := range []string{"evidence_pack", "session_id", "citations", "answer", "formatted_answer"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("summary 响应缺少 %q；顶层键: %v", k, keys)
		}
	}

	var got summaryResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("解码 summary 响应: %v", err)
	}
	if !strings.HasPrefix(got.EvidencePack.QueryID, "q-") || len(got.EvidencePack.Candidates) == 0 {
		t.Fatalf("evidence_pack 异常: %+v", got.EvidencePack)
	}
	if !strings.HasPrefix(got.SessionID, "s-") {
		t.Fatalf("session_id 形态异常: %q", got.SessionID)
	}
	if len(got.Citations) == 0 || len(got.Citations) > 8 {
		t.Fatalf("citations 数量 = %d（应 1..8）", len(got.Citations))
	}
	c := got.Citations[0]
	if c.Index != 1 || c.DocKey == "" || c.RevisionID == "" || c.Quote == "" {
		t.Fatalf("citations[0] 字段不完整: %+v", c)
	}
	if c.Locator.ParaIndex < 0 || c.Locator.Quote == "" {
		t.Fatalf("citations[0].locator 不完整: %+v", c.Locator)
	}
	if !strings.Contains(got.Answer, "[1]") {
		t.Fatalf("answer 缺行内角标: %q", got.Answer)
	}
	if !strings.Contains(got.FormattedAnswer, "[1](#cit-1)") {
		t.Fatalf("formatted_answer 缺 #cit-1 锚点: %q", got.FormattedAnswer)
	}
}

// 正例：tools 交付——200 + 仅 evidence_pack（无 session/citations/answer）。
func TestSearchToolsDelivery(t *testing.T) {
	h := newTestHandler(t)
	res, body := doJSON(t, h, http.MethodPost, "/api/v1/search",
		`{"query":"海洋观测中观测网络建设的核心要点有哪些？","tier":"deep","delivery":"tools"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d；正文: %s", res.StatusCode, body)
	}
	keys := topKeys(t, body)
	if len(keys) != 1 {
		t.Fatalf("tools 响应应只有 evidence_pack 一个顶层键；实际: %v", keys)
	}
	if _, ok := keys["evidence_pack"]; !ok {
		t.Fatalf("tools 响应缺少 evidence_pack；顶层键: %v", keys)
	}

	var got toolsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("解码 tools 响应: %v", err)
	}
	if !strings.HasPrefix(got.EvidencePack.QueryID, "q-") || len(got.EvidencePack.Candidates) == 0 {
		t.Fatalf("evidence_pack 异常: %+v", got.EvidencePack)
	}
	for _, c := range got.EvidencePack.Candidates {
		if c.DocKey == "" || len(c.Evidence) == 0 {
			t.Fatalf("候选 %s 缺 doc_key 或 evidence: %+v", c.DocKey, c)
		}
	}
}

// 校验失败：空查询/未知档位/未知交付/坏 JSON 一律 400 + 错误信封。
func TestSearchBadRequest(t *testing.T) {
	h := newTestHandler(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"空查询", `{"query":"","tier":"fast","delivery":"summary"}`, "query 不能为空"},
		{"查询全空白", `{"query":"   ","tier":"fast","delivery":"summary"}`, "query 不能为空"},
		{"缺 query 字段", `{"tier":"fast","delivery":"summary"}`, "query 不能为空"},
		{"未知档位", `{"query":"海洋观测","tier":"turbo","delivery":"summary"}`, "未知档位"},
		{"未知交付", `{"query":"海洋观测","tier":"fast","delivery":"chat"}`, "未知交付形态"},
		{"坏 JSON", `{"query":`, "不是合法 JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := doJSON(t, h, http.MethodPost, "/api/v1/search", tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d；正文: %s", res.StatusCode, body)
			}
			var env struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data any    `json:"data"`
			}
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("错误信封不是 JSON: %v；正文: %s", err, body)
			}
			if env.Code != http.StatusBadRequest || !strings.Contains(env.Msg, tc.want) || env.Data != nil {
				t.Fatalf("错误信封 = %+v；期望含 %q", env, tc.want)
			}
		})
	}
}

// health：GET → 200 {"status":"ok"}；非 GET → 405。
func TestHealth(t *testing.T) {
	h := newTestHandler(t)
	res, body := doJSON(t, h, http.MethodGet, "/health", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d；正文: %s", res.StatusCode, body)
	}
	var got healthResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("解码 health 响应: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status 字段 = %q", got.Status)
	}

	res, body = doJSON(t, h, http.MethodPost, "/health", "")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health status = %d；正文: %s", res.StatusCode, body)
	}
}

// CORS：预检 204 + 头三件套；实际 POST 响应带 Allow-Origin *；
// /api/v1/search 非 POST → 405。
func TestCORSAndMethod(t *testing.T) {
	h := newTestHandler(t)
	pre := httptest.NewRequest(http.MethodOptions, "/api/v1/search", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检 status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Fatalf("Allow-Methods = %q", got)
	}

	res, body := doJSON(t, h, http.MethodPost, "/api/v1/search",
		`{"query":"海洋观测的核心要点","tier":"fast","delivery":"tools"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d；正文: %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("POST 响应 Allow-Origin = %q", got)
	}

	res, body = doJSON(t, h, http.MethodGet, "/api/v1/search", "")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/v1/search status = %d；正文: %s", res.StatusCode, body)
	}
}

// 参数解析：tier/delivery 空值取默认（balanced/summary），未知值报错。
func TestParseTierDeliveryDefaults(t *testing.T) {
	if tier, err := parseTier(""); err != nil || tier != retrieval.TierBalanced {
		t.Fatalf("空 tier 应默认 balanced: %v, %v", tier, err)
	}
	if d, err := parseDelivery(""); err != nil || d != pipeline.DeliverySummary {
		t.Fatalf("空 delivery 应默认 summary: %v, %v", d, err)
	}
	if _, err := parseTier("turbo"); err == nil {
		t.Fatal("tier turbo 应报错")
	}
	if _, err := parseDelivery("chat"); err == nil {
		t.Fatal("delivery chat 应报错")
	}
}
