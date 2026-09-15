// Package communityauthority is a projection-neutral RTW authority client. It
// verifies source identity and transport evidence without producing usermodel
// actions, warehouse rows, features, or recommendation semantics.
package communityauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

var (
	ErrUnavailable = errors.New("RTW community authority unavailable")
	ErrContract    = errors.New("RTW community authority contract mismatch")
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type SubjectRef struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

type Evidence struct {
	Event     eventing.Event
	InputHash string
	Offset    int64
	Receipt   eventing.Receipt
}

type Fact struct {
	Event              eventing.Event   `json:"event"`
	SubjectRef         SubjectRef       `json:"subject_ref"`
	PredecessorEventID string           `json:"predecessor_event_id,omitempty"`
	TechnicalReceipt   eventing.Receipt `json:"technical_receipt"`
	SourceEventHash    string           `json:"source_event_hash"`
}

type Config struct {
	BaseURL  string
	Token    string
	Producer string
	Client   *http.Client
}

type Client struct {
	baseURL  string
	token    string
	producer string
	http     *http.Client
}

func New(cfg Config) (*Client, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		len(cfg.Token) < 32 || (cfg.Producer != "rtw.comment-rpc" && cfg.Producer != "rtw.like-mq") {
		return nil, ErrContract
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if cfg.Client != nil {
		copy := *cfg.Client
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 3 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: parsed.String(), token: cfg.Token, producer: cfg.Producer, http: client}, nil
}

func (c *Client) Lookup(ctx context.Context, source Evidence) (Fact, error) {
	if c == nil || source.Event.Producer != c.producer || source.Offset < 1 ||
		!hashPattern.MatchString(source.InputHash) || source.Receipt.EventID != source.Event.EventID ||
		source.Receipt.Producer != source.Event.Producer || source.Receipt.InputHash != source.InputHash ||
		source.Receipt.Offset != source.Offset || source.Receipt.ReceiptID == "" || source.Receipt.TechnicalStatus != "accepted" {
		return Fact{}, ErrContract
	}
	endpoint := c.baseURL + "/internal/v1/community/facts?producer=" + url.QueryEscape(c.producer) +
		"&event_id=" + url.QueryEscape(source.Event.EventID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Fact{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := c.http.Do(request)
	if err != nil {
		return Fact{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return Fact{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return Fact{}, ErrContract
	}
	var fact Fact
	if err := strictJSON(bytes.NewReader(body), &fact); err != nil {
		return Fact{}, ErrContract
	}
	if err := Verify(source, fact); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

func Verify(source Evidence, fact Fact) error {
	if fact.SubjectRef.Issuer != "rtw.identity" || !positiveID(fact.SubjectRef.SubjectID) ||
		fact.TechnicalReceipt != source.Receipt || fact.SourceEventHash != source.InputHash {
		return ErrContract
	}
	sourceRaw, err := json.Marshal(source.Event)
	if err != nil {
		return err
	}
	factRaw, err := json.Marshal(fact.Event)
	if err != nil {
		return err
	}
	sourceCanonical, err := jsoncanonicalizer.Transform(sourceRaw)
	if err != nil {
		return ErrContract
	}
	factCanonical, err := jsoncanonicalizer.Transform(factRaw)
	if err != nil || !bytes.Equal(sourceCanonical, factCanonical) {
		return ErrContract
	}
	sum := sha256.Sum256(factCanonical)
	if hex.EncodeToString(sum[:]) != source.InputHash {
		return ErrContract
	}
	if _, err := time.Parse(time.RFC3339Nano, fact.TechnicalReceipt.ReceivedAt); err != nil {
		return ErrContract
	}
	return nil
}

func positiveID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func strictJSON(reader io.Reader, value any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrContract
	}
	return nil
}
