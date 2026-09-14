// Package httpclient owns the bounded JSON transport shared by provider clients.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type Config struct {
	BaseURL          string
	Token            string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type Client struct {
	base     *url.URL
	token    string
	http     *http.Client
	maxBytes int64
}

// HTTPError preserves a non-success status without retrying a possible mutation.
type HTTPError struct {
	StatusCode int
	Body       string
	RetryAfter string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("provider HTTP status %d: %s", e.StatusCode, e.Body)
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("provider requires an absolute HTTP base URL without query, credentials or fragment")
	}
	if strings.TrimSpace(cfg.Token) != cfg.Token || strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("invalid provider token")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	// Redirects can change a fixed provider or replay a mutation with different semantics.
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	limit := cfg.MaxResponseBytes
	if limit == 0 {
		limit = 32 << 20
	}
	if limit < 1 {
		return nil, errors.New("invalid response byte limit")
	}
	return &Client{u, cfg.Token, &cloned, limit}, nil
}

// Do returns bytes and status so 204 claim exhaustion remains distinct from errors.
// A caller owns the deadline; cancellation reaches the active HTTP request/body read.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, input any, idempotency string) ([]byte, int, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, 0, errors.New("provider path must be absolute")
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, 0, fmt.Errorf("encode provider request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	target := *c.base
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawPath = ""
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("provider request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read provider response: %w", err)
	}
	if int64(len(raw)) > c.maxBytes {
		return nil, resp.StatusCode, errors.New("provider response exceeds byte limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &HTTPError{resp.StatusCode, string(raw), resp.Header.Get("Retry-After")}
	}
	return raw, resp.StatusCode, nil
}

// Segment rejects path syntax instead of letting an ID choose another endpoint.
func Segment(value string) (string, error) {
	if value == "" || value == "." || value == ".." || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/%?#\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("invalid provider resource ID")
	}
	return value, nil
}

func Decode(raw []byte, value any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null provider response")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing provider response JSON")
	}
	return nil
}
