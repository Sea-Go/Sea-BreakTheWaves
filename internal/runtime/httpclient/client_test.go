package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestTransportBoundaries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/v1/error":
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"conflict"}`)
		case "/v1/redirect":
			http.Redirect(w, r, "/v1/error", 307)
		case "/v1/large":
			io.WriteString(w, "123456789")
		case "/v1/cancel":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			t.Error("unexpected route", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, MaxResponseBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Do(context.Background(), "GET", "/v1/large", nil, nil, "")
	if err == nil {
		t.Fatal("accepted oversized body")
	}
	client, _ = New(Config{BaseURL: server.URL})
	_, _, err = client.Do(context.Background(), "POST", "/v1/error", nil, map[string]int{"n": 1}, "operation")
	var status *HTTPError
	if !errors.As(err, &status) || status.StatusCode != 409 || status.RetryAfter != "3" {
		t.Fatalf("status error %v", err)
	}
	before := calls.Load()
	_, _, err = client.Do(context.Background(), "POST", "/v1/redirect", nil, nil, "")
	if !errors.As(err, &status) || status.StatusCode != 307 || calls.Load() != before+1 {
		t.Fatalf("redirect followed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err = client.Do(ctx, "GET", "/v1/cancel", nil, nil, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost deadline: %v", err)
	}
	for _, id := range []string{"", "..", "a/b", "%2f", "a?b", "a\\b"} {
		if _, err := Segment(id); err == nil {
			t.Fatalf("accepted path %q", id)
		}
	}
}

func TestDoPropagatesRealTraceContext(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("test").Start(context.Background(), "client-operation")
	defer span.End()
	parent := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parent <- req.Header.Get("traceparent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = client.Do(ctx, "GET", "/fixed", nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	actualParent := <-parent
	carrier := propagation.MapCarrier{"traceparent": actualParent}
	extracted := propagation.TraceContext{}.Extract(context.Background(), carrier)
	got := trace.SpanContextFromContext(extracted)
	if !got.IsValid() || got.TraceID() != span.SpanContext().TraceID() || got.SpanID() != span.SpanContext().SpanID() {
		t.Fatalf("outbound traceparent=%q did not carry live span", actualParent)
	}
}
