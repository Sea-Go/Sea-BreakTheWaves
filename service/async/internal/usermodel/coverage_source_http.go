package usermodel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type CoverageDCReceiptClient interface {
	EventReceipt(context.Context, string, string) (eventing.Receipt, error)
}

type FavoriteCoverageProofConfig struct {
	AuthorityURL   string
	AuthorityToken string
	HTTPClient     *http.Client
}

// FavoriteCoverageHTTPProof is the production Adapter at the remote-owned
// source seam. It independently re-reads both DC receipt and RTW frozen fact;
// warehouse payload subject fields alone do not satisfy this Interface.
type FavoriteCoverageHTTPProof struct {
	dc     CoverageDCReceiptClient
	base   string
	token  string
	client *http.Client
}

func NewFavoriteCoverageHTTPProof(dc CoverageDCReceiptClient, cfg FavoriteCoverageProofConfig) (*FavoriteCoverageHTTPProof, error) {
	parsed, err := url.Parse(cfg.AuthorityURL)
	if dc == nil || err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || len(cfg.AuthorityToken) < 32 {
		return nil, ErrCoverageUnverified
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if cfg.HTTPClient != nil {
		copy := *cfg.HTTPClient
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 3 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &FavoriteCoverageHTTPProof{dc: dc, base: parsed.String(), token: cfg.AuthorityToken, client: client}, nil
}

func (p *FavoriteCoverageHTTPProof) VerifyEvent(ctx context.Context, row sourcecoverage.EventIndexRow) error {
	if p == nil || p.dc == nil || row.Producer != favoriteCoverageProducer {
		return ErrCoverageUnverified
	}
	position, err := strconv.ParseInt(row.Offset, 10, 64)
	if err != nil || position < 1 {
		return ErrCoverageConflict
	}
	dcReceipt, err := p.dc.EventReceipt(ctx, row.Producer, row.EventID)
	if err != nil {
		return ErrCoveragePending
	}
	if !matchingCoverageReceipt(dcReceipt, row, position) {
		return ErrCoverageConflict
	}
	endpoint := p.base + "/internal/v1/favorite/facts/" + url.PathEscape(row.Producer) + "/" + url.PathEscape(row.EventID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrCoverageConflict
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := p.client.Do(request)
	if err != nil || response == nil {
		return ErrCoveragePending
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrCoveragePending
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	if err != nil || len(body) > 256<<10 {
		return ErrCoverageConflict
	}
	var fact struct {
		Event              json.RawMessage           `json:"event"`
		SubjectRef         sourcecoverage.SubjectRef `json:"subject_ref"`
		PredecessorEventID string                    `json:"predecessor_event_id,omitempty"`
		TechnicalReceipt   eventing.Receipt          `json:"technical_receipt"`
		SourceEventHash    string                    `json:"source_event_hash"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fact) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrCoverageConflict
	}
	if fact.SubjectRef != row.Subject || fact.SourceEventHash != row.InputHash ||
		!matchingCoverageReceipt(fact.TechnicalReceipt, row, position) {
		return ErrCoverageConflict
	}
	canonicalRTW, err := jsoncanonicalizer.Transform(fact.Event)
	if err != nil || coverageHash(canonicalRTW) != row.InputHash {
		return ErrCoverageConflict
	}
	canonicalIndex, err := jsoncanonicalizer.Transform(row.EventSpec)
	if err != nil || !bytes.Equal(canonicalRTW, canonicalIndex) {
		return ErrCoverageConflict
	}
	return nil
}

func matchingCoverageReceipt(receipt eventing.Receipt, row sourcecoverage.EventIndexRow, position int64) bool {
	if receipt.TechnicalStatus != "accepted" || receipt.Producer != row.Producer ||
		receipt.EventID != row.EventID || receipt.InputHash != row.InputHash ||
		receipt.ReceiptID != row.ReceiptID || receipt.Offset != position {
		return false
	}
	actual, err1 := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	expected, err2 := time.Parse(time.RFC3339Nano, row.ReceivedAt)
	return err1 == nil && err2 == nil && actual.Equal(expected)
}
