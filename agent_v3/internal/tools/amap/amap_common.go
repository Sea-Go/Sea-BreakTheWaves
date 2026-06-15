package amap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent_v3/internal/config"
)

type amapRuntime struct {
	cfg        config.AmapConfig
	httpClient *http.Client
	limiter    *amapRateLimiter
}

type amapRateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newAmapRateLimiter(qps float64) *amapRateLimiter {
	if qps <= 0 {
		return nil
	}
	return &amapRateLimiter{
		interval: time.Duration(float64(time.Second) / qps),
	}
}

func (rl *amapRateLimiter) wait(ctx context.Context) error {
	if rl == nil {
		return nil
	}
	rl.mu.Lock()
	now := time.Now()
	if now.Before(rl.next) {
		rl.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rl.next.Sub(now)):
		}
		rl.mu.Lock()
	}
	rl.next = time.Now().Add(rl.interval)
	rl.mu.Unlock()
	return nil
}

type AmapResponse struct {
	OK         bool           `json:"ok"`
	Endpoint   string         `json:"endpoint"`
	StatusCode int            `json:"status_code"`
	Status     string         `json:"status,omitempty"`
	Info       string         `json:"info,omitempty"`
	Infocode   string         `json:"infocode,omitempty"`
	LatencyMs  int64          `json:"latency_ms"`
	Raw        map[string]any `json:"raw,omitempty"`
}

