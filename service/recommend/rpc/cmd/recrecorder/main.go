// Package main records v2 recommendation responses as golden snapshots.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type SourceRequest struct {
	RecRequestID string `json:"rec_request_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	UserID       string `json:"user_id"`
	AnonymousID  string `json:"anonymous_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Surface      string `json:"surface"`
	Channel      string `json:"channel,omitempty"`
	Scenario     string `json:"scenario,omitempty"`
	Query        string `json:"query"`
	Explain      bool   `json:"explain,omitempty"`
	PeriodBucket string `json:"period_bucket,omitempty"`
	TopK         int    `json:"top_k,omitempty"`
	PathMode     string `json:"path_mode,omitempty"`
}

type RecommendRequest struct {
	TenantID  string         `json:"tenant_id"`
	RequestID string         `json:"request_id,omitempty"`
	Scenario  string         `json:"scenario"`
	Channel   string         `json:"channel"`
	User      UserIdentity   `json:"user"`
	Query     string         `json:"query,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
	TopK      int            `json:"top_k,omitempty"`
	PathMode  string         `json:"path_mode,omitempty"`
	Debug     bool           `json:"debug,omitempty"`
}

type UserIdentity struct {
	TenantID    string `json:"tenant_id"`
	UserID      string `json:"user_id,omitempty"`
	AnonymousID string `json:"anonymous_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Channel     string `json:"channel,omitempty"`
	Locale      string `json:"locale,omitempty"`
}

const recommendPath = "/api/v2/recommend"

func main() {
	addr := flag.String("addr", "http://localhost:8080", "推荐服务根地址")
	requestsPath := flag.String("requests", "testdata/requests.json", "v2 请求集合文件路径，也兼容旧录制请求字段")
	outDir := flag.String("out", "testdata/baseline", "v2 响应快照输出目录")
	timeout := flag.Duration("timeout", 30*time.Second, "单次请求超时")
	flag.Parse()

	if err := run(*addr, *requestsPath, *outDir, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "录制失败: %v\n", err)
		os.Exit(1)
	}
}

// run 执行整体录制流程：加载请求 -> 逐个发送 -> 落盘。
func run(addr, requestsPath, outDir string, timeout time.Duration) error {
	reqs, err := loadRequests(requestsPath)
	if err != nil {
		return fmt.Errorf("加载请求集合: %w", err)
	}
	if len(reqs) == 0 {
		return fmt.Errorf("请求集合为空: %s", requestsPath)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("创建输出目录: %w", err)
	}

	client := &http.Client{Timeout: timeout}

	ok, failed := 0, 0
	for i, req := range reqs {
		idx := i + 1
		fileName := fmt.Sprintf("case_%02d.json", idx)
		outPath := filepath.Join(outDir, fileName)

		fmt.Printf("[%02d/%02d] channel=%s user=%s query=%q ... ", idx, len(reqs), req.Channel, req.User.UserID, req.Query)

		raw, status, err := sendRequest(client, addr, req)
		if err != nil {
			fmt.Printf("请求失败: %v\n", err)
			failed++
			writeErrorSnapshot(outPath, req, err)
			continue
		}

		pretty, perr := prettyJSON(raw)
		if perr != nil {
			pretty = raw
		}
		if err := os.WriteFile(outPath, pretty, 0o644); err != nil {
			fmt.Printf("写入失败: %v\n", err)
			failed++
			continue
		}
		fmt.Printf("HTTP %d -> %s\n", status, fileName)
		ok++
	}

	fmt.Printf("\n录制完成: 成功 %d, 失败 %d, 输出目录 %s\n", ok, failed, outDir)
	if failed > 0 {
		return fmt.Errorf("存在 %d 个失败请求", failed)
	}
	return nil
}

func loadRequests(path string) ([]RecommendRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var source []SourceRequest
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("解析请求 JSON: %w", err)
	}
	reqs := make([]RecommendRequest, 0, len(source))
	for _, req := range source {
		reqs = append(reqs, normalizeSourceRequest(req))
	}
	return reqs, nil
}

func sendRequest(client *http.Client, addr string, req RecommendRequest) ([]byte, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("序列化请求: %w", err)
	}

	url := addr + recommendPath
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("发送请求: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取响应: %w", err)
	}
	return raw, resp.StatusCode, nil
}

func prettyJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

func writeErrorSnapshot(outPath string, req RecommendRequest, reqErr error) {
	snap := map[string]any{
		"recorder_error": reqErr.Error(),
		"recorded_at":    time.Now().Format(time.RFC3339),
		"request":        req,
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(outPath, b, 0o644)
}

func normalizeSourceRequest(req SourceRequest) RecommendRequest {
	requestID := req.RequestID
	if requestID == "" {
		requestID = req.RecRequestID
	}
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	channel := req.Channel
	if channel == "" {
		channel = req.Surface
	}
	if channel == "" {
		channel = "home_feed"
	}
	scenario := req.Scenario
	if scenario == "" {
		scenario = "recommend"
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 10
	}
	pathMode := req.PathMode
	if pathMode == "" {
		pathMode = "hybrid"
	}
	return RecommendRequest{
		TenantID:  tenantID,
		RequestID: requestID,
		Scenario:  scenario,
		Channel:   channel,
		User: UserIdentity{
			TenantID:    tenantID,
			UserID:      req.UserID,
			AnonymousID: req.AnonymousID,
			SessionID:   req.SessionID,
			Channel:     channel,
			Locale:      "zh-CN",
		},
		Query:    req.Query,
		TopK:     topK,
		PathMode: pathMode,
		Debug:    req.Explain,
		Context: map[string]any{
			"period_bucket": req.PeriodBucket,
		},
	}
}
