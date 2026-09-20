package sourceproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/warehouse/wikiqualitysource"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
)

// ErrDCAckEvidence separates a missing/contradictory DC delivery witness
// from the verified, but merely committed, BTW ODS source projection.
var ErrDCAckEvidence = errors.New("Wiki quality DC acknowledged-prefix evidence missing")

// This read-only wire is pinned to DataCenter's acknowledged-prefix v1
// candidate. It is not hand-edited into the generated DataCenter client wire.
type DCAckPage struct {
	Consumer           string           `json:"consumer"`
	Producer           string           `json:"producer"`
	CutoffOffset       int64            `json:"cutoff_offset"`
	AcknowledgedOffset int64            `json:"acknowledged_offset"`
	ProducerOffset     int64            `json:"producer_offset"`
	FromOffset         int64            `json:"from_offset"`
	ToOffset           int64            `json:"to_offset"`
	PrefixVerified     bool             `json:"prefix_verified"`
	Events             []DCAckItem      `json:"events"`
	DeliveryReceipts   []DCBatchReceipt `json:"delivery_receipts"`
}

type DCAckItem struct {
	Offset     int64          `json:"offset"`
	EventID    string         `json:"event_id"`
	InputHash  string         `json:"input_hash"`
	ReceiptID  string         `json:"receipt_id"`
	ReceivedAt string         `json:"received_at"`
	Event      eventing.Event `json:"event"`
}

type DCBatchReceipt struct {
	FromOffset int64  `json:"from_offset"`
	ToOffset   int64  `json:"to_offset"`
	BatchHash  string `json:"batch_hash"`
	AcceptedAt string `json:"accepted_at"`
}

type DCAckReader interface {
	ReadAcknowledgedPrefix(context.Context, string, string, int64, int64, int) (DCAckPage, error)
}

// AcknowledgedSourceProjection has verified DC pages and ODS rows. Its
// judgments still mean last transported at cutoff, not RTW as-of head.
type AcknowledgedSourceProjection struct {
	SourceProjection
	AcknowledgedAtLeast int64
	DCIndexSHA256       string
	DCBatchReceipts     []DCBatchReceipt
}

