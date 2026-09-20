package recommendationv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	activityWorkerRequestSchema  = "activity-event-analysis-request.v1"
	activityWorkerResponseSchema = "activity-event-analysis-response.v1"
	defaultActivityTimeout       = 60 * time.Second
	defaultActivityThreshold     = 1.0
	defaultActivityCacheSize     = 4096
	defaultActivityMaxRequest    = 1024 * 1024
	defaultActivityMaxRawEvent   = 768 * 1024
)

var (
	errActivityInvalidInput = errors.New("activity analysis: invalid input")
	errActivityUnavailable  = errors.New("activity analysis: worker unavailable")
	errActivityContract     = errors.New("activity analysis: worker contract violation")
)

// ActivityAnalyzer analyzes a complete raw event and returns the local trigger
// decision for that event. It must not mutate the supplied raw JSON.
type ActivityAnalyzer interface {
	Analyze(ctx context.Context, input ActivityAnalysisInput) (ActivityAnalysisResult, error)
}

// ActivityAnalysisInput is the event data sent to the remote Qwen worker.
type ActivityAnalysisInput struct {
	EventID         string
	TenantID        string
	User            UserIdentity
	RawEvent        json.RawMessage
	ActivityContext map[string]any
}

// ActivityAnalysisConfig configures the client-side connection and trigger.
// The bearer token is configuration only and must never be logged.
type ActivityAnalysisConfig struct {
	Enabled           bool
	Endpoint          string
	BearerToken       string
	Timeout           time.Duration
	ScoreThreshold    float64
	DecisionCacheSize int
	MaxRequestBytes   int
	MaxRawEventBytes  int
}

// ActivityEvent is one normalized event returned by the Qwen worker.
type ActivityEvent struct {
	SourceEventIDs []string `json:"source_event_ids"`
	Activity       string   `json:"activity"`
	GoalRelevance  string   `json:"goal_relevance"`
	Confidence     float64  `json:"confidence"`
	ReasonCodes    []string `json:"reason_codes"`
	Evidence       []string `json:"evidence"`
	StartedAtMS    *int64   `json:"started_at_ms"`
	EndedAtMS      *int64   `json:"ended_at_ms"`
}

// ActivityAnalysisResult is returned to the local event trigger. A non-empty
// TriggerID is an idempotency key for the downstream Agent invocation.
type ActivityAnalysisResult struct {
	Status           string          `json:"status"`
	RequestID        string          `json:"request_id"`
	Events           []ActivityEvent `json:"events,omitempty"`
	Score            float64         `json:"score"`
	ScoreReason      string          `json:"score_reason,omitempty"`
	AccumulatedScore float64         `json:"accumulated_score"`
	ShouldCallAgent  bool            `json:"should_call_agent"`
	TriggerID        string          `json:"trigger_id,omitempty"`
	ErrorCode        string          `json:"error_code,omitempty"`
}

type activityWorkerRequest struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	RawEvent      json.RawMessage `json:"raw_event"`
	Context       map[string]any  `json:"context,omitempty"`
}

type activityWorkerResponse struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Events        []ActivityEvent `json:"events"`
	Score         float64         `json:"score"`
	ScoreReason   string          `json:"score_reason"`
}

type activityWorkerClient struct {
	config     ActivityAnalysisConfig
	httpClient *http.Client
	trigger    *activityTrigger
	inflight   singleflight.Group
}

// NewActivityWorkerClient builds the synchronous client-side bridge to the
// remote Qwen event worker. A disabled configuration returns nil so callers
// retain their existing event behavior.
func NewActivityWorkerClient(config ActivityAnalysisConfig) (*activityWorkerClient, error) {
	if !config.Enabled {
		return nil, nil
	}
	if err := normalizeActivityConfig(&config); err != nil {
		return nil, err
	}
	return &activityWorkerClient{
		config:     config,
		httpClient: &http.Client{Timeout: config.Timeout},
		trigger:    newActivityTrigger(config.ScoreThreshold, config.DecisionCacheSize),
	}, nil
}

