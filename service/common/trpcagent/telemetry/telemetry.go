package telemetry

import (
	"context"

	tracetelemetry "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

// Start turns on framework tracing with the Sea service identity.
func Start(ctx context.Context, serviceName string) (func() error, error) {
	return tracetelemetry.Start(ctx, tracetelemetry.WithServiceName(serviceName), tracetelemetry.WithServiceNamespace("sea"))
}