// ReadAcknowledgedPinnedSource checks each DC page against a second immutable
// ODS prefix read. ODS rows and original Event sidecars are append-only, so a
// concurrent committed cursor may advance without changing the fixed cutoff.
// DC's read-only API independently verifies its original ACK batch receipts.
func (r AuthorityReader) ReadAcknowledgedPinnedSource(ctx context.Context,
	req PinnedRequest, dc DCAckReader) (AcknowledgedSourceProjection, error) {
	var out AcknowledgedSourceProjection
	if dc == nil {
		return out, ErrDCAckEvidence
	}
	source, err := r.ReadPinnedSource(ctx, req)
	if err != nil {
		return out, err
	}
	snapshot, err := r.ODS.ReadPrefix(ctx, req.CutoffOffset)
	if err != nil || snapshot.Producer != Producer ||
		snapshot.Consumer != wikiqualitysource.DefaultConsumer ||
		int64(len(snapshot.Rows)) != req.CutoffOffset ||
		snapshot.CommittedOffset < req.CutoffOffset {
		return out, ErrPrefix
	}
	index := make([]PrefixEvent, 0, len(snapshot.Rows))
	for i, row := range snapshot.Rows {
		if _, err := verifyODSRow(row, int64(i)+1); err != nil {
			return out, err
		}
		index = append(index, PrefixEvent{DCOffset: strconv.FormatInt(row.Offset, 10),
			EventID: row.EventID, JCSSHA256: row.DCInputHash})
	}
	_, sourceIndexSHA, err := jcs(index)
	if err != nil || sourceIndexSHA != source.ODSPrefixSHA256 {
		return out, ErrPrefix
	}
	_, sourceEvidenceSHA, err := jcs(snapshot.Rows)
	if err != nil || sourceEvidenceSHA != source.ODSEvidenceSHA256 {
		return out, ErrPrefix
	}
	pageItems := make([]DCAckItem, 0, req.CutoffOffset)
	receipts := map[int64]DCBatchReceipt{}
	minACK := int64(0)
	for from := int64(1); from <= req.CutoffOffset; {
		page, err := dc.ReadAcknowledgedPrefix(ctx, wikiqualitysource.DefaultConsumer,
			Producer, req.CutoffOffset, from, 128)
		if err != nil {
			return out, fmt.Errorf("DC historical ACK prefix read: %w", err)
		}
		if !page.PrefixVerified || page.Consumer != wikiqualitysource.DefaultConsumer ||
			page.Producer != Producer || page.CutoffOffset != req.CutoffOffset ||
			page.AcknowledgedOffset < req.CutoffOffset ||
			page.ProducerOffset < req.CutoffOffset || page.FromOffset != from ||
			page.ToOffset < from || page.ToOffset > req.CutoffOffset ||
			page.ToOffset-from >= 128 ||
			int64(len(page.Events)) != page.ToOffset-from+1 ||
			len(page.DeliveryReceipts) == 0 {
			return out, ErrDCAckEvidence
		}
		if minACK == 0 || page.AcknowledgedOffset < minACK {
			minACK = page.AcknowledgedOffset
		}
		for i, item := range page.Events {
			row := snapshot.Rows[from+int64(i)-1]
			if item.Offset != from+int64(i) || item.Offset != row.Offset ||
				item.EventID != row.EventID || item.InputHash != row.DCInputHash ||
				item.ReceiptID == "" || item.Event.EventID != row.EventID ||
				item.Event.Producer != Producer {
				return out, ErrDCAckEvidence
			}
			raw, err := json.Marshal(item.Event)
			canonical, jcsErr := canonicalRaw(raw)
			if err != nil || jcsErr != nil || !bytes.Equal(canonical, row.EventSpec) {
				return out, ErrDCAckEvidence
			}
			var odsr eventing.Receipt
			if json.Unmarshal(row.DCReceipt, &odsr) != nil ||
				item.ReceiptID != odsr.ReceiptID {
				return out, ErrDCAckEvidence
			}
			actual, actualErr := time.Parse(time.RFC3339Nano, item.ReceivedAt)
			stored, storedErr := time.Parse(time.RFC3339Nano, odsr.ReceivedAt)
			if actualErr != nil || storedErr != nil || !actual.Equal(stored) {
				return out, ErrDCAckEvidence
			}
			pageItems = append(pageItems, item)
		}
		for _, receipt := range page.DeliveryReceipts {
			if receipt.FromOffset < 1 || receipt.ToOffset < receipt.FromOffset ||
				receipt.ToOffset-receipt.FromOffset >= 128 ||
				!validSHA(receipt.BatchHash) ||
				receipt.FromOffset > page.ToOffset || receipt.ToOffset < page.FromOffset {
				return out, ErrDCAckEvidence
			}
			if _, err := time.Parse(time.RFC3339Nano, receipt.AcceptedAt); err != nil {
				return out, ErrDCAckEvidence
			}
			if old, found := receipts[receipt.FromOffset]; found && old != receipt {
				return out, ErrDCAckEvidence
			}
			receipts[receipt.FromOffset] = receipt
		}
		from = page.ToOffset + 1
	}
	for next := int64(1); next <= req.CutoffOffset; {
		receipt, found := receipts[next]
		if !found || receipt.ToOffset < next {
			return out, ErrDCAckEvidence
		}
		out.DCBatchReceipts = append(out.DCBatchReceipts, receipt)
		next = receipt.ToOffset + 1
	}
	for _, receipt := range out.DCBatchReceipts {
		if receipt.ToOffset > req.CutoffOffset {
			continue // DC verifies the complete original batch outside cutoff.
		}
		items := make([]eventing.Item, 0, receipt.ToOffset-receipt.FromOffset+1)
		for offset := receipt.FromOffset; offset <= receipt.ToOffset; offset++ {
			item := pageItems[offset-1]
			items = append(items, eventing.Item{Offset: item.Offset, InputHash: item.InputHash,
				Event: item.Event})
		}
		_, batchSHA, err := jcs(items)
		if err != nil || batchSHA != receipt.BatchHash {
			return out, ErrDCAckEvidence
		}
	}
	_, dcIndexSHA, err := jcs(pageItems)
	if err != nil || len(pageItems) != int(req.CutoffOffset) {
		return out, ErrDCAckEvidence
	}
	out.SourceProjection, out.AcknowledgedAtLeast, out.DCIndexSHA256 =
		source, minACK, dcIndexSHA
	return out, nil
}

// HTTPDCAckReader calls only the DataCenter service-token read endpoint.
// It does not create/advance a consumer or perform any write.
type HTTPDCAckReader struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPDCAckReader(baseURL, token string) (*HTTPDCAckReader, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		token == "" || strings.TrimSpace(token) != token ||
		strings.ContainsAny(token, "\r\n") {
		return nil, ErrDCAckEvidence
	}
	return &HTTPDCAckReader{baseURL: strings.TrimRight(baseURL, "/"), token: token,
		client: &http.Client{Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}}, nil
}

func (r *HTTPDCAckReader) ReadAcknowledgedPrefix(ctx context.Context, consumer, producer string,
	cutoff, from int64, limit int) (DCAckPage, error) {
	var page DCAckPage
	if r == nil || r.client == nil || !idPattern.MatchString(consumer) ||
		!idPattern.MatchString(producer) || cutoff < 1 || from < 1 ||
		from > cutoff || limit < 1 || limit > 128 {
		return page, ErrDCAckEvidence
	}
	q := url.Values{"producer": {producer}, "cutoff": {strconv.FormatInt(cutoff, 10)},
		"from_offset": {strconv.FormatInt(from, 10)}, "limit": {strconv.Itoa(limit)}}
	endpoint := r.baseURL + "/v1/event-consumers/" + url.PathEscape(consumer) +
		"/acknowledged-prefix?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return page, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page, ErrDCAckEvidence
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF ||
		page.Consumer != consumer || page.Producer != producer ||
		page.CutoffOffset != cutoff || page.FromOffset != from {
		return DCAckPage{}, ErrDCAckEvidence
	}
	return page, nil
}