func normalizeActivityConfig(config *ActivityAnalysisConfig) error {
	if config == nil {
		return fmt.Errorf("%w: missing configuration", errActivityInvalidInput)
	}
	endpoint := strings.TrimSpace(config.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("%w: endpoint must be an absolute HTTP(S) URL", errActivityInvalidInput)
	}
	if strings.TrimSpace(config.BearerToken) == "" {
		return fmt.Errorf("%w: bearer token is required when enabled", errActivityInvalidInput)
	}
	config.Endpoint = endpoint
	if config.Timeout <= 0 {
		config.Timeout = defaultActivityTimeout
	}
	if config.ScoreThreshold <= 0 {
		config.ScoreThreshold = defaultActivityThreshold
	}
	if config.ScoreThreshold > 100 {
		return fmt.Errorf("%w: score threshold must be at most 100", errActivityInvalidInput)
	}
	if config.DecisionCacheSize <= 0 {
		config.DecisionCacheSize = defaultActivityCacheSize
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = defaultActivityMaxRequest
	}
	if config.MaxRawEventBytes <= 0 {
		config.MaxRawEventBytes = defaultActivityMaxRawEvent
	}
	if config.MaxRawEventBytes > config.MaxRequestBytes {
		return fmt.Errorf("%w: raw event size cannot exceed request size", errActivityInvalidInput)
	}
	return nil
}

func (c *activityWorkerClient) Analyze(ctx context.Context, input ActivityAnalysisInput) (ActivityAnalysisResult, error) {
	if c == nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: client is disabled", errActivityUnavailable)
	}
	requestID, err := activityRequestID(input.EventID)
	if err != nil {
		return ActivityAnalysisResult{}, err
	}
	identity, err := activityIdentityKey(input.TenantID, input.User)
	if err != nil {
		return ActivityAnalysisResult{Status: "skipped", RequestID: requestID, ErrorCode: "missing_identity"}, nil
	}
	if err := validateActivityRawEvent(input.RawEvent, c.config.MaxRawEventBytes); err != nil {
		return ActivityAnalysisResult{}, err
	}
	cacheKey := identity + "\x00" + requestID
	if cached, ok := c.trigger.get(cacheKey); ok {
		return cached, nil
	}
	value, err, _ := c.inflight.Do(cacheKey, func() (any, error) {
		if cached, ok := c.trigger.get(cacheKey); ok {
			return cached, nil
		}
		return c.analyzeUncached(ctx, input, requestID, identity, cacheKey)
	})
	if err != nil {
		return ActivityAnalysisResult{}, err
	}
	result, ok := value.(ActivityAnalysisResult)
	if !ok {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: invalid in-flight result", errActivityContract)
	}
	return cloneActivityAnalysisResult(result), nil
}

func (c *activityWorkerClient) analyzeUncached(
	ctx context.Context,
	input ActivityAnalysisInput,
	requestID string,
	identity string,
	cacheKey string,
) (ActivityAnalysisResult, error) {
	payload, err := json.Marshal(activityWorkerRequest{
		SchemaVersion: activityWorkerRequestSchema,
		RequestID:     requestID,
		RawEvent:      append(json.RawMessage(nil), input.RawEvent...),
		Context:       cloneAnyMap(input.ActivityContext),
	})
	if err != nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: marshal request", errActivityInvalidInput)
	}
	if len(payload) > c.config.MaxRequestBytes {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: request exceeds configured size", errActivityInvalidInput)
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: build request", errActivityInvalidInput)
	}
	request.Header.Set("Authorization", "Bearer "+c.config.BearerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: request failed: %w", errActivityUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: status %d", errActivityUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(c.config.MaxRequestBytes)+1))
	if err != nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: read response", errActivityUnavailable)
	}
	if len(body) > c.config.MaxRequestBytes {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: response exceeds configured size", errActivityContract)
	}
	var workerResponse activityWorkerResponse
	if err := json.Unmarshal(body, &workerResponse); err != nil {
		return ActivityAnalysisResult{}, fmt.Errorf("%w: invalid JSON response", errActivityContract)
	}
	if err := validateActivityWorkerResponse(workerResponse, requestID); err != nil {
		return ActivityAnalysisResult{}, err
	}

	result := ActivityAnalysisResult{
		Status:      "analyzed",
		RequestID:   requestID,
		Events:      cloneActivityEvents(workerResponse.Events),
		Score:       workerResponse.Score,
		ScoreReason: workerResponse.ScoreReason,
	}
	return c.trigger.apply(identity, cacheKey, result), nil
}

func validateActivityRawEvent(raw json.RawMessage, maximum int) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: raw_event is required", errActivityInvalidInput)
	}
	if len(raw) > maximum {
		return fmt.Errorf("%w: raw_event exceeds configured size", errActivityInvalidInput)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%w: raw_event must be valid JSON", errActivityInvalidInput)
	}
	switch value.(type) {
	case map[string]any, []any:
		return nil
	default:
		return fmt.Errorf("%w: raw_event must be a JSON object or array", errActivityInvalidInput)
	}
}

