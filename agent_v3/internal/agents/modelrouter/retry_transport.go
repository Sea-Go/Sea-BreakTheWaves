package modelrouter

import (
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

var rateLimitHTTPClient = &http.Client{
	Transport: newRetryAfter429Transport(2),
}

// retryAfter429Transport wraps an http.RoundTripper and on 429 responses,
// waits 60 seconds and retries the request up to maxRetries times.
type retryAfter429Transport struct {
	transport  http.RoundTripper
	maxRetries int
}

func newRetryAfter429Transport(maxRetries int) *retryAfter429Transport {
	return &retryAfter429Transport{
		transport:  http.DefaultTransport,
		maxRetries: maxRetries,
	}
}

func (t *retryAfter429Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		// Clone request body for retry (the original body may be consumed).
		if attempt > 0 && req.Body != nil && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := t.transport.RoundTrip(req)
		if err != nil {
			lastErr = err
			if attempt < t.maxRetries {
				time.Sleep(60 * time.Second)
			}
			continue
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			log.Warnf("[retry-transport] 429 rate limited, waiting 60s before retry %d/%d", attempt+1, t.maxRetries)
			if attempt < t.maxRetries {
				time.Sleep(60 * time.Second)
			}
			continue
		}

		return resp, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	// Should not reach here normally, but return a generic 429 error if all retries exhausted.
	return nil, &http.MaxBytesError{}
}
