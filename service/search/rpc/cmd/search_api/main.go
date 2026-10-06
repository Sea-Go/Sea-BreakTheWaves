// Command search_api 是 B1 btw-search-api 的 dev 形态：把端到端检索管线
// （internal/pipeline）暴露为 HTTP 端点，让前端 chat 页在真实 A1 网关
// 就绪前即可按 C-3 契约联调（POST /api/v1/search），从 BFF 503 退化为
// 可交互。
//
// 装载与 search_demo 同构：启动时 devseed.LoadCorpus（冻结种子集）→
// retrieval.Store → pipeline.NewDefaultPipeline（FakeEncoder + 确定性摘要
// stub），随后常驻监听。不引 go-zero 框架，纯 net/http 保持 dev 轻量；
// CORS 全开放（*），允许 http://localhost:3000 的前端页直连。
//
// 端点：
//
//	POST /api/v1/search   C-3 请求 {query, tier, delivery} → JSON 交付：
//	                      summary → {evidence_pack, session_id, citations,
//	                      answer, formatted_answer}；tools → {evidence_pack}
//	GET  /health          存活探测 → {"status":"ok"}
//
// C-3 请求侧的 module_scope/budget 在 dev 形态接受但忽略（宽松解码，
// 不校验未知字段）；错误统一 RTW 前端信封 {code, msg, data:null}。
//
// 用法（仓库根，种子集默认相对仓库根解析）：
//
//	go run ./service/search/rpc/cmd/search_api \
//	  [--port 8300] [--seeds testdata/index/seeds]
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/pipeline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// dev 形态常量。
const (
	// defaultPort 默认监听端口（前端接线口径 SEA_PRODUCT_API_SERVER_URL
	// 指 http://localhost:8300）。
	defaultPort = "8300"
	// defaultSeeds 冻结种子集目录（相对仓库根，与 search_demo 同口径）。
	defaultSeeds = "testdata/index/seeds"
	// maxBodyBytes 请求体上限（C-3 查询侧 token ≤128，1MiB 足以兜住
	// 误发的大包并防滥用）。
	maxBodyBytes = 1 << 20
	// requestTimeout 单次检索执行时限：管线为纯内存计算，此处只是
	// 兜底护栏，不承载 C-3 budget 语义。
	requestTimeout = 30 * time.Second
	// shutdownTimeout 优雅关停的宽限期。
	shutdownTimeout = 5 * time.Second
)

// searchRequest 是 POST /api/v1/search 的请求体（C-3 的 dev 子集）。
// tier/delivery 缺省时取 balanced/summary；query 非空必填。C-3 的
// module_scope/budget 字段被宽松解码接受但 dev 形态不消费。
type searchRequest struct {
	// Query 用户查询文本（TrimSpace 后非空）。
	Query string `json:"query"`
	// Tier 检索档位（fast|balanced|deep；空值取 balanced）。
	Tier string `json:"tier"`
	// Delivery 交付形态（summary|tools；空值取 summary）。
	Delivery string `json:"delivery"`
}

// citationDTO 是响应 citations 的一项（C-3 字段 + 前端挂锚点用的
// index）：revision_id/quote 与 locator 内同名字段一致，顶层重复给出
// 方便消费方免解嵌套。
type citationDTO struct {
	// Index 行内引用角标（1..8），对应答案内 [n] 与 #cit-n 锚点。
	Index int `json:"index"`
	// DocKey 被引用的候选文档键。
	DocKey string `json:"doc_key"`
	// RevisionID 该引用所属冻结修订 ID。
	RevisionID string `json:"revision_id"`
	// Locator 可验证的证据地址（section_path/para_index/quote）。
	Locator evidence.Locator `json:"locator"`
	// Quote 证据引文（≤200 rune）。
	Quote string `json:"quote"`
}

// summaryResponse 是 summary 交付的响应体（C-3 出口）。
type summaryResponse struct {
	// EvidencePack B5 组装的证据包（两种交付共有）。
	EvidencePack evidence.EvidencePack `json:"evidence_pack"`
	// SessionID 本次请求的会话标识（s-<12hex>，dev 形态逐请求生成）。
	SessionID string `json:"session_id"`
	// Citations 引用列表（角标 + doc_key + Locator）。
	Citations []citationDTO `json:"citations"`
	// Answer B6 答案文本，内嵌 [n] 行内引用角标。
	Answer string `json:"answer"`
	// FormattedAnswer Answer 的 Markdown 形态（[n] → [n](#cit-n)）。
	FormattedAnswer string `json:"formatted_answer"`
}

// toolsResponse 是 tools 交付的响应体：EvidencePack 直返，不经 B6。
type toolsResponse struct {
	// EvidencePack B5 证据包（tools 交付的唯一载荷）。
	EvidencePack evidence.EvidencePack `json:"evidence_pack"`
}

// healthResponse 是 GET /health 的响应体。
type healthResponse struct {
	Status string `json:"status"`
}

// apiServer 持有一次装配完成的管线，按请求无状态执行（Pipeline 自身
// 并发安全，一次 Execute 不与另一次共享可变数据）。
type apiServer struct {
	pipe *pipeline.Pipeline
}

