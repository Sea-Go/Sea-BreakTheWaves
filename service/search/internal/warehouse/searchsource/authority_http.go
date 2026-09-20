package searchsource

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

// HTTPAuthority calls RTW's Worker read endpoint. The bearer is configured
// server-side and is never taken from a qrel event or a client request.
type HTTPAuthority struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPAuthority(baseURL, token string) (*HTTPAuthority, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.TrimSpace(token) == "" {
		return nil, ErrContract
	}
	return &HTTPAuthority{baseURL: strings.TrimRight(baseURL, "/"), token: token,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

func (a *HTTPAuthority) ReadJudgmentEvent(ctx context.Context, eventID string) ([]byte, string, error) {
	if a == nil || a.client == nil || !eventIDPattern.MatchString(eventID) {
		return nil, "", ErrContract
	}
	endpoint := a.baseURL + "/internal/v1/knowledge/search-judgments/events/" + eventID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("RTW judgment authority request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: RTW judgment authority HTTP %d", ErrContract, resp.StatusCode)
	}
	var receipt struct {
		Code int `json:"code"`
		Data struct {
			EventID     string `json:"event_id"`
			EventJSON   string `json:"event_json"`
			EventSHA256 string `json:"event_sha256"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&receipt); err != nil {
		return nil, "", fmt.Errorf("%w: invalid RTW judgment authority receipt", ErrContract)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || receipt.Code != http.StatusOK ||
		receipt.Data.EventID != eventID || receipt.Data.EventJSON == "" ||
		!shaPattern.MatchString(receipt.Data.EventSHA256) {
		return nil, "", ErrContract
	}
	return []byte(receipt.Data.EventJSON), receipt.Data.EventSHA256, nil
}
