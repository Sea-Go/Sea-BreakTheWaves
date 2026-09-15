package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type ToolsExecutor interface {
	Search(context.Context, searchdomain.ToolRunRequest) (searchdomain.SearchResult, error)
}

// ToolsProfilePreflight is required by the optional medium route so a missing
// policy or signed downgrade returns 503 before the framework Runner starts.
type ToolsProfilePreflight interface {
	PreflightProfile(context.Context, searchdomain.Request) error
}

type toolsHandler struct {
	resolver      ToolsScopeResolver
	executor      ToolsExecutor
	observed      *telemetry.Bundle
	mediumEnabled bool
}

func NewToolsHandler(resolver ToolsScopeResolver, executor ToolsExecutor, observed *telemetry.Bundle) (http.Handler, error) {
	return newToolsHandler(resolver, executor, observed, false)
}

// NewToolsHandlerWithFastMedium is an explicit opt-in for one additional
// product profile. The original constructor remains fast/low only.
func NewToolsHandlerWithFastMedium(resolver ToolsScopeResolver, executor ToolsExecutor,
	observed *telemetry.Bundle) (http.Handler, error) {
	if _, ok := executor.(ToolsProfilePreflight); !ok {
		return nil, ErrInvalidRequest
	}
	return newToolsHandler(resolver, executor, observed, true)
}

func newToolsHandler(resolver ToolsScopeResolver, executor ToolsExecutor, observed *telemetry.Bundle,
	mediumEnabled bool) (http.Handler, error) {
	if nilDependency(resolver) || nilDependency(executor) || observed == nil || !observed.Installed() || observed.Closed() {
		return nil, ErrInvalidRequest
	}
	h := &toolsHandler{resolver: resolver, executor: executor, observed: observed, mediumEnabled: mediumEnabled}
	return otelhttp.NewHandler(h, "search.tools", otelhttp.WithSpanNameFormatter(func(_ string, _ *http.Request) string {
		return "POST " + ToolsRoute
	})), nil
}

type ToolEvidence struct {
	EvidenceID string `json:"evidence_id"`
	RevisionID string `json:"revision_id"`
	Locator    string `json:"locator"`
	Quote      string `json:"quote"`
	QuoteHash  string `json:"quote_hash"`
	SourceKind string `json:"source_kind"`
}

type ToolsResponse struct {
	SearchID              string                        `json:"search_id"`
	Status                string                        `json:"status"`
	StopReason            string                        `json:"stop_reason"`
	SnapshotRef           string                        `json:"snapshot_ref"`
	RequestedIntelligence searchdomain.Intelligence     `json:"requested_intelligence"`
	EffectiveIntelligence searchdomain.Intelligence     `json:"effective_intelligence"`
	Evidence              []ToolEvidence                `json:"evidence"`
	Gaps                  []string                      `json:"gaps"`
	Conflicts             []string                      `json:"conflicts"`
	PackHash              string                        `json:"pack_hash,omitempty"`
	CitationReceipt       *searchdomain.CitationReceipt `json:"citation_receipt,omitempty"`
	Usage                 struct {
		ReadCalls  int `json:"read_calls"`
		QuoteRunes int `json:"quote_runes"`
	} `json:"usage"`
}

