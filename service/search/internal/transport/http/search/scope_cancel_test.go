package search

import (
	"context"
	"testing"
)

func TestScopeCancellationKeepsTransportStatusBeforeRunner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"cancelled native preparation", context.Canceled, 499, "CANCELLED"},
		{"native load deadline", context.DeadlineExceeded, 504, "TIMEOUT"},
		{"old invalid signature", ErrScopeDenied, 403, "SEARCH_SCOPE_DENIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, code := scopeError(tc.err)
			if status != tc.status || code != tc.code {
				t.Fatalf("scope failure reached Runner status: %d %s", status, code)
			}
		})
	}
}
