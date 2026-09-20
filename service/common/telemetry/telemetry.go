// Package telemetry owns process-level logging, tracing and bounded metrics for
// the new BTW application module. Domain packages receive a Bundle from assembly.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	framemetrics "trpc.group/trpc-go/trpc-agent-go/telemetry/semconv/metrics"
	frameconv "trpc.group/trpc-go/trpc-agent-go/telemetry/semconv/trace"
)

var ErrConfig = errors.New("invalid telemetry configuration")
var eventName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,5}$`)

type Config struct {
	Service       string
	Environment   string
	Version       string
	InstanceID    string
	Output        io.Writer
	Level         slog.Level
	OTLPEndpoint  string
	TraceExporter sdktrace.SpanExporter // explicit exporter for isolated acceptance
	SampleRatio   float64
	Registry      *prometheus.Registry
}

type Bundle struct {
	base       *slog.Logger
	provider   *sdktrace.TracerProvider
	meter      *sdkmetric.MeterProvider
	tracer     trace.Tracer
	registry   *prometheus.Registry
	operations *prometheus.CounterVec
	durations  *prometheus.HistogramVec
	inflight   *prometheus.GaugeVec
	dropped    prometheus.Counter
	mu         sync.Mutex
	active     int
	closed     bool
	done       chan struct{}
	stop       sync.Once
	stopErr    error
}

func New(ctx context.Context, cfg Config) (*Bundle, error) {
	if cfg.Service == "" || cfg.Environment == "" || cfg.Version == "" || cfg.InstanceID == "" || cfg.Output == nil ||
		(cfg.TraceExporter == nil && cfg.OTLPEndpoint == "") || (cfg.TraceExporter != nil && cfg.OTLPEndpoint != "") {
		return nil, fmt.Errorf("%w: process identity, output and one trace exporter required", ErrConfig)
	}
	if cfg.Level < slog.LevelDebug || cfg.Level > slog.LevelError || cfg.SampleRatio <= 0 || cfg.SampleRatio > 1 {
		return nil, fmt.Errorf("%w: unsupported level", ErrConfig)
	}
	exporter := cfg.TraceExporter
	if exporter == nil {
		var err error
		exporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, fmt.Errorf("create trace exporter: %w", err)
		}
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", cfg.Service),
		attribute.String("service.version", cfg.Version), attribute.String("deployment.environment", cfg.Environment)))
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	registry := cfg.Registry
	if registry == nil {
		registry = prometheus.NewPedanticRegistry()
	}
	metricExporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("register framework metric exporter: %w", err)
	}
	// The framework emits per-user/session attributes. Keep only configured
	// operation/model/agent/tool dimensions in Prometheus series.
	// The framework emits request identities as dropped metric attributes. SDK
	// exemplars would reattach them and can exceed Prometheus' label limit;
	// trace/log correlation remains available through the shared provider.
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(metricExporter),
		sdkmetric.WithView(frameworkMetricView), sdkmetric.WithExemplarFilter(exemplar.AlwaysOffFilter))
	b := &Bundle{registry: registry, done: make(chan struct{}),
		meter:      meter,
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sea_btw_operations_total", Help: "Completed BTW operations by bounded component and outcome."}, []string{"component", "outcome"}),
		durations:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sea_btw_operation_duration_seconds", Help: "Completed BTW operation latency in seconds.", Buckets: prometheus.DefBuckets}, []string{"component", "outcome"}),
		inflight:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "sea_btw_operations_inflight", Help: "Current BTW operations by bounded component."}, []string{"component"}),
		dropped:    prometheus.NewCounter(prometheus.CounterOpts{Name: "sea_btw_log_write_failures_total", Help: "Structured log writes rejected by the output sink."}),
	}
	for _, collector := range []prometheus.Collector{b.operations, b.durations, b.inflight, b.dropped} {
		if err := registry.Register(collector); err != nil {
			return nil, fmt.Errorf("register telemetry metric: %w", err)
		}
	}
	handler := slog.NewJSONHandler(cfg.Output, &slog.HandlerOptions{Level: cfg.Level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case slog.TimeKey:
			return slog.Time("timestamp", a.Value.Time().UTC())
		case slog.LevelKey:
			return slog.String("level", strings.ToLower(a.Value.String()))
		case slog.MessageKey:
			return slog.String("message", a.Value.String())
		}
		return a
	}})
	b.base = slog.New(&contextHandler{next: handler, dropped: b.dropped}).With(
		"service", cfg.Service, "environment", cfg.Environment, "service_version", cfg.Version, "instance_id", cfg.InstanceID)
	b.provider = sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))))
	b.tracer = b.provider.Tracer("github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry")
	return b, nil
}

// The locked framework uses three meters with some identical instrument names.
// Scope-specific stream names prevent collisions while preserving its native
// measurements. Values derived from users, sessions, model responses, agent
// names and tool names are excluded; only framework-owned enums and bools remain.
func frameworkMetricView(instrument sdkmetric.Instrument) (sdkmetric.Stream, bool) {
	var scope string
	switch instrument.Scope.Name {
	case framemetrics.MeterNameChat:
		scope = "chat"
	case framemetrics.MeterNameExecuteTool:
		scope = "tool"
	case framemetrics.MeterNameInvokeAgent:
		scope = "agent"
	case framemetrics.MeterNameWorkflow:
		scope = "workflow"
	default:
		return sdkmetric.Stream{}, false
	}
	return sdkmetric.Stream{
		Name:        "trpc_agent_go_" + scope + "_" + strings.ReplaceAll(instrument.Name, ".", "_"),
		Description: instrument.Description,
		Unit:        instrument.Unit,
		AttributeFilter: attribute.NewAllowKeysFilter(
			attribute.Key(frameconv.KeyGenAIOperationName),
			attribute.Key(framemetrics.KeyGenAITokenType), attribute.Key(framemetrics.KeyTRPCAgentGoStream),
		),
	}, true
}

// Logger is the single structured entry point for an approved bounded
// component. The source says whether a record originates in application code
// or an adapted framework; IDs are per-record attributes, never metric labels.
func (b *Bundle) Logger(component, source string) (*slog.Logger, error) {
	if _, ok := componentNames[component]; !ok || !validSource(source) {
		return nil, fmt.Errorf("%w: unknown component or log source", ErrConfig)
	}
	return b.base.With("component", component, "log_source", source), nil
}

var componentNames = map[string]struct{}{
	"runtime": {}, "content": {}, "dense": {}, "sparse": {}, "multivector": {},
	"search": {}, "recommend": {}, "usermodel": {}, "training": {}, "warehouse": {},
	"datacenter_client": {}, "ridethewind_client": {}, "serving": {},
}

func validSource(source string) bool {
	switch source {
	case "application", "framework", "access", "runtime", "collector":
		return true
	default:
		return false
	}
}

type contextHandler struct {
	next    slog.Handler
	dropped prometheus.Counter
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	copy := r.Clone()
	span := trace.SpanContextFromContext(ctx)
	if span.IsValid() {
		copy.AddAttrs(slog.String("trace_id", span.TraceID().String()), slog.String("span_id", span.SpanID().String()), slog.Bool("trace_sampled", span.IsSampled()))
	}
	if err := h.next.Handle(ctx, copy); err != nil {
		h.dropped.Inc() // never recurse through a failing log sink
		return err
	}
	return nil
}
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{next: h.next.WithAttrs(attrs), dropped: h.dropped}
}
func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{next: h.next.WithGroup(name), dropped: h.dropped}
}

func (b *Bundle) Tracer() trace.Tracer { return b.tracer }
func (b *Bundle) Closed() bool         { b.mu.Lock(); defer b.mu.Unlock(); return b.closed }
func (b *Bundle) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(b.registry, promhttp.HandlerOpts{})
}
func (b *Bundle) Registry() *prometheus.Registry { return b.registry }

type Stage struct {
	owner     *Bundle
	logger    *slog.Logger
	span      trace.Span
	event     string
	component string
	start     time.Time
	attrs     []slog.Attr
	finished  atomic.Bool
}

// Begin records one application stage and returns its tracing context. End must
// run exactly once after the domain commit or failure. Stage attributes are
// bounded scalar context, not model bodies or whole shard arrays.
func (b *Bundle) Begin(ctx context.Context, component, event string, attrs ...slog.Attr) (context.Context, *Stage, error) {
	if !eventName.MatchString(event) {
		return ctx, nil, ErrConfig
	}
	logger, err := b.Logger(component, "application")
	if err != nil {
		return ctx, nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ctx, nil, ErrConfig
	}
	b.active++
	b.mu.Unlock()
	ctx, span := b.tracer.Start(ctx, event, trace.WithAttributes(spanAttrs(attrs)...))
	b.inflight.WithLabelValues(component).Inc()
	start := time.Now()
	fields := append([]slog.Attr{slog.String("event", event+".started")}, attrs...)
	logger.LogAttrs(ctx, slog.LevelInfo, "operation started", fields...)
	return ctx, &Stage{owner: b, logger: logger, span: span, event: event, component: component, start: start,
		attrs: append([]slog.Attr(nil), attrs...)}, nil
}

var spanField = map[string]bool{
	"request_id": true, "operation_id": true, "run_id": true, "session_id": true,
	"job_id": true, "build_id": true, "module_id": true, "release_id": true,
	"revision_id": true, "generation": true, "attempt_id": true, "search_id": true,
	"pair_id": true, "model_call_id": true, "logical_call_id": true, "candidate_id": true, "task": true,
	"publication_revision": true,
	"lease_epoch":          true, "cancel_version": true, "configuration_id": true, "representation_contract_id": true,
	"representation_space": true, "lane": true,
}

func spanAttrs(attrs []slog.Attr) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if !spanField[a.Key] {
			continue
		}
		switch a.Value.Kind() {
		case slog.KindString:
			result = append(result, attribute.String(a.Key, a.Value.String()))
		case slog.KindInt64:
			result = append(result, attribute.Int64(a.Key, a.Value.Int64()))
		case slog.KindBool:
			result = append(result, attribute.Bool(a.Key, a.Value.Bool()))
		}
	}
	return result
}

// SetAttributes binds identifiers learned after a stage starts to the same
// application span. Only the fixed spanField allowlist is accepted, so a
// caller cannot accidentally attach query text, model bodies or credentials.
func (s *Stage) SetAttributes(attrs ...slog.Attr) {
	if s == nil || s.finished.Load() {
		return
	}
	s.span.SetAttributes(spanAttrs(attrs)...)
}

func (s *Stage) End(ctx context.Context, outcome, errorCode string, cause error, attrs ...slog.Attr) {
	if !s.finished.CompareAndSwap(false, true) {
		return
	}
	if cause != nil && outcome == "succeeded" {
		outcome = "failed"
	}
	if !validOutcome(outcome) {
		outcome = "failed"
		if cause == nil {
			cause = errors.New("invalid stage outcome")
		}
		if errorCode == "" {
			errorCode = "INVALID_OUTCOME"
		}
	}
	duration := time.Since(s.start)
	fields := append([]slog.Attr{slog.String("event", s.event+".finished"), slog.String("outcome", outcome),
		slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond))}, s.attrs...)
	if cause != nil {
		if errorCode == "" {
			errorCode = "UNCLASSIFIED"
		}
		fields = append(fields, slog.String("error_code", errorCode), slog.String("error_type", fmt.Sprintf("%T", cause)), slog.String("error_message", cause.Error()))
		s.span.RecordError(cause)
		s.span.SetStatus(codes.Error, errorCode)
	}
	fields = append(fields, attrs...)
	s.span.SetAttributes(attribute.String("outcome", outcome), attribute.Float64("duration_ms", float64(duration)/float64(time.Millisecond)))
	level := slog.LevelInfo
	if cause != nil && outcome != "rejected" && outcome != "cancelled" {
		level = slog.LevelError
	} else if cause != nil || outcome == "rejected" {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(ctx, level, "operation finished", fields...)
	s.owner.operations.WithLabelValues(s.component, outcome).Inc()
	s.owner.durations.WithLabelValues(s.component, outcome).Observe(duration.Seconds())
	s.owner.inflight.WithLabelValues(s.component).Dec()
	s.span.End()
	s.owner.mu.Lock()
	s.owner.active--
	if s.owner.closed && s.owner.active == 0 {
		close(s.owner.done)
	}
	s.owner.mu.Unlock()
}

func validOutcome(value string) bool {
	switch value {
	case "succeeded", "failed", "cancelled", "rejected", "timed_out", "partial":
		return true
	default:
		return false
	}
}

func (b *Bundle) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		if b.active == 0 {
			close(b.done)
		}
	}
	done := b.done
	b.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.stop.Do(func() { b.stopErr = errors.Join(b.provider.Shutdown(ctx), b.meter.Shutdown(ctx)) })
	return b.stopErr
}
