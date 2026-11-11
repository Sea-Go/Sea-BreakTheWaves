// 图修订 HTTP API(loopback+Bearer,与 MCP 同一鉴权面)。
package main

import (
	"encoding/json"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
)

func graphHandler(store *GraphStore, rtw *ridethewind.Client, bearer string) http.Handler {
	mux := http.NewServeMux()
	requireAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return false
		}
		return true
	}
	mux.HandleFunc("/graph/revisions/seed", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) || r.Method != http.MethodPost {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			}
			return
		}
		var req struct {
			ModuleID         string `json:"module_id"`
			SourceRevisionID string `json:"source_revision_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ModuleID == "" || req.SourceRevisionID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "module_id 与 source_revision_id 必填"})
			return
		}
		ctx := r.Context()
		rev, err := rtw.GetRevision(ctx, req.SourceRevisionID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "读 RTW 修订失败: " + err.Error()})
			return
		}
		graphID := "course:" + req.ModuleID
		meta, g, err := store.SeedRevision(ctx, graphID, rev.ContentHash, rev.Content, "seed: replay from RTW manifest "+req.SourceRevisionID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revision": meta, "graph": g})
	})
	mux.HandleFunc("/graph/revisions", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			graphID := r.URL.Query().Get("graph_id")
			if graphID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "graph_id 必填"})
				return
			}
			list, err := store.ListRevisions(ctx, graphID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"revisions": list})
		case http.MethodPost:
			var req struct {
				GraphID   string    `json:"graph_id"`
				OpSummary string    `json:"op_summary"`
				Ops       []GraphOp `json:"ops"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GraphID == "" || len(req.Ops) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "graph_id 与非空 ops 必填"})
				return
			}
			meta, g, err := store.AppendRevision(ctx, req.GraphID, req.OpSummary, req.Ops)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"revision": meta, "graph": g})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET/POST only"})
		}
	})
	mux.HandleFunc("/graph/current", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) || r.Method != http.MethodGet {
			if r.Method != http.MethodGet {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
			}
			return
		}
		graphID := r.URL.Query().Get("graph_id")
		if graphID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "graph_id 必填"})
			return
		}
		meta, g, err := store.ActiveRevision(r.Context(), graphID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"revision": meta, "graph": g})
	})
	mux.HandleFunc("/graph/query", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) || r.Method != http.MethodPost {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			}
			return
		}
		var req struct {
			GraphID  string `json:"graph_id"`
			NodeType string `json:"node_type"`
			Keyword  string `json:"keyword"`
			Hops     int    `json:"hops"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.GraphID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "graph_id 必填"})
			return
		}
		res, err := store.QueryCurrent(r.Context(), req.GraphID, req.NodeType, req.Keyword, req.Hops)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	return mux
}
