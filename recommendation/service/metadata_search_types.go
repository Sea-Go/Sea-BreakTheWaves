package service

import (
	"errors"
	"reflect"
	"strings"

	"github.com/google/uuid"
)

var ErrSourceMetadataUnavailable = errors.New("source_metadata_unavailable")

type StructuredSearchRequest struct {
	SearchRequestID string `json:"search_request_id,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	Query           string `json:"query" binding:"required"`
	TopK            int    `json:"topk,omitempty"`
	TopKLegacy      int    `json:"top_k,omitempty"`
}

func normalizeStructuredSearchRequest(prefix string, req StructuredSearchRequest) StructuredSearchRequest {
	req.Query = strings.TrimSpace(req.Query)
	if req.SearchRequestID == "" {
		req.SearchRequestID = strings.TrimSpace(req.RequestID)
	}
	if req.SearchRequestID == "" {
		req.SearchRequestID = prefix + "_" + compactUUID(16)
	}
	if req.TopK <= 0 {
		req.TopK = req.TopKLegacy
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 50 {
		req.TopK = 50
	}
	return req
}

func newMetadataSearchTraceID() string {
	return compactUUID(32)
}

func compactUUID(maxLen int) string {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	if maxLen > 0 && len(id) > maxLen {
		return id[:maxLen]
	}
	return id
}

func isNilSearchDependency(value any) bool {
	if value == nil {
		return true
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
