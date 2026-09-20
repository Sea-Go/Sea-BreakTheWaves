package recommendationv2

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRecommendationRuntimeEmitsOTelSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = provider.Shutdown(context.Background()) }()

	resp, err := NewRecommendationRuntime().Recommend(context.Background(), RecommendRequest{
		TenantID: "tenant-otel",
		Channel:  "otel_channel",
		User:     UserIdentity{UserID: "u-otel"},
		TopK:     2,
		PathMode: PathHybrid,
		Debug:    true,
	})
	if err != nil {
		t.Fatalf("Recommend returned error: %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected OTel spans to be exported")
	}

	root := findSpanByName(spans, "recommendation.v2.run")
	if root == nil {
		t.Fatalf("missing recommendation.v2.run span, got spans: %+v", spanNames(spans))
	}
	assertSpanAttr(t, root.Attributes, "sea.trace_id", resp.TraceID)
	assertSpanAttr(t, root.Attributes, "recommendation.request_id", resp.RequestID)
	assertSpanAttr(t, root.Attributes, "recommendation.tenant_id", "tenant-otel")
	assertSpanAttr(t, root.Attributes, "recommendation.path_taken", PathHybrid)

	for _, name := range []string{
		"recommendation.v2.node.normalize_request",
		"recommendation.v2.node.hybrid_recall",
		"recommendation.v2.node.agent_rerank",
		"recommendation.v2.node.build_response",
	} {
		span := findSpanByName(spans, name)
		if span == nil {
			t.Fatalf("missing node span %s, got spans: %+v", name, spanNames(spans))
		}
		assertSpanAttr(t, span.Attributes, "sea.trace_id", resp.TraceID)
	}
}

func findSpanByName(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	return names
}

func assertSpanAttr(t *testing.T, attrs []attribute.KeyValue, key string, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			if attr.Value.AsString() != want {
				t.Fatalf("span attr %s = %q, want %q", key, attr.Value.AsString(), want)
			}
			return
		}
	}
	t.Fatalf("span attr %s not found in %+v", key, attrs)
}
