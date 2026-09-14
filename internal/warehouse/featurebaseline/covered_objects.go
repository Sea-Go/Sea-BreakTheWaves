package featurebaseline

import "context"

// RunnerCoveredObjects reuses the existing local S3 HTTP transport and its
// immutable preflight/readback rule. It has no CH/dbt dependency; v2 covered
// candidates are still default-off until WS08 Accept v2 and Serving wiring.
type RunnerCoveredObjects struct{ Runner Runner }

func (r RunnerCoveredObjects) ReadFixed(ctx context.Context, target, expectedHash string) ([]byte, error) {
	return r.Runner.getFixed(ctx, target, expectedHash)
}

func (r RunnerCoveredObjects) PutFixed(ctx context.Context, target string, body []byte) error {
	return r.Runner.putFixed(ctx, target, body)
}
