package content

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type testTraceSink struct{}

func (testTraceSink) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (testTraceSink) Shutdown(context.Context) error                             { return nil }

var testObservationOnce sync.Once
var testObservation *telemetry.Bundle
var testObservationError error
var testObservationOutput bytes.Buffer

func contentObservationForTest(t *testing.T) *telemetry.Bundle {
	t.Helper()
	testObservationOnce.Do(func() {
		testObservation, testObservationError = telemetry.New(context.Background(), telemetry.Config{Service: "btw-content-test", Environment: "test",
			Version: "test-revision", InstanceID: "test-worker", Output: &testObservationOutput, Level: slog.LevelInfo, TraceExporter: testTraceSink{}, SampleRatio: 1})
		if testObservationError == nil {
			testObservationError = testObservation.InstallGlobals()
		}
	})
	if testObservationError != nil {
		t.Fatal(testObservationError)
	}
	return testObservation
}

func TestMain(m *testing.M) {
	code := m.Run()
	if testObservation != nil {
		if err := testObservation.Close(context.Background()); err != nil {
			code = 1
		}
	}
	os.Exit(code)
}
