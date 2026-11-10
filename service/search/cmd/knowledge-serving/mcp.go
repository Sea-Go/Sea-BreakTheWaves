// MCP Streamable HTTP 端点(JSON-RPC 2.0):initialize / tools/list / tools/call。
// 静态 Bearer 鉴权 + loopback 绑定;首版不做裸 Cypher/任意方法透传。
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	mcpProtocolVersion = "2025-03-26"
	toolName           = "knowledge_search"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

type rpcError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rpcResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCErr(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	var e rpcError
	e.JSONRPC = "2.0"
	e.ID = id
	e.Error.Code = code
	e.Error.Message = message
	writeJSON(w, http.StatusOK, e)
}

// handler 返回 MCP 端点;mcpToken 为空时拒绝启动(由 main 校验)。
func handler(s *Searcher, mcpToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+mcpToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPCErr(w, nil, -32700, "解析错误")
			return
		}
		if req.JSONRPC != "2.0" {
			writeRPCErr(w, req.ID, -32600, "jsonrpc 必须为 2.0")
			return
		}
		switch req.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, rpcResult{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "btw-knowledge-serving", "version": "0.1.0"},
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSON(w, http.StatusOK, rpcResult{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"tools": []map[string]any{{
					"name":        toolName,
					"description": "在 RTW 知识模块的当前发布版本中执行三路检索(Dense/Sparse/Multi-vector + RRF 融合),返回带 locator/revision/release 的证据。学习计划制定前用此工具获取课程结构与考点。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"query":     map[string]any{"type": "string", "description": "检索词(概念名/主题/考点)"},
							"module_id": map[string]any{"type": "string", "description": "知识模块 id"},
							"top_k":     map[string]any{"type": "integer", "description": "返回证据条数上限(默认 5,最大 8)"},
						},
						"required": []string{"query", "module_id"},
					},
				}},
			}})
		case "tools/call":
			if req.Params.Name != toolName {
				writeRPCErr(w, req.ID, -32602, fmt.Sprintf("未知工具: %s", req.Params.Name))
				return
			}
			var args struct {
				Query    string `json:"query"`
				ModuleID string `json:"module_id"`
				TopK     int    `json:"top_k"`
			}
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					writeRPCErr(w, req.ID, -32602, "参数解析失败")
					return
				}
			}
			topK := args.TopK
			if topK <= 0 {
				topK = 5
			}
			evidence, releaseID, err := s.SearchEvidence(r.Context(), args.ModuleID, args.Query, topK)
			if err != nil {
				writeJSON(w, http.StatusOK, rpcResult{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("检索失败: %v", err)}},
					"isError": true,
				}})
				return
			}
			payload, _ := json.Marshal(map[string]any{
				"module_id": args.ModuleID, "release_id": releaseID,
				"evidence": evidence,
				"note":     "引用时必须携带 locator 与 release_id;release_id 是本次证据的固定版本。",
			})
			text := string(payload)
			if len(text) > 60_000 {
				text = text[:60_000]
			}
			writeJSON(w, http.StatusOK, rpcResult{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			}})
		default:
			writeRPCErr(w, req.ID, -32601, fmt.Sprintf("未知方法: %s", req.Method))
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

// serve 启动 HTTP 服务。
func serve(addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}
