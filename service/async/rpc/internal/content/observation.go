package content

import (
	"context"
	"errors"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
)

// contentObservation classifies one application operation after its actual
// result is known. Low-level functions retain errors and do not log them again.
func contentObservation(err error) (string, string) {
	if err == nil {
		return "succeeded", ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled", "CANCELLED"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out", "TIMEOUT"
	case errors.Is(err, ErrInvalidated):
		return "rejected", "REVISION_INVALIDATED"
	case errors.Is(err, ErrConflict):
		return "rejected", "BUILD_CONFLICT"
	case errors.Is(err, ErrInvalid):
		return "rejected", "INVALID_CONTENT"
	}
	var upstream *httpclient.HTTPError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusNotFound:
			return "rejected", "FIXED_SOURCE_MISSING"
		case http.StatusConflict:
			return "rejected", "SOURCE_VERSION_CONFLICT"
		case http.StatusGone:
			return "rejected", "REVISION_WITHDRAWN"
		case http.StatusTooManyRequests:
			return "failed", "UPSTREAM_RATE_LIMITED"
		default:
			if upstream.StatusCode >= http.StatusInternalServerError {
				return "failed", "UPSTREAM_FAILURE"
			}
			return "rejected", "UPSTREAM_REJECTED"
		}
	}
	return "failed", "CONTENT_OPERATION_FAILED"
}
