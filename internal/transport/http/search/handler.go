// Package search exposes a narrow HTTP handoff to the RTW product facade.
// The facade supplies authoritative identity, session and publication scope;
// client JSON cannot choose them.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const Route = "/v1/search/summary"
const maxBodyBytes = 16 << 10

var (
	ErrInvalidRequest   = errors.New("invalid search HTTP request")
	ErrScopeDenied      = errors.New("trusted search scope denied")
	ErrScopeUnavailable = errors.New("trusted search scope unavailable")
	ErrInvalidResult    = errors.New("search result did not satisfy public contract")
)

// TrustedScope is issued by the authenticated RTW facade for exactly one
// logical search. It is never decoded from the client's request body.
type TrustedScope struct {
	Subject                btwruntime.SubjectRef
	SessionID              string
	SearchID               string
	AnswerID               string
	Snapshot               searchdomain.Snapshot
	AllowPartial           bool
	AllowLowerIntelligence bool
}

type ScopeResolver interface {
	ResolveSearch(context.Context, *http.Request, PublicRequest) (TrustedScope, error)
}

type ScopeFunc func(context.Context, *http.Request, PublicRequest) (TrustedScope, error)

func (f ScopeFunc) ResolveSearch(ctx context.Context, r *http.Request, request PublicRequest) (TrustedScope, error) {
	return f(ctx, r, request)
}

type Handler struct {
	resolver ScopeResolver
	summary  *searchdomain.RootSessionBoundary
	observed *telemetry.Bundle
}

// NewHandler requires the accepted-history boundary at the type level. The
// bare RootSummarizer may persist invalid model events in its framework Session
// and must never be wired to a product-facing HTTP path.
func NewHandler(resolver ScopeResolver, summary *searchdomain.RootSessionBoundary, observed *telemetry.Bundle) (http.Handler, error) {
	if nilDependency(resolver) || summary == nil || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, ErrInvalidRequest
	}
	h := &Handler{resolver: resolver, summary: summary, observed: observed}
	return otelhttp.NewHandler(h, "search.summary", otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
		return "POST " + Route
	})), nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// PublicRequest is the sole client-controlled input to the trusted scope
// resolver. Its JSON field order is the H02 SHA256 request-hash wire contract.
type PublicRequest struct {
	ModuleID     string                    `json:"module_id"`
	Query        string                    `json:"query"`
	Depth        searchdomain.Depth        `json:"depth"`
	Intelligence searchdomain.Intelligence `json:"intelligence"`
}

type Citation struct {
	EvidenceID string          `json:"evidence_id"`
	SourceKind string          `json:"source_kind"`
	ContentID  string          `json:"content_id"`
	RevisionID string          `json:"revision_id"`
	Locator    corpus.Location `json:"locator"`
	Original   corpus.Ref      `json:"original"`
	Quote      string          `json:"quote"`
	QuoteHash  string          `json:"quote_hash"`
}