func validateActivityWorkerResponse(response activityWorkerResponse, requestID string) error {
	if response.SchemaVersion != activityWorkerResponseSchema || response.RequestID != requestID {
		return fmt.Errorf("%w: response identity mismatch", errActivityContract)
	}
	if strings.TrimSpace(response.ScoreReason) == "" {
		return fmt.Errorf("%w: score reason is required", errActivityContract)
	}
	if math.IsNaN(response.Score) || math.IsInf(response.Score, 0) || response.Score < 0 || response.Score > 1 {
		return fmt.Errorf("%w: score is outside [0,1]", errActivityContract)
	}
	if len(response.Events) == 0 && response.Score != 0 {
		return fmt.Errorf("%w: empty event list has non-zero score", errActivityContract)
	}
	for index, event := range response.Events {
		if len(event.SourceEventIDs) == 0 || strings.TrimSpace(event.Activity) == "" || strings.TrimSpace(event.GoalRelevance) == "" {
			return fmt.Errorf("%w: event %d is incomplete", errActivityContract, index)
		}
		if math.IsNaN(event.Confidence) || math.IsInf(event.Confidence, 0) || event.Confidence < 0 || event.Confidence > 1 {
			return fmt.Errorf("%w: event %d confidence is outside [0,1]", errActivityContract, index)
		}
	}
	return nil
}

func activityRequestID(eventID string) (string, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return "", fmt.Errorf("%w: event_id is required", errActivityInvalidInput)
	}
	if len(eventID) <= 128 {
		return eventID, nil
	}
	digest := sha256.Sum256([]byte(eventID))
	return "activity_" + hex.EncodeToString(digest[:]), nil
}

func activityIdentityKey(tenantID string, user UserIdentity) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		tenantID = strings.TrimSpace(user.TenantID)
	}
	for _, candidate := range []struct {
		kind  string
		value string
	}{
		{kind: "user", value: user.UserID},
		{kind: "anonymous", value: user.AnonymousID},
		{kind: "session", value: user.SessionID},
		{kind: "device", value: user.DeviceID},
	} {
		if value := strings.TrimSpace(candidate.value); value != "" {
			return tenantID + "\x00" + candidate.kind + "\x00" + value, nil
		}
	}
	return "", fmt.Errorf("%w: user, anonymous, session, or device identity is required", errActivityInvalidInput)
}

type activityTrigger struct {
	mu        sync.Mutex
	threshold float64
	maxCached int
	totals    map[string]float64
	results   map[string]ActivityAnalysisResult
	order     []string
}

func newActivityTrigger(threshold float64, maxCached int) *activityTrigger {
	return &activityTrigger{
		threshold: threshold,
		maxCached: maxCached,
		totals:    make(map[string]float64),
		results:   make(map[string]ActivityAnalysisResult),
	}
}

func (t *activityTrigger) get(cacheKey string) (ActivityAnalysisResult, bool) {
	if t == nil {
		return ActivityAnalysisResult{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result, ok := t.results[cacheKey]
	return cloneActivityAnalysisResult(result), ok
}

func (t *activityTrigger) apply(identity, cacheKey string, result ActivityAnalysisResult) ActivityAnalysisResult {
	if t == nil {
		return result
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cached, ok := t.results[cacheKey]; ok {
		return cloneActivityAnalysisResult(cached)
	}
	total := t.totals[identity] + result.Score
	result.AccumulatedScore = total
	if total >= t.threshold {
		result.ShouldCallAgent = true
		result.TriggerID = activityTriggerID(cacheKey)
		t.totals[identity] = 0
	} else {
		t.totals[identity] = total
	}
	t.results[cacheKey] = cloneActivityAnalysisResult(result)
	t.order = append(t.order, cacheKey)
	for len(t.order) > t.maxCached {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.results, oldest)
	}
	return cloneActivityAnalysisResult(result)
}

func activityTriggerID(cacheKey string) string {
	digest := sha256.Sum256([]byte(cacheKey))
	return "activity_" + hex.EncodeToString(digest[:16])
}

func cloneActivityAnalysisResult(input ActivityAnalysisResult) ActivityAnalysisResult {
	output := input
	output.Events = cloneActivityEvents(input.Events)
	return output
}

func cloneActivityEvents(input []ActivityEvent) []ActivityEvent {
	if len(input) == 0 {
		return nil
	}
	output := make([]ActivityEvent, len(input))
	for index, event := range input {
		output[index] = event
		output[index].SourceEventIDs = append([]string(nil), event.SourceEventIDs...)
		output[index].ReasonCodes = append([]string(nil), event.ReasonCodes...)
		output[index].Evidence = append([]string(nil), event.Evidence...)
		if event.StartedAtMS != nil {
			value := *event.StartedAtMS
			output[index].StartedAtMS = &value
		}
		if event.EndedAtMS != nil {
			value := *event.EndedAtMS
			output[index].EndedAtMS = &value
		}
	}
	return output
}
