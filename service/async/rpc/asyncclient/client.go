package asyncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client is the stable transport facade for Async RPC/HTTP. It intentionally
// accepts raw event batches: event ownership and schema evolution remain inside
// Async, while Recommend only forwards a compatibility batch.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) SubmitEvents(ctx context.Context, contentType string, body []byte, out any) error {
	if c == nil || c.BaseURL == "" {
		return fmt.Errorf("async endpoint is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("async endpoint returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