type AmapStaticMapResult struct {
	OK            bool   `json:"ok"`
	Endpoint      string `json:"endpoint"`
	URLRedacted   string `json:"url_redacted"`
	Validated     bool   `json:"validated"`
	StatusCode    int    `json:"status_code,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
	ContentLength int64  `json:"content_length,omitempty"`
	LatencyMs     int64  `json:"latency_ms,omitempty"`
}

type AmapAPIError struct {
	Endpoint string
	Status   string
	Info     string
	Infocode string
}

func (e AmapAPIError) Error() string {
	parts := []string{"amap api returned failure", "endpoint=" + e.Endpoint}
	if e.Status != "" {
		parts = append(parts, "status="+e.Status)
	}
	if e.Info != "" {
		parts = append(parts, "info="+e.Info)
	}
	if e.Infocode != "" {
		parts = append(parts, "infocode="+e.Infocode)
	}
	return strings.Join(parts, " ")
}

func newAmapRuntime(cfg config.AmapConfig) *amapRuntime {
	cfg = cfg.WithDefaults()
	return &amapRuntime{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
		limiter: newAmapRateLimiter(cfg.QPS),
	}
}

func newAmapRuntimeWithHTTPClient(cfg config.AmapConfig, httpClient *http.Client) *amapRuntime {
	runtime := newAmapRuntime(cfg)
	if httpClient != nil {
		runtime.httpClient = httpClient
	}
	return runtime
}

func (r *amapRuntime) get(ctx context.Context, endpoint string, q url.Values, includeOutput bool) (AmapResponse, error) {
	endpoint = normalizeEndpoint(endpoint)
	if q == nil {
		q = url.Values{}
	}
	if err := r.injectCommonQuery(q, includeOutput); err != nil {
		return AmapResponse{OK: false, Endpoint: endpoint}, err
	}

	requestURL, err := r.buildURL(endpoint, q)
	if err != nil {
		return AmapResponse{OK: false, Endpoint: endpoint}, err
	}

	if err := r.limiter.wait(ctx); err != nil {
		return AmapResponse{OK: false, Endpoint: endpoint}, err
	}

	var lastErr error
	attempts := r.cfg.Retry.MaxRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		start := time.Now()
		resp, body, err := r.doHTTPGet(ctx, requestURL.String())
		latencyMs := time.Since(start).Milliseconds()
		if err != nil {
			lastErr = err
			if waitErr := r.waitBeforeRetry(ctx, attempt, attempts); waitErr != nil {
				return AmapResponse{OK: false, Endpoint: endpoint, LatencyMs: latencyMs}, waitErr
			}
			continue
		}

		statusCode := resp.StatusCode
		if statusCode >= http.StatusInternalServerError && attempt+1 < attempts {
			lastErr = fmt.Errorf("amap http status %d", statusCode)
			if waitErr := r.waitBeforeRetry(ctx, attempt, attempts); waitErr != nil {
				return AmapResponse{OK: false, Endpoint: endpoint, StatusCode: statusCode, LatencyMs: latencyMs}, waitErr
			}
			continue
		}
		if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
			return AmapResponse{OK: false, Endpoint: endpoint, StatusCode: statusCode, LatencyMs: latencyMs},
				fmt.Errorf("amap http status %d: %s", statusCode, strings.TrimSpace(string(body)))
		}

		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			return AmapResponse{OK: false, Endpoint: endpoint, StatusCode: statusCode, LatencyMs: latencyMs},
				fmt.Errorf("decode amap response: %w", err)
		}

		result := normalizeAmapResponse(endpoint, statusCode, latencyMs, raw)
		if !result.OK {
			return result, AmapAPIError{
				Endpoint: result.Endpoint,
				Status:   result.Status,
				Info:     result.Info,
				Infocode: result.Infocode,
			}
		}
		return result, nil
	}

	return AmapResponse{OK: false, Endpoint: endpoint}, lastErr
}

func (r *amapRuntime) staticMap(ctx context.Context, in AmapStaticMapInput) (AmapStaticMapResult, error) {
	endpoint := "/staticmap"
	q := url.Values{}
	putString(q, "location", in.Location)
	putInt(q, "zoom", in.Zoom)
	putString(q, "size", in.Size)
	putInt(q, "scale", in.Scale)
	putString(q, "markers", in.Markers)
	putString(q, "labels", in.Labels)
	putString(q, "paths", in.Paths)
	if in.Traffic {
		q.Set("traffic", "1")
	}
	if q.Get("location") == "" && q.Get("markers") == "" && q.Get("paths") == "" {
		return AmapStaticMapResult{OK: false, Endpoint: endpoint}, errors.New("location、markers、paths 至少填写一个")
	}
	if err := r.injectCommonQuery(q, false); err != nil {
		return AmapStaticMapResult{OK: false, Endpoint: endpoint}, err
	}

	requestURL, err := r.buildURL(endpoint, q)
	if err != nil {
		return AmapStaticMapResult{OK: false, Endpoint: endpoint}, err
	}

	result := AmapStaticMapResult{
		OK:          true,
		Endpoint:    endpoint,
		URLRedacted: redactKey(requestURL.String()),
	}
	if !in.Validate {
		return result, nil
	}

	if err := r.limiter.wait(ctx); err != nil {
		result.OK = false
		return result, err
	}

	start := time.Now()
	resp, body, err := r.doHTTPGet(ctx, requestURL.String())
	result.LatencyMs = time.Since(start).Milliseconds()
	result.Validated = true
	if err != nil {
		result.OK = false
		return result, err
	}
	result.StatusCode = resp.StatusCode
	result.ContentType = resp.Header.Get("Content-Type")
	result.ContentLength = resp.ContentLength
	if result.ContentLength < 0 {
		result.ContentLength = int64(len(body))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		result.OK = false
		return result, fmt.Errorf("amap static map http status %d", resp.StatusCode)
	}
	if !strings.HasPrefix(strings.ToLower(result.ContentType), "image/") {
		result.OK = false
		return result, fmt.Errorf("amap static map returned non-image content type %q", result.ContentType)
	}
	return result, nil
}

func (r *amapRuntime) doHTTPGet(ctx context.Context, requestURL string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

func (r *amapRuntime) buildURL(endpoint string, q url.Values) (*url.URL, error) {
	baseURL := strings.TrimSpace(r.cfg.BaseURL)
	if baseURL == "" {
		return nil, errors.New("高德 baseurl 为空，请配置 amap.baseurl")
	}
	rawURL := strings.TrimRight(baseURL, "/") + normalizeEndpoint(endpoint)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	parsed.RawQuery = q.Encode()
	return parsed, nil
}

func (r *amapRuntime) injectCommonQuery(q url.Values, includeOutput bool) error {
	key := strings.TrimSpace(r.cfg.APIKey)
	if key == "" {
		return fmt.Errorf("高德 API Key 为空，请配置 amap.api_key")
	}
	q.Set("key", key)
	if includeOutput {
		output := strings.ToUpper(strings.TrimSpace(r.cfg.Output))
		if output == "" {
			output = "JSON"
		}
		q.Set("output", output)
	}
	return nil
}

func (r *amapRuntime) waitBeforeRetry(ctx context.Context, attempt, attempts int) error {
	if attempt+1 >= attempts {
		return nil
	}
	backoff := time.Duration(r.cfg.Retry.BackoffSeconds * float64(time.Second))
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	timer := time.NewTimer(backoff * time.Duration(attempt+1))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeAmapResponse(endpoint string, statusCode int, latencyMs int64, raw map[string]any) AmapResponse {
	status := rawString(raw, "status")
	info := rawString(raw, "info")
	infocode := rawString(raw, "infocode")
	ok := status == "" || status == "1"

	if status == "" {
		errcode := rawString(raw, "errcode")
		if errcode != "" {
			status = errcode
			info = firstNonEmpty(rawString(raw, "errmsg"), info)
			infocode = firstNonEmpty(rawString(raw, "errdetail"), infocode)
			ok = errcode == "0" || errcode == "10000"
		}
	}

	return AmapResponse{
		OK:         ok,
		Endpoint:   endpoint,
		StatusCode: statusCode,
		Status:     status,
		Info:       info,
		Infocode:   infocode,
		LatencyMs:  latencyMs,
		Raw:        raw,
	}
}

func normalizeEndpoint(endpoint string) string {
	return "/" + strings.TrimLeft(strings.TrimSpace(endpoint), "/")
}

func rawString(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	switch v := raw[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func redactKey(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := parsed.Query()
	if q.Has("key") {
		q.Set("key", "***")
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func newValues() url.Values {
	return url.Values{}
}

func putString(q url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		q.Set(key, value)
	}
}

func putInt(q url.Values, key string, value int) {
	if value != 0 {
		q.Set(key, strconv.Itoa(value))
	}
}

func putBool(q url.Values, key string, value bool) {
	if value {
		q.Set(key, "true")
	}
}

func putJoined(q url.Values, key string, values []string, sep string) {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) > 0 {
		q.Set(key, strings.Join(clean, sep))
	}
}
