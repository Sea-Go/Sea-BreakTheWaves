package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	a2alog "trpc.group/trpc-go/trpc-a2a-go/log"
	agentlog "trpc.group/trpc-go/trpc-agent-go/log"
	agentmetric "trpc.group/trpc-go/trpc-agent-go/telemetry/metric"
	agenttrace "trpc.group/trpc-go/trpc-agent-go/telemetry/trace"
)

var installMu sync.Mutex
var installed *Bundle

func (b *Bundle) Installed() bool {
	installMu.Lock()
	defer installMu.Unlock()
	return installed == b
}

// InstallGlobals is called once by the process assembly before any Runner,
// Agent or worker goroutine starts. It bridges the locked framework's public
// Logger and Context functions instead of copying its internal event loop.
func (b *Bundle) InstallGlobals() error {
	installMu.Lock()
	defer installMu.Unlock()
	if b.Closed() {
		return fmt.Errorf("%w: cannot install closed telemetry bundle", ErrConfig)
	}
	if installed != nil {
		if installed == b {
			return nil
		}
		return fmt.Errorf("%w: another telemetry bundle already installed", ErrConfig)
	}
	if err := agentmetric.InitMeterProvider(b.meter); err != nil {
		return fmt.Errorf("initialize framework metrics: %w", err)
	}
	logger, err := b.Logger("runtime", "framework")
	if err != nil {
		return err
	}
	collector, err := b.Logger("runtime", "collector")
	if err != nil {
		return err
	}
	f := &frameworkLogger{logger: logger}
	// The version-locked Agent package initializes a2a.Default separately.
	// Both must use the same sink to avoid a second console-format stream.
	agentlog.Default = f
	agentlog.ContextDefault = f
	a2alog.Default = f
	agentlog.DebugContext = func(ctx context.Context, args ...any) { f.context(ctx, slog.LevelDebug, fmt.Sprint(args...)) }
	agentlog.DebugfContext = func(ctx context.Context, format string, args ...any) {
		f.context(ctx, slog.LevelDebug, fmt.Sprintf(format, args...))
	}
	agentlog.InfoContext = func(ctx context.Context, args ...any) { f.context(ctx, slog.LevelInfo, fmt.Sprint(args...)) }
	agentlog.InfofContext = func(ctx context.Context, format string, args ...any) {
		f.context(ctx, slog.LevelInfo, fmt.Sprintf(format, args...))
	}
	agentlog.WarnContext = func(ctx context.Context, args ...any) { f.context(ctx, slog.LevelWarn, fmt.Sprint(args...)) }
	agentlog.WarnfContext = func(ctx context.Context, format string, args ...any) {
		f.context(ctx, slog.LevelWarn, fmt.Sprintf(format, args...))
	}
	agentlog.ErrorContext = func(ctx context.Context, args ...any) { f.context(ctx, slog.LevelError, fmt.Sprint(args...)) }
	agentlog.ErrorfContext = func(ctx context.Context, format string, args ...any) {
		f.context(ctx, slog.LevelError, fmt.Sprintf(format, args...))
	}
	agentlog.FatalContext = func(ctx context.Context, args ...any) { f.fatal(ctx, fmt.Sprint(args...)) }
	agentlog.FatalfContext = func(ctx context.Context, format string, args ...any) { f.fatal(ctx, fmt.Sprintf(format, args...)) }
	agentlog.TracefContext = func(ctx context.Context, format string, args ...any) {
		if agentlog.IsTraceEnabled() {
			f.context(ctx, slog.LevelDebug, "[TRACE] "+fmt.Sprintf(format, args...))
		}
	}
	otel.SetTracerProvider(b.provider)
	// v1.8.1 Agent, Tool and Graph spans use this exported framework tracer,
	// which starts as noop and is not replaced by otel.SetTracerProvider alone.
	// Reuse the process provider instead of starting a second OTLP exporter.
	agenttrace.TracerProvider = b.provider
	agenttrace.Tracer = otel.Tracer("trpc.agent.go")
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetErrorHandler(exportErrorHandler{logger: collector})
	installed = b
	return nil
}

type exportErrorHandler struct{ logger *slog.Logger }

func (h exportErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	h.logger.LogAttrs(context.Background(), slog.LevelError, "trace export failed",
		slog.String("event", "telemetry.export.failed"), slog.String("outcome", "failed"),
		slog.String("error_code", "TRACE_EXPORT_FAILED"), slog.String("error_type", fmt.Sprintf("%T", err)),
		slog.String("error_message", err.Error()))
}

type frameworkLogger struct{ logger *slog.Logger }

func (f *frameworkLogger) context(ctx context.Context, level slog.Level, message string) {
	if !f.logger.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{slog.String("event", "framework.log")}
	if level >= slog.LevelError {
		attrs = append(attrs, slog.String("outcome", "failed"), slog.String("error_code", "FRAMEWORK_ERROR"),
			slog.String("error_type", "framework.Logger"), slog.String("error_message", message))
	}
	f.logger.LogAttrs(ctx, level, message, attrs...)
}
func (f *frameworkLogger) Debug(args ...any) {
	f.context(context.Background(), slog.LevelDebug, fmt.Sprint(args...))
}
func (f *frameworkLogger) Debugf(format string, args ...any) {
	f.context(context.Background(), slog.LevelDebug, fmt.Sprintf(format, args...))
}
func (f *frameworkLogger) Info(args ...any) {
	f.context(context.Background(), slog.LevelInfo, fmt.Sprint(args...))
}
func (f *frameworkLogger) Infof(format string, args ...any) {
	f.context(context.Background(), slog.LevelInfo, fmt.Sprintf(format, args...))
}
func (f *frameworkLogger) Warn(args ...any) {
	f.context(context.Background(), slog.LevelWarn, fmt.Sprint(args...))
}
func (f *frameworkLogger) Warnf(format string, args ...any) {
	f.context(context.Background(), slog.LevelWarn, fmt.Sprintf(format, args...))
}
func (f *frameworkLogger) Error(args ...any) {
	f.context(context.Background(), slog.LevelError, fmt.Sprint(args...))
}
func (f *frameworkLogger) Errorf(format string, args ...any) {
	f.context(context.Background(), slog.LevelError, fmt.Sprintf(format, args...))
}
func (f *frameworkLogger) Fatal(args ...any) { f.fatal(context.Background(), fmt.Sprint(args...)) }
func (f *frameworkLogger) Fatalf(format string, args ...any) {
	f.fatal(context.Background(), fmt.Sprintf(format, args...))
}

type frameworkFatal struct{ message string }

func (e frameworkFatal) Error() string { return e.message }

func (f *frameworkLogger) fatal(ctx context.Context, message string) {
	f.logger.LogAttrs(ctx, slog.LevelError, message, slog.String("event", "framework.fatal"),
		slog.String("outcome", "failed"), slog.String("error_code", "FRAMEWORK_FATAL"),
		slog.String("error_type", "framework.Fatal"), slog.String("error_message", message),
		slog.Bool("process_exit_requested", true))
	// An application entry point owns shutdown and exit. A typed panic keeps
	// framework Fatal from silently continuing or calling os.Exit in a Tool.
	panic(frameworkFatal{message: message})
}
