package wikiqualitysource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var eventIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// HTTPAuthority reads the RTW Worker-only original Event receipt. Its bearer
// belongs to this server; DC event payloads and product requests never set it.
type HTTPAuthority struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPAuthority(baseURL, token string) (*HTTPAuthority, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || token == "" ||
		token != strings.TrimSpace(token) || strings.ContainsAny(token, "\r\n") {
		return nil, ErrContract
	}
	return &HTTPAuthority{baseURL: strings.TrimRight(baseURL, "/"), token: token,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func (a *HTTPAuthority) ReadQualityEvent(ctx context.Context, eventID string) ([]byte, string, error) {
	if a == nil || a.client == nil || !eventIDPattern.MatchString(eventID) {
		return nil, "", ErrContract
	}
	endpoint := a.baseURL + "/internal/v1/knowledge/wiki-quality/events/" + eventID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("RTW Wiki quality authority request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: RTW Wiki quality authority HTTP %d", ErrContract, resp.StatusCode)
	}
	var receipt struct {
		Code int `json:"code"`
		Data struct {
			EventID        string `json:"event_id"`
			EventJSON      string `json:"event_json"`
			EventRawSHA256 string `json:"event_raw_sha256"`
			EventJCSSHA256 string `json:"event_jcs_sha256"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&receipt); err != nil {
		return nil, "", ErrContract
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) ||
		receipt.Code != http.StatusOK || receipt.Data.EventID != eventID ||
		receipt.Data.EventJSON == "" || !shaPattern.MatchString(receipt.Data.EventRawSHA256) ||
		!shaPattern.MatchString(receipt.Data.EventJCSSHA256) {
		return nil, "", ErrContract
	}
	raw := []byte(receipt.Data.EventJSON)
	encoded, err := canonical(raw)
	if err != nil || digest(raw) != receipt.Data.EventRawSHA256 ||
		digest(encoded) != receipt.Data.EventJCSSHA256 {
		return nil, "", ErrContract
	}
	return raw, receipt.Data.EventRawSHA256, nil
}