type Response struct {
	SearchID   string     `json:"search_id"`
	AnswerID   string     `json:"answer_id"`
	Status     string     `json:"status"`
	Answer     string     `json:"answer,omitempty"`
	Citations  []Citation `json:"citations"`
	ReceiptRef string     `json:"citation_receipt_ref,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, stage, err := h.observed.Begin(r.Context(), "search", "search.http.summary", slog.String("route", Route))
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "OBSERVABILITY_UNAVAILABLE")
		return
	}
	status, outcome, code := http.StatusInternalServerError, "failed", "SEARCH_FAILED"
	var cause error
	var operationID string
	defer func() {
		stage.End(ctx, outcome, code, cause, slog.Int("http_status", status), slog.String("operation_id", operationID))
	}()
	if r.Method != http.MethodPost {
		status, outcome, code = http.StatusMethodNotAllowed, "rejected", "METHOD_NOT_ALLOWED"
		cause = ErrInvalidRequest
		h.writeError(w, status, code)
		return
	}
	if r.URL.Path != Route {
		status, outcome, code = http.StatusNotFound, "rejected", "ROUTE_NOT_FOUND"
		cause = ErrInvalidRequest
		h.writeError(w, status, code)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		status, outcome, code = http.StatusUnsupportedMediaType, "rejected", "SEARCH_MEDIA_TYPE"
		cause = ErrInvalidRequest
		h.writeError(w, status, code)
		return
	}
	var body PublicRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF ||
		body.ModuleID == "" || strings.TrimSpace(body.Query) == "" || len(body.Query) > 4096 ||
		(body.Depth != searchdomain.Fast && body.Depth != searchdomain.Detailed) ||
		(body.Intelligence != searchdomain.Low && body.Intelligence != searchdomain.Medium && body.Intelligence != searchdomain.High) {
		status, outcome, code = http.StatusBadRequest, "rejected", "SEARCH_REQUEST_INVALID"
		cause = ErrInvalidRequest
		h.writeError(w, status, code)
		return
	}
	scope, err := h.resolver.ResolveSearch(ctx, r.WithContext(ctx), body)
	if err != nil {
		status, outcome, code = scopeError(err)
		cause = err
		h.writeError(w, status, code)
		return
	}
	if err := validScope(body.ModuleID, scope); err != nil {
		status, outcome, code = http.StatusBadGateway, "failed", "SEARCH_SCOPE_INVALID"
		cause = err
		h.writeError(w, status, code)
		return
	}
	operationID = scope.AnswerID
	q := searchdomain.SummaryRequest{Subject: scope.Subject, SessionID: scope.SessionID,
		SearchID: scope.SearchID, AnswerID: scope.AnswerID,
		Search: searchdomain.Request{Query: body.Query, Depth: body.Depth, Intelligence: body.Intelligence,
			AllowPartial: scope.AllowPartial, AllowLowerIntelligence: scope.AllowLowerIntelligence,
			Snapshot: copySnapshot(scope.Snapshot)}}
	result, err := h.summary.Summarize(ctx, q)
	if err != nil {
		status, outcome, code = summaryError(err)
		cause = err
		if ctx.Err() == nil {
			h.writeError(w, status, code)
		}
		return
	}
	public, err := projectResult(result, q)
	if err != nil {
		status, outcome, code = http.StatusBadGateway, "failed", "SEARCH_RESULT_INVALID"
		cause = err
		h.writeError(w, status, code)
		return
	}
	raw, err := json.Marshal(public)
	if err != nil {
		status, outcome, code = http.StatusBadGateway, "failed", "SEARCH_RESULT_ENCODING"
		cause = err
		h.writeError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(append(raw, '\n')); err != nil || n != len(raw)+1 {
		status, outcome, code = http.StatusInternalServerError, "failed", "SEARCH_RESPONSE_WRITE_FAILED"
		cause = errors.Join(err, io.ErrShortWrite)
		return
	}
	status, outcome, code = http.StatusOK, "succeeded", ""
}

func copySnapshot(s searchdomain.Snapshot) searchdomain.Snapshot {
	s.ValidRevisionIDs = append([]string(nil), s.ValidRevisionIDs...)
	indexes := make(map[searchdomain.Lane]corpus.Ref, len(s.Indexes))
	for lane, ref := range s.Indexes {
		indexes[lane] = ref
	}
	s.Indexes = indexes
	return s
}

func validScope(module string, s TrustedScope) error {
	if module != s.Snapshot.ModuleID || s.SessionID == "" || s.SearchID == "" || s.AnswerID == "" {
		return ErrInvalidResult
	}
	if _, err := s.Subject.UserKey(); err != nil {
		return ErrInvalidResult
	}
	_, err := searchdomain.SearchGraphRunOption(searchdomain.Request{Query: "scope_check", Depth: searchdomain.Fast,
		Intelligence: searchdomain.Low, Snapshot: copySnapshot(s.Snapshot)})
	return err
}

func projectResult(result searchdomain.SummaryResult, request searchdomain.SummaryRequest) (Response, error) {
	if result.AnswerID != request.AnswerID || result.Search.Pack.SearchID != request.SearchID ||
		!reflect.DeepEqual(result.Search.Pack.Snapshot, request.Search.Snapshot) {
		return Response{}, ErrInvalidResult
	}
	response := Response{SearchID: request.SearchID, AnswerID: request.AnswerID,
		Status: result.SummaryStatus, Citations: []Citation{}}
	if result.SummaryStatus == "insufficient" {
		if result.Answer != "" || len(result.Citations) != 0 || len(result.Search.Pack.Evidence) != 0 ||
			result.Search.Pack.Status != "empty" || result.Search.Receipt != (searchdomain.CitationReceipt{}) {
			return Response{}, ErrInvalidResult
		}
		return response, nil
	}
	if result.SummaryStatus != "succeeded" || strings.TrimSpace(result.Answer) == "" ||
		len(result.Citations) == 0 || result.Search.Receipt.DurableRef == "" ||
		result.Search.Receipt.SearchID != request.SearchID {
		return Response{}, ErrInvalidResult
	}
	hash, err := result.Search.Pack.Hash()
	if err != nil || hash != result.Search.Receipt.PackHash {
		return Response{}, ErrInvalidResult
	}
	evidence := make(map[string]searchdomain.Evidence, len(result.Search.Pack.Evidence))
	for _, item := range result.Search.Pack.Evidence {
		if item.ID == "" || item.Quote == "" || artifacts.Hash([]byte(item.Quote)) != item.QuoteHash {
			return Response{}, ErrInvalidResult
		}
		if _, exists := evidence[item.ID]; exists {
			return Response{}, ErrInvalidResult
		}
		evidence[item.ID] = item
	}
	seen := make(map[string]bool, len(result.Citations))
	for _, id := range result.Citations {
		item, ok := evidence[id]
		if !ok || seen[id] {
			return Response{}, ErrInvalidResult
		}
		seen[id] = true
		response.Citations = append(response.Citations, Citation{EvidenceID: item.ID,
			SourceKind: item.Key.SourceKind, ContentID: item.Key.ContentID,
			RevisionID: item.Key.RevisionID, Locator: item.Locator, Original: item.Original,
			Quote: item.Quote, QuoteHash: item.QuoteHash})
	}
	response.Answer, response.ReceiptRef = result.Answer, result.Search.Receipt.DurableRef
	return response, nil
}

func scopeError(err error) (int, string, string) {
	if errors.Is(err, ErrScopeDenied) {
		return http.StatusForbidden, "rejected", "SEARCH_SCOPE_DENIED"
	}
	return http.StatusServiceUnavailable, "failed", "SEARCH_SCOPE_UNAVAILABLE"
}

func summaryError(err error) (int, string, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return 499, "cancelled", "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timed_out", "TIMEOUT"
	case errors.Is(err, searchdomain.ErrInvalid):
		return http.StatusBadRequest, "rejected", "SEARCH_INVALID"
	case errors.Is(err, searchdomain.ErrReceipt), errors.Is(err, searchdomain.ErrAcceptedHistory):
		return http.StatusConflict, "rejected", "SEARCH_RECEIPT_CONFLICT"
	default:
		return http.StatusBadGateway, "failed", "SEARCH_RUN_FAILED"
	}
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error_code":%q}`+"\n", code)
}
