package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sea/config"
	"sea/zlog"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"
	"go.uber.org/zap"
)

const (
	defaultDashScopeRerankURL   = "https://dashscope.aliyuncs.com/compatible-api/v1/reranks"
	defaultDashScopeRerankModel = "qwen3-rerank"
	defaultDashScopeTopNCap     = 100
	maxDashScopeDocuments       = 500
)

// DashScopeTextRerankInput 用于阿里百炼 qwen3-rerank 的输入参数。
type DashScopeTextRerankInput struct {
	Query     string   `json:"query" jsonschema:"description=用户检索问题或重排查询,required"`
	Documents []string `json:"documents" jsonschema:"description=待重排的候选文本列表，建议先做向量召回后再传入,required"`
	TopK      int      `json:"topk" jsonschema:"description=返回前 K 条，默认 10,default=10"`
	Instruct  string   `json:"instruct" jsonschema:"description=可选的 rerank 指令；为空时使用配置默认值"`
}

type DashScopeTextRerankOutput struct {
	Model       string                `json:"model"`
	Query       string                `json:"query"`
	TopK        int                   `json:"topk"`
	InputCount  int                   `json:"input_count"`
	Returned    int                   `json:"returned"`
	RequestID   string                `json:"request_id,omitempty"`
	TotalTokens int                   `json:"total_tokens,omitempty"`
	Items       []DashScopeRerankItem `json:"items"`
	LatencyMs   int64                 `json:"latency_ms"`
}

// 内部类型

type dashscopeRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
	Instruct  string   `json:"instruct,omitempty"`
}

type dashscopeRerankResponse struct {
	RequestID string `json:"request_id"`
	Output    struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Document       struct {
				Text string `json:"text"`
			} `json:"document"`
		} `json:"results"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DashScopeRerankItem struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Document       string  `json:"document,omitempty"`
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func NewDashScope() tool.CallableTool {
	httpClient := &http.Client{Timeout: 60 * time.Second}
	return function.NewFunctionTool(
		func(ctx context.Context, args DashScopeTextRerankInput) (DashScopeTextRerankOutput, error) {
			ctx, sp := zlog.StartSpan(ctx, "skills.rerank.dashscope_text_rerank")
			start := time.Now()

			args.Query = strings.TrimSpace(args.Query)
			if args.Query == "" {
				sp.End(zlog.StatusError, errors.New("query 不能为空"))
				return DashScopeTextRerankOutput{}, errors.New("query 不能为空")
			}
			if len(args.Documents) == 0 {
				sp.End(zlog.StatusError, errors.New("documents 不能为空"))
				return DashScopeTextRerankOutput{}, errors.New("documents 不能为空")
			}

			cleanedDocs := make([]string, 0, len(args.Documents))
			for _, doc := range args.Documents {
				doc = strings.TrimSpace(doc)
				if doc == "" {
					continue
				}
				cleanedDocs = append(cleanedDocs, doc)
				if len(cleanedDocs) >= maxDashScopeDocuments {
					break
				}
			}
			if len(cleanedDocs) == 0 {
				sp.End(zlog.StatusError, errors.New("documents 清洗后为空"))
				return DashScopeTextRerankOutput{}, errors.New("documents 清洗后为空")
			}

			model := strings.TrimSpace(config.Cfg.Ali.RerankModel)
			if model == "" {
				model = defaultDashScopeRerankModel
			}
			url := strings.TrimSpace(config.Cfg.Ali.RerankURL)
			if url == "" {
				url = defaultDashScopeRerankURL
			}

			topCap := config.Cfg.Ali.RerankTopNCap
			if topCap <= 0 {
				topCap = defaultDashScopeTopNCap
			}
			if args.TopK <= 0 {
				args.TopK = minInt(len(cleanedDocs), 10)
			}
			if args.TopK > len(cleanedDocs) {
				args.TopK = len(cleanedDocs)
			}
			if args.TopK > topCap {
				args.TopK = topCap
			}

			instruct := strings.TrimSpace(args.Instruct)
			if instruct == "" {
				instruct = strings.TrimSpace(config.Cfg.Ali.RerankInstruct)
			}

			if strings.TrimSpace(config.Cfg.Ali.APIKey) == "" {
				sp.End(zlog.StatusError, errors.New("ali.apikey 未配置"))
				return DashScopeTextRerankOutput{}, errors.New("ali.apikey 未配置")
			}

			payload := dashscopeRerankRequest{
				Model:     model,
				Query:     args.Query,
				Documents: cleanedDocs,
				TopN:      args.TopK,
				Instruct:  instruct,
			}

			body, err := json.Marshal(payload)
			if err != nil {
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}
			req.Header.Set("Authorization", "Bearer "+config.Cfg.Ali.APIKey)
			req.Header.Set("Content-Type", "application/json")

			resp, err := httpClient.Do(req)
			if err != nil {
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}
			defer resp.Body.Close()

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}

			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("dashscope rerank failed: http=%d body=%s", resp.StatusCode, string(respBody))
				sp.End(zlog.StatusError, err,
					zap.Int("input_count", len(cleanedDocs)),
					zap.Int("topk", args.TopK),
				)
				return DashScopeTextRerankOutput{}, err
			}

			var out dashscopeRerankResponse
			if err := json.Unmarshal(respBody, &out); err != nil {
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}
			if strings.TrimSpace(out.Code) != "" {
				err = fmt.Errorf("dashscope rerank error: code=%s message=%s", out.Code, out.Message)
				sp.End(zlog.StatusError, err)
				return DashScopeTextRerankOutput{}, err
			}

			items := make([]DashScopeRerankItem, 0, len(out.Output.Results))
			for _, r := range out.Output.Results {
				items = append(items, DashScopeRerankItem{
					Index:          r.Index,
					RelevanceScore: r.RelevanceScore,
					Document:       r.Document.Text,
				})
			}

			result := DashScopeTextRerankOutput{
				Model:       model,
				Query:       args.Query,
				TopK:        args.TopK,
				InputCount:  len(cleanedDocs),
				Returned:    len(items),
				RequestID:   out.RequestID,
				TotalTokens: out.Usage.TotalTokens,
				Items:       items,
				LatencyMs:   time.Since(start).Milliseconds(),
			}

			sp.End(zlog.StatusOK, nil,
				zap.String("model", model),
				zap.Int("input_count", len(cleanedDocs)),
				zap.Int("returned", len(items)),
				zap.Int("topk", args.TopK),
				zap.Int64("latency_ms", result.LatencyMs),
			)
			return result, nil
		},
		function.WithName("dashscope_text_rerank"),
		function.WithDescription("使用阿里百炼 qwen3-rerank 对 query + documents 做语义精排，适合在向量召回后对候选文本重排序。"),
	)
}