// newAPIServer 用装配好的管线构造 API 服务器。
func newAPIServer(p *pipeline.Pipeline) *apiServer {
	return &apiServer{pipe: p}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("search_api: %v", err)
	}
}

// run 解析参数 → 装载种子集与管线 → 常驻监听 → 收到 SIGINT/SIGTERM 后
// 优雅关停。
func run() error {
	port := flag.String("port", defaultPort, "HTTP 监听端口")
	seeds := flag.String("seeds", defaultSeeds, "种子集目录（含 corpus/，相对仓库根）")
	flag.Parse()

	if *seeds == "" {
		return errors.New("--seeds is required（种子集目录，见 --help）")
	}
	corpus, err := devseed.LoadCorpus(*seeds)
	if err != nil {
		return fmt.Errorf("search_api: 装载种子集: %w", err)
	}
	p, err := pipeline.NewDefaultPipeline(corpus.Store)
	if err != nil {
		return fmt.Errorf("search_api: 装配管线: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: ":" + *port, Handler: newAPIServer(p).handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("search_api dev: listening on :%s seeds=%s docs=%d encoder=%s manifest_id=%s",
		*port, *seeds, len(corpus.Docs), corpus.EncoderID, corpus.ManifestID)

	select {
	case err := <-errCh:
		return fmt.Errorf("search_api: 监听失败: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// handler 组装路由并套 CORS 与请求日志中间件。
func (s *apiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/search", s.handleSearch)
	mux.HandleFunc("/health", s.handleHealth)
	return withCORS(withRequestLog(mux))
}

// handleSearch 执行一次 C-3 检索：解码 → 校验（空查询/未知档位/未知
// 交付均 400）→ Pipeline.Execute → 按交付形态回包。
func (s *apiServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "仅支持 POST（C-3 检索）")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "读请求体失败: "+err.Error())
		return
	}
	if len(body) > maxBodyBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("请求体超过上限 %d 字节", maxBodyBytes))
		return
	}
	var req searchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeJSONError(w, http.StatusBadRequest, "query 不能为空")
		return
	}
	tier, err := parseTier(req.Tier)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	delivery, err := parseDelivery(req.Delivery)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	res, err := s.pipe.Execute(ctx, pipeline.PipelineRequest{
		Query:    req.Query,
		Tier:     tier,
		Delivery: delivery,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "检索失败: "+err.Error())
		return
	}

	if delivery == pipeline.DeliveryTools {
		writeJSON(w, http.StatusOK, toolsResponse{EvidencePack: res.Pack})
		return
	}
	citations := make([]citationDTO, len(res.Citations))
	for i, c := range res.Citations {
		citations[i] = citationDTO{
			Index:      c.Index,
			DocKey:     c.DocKey,
			RevisionID: c.Locator.RevisionID,
			Locator:    c.Locator,
			Quote:      c.Locator.Quote,
		}
	}
	writeJSON(w, http.StatusOK, summaryResponse{
		EvidencePack:    res.Pack,
		SessionID:       newSessionID(res.Pack.QueryID),
		Citations:       citations,
		Answer:          res.Answer,
		FormattedAnswer: res.FormattedAnswer,
	})
}

// handleHealth 存活探测：200 + {"status":"ok"}。
func (s *apiServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "仅支持 GET（存活探测）")
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// parseTier 解析请求 tier：空值取默认 balanced（chat 页默认档），
// 未知值报错（入口 400，不留给管线兜底）。
func parseTier(v string) (retrieval.Tier, error) {
	switch v {
	case "":
		return retrieval.TierBalanced, nil
	case "fast":
		return retrieval.TierFast, nil
	case "balanced":
		return retrieval.TierBalanced, nil
	case "deep":
		return retrieval.TierDeep, nil
	default:
		return "", fmt.Errorf("未知档位 %q（合法值 fast|balanced|deep）", v)
	}
}

// parseDelivery 解析请求 delivery：空值取默认 summary（chat 页口径），
// 未知值报错。
func parseDelivery(v string) (pipeline.Delivery, error) {
	switch v {
	case "":
		return pipeline.DeliverySummary, nil
	case "summary":
		return pipeline.DeliverySummary, nil
	case "tools":
		return pipeline.DeliveryTools, nil
	default:
		return "", fmt.Errorf("未知交付形态 %q（合法值 summary|tools）", v)
	}
}

// newSessionID 生成会话标识 s-<12hex>（逐请求随机；crypto/rand 失败几乎
// 不可达，退化用 query_id 保证仍可追踪）。
func newSessionID(queryID string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return queryID
	}
	return "s-" + hex.EncodeToString(b[:])
}

// withCORS 为所有响应补 CORS 头并应答预检：Allow-Origin * 允许
// http://localhost:3000 的前端页直连（dev 形态无鉴权，不收窄来源）。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		h.Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder 捕获下游写入的状态码供请求日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// withRequestLog 给每个请求打一行访问日志（方法/路径/状态/耗时）。
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s → %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
	})
}

// writeJSON 写 2xx JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("search_api: 写响应失败: %v", err)
	}
}

// writeJSONError 写错误信封 {code, msg, data:null}（与 RTW 前端 BFF 的
// 错误形态一致，前端透传后可按同一口径处理）。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"code": status, "msg": msg, "data": nil})
}
