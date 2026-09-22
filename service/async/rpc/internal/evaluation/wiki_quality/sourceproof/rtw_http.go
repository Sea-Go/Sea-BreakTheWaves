package sourceproof

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/warehouse/wikiqualitysource"
)

// RTWHTTPReader uses two existing RTW read surfaces: Worker-only original
// Event GET and administrator-authenticated explicit FactSet revision GET.
// Its administrator bearer is not a UserCenter UID or a product session.
type RTWHTTPReader struct {
	worker     *wikiqualitysource.HTTPAuthority
	baseURL    string
	adminToken string
	client     *http.Client
}

func NewRTWHTTPReader(baseURL, workerToken, adminToken string) (*RTWHTTPReader, error) {
	worker, err := wikiqualitysource.NewHTTPAuthority(baseURL, workerToken)
	if err != nil || adminToken == "" || strings.TrimSpace(adminToken) != adminToken ||
		strings.ContainsAny(adminToken, "\r\n") {
		return nil, ErrHistoricalAuthority
	}
	return &RTWHTTPReader{worker: worker, baseURL: strings.TrimRight(baseURL, "/"),
		adminToken: adminToken,
		client: &http.Client{Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}}, nil
}

func (r *RTWHTTPReader) ReadFactSetEvent(ctx context.Context, id string) (
	wikiqualitysource.FactSetEventProof, error) {
	if r == nil || r.worker == nil {
		return wikiqualitysource.FactSetEventProof{}, ErrHistoricalAuthority
	}
	return r.worker.ReadFactSetEvent(ctx, id)
}

func (r *RTWHTTPReader) ReadQualityEvent(ctx context.Context, id string) ([]byte, string, error) {
	if r == nil || r.worker == nil {
		return nil, "", ErrHistoricalAuthority
	}
	return r.worker.ReadQualityEvent(ctx, id)
}

func (r *RTWHTTPReader) ReadHistoricalFactSet(ctx context.Context, moduleID, pageID,
	revisionID, wikiRevisionID string) (HistoricalFactSet, error) {
	var result HistoricalFactSet
	if r == nil || r.client == nil || !idPattern.MatchString(moduleID) ||
		!idPattern.MatchString(revisionID) || !idPattern.MatchString(wikiRevisionID) ||
		pageID == "" || len(pageID) > 200 || strings.ContainsAny(pageID, "/?#\r\n") {
		return result, ErrHistoricalAuthority
	}
	endpoint := r.baseURL + "/v1/knowledge/modules/" + url.PathEscape(moduleID) +
		"/wiki-pages/" + url.PathEscape(pageID) + "/fact-set-revisions/" +
		url.PathEscape(revisionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+r.adminToken)
	resp, err := r.client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, ErrHistoricalAuthority
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			wikiqualitysource.FactSetV1
			FactSetJCSSHA256 string `json:"fact_set_jcs_sha256"`
			EventID          string `json:"event_id"`
			EventRawSHA256   string `json:"event_raw_sha256"`
			EventJCSSHA256   string `json:"event_jcs_sha256"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF ||
		envelope.Code != http.StatusOK || envelope.Data.ModuleID != moduleID ||
		envelope.Data.PageID != pageID || envelope.Data.WikiRevisionID != wikiRevisionID ||
		envelope.Data.FactSetRevisionID != revisionID ||
		!idPattern.MatchString(envelope.Data.EventID) ||
		!validSHA(envelope.Data.EventRawSHA256) ||
		!validSHA(envelope.Data.EventJCSSHA256) ||
		!validSHA(envelope.Data.FactSetJCSSHA256) {
		return HistoricalFactSet{}, ErrHistoricalAuthority
	}
	return HistoricalFactSet{Record: envelope.Data.FactSetV1,
		EventID: envelope.Data.EventID, EventRawSHA256: envelope.Data.EventRawSHA256,
		EventJCSSHA256:   envelope.Data.EventJCSSHA256,
		FactSetJCSSHA256: envelope.Data.FactSetJCSSHA256}, nil
}