func (h *toolsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, stage, err := h.observed.Begin(r.Context(), "search", "search.http.tools", slog.String("route", ToolsRoute))
	if err != nil {
		writeToolsError(w, http.StatusServiceUnavailable, "OBSERVABILITY_UNAVAILABLE")
		return
	}
	status, outcome, code := http.StatusInternalServerError, "failed", "TOOLS_SEARCH_FAILED"
	var cause error
	var operationID, searchID string
	defer func() {
		stage.End(ctx, outcome, code, cause, slog.Int("http_status", status),
			slog.String("operation_id", operationID), slog.String("search_id", searchID))
	}()
	if r.Method != http.MethodPost {
		status, outcome, code = http.StatusMethodNotAllowed, "rejected", "METHOD_NOT_ALLOWED"
		cause = ErrInvalidRequest
		writeToolsError(w, status, code)
		return
	}
	if r.URL.Path != ToolsRoute {
		status, outcome, code = http.StatusNotFound, "rejected", "ROUTE_NOT_FOUND"
		cause = ErrInvalidRequest
		writeToolsError(w, status, code)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		status, outcome, code = http.StatusUnsupportedMediaType, "rejected", "TOOLS_MEDIA_TYPE"
		cause = ErrInvalidRequest
		writeToolsError(w, status, code)
		return
	}
	var body ToolsRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || !strictJSON(raw, &body) {
		status, outcome, code = http.StatusBadRequest, "rejected", "TOOLS_REQUEST_INVALID"
		cause = ErrInvalidRequest
		writeToolsError(w, status, code)
		return
	}
	canonical, err := json.Marshal(body)
	if err != nil || !bytes.Equal(raw, canonical) || !validToolsID(body.ModuleID) || !validToolsID(body.SearchID) ||
		strings.TrimSpace(body.Query) == "" || len(body.Query) > 4096 ||
		(body.Depth != searchdomain.Fast && body.Depth != searchdomain.Detailed) ||
		(body.Intelligence != searchdomain.Low && body.Intelligence != searchdomain.Medium && body.Intelligence != searchdomain.High) ||
		body.Limits.ReadCalls < 1 || body.Limits.ReadCalls > maxToolsReads ||
		body.Limits.QuoteRunes < 1 || body.Limits.QuoteRunes > maxToolsQuoteRunes {
		status, outcome, code = http.StatusBadRequest, "rejected", "TOOLS_REQUEST_INVALID"
		cause = ErrInvalidRequest
		writeToolsError(w, status, code)
		return
	}
	scope, err := h.resolver.ResolveTools(ctx, r.WithContext(ctx), body)
	if err != nil {
		status, outcome, code = scopeError(err)
		cause = err
		writeToolsError(w, status, code)
		return
	}
	operationID, searchID = scope.OperationID, scope.SearchID
	if !validToolsScope(body, scope) {
		status, outcome, code = http.StatusForbidden, "rejected", "TOOLS_SCOPE_INVALID"
		cause = ErrScopeDenied
		writeToolsError(w, status, code)
		return
	}
	stage.SetAttributes(slog.String("operation_id", operationID), slog.String("search_id", searchID),
		slog.String("release_id", scope.Snapshot.ReleaseID), slog.Int64("generation", scope.Snapshot.Generation),
		slog.String("publication_revision", scope.Snapshot.PublicationRevision))
	// The medium candidate is independent of the existing Summary policy.
	// Unsupported signed profiles must never reach a Reader or Agent.
	if body.Depth != searchdomain.Fast || body.Intelligence != searchdomain.Low &&
		(body.Intelligence != searchdomain.Medium || !h.mediumEnabled) {
		status, outcome, code = http.StatusServiceUnavailable, "rejected", "TOOLS_PROFILE_UNAVAILABLE"
		cause = searchdomain.ErrUnavailable
		writeToolsError(w, status, code)
		return
	}
	q := searchdomain.ToolRunRequest{Subject: scope.Subject, SessionID: scope.SessionID,
		OperationID: scope.OperationID, BudgetRef: scope.BudgetRef, SearchID: scope.SearchID,
		Limits: searchdomain.EvidenceLimits{MaxReads: body.Limits.ReadCalls, MaxQuoteRunes: body.Limits.QuoteRunes},
		Search: searchdomain.Request{Query: body.Query, Depth: body.Depth, Intelligence: body.Intelligence,
			AllowPartial: scope.AllowPartial, AllowLowerIntelligence: scope.AllowLowerIntelligence,
			Snapshot: copySnapshot(scope.Snapshot)}}
	if body.Intelligence == searchdomain.Medium {
		if err := h.executor.(ToolsProfilePreflight).PreflightProfile(ctx, q.Search); err != nil {
			status, outcome, code = toolsRunError(err)
			cause = err
			if ctx.Err() == nil {
				writeToolsError(w, status, code)
			}
			return
		}
	}
	found, err := h.executor.Search(ctx, q)
	if err != nil {
		status, outcome, code = toolsRunError(err)
		cause = err
		if ctx.Err() == nil {
			writeToolsError(w, status, code)
		}
		return
	}
	public, err := projectToolsResult(found, q, scope.SnapshotRef)
	if err != nil {
		status, outcome, code = http.StatusBadGateway, "failed", "TOOLS_RESULT_INVALID"
		cause = err
		writeToolsError(w, status, code)
		return
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		status, outcome, code = http.StatusBadGateway, "failed", "TOOLS_RESULT_ENCODING"
		cause = err
		writeToolsError(w, status, code)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(append(encoded, '\n')); err != nil || n != len(encoded)+1 {
		status, outcome, code = http.StatusInternalServerError, "failed", "TOOLS_RESPONSE_WRITE_FAILED"
		cause = errors.Join(err, io.ErrShortWrite)
		return
	}
	status, outcome, code = http.StatusOK, "succeeded", ""
}

func validToolsScope(body ToolsRequest, scope TrustedToolsScope) bool {
	if !constantTimeStringEqual(body.SearchID, scope.SearchID) ||
		!constantTimeStringEqual(body.ModuleID, scope.Snapshot.ModuleID) ||
		!validToolsSession(scope.SessionID) || !validToolsID(scope.OperationID) ||
		!validToolsID(scope.BudgetRef) || scope.SnapshotRef == "" {
		return false
	}
	if _, err := scope.Subject.UserKey(); err != nil {
		return false
	}
	_, err := searchdomain.SearchGraphRunOption(searchdomain.Request{Query: "scope_check", Depth: searchdomain.Fast,
		Intelligence: searchdomain.Low, Snapshot: scope.Snapshot})
	return err == nil
}

func projectToolsResult(found searchdomain.SearchResult, q searchdomain.ToolRunRequest, snapshotRef string) (ToolsResponse, error) {
	var out ToolsResponse
	pack := found.Pack
	if pack.SearchID != q.SearchID || !reflect.DeepEqual(pack.Snapshot, q.Search.Snapshot) ||
		pack.Status != "complete" && pack.Status != "partial" && pack.Status != "empty" ||
		pack.StopReason == "" || len(pack.StopReason) > 256 ||
		pack.Profile.RequestedDepth != q.Search.Depth || pack.Profile.EffectiveDepth != q.Search.Depth ||
		pack.Profile.RequestedIntelligence != q.Search.Intelligence ||
		found.Usage.SourceReadAttempts < len(pack.Evidence) || found.Usage.SourceReadAttempts > q.Limits.MaxReads ||
		found.Usage.QuoteRunes < 0 || found.Usage.QuoteRunes > q.Limits.MaxQuoteRunes ||
		len(pack.Evidence) > 100 || len(pack.Gaps) > 100 ||
		pack.Status == "complete" && len(pack.Gaps) != 0 {
		return out, ErrInvalidResult
	}
	if pack.Profile.EffectiveIntelligence != q.Search.Intelligence {
		return out, ErrInvalidResult
	}
	out = ToolsResponse{SearchID: q.SearchID, Status: pack.Status, StopReason: pack.StopReason,
		SnapshotRef: snapshotRef, RequestedIntelligence: pack.Profile.RequestedIntelligence,
		EffectiveIntelligence: pack.Profile.EffectiveIntelligence,
		Evidence:              []ToolEvidence{}, Gaps: append([]string{}, pack.Gaps...), Conflicts: []string{}}
	out.Usage.ReadCalls, out.Usage.QuoteRunes = found.Usage.SourceReadAttempts, found.Usage.QuoteRunes
	var runes int
	for _, item := range pack.Evidence {
		if item.ID == "" || item.Key.RevisionID == "" || item.Locator.Locator == "" ||
			item.Quote == "" || artifacts.Hash([]byte(item.Quote)) != item.QuoteHash {
			return ToolsResponse{}, ErrInvalidResult
		}
		runes += utf8.RuneCountInString(item.Quote)
		out.Evidence = append(out.Evidence, ToolEvidence{EvidenceID: item.ID, RevisionID: item.Key.RevisionID,
			Locator: item.Locator.Locator, Quote: item.Quote, QuoteHash: item.QuoteHash,
			SourceKind: item.Key.SourceKind})
	}
	if runes > found.Usage.QuoteRunes {
		return ToolsResponse{}, ErrInvalidResult
	}
	if len(pack.Evidence) == 0 {
		if pack.Status != "empty" || found.Receipt != (searchdomain.CitationReceipt{}) {
			return ToolsResponse{}, ErrInvalidResult
		}
		return out, nil
	}
	if pack.Status == "empty" || found.Receipt.SearchID != q.SearchID || found.Receipt.DurableRef == "" {
		return ToolsResponse{}, ErrInvalidResult
	}
	hash, err := pack.Hash()
	if err != nil || hash != found.Receipt.PackHash {
		return ToolsResponse{}, ErrInvalidResult
	}
	out.PackHash, out.CitationReceipt = hash, &found.Receipt
	return out, nil
}

func toolsRunError(err error) (int, string, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return 499, "cancelled", "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timed_out", "TIMEOUT"
	case errors.Is(err, searchdomain.ErrUnavailable):
		return http.StatusServiceUnavailable, "rejected", "TOOLS_PROFILE_UNAVAILABLE"
	case errors.Is(err, searchdomain.ErrInvalid):
		return http.StatusBadRequest, "rejected", "TOOLS_SEARCH_INVALID"
	case errors.Is(err, searchdomain.ErrReceipt):
		return http.StatusConflict, "rejected", "TOOLS_RECEIPT_CONFLICT"
	default:
		return http.StatusBadGateway, "failed", "TOOLS_RUN_FAILED"
	}
}

func writeToolsError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoded, _ := json.Marshal(struct {
		ErrorCode string `json:"error_code"`
	}{code})
	_, _ = w.Write(append(encoded, '\n'))
}
