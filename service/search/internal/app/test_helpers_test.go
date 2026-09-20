package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
)

type workerTestExporter struct{}

func (workerTestExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (workerTestExporter) Shutdown(context.Context) error                             { return nil }

var (
	workerObservationOnce sync.Once
	workerObservation     *telemetry.Bundle
	workerObservationErr  error
	workerOutput          bytes.Buffer
)

func testWorkerObservation(t *testing.T) *telemetry.Bundle {
	t.Helper()
	workerObservationOnce.Do(func() {
		workerObservation, workerObservationErr = telemetry.New(context.Background(), telemetry.Config{
			Service: "btw-search-app-test", Environment: "test", Version: "fixture-revision", InstanceID: "test-search",
			Output: &workerOutput, Level: slog.LevelInfo, TraceExporter: workerTestExporter{}, SampleRatio: 1,
		})
		if workerObservationErr == nil {
			workerObservationErr = workerObservation.InstallGlobals()
		}
	})
	if workerObservationErr != nil {
		t.Fatal(workerObservationErr)
	}
	return workerObservation
}

func TestMain(m *testing.M) {
	code := m.Run()
	if workerObservation != nil {
		if err := workerObservation.Close(context.Background()); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}
