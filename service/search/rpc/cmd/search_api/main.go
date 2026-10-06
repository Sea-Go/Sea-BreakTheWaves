// Command search_api 是 B1 btw-search-api 的 dev 形态：把端到端检索
// 管线暴露为 HTTP 端点，让前端 chat 页从 503 变为可交互。
//
// 启动：go run ./service/search/rpc/cmd/search_api --port 8300
// 前端：SEA_PRODUCT_API_SERVER_URL=http://localhost:8300
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/pipeline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

type searchRequest struct {
	Query    string `json:"query"`
	Tier     string `json:"tier"`
	Delivery string `json:"delivery"`
}

type searchResponse struct {
	EvidencePack    interface{}       `json:"evidence_pack,omitempty"`
	SessionID       string            `json:"session_id"`
	Citations       []map[string]interface{} `json:"citations,omitempty"`
	Answer          string            `json:"answer,omitempty"`
	FormattedAnswer string            `json:"formatted_answer,omitempty"`
}

func main() {
	port := flag.Int("port", 8300, "listen port")
	seedsDir := flag.String("seeds", "testdata/index/seeds", "seeds directory")
	flag.Parse()

	corpus, err := devseed.LoadCorpus(*seedsDir)
	if err != nil {
		log.Fatalf("load seeds: %v", err)
	}
	p, err := pipeline.NewDefaultPipeline(corpus.Store)
	if err != nil {
		log.Fatalf("pipeline: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		if req.Query == "" {
			http.Error(w, `{"error":"query is required"}`, http.StatusBadRequest)
			return
		}
		tier := retrieval.Tier(req.Tier)
		if tier == "" {
			tier = retrieval.TierFast
		}
		if tier != retrieval.TierFast && tier != retrieval.TierBalanced && tier != retrieval.TierDeep {
			http.Error(w, `{"error":"invalid tier"}`, http.StatusBadRequest)
			return
		}
		delivery := pipeline.Delivery(req.Delivery)
		if delivery == "" {
			delivery = pipeline.DeliverySummary
		}
		res, err := p.Execute(r.Context(), pipeline.PipelineRequest{
			Query: req.Query, Tier: tier, Delivery: delivery,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		resp := searchResponse{SessionID: res.Pack.QueryID, EvidencePack: res.Pack}
		if delivery == pipeline.DeliverySummary {
			resp.Answer = res.Answer
			resp.FormattedAnswer = res.FormattedAnswer
			for _, c := range res.Citations {
				resp.Citations = append(resp.Citations, map[string]interface{}{
					"index": c.Index, "doc_key": c.DocKey, "locator": c.Locator,
				})
			}
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("OPTIONS /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("search_api dev listening on %s (seeds=%s)", addr, *seedsDir)
	srv := &http.Server{Addr: addr, Handler: mux, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
	os.Exit(0)
}
