package wikiqualitysource

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

// FactSetEventProof preserves three independent RTW hash domains. Its Event
// JSON is the original emitted byte sequence, not an Outbox JSONB projection.
type FactSetEventProof struct {
	EventJSON        []byte
	EventRawSHA256   string
	EventJCSSHA256   string
	FactSetJCSSHA256 string
}

// ReadFactSetEvent reads RTW's private Worker-only FactSet original Event.
// The HTTP reply verifies its own bytes and payload; DC's InputHash/offset
// is a separate technical authority and remains the consumer's responsibility.
func (a *HTTPAuthority) ReadFactSetEvent(ctx context.Context, eventID string) (FactSetEventProof, error) {
	var proof FactSetEventProof
	if a == nil || a.client == nil || !eventIDPattern.MatchString(eventID) {
		return proof, ErrContract
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+"/internal/v1/knowledge/wiki-fact-sets/events/"+eventID, nil)
	if err != nil {
		return proof, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return proof, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return proof, ErrContract
	}
	var receipt struct {
		Code int `json:"code"`
		Data struct {
			EventID          string `json:"event_id"`
			EventJSON        string `json:"event_json"`
			EventRawSHA256   string `json:"event_raw_sha256"`
			EventJCSSHA256   string `json:"event_jcs_sha256"`
			FactSetJCSSHA256 string `json:"fact_set_jcs_sha256"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF ||
		receipt.Code != http.StatusOK || receipt.Data.EventID != eventID ||
		receipt.Data.EventJSON == "" ||
		!shaPattern.MatchString(receipt.Data.EventRawSHA256) ||
		!shaPattern.MatchString(receipt.Data.EventJCSSHA256) ||
		!shaPattern.MatchString(receipt.Data.FactSetJCSSHA256) {
		return FactSetEventProof{}, ErrContract
	}
	raw := []byte(receipt.Data.EventJSON)
	if digest(raw) != receipt.Data.EventRawSHA256 {
		return FactSetEventProof{}, ErrContract
	}
	canonicalEvent, err := canonical(raw)
	if err != nil || digest(canonicalEvent) != receipt.Data.EventJCSSHA256 {
		return FactSetEventProof{}, ErrContract
	}
	var event eventing.Event
	if json.Unmarshal(raw, &event) != nil || event.EventID != eventID {
		return FactSetEventProof{}, ErrContract
	}
	if _, err := ParseFactSetV1(event, raw, receipt.Data.FactSetJCSSHA256); err != nil {
		return FactSetEventProof{}, err
	}
	return FactSetEventProof{EventJSON: raw, EventRawSHA256: receipt.Data.EventRawSHA256,
		EventJCSSHA256:   receipt.Data.EventJCSSHA256,
		FactSetJCSSHA256: receipt.Data.FactSetJCSSHA256}, nil
}
