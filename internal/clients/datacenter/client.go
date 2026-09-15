// Package datacenter consumes technical execution and model contracts.
package datacenter

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/prediction"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/representation"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

type Client struct{ http *httpclient.Client }

type PredictionResult struct {
	Response prediction.Response
	Body     []byte
}

func New(config httpclient.Config) (*Client, error) {
	c, e := httpclient.New(config)
	if e != nil {
		return nil, e
	}
	return &Client{c}, nil
}

var (
	ErrNoWork                   = errors.New("DataCenter has no claimable work")
	ErrPredictionOutcomeUnknown = errors.New("DataCenter prediction outcome is unknown")
	ErrPredictionInFlight       = errors.New("DataCenter prediction logical call is still in progress")
)

func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, input any, key string) (T, error) {
	var value T
	raw, status, err := c.http.Do(ctx, method, path, query, input, key)
	if err != nil {
		return value, err
	}
	if status == http.StatusNoContent {
		return value, ErrNoWork
	}
	if err = httpclient.Decode(raw, &value); err != nil {
		return value, err
	}
	return value, nil
}
func resource(prefix, id, suffix string) (string, error) {
	s, e := httpclient.Segment(id)
	if e != nil {
		return "", e
	}
	return prefix + s + suffix, nil
}

// Represent starts an independent model call. Identical payloads are not the
// same operation. Call RepresentWithKey when persisting/retrying one operation.
func (c *Client) Represent(ctx context.Context, q representation.Request, contract representation.Contract, physicalModel string) (representation.Response, error) {
	return c.RepresentWithKey(ctx, q, contract, physicalModel, rand.Text())
}

var representationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,200}$`)

// RepresentWithKey uses a caller-owned logical call key. Persist the key before
// dispatch and reuse it after uncertain outcomes; never derive it from body hash
// alone, because separate calls with equal bodies have separate usage receipts.
// The client does not automatically retry model calls.
func (c *Client) RepresentWithKey(ctx context.Context, q representation.Request, contract representation.Contract, physicalModel, key string) (representation.Response, error) {
	var result representation.Response
	if !representationKeyPattern.MatchString(key) {
		return result, errors.New("representation requires a valid logical call Idempotency-Key")
	}
	if err := q.Validate(); err != nil {
		return result, err
	}
	if err := q.Match(contract, q.Space, q.ConfigurationID); err != nil {
		return result, err
	}
	if physicalModel == "" {
		return result, errors.New("fixed physical model is required")
	}
	raw, _, err := c.http.Do(ctx, http.MethodPost, "/v1/representations", nil, q, key)
	if err != nil {
		return result, err
	}
	if err = representation.Decode(raw, &result); err != nil {
		return result, err
	}
	if err = result.Validate(q, contract, physicalModel); err != nil {
		return result, fmt.Errorf("DataCenter representation contract: %w", err)
	}
	return result, nil
}

// Predict executes one caller-owned logical prediction call. The caller must
// persist and reuse key after an unknown response; this client never invents a
// replacement key or retries a mutation on its own.
func (c *Client) Predict(ctx context.Context, q prediction.Request, key string) (PredictionResult, error) {
	var result PredictionResult
	if !representationKeyPattern.MatchString(key) {
		return result, errors.New("prediction requires a valid logical call Idempotency-Key")
	}
	if err := q.Validate(); err != nil {
		return result, err
	}
	raw, _, err := c.http.Do(ctx, http.MethodPost, "/v1/predictions", nil, q, key)
	if err != nil {
		var response *httpclient.HTTPError
		if errors.As(err, &response) {
			// DC uses 409 for both an immutable binding conflict and an active
			// logical-call lease. Only its fixed in-progress envelope plus a
			// Retry-After marks work that should be polled with the same key.
			if response.StatusCode == http.StatusConflict && response.RetryAfter != "" &&
				predictionErrorEnvelope(response.Body, "prediction logical call is still in progress") {
				return result, fmt.Errorf("%w: %w", ErrPredictionInFlight, err)
			}
			if response.StatusCode == http.StatusServiceUnavailable && response.RetryAfter != "" &&
				predictionErrorEnvelope(response.Body, "prediction outcome is unknown; retry the same idempotency key") {
				return result, fmt.Errorf("%w: %w", ErrPredictionOutcomeUnknown, err)
			}
			return result, err
		}
		return result, fmt.Errorf("%w: %v", ErrPredictionOutcomeUnknown, err)
	}
	if err := prediction.Decode(raw, &result.Response); err != nil {
		return PredictionResult{}, fmt.Errorf("%w: invalid response JSON", ErrPredictionOutcomeUnknown)
	}
	if err := result.Response.Validate(q); err != nil {
		return PredictionResult{}, fmt.Errorf("%w: response contract: %v", ErrPredictionOutcomeUnknown, err)
	}
	result.Body = append([]byte(nil), raw...)
	return result, nil
}

func predictionErrorEnvelope(body, expected string) bool {
	var envelope struct {
		Error string `json:"error"`
	}
	if err := prediction.Decode([]byte(body), &envelope); err != nil {
		return false
	}
	return envelope.Error == expected
}
func (c *Client) PublishEvent(ctx context.Context, q eventing.Event) (eventing.Receipt, error) {
	v, e := call[eventing.Receipt](ctx, c, http.MethodPost, "/v1/events", nil, q, q.EventID)
	if e == nil && (v.EventID != q.EventID || v.Producer != q.Producer || v.TechnicalStatus != "accepted" || v.ReceiptID == "") {
		e = errors.New("DataCenter event receipt mismatch")
	}
	return v, e
}
func (c *Client) EventReceipt(ctx context.Context, producer, id string) (eventing.Receipt, error) {
	p, e := httpclient.Segment(producer)
	if e != nil {
		return eventing.Receipt{}, e
	}
	path, e := resource("/v1/events/"+p+"/", id, "")
	if e != nil {
		return eventing.Receipt{}, e
	}
	v, e := call[eventing.Receipt](ctx, c, http.MethodGet, path, nil, nil, "")
	if e == nil && (v.EventID != id || v.Producer != producer || v.TechnicalStatus != "accepted" || v.ReceiptID == "") {
		e = errors.New("DataCenter event receipt mismatch")
	}
	return v, e
}
func (c *Client) ReadEvents(ctx context.Context, consumer, producer string, limit int) (eventing.Batch, error) {
	path, e := resource("/v1/event-consumers/", consumer, "/events")
	if e != nil {
		return eventing.Batch{}, e
	}
	v, e := call[eventing.Batch](ctx, c, http.MethodGet, path, url.Values{"producer": {producer}, "limit": {strconv.Itoa(limit)}}, nil, "")
	if e == nil && (v.Consumer != consumer || v.Producer != producer) {
		e = errors.New("DataCenter event batch scope mismatch")
	}
	return v, e
}
func (c *Client) AcknowledgeEvents(ctx context.Context, consumer string, q eventing.Acknowledge) (eventing.DeliveryReceipt, error) {
	path, e := resource("/v1/event-consumers/", consumer, "/ack")
	if e != nil {
		return eventing.DeliveryReceipt{}, e
	}
	return call[eventing.DeliveryReceipt](ctx, c, http.MethodPost, path, nil, q, "")
}
func (c *Client) SourceWatermark(ctx context.Context, producer, aggregate string) (eventing.SourceWatermark, error) {
	p, e := httpclient.Segment(producer)
	if e != nil {
		return eventing.SourceWatermark{}, e
	}
	path, e := resource("/v1/event-sources/"+p+"/aggregates/", aggregate, "")
	if e != nil {
		return eventing.SourceWatermark{}, e
	}
	return call[eventing.SourceWatermark](ctx, c, http.MethodGet, path, nil, nil, "")
}
func (c *Client) SubmitJob(ctx context.Context, q jobs.Submit) (jobs.SubmissionReceipt, error) {
	v, e := call[jobs.SubmissionReceipt](ctx, c, http.MethodPost, "/v1/jobs", nil, q, q.OperationID)
	if e == nil && (v.ID == "" || v.OperationID != q.OperationID || v.Producer != q.Producer || v.TechnicalStatus != "accepted") {
		e = errors.New("DataCenter job receipt mismatch")
	}
	return v, e
}
func (c *Client) ClaimJob(ctx context.Context, q jobs.Claim) (jobs.Job, error) {
	v, e := call[jobs.Job](ctx, c, http.MethodPost, "/v1/jobs/claim", nil, q, "")
	if e == nil && (v.ID == "" || v.InputHash == "" || v.WorkerID != q.WorkerID || v.AttemptID == "" || v.LeaseEpoch <= 0 || v.LeaseExpiresAt == "" || v.Request.JobType != q.JobType || v.Request.ResourceProfile != q.ResourceProfile || v.State != "running") {
		e = errors.New("DataCenter claim receipt mismatch")
	}
	return v, e
}
func (c *Client) GetJob(ctx context.Context, id string) (jobs.Job, error) {
	path, e := resource("/v1/jobs/", id, "")
	if e != nil {
		return jobs.Job{}, e
	}
	v, e := call[jobs.Job](ctx, c, http.MethodGet, path, nil, nil, "")
	if e == nil && v.ID != id {
		e = errors.New("DataCenter job identity mismatch")
	}
	return v, e
}
func (c *Client) RenewJob(ctx context.Context, id string, q jobs.Renew) (jobs.Job, error) {
	path, e := resource("/v1/jobs/", id, "/renew")
	if e != nil {
		return jobs.Job{}, e
	}
	v, e := call[jobs.Job](ctx, c, http.MethodPost, path, nil, q, "")
	if e == nil && (v.ID != id || v.WorkerID != q.WorkerID || v.AttemptID != q.AttemptID || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion) {
		e = errors.New("DataCenter lease receipt mismatch")
	}
	return v, e
}
func (c *Client) CompleteJob(ctx context.Context, id string, q jobs.Complete) (jobs.CompletionReceipt, error) {
	path, e := resource("/v1/jobs/", id, "/complete")
	if e != nil {
		return jobs.CompletionReceipt{}, e
	}
	v, e := call[jobs.CompletionReceipt](ctx, c, http.MethodPost, path, nil, q, "")
	if e == nil && (v.JobID != id || v.AttemptID != q.AttemptID || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion || v.TechnicalState != q.Result.State || v.ResultHash == "") {
		e = errors.New("DataCenter completion receipt mismatch")
	}
	return v, e
}
func (c *Client) CancelJob(ctx context.Context, id string, q jobs.Cancel) (jobs.CancelReceipt, error) {
	path, e := resource("/v1/jobs/", id, "/cancel")
	if e != nil {
		return jobs.CancelReceipt{}, e
	}
	return call[jobs.CancelReceipt](ctx, c, http.MethodPost, path, nil, q, "")
}
func (c *Client) AcknowledgeCancellation(ctx context.Context, id string, q jobs.Lease) (jobs.Job, error) {
	path, e := resource("/v1/jobs/", id, "/cancel/ack")
	if e != nil {
		return jobs.Job{}, e
	}
	v, e := call[jobs.Job](ctx, c, http.MethodPost, path, nil, q, "")
	if e == nil && (v.ID != id || v.WorkerID != q.WorkerID || v.AttemptID != q.AttemptID || v.LeaseEpoch != q.LeaseEpoch || v.CancelVersion != q.CancelVersion) {
		e = errors.New("DataCenter lease receipt mismatch")
	}
	return v, e
}
