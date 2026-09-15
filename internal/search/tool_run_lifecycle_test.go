package search

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The parent/child boundary isolates tRPC-Agent-Go's process-wide telemetry.
// The acceptor simulates a durable RTW receipt whose response is slow even
// after the caller cancels: closing the host must await that in-flight run.
func TestToolRunCloseWaitsForInFlightCitationCommit(t *testing.T) {
	if os.Getenv("SEARCH_TOOL_CLOSE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestToolRunCloseWaitsForInFlightCitationCommit$", "-test.v")
		command.Env = append(os.Environ(), "SEARCH_TOOL_CLOSE_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native Tool Runner lifecycle failed: %v\n%s", err, output)
		}
		return
	}
	var logs bytes.Buffer
	observed, err := telemetry.New(context.Background(), telemetry.Config{Service: "btw-tool-close-test",
		Environment: "test", Version: "local", InstanceID: "fixture", Output: &logs,
		Level: slog.LevelInfo, SampleRatio: 1, TraceExporter: tracetest.NewInMemoryExporter()})
	if err != nil {
		t.Fatal(err)
	}
	if err := observed.InstallGlobals(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observed.Close(context.Background()) }()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	delivery := fixtureDelivery(t, []corpus.Chunk{sourceChunk("a")}, SourceReadFunc(exactSource),
		AcceptFunc(func(_ context.Context, pack EvidencePack) (CitationReceipt, error) {
			close(entered)
			<-release // An accepted RTW receipt can complete after client cancellation.
			return accepted(context.Background(), pack)
		}))
	boundary, err := NewToolRunBoundary(delivery, observed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		_ = boundary.Close()
	}()
	q := ToolRunRequest{Subject: btwruntime.SubjectRef{AuthorityID: "rtw.identity", TenantID: "platform", SubjectID: "123"},
		SessionID: "tool-close-session", OperationID: "tool-close-operation", BudgetRef: "tool-close-budget",
		SearchID: "tool-close-search", Search: searchRequest(Fast, Low), Limits: EvidenceLimits{MaxReads: 1, MaxQuoteRunes: 80}}
	type outcome struct {
		found SearchResult
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		found, runErr := boundary.Search(context.Background(), q)
		result <- outcome{found: found, err: runErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native Runner never reached RTW CitationAcceptor")
	}
	closed := make(chan error, 1)
	go func() { closed <- boundary.Close() }()
	early := false
	var closeErr error
	select {
	case closeErr = <-closed:
		early = true
	case <-time.After(80 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case completed := <-result:
		if completed.err == nil || len(completed.found.Pack.Evidence) != 0 ||
			completed.found.Receipt != (CitationReceipt{}) {
			t.Fatalf("closed Tool call published citation after RTW completion: result=%+v error=%v",
				completed.found, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing native Tool Runner did not finish its in-flight call")
	}
	if !early {
		select {
		case closeErr = <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("native Tool Runner close waited forever after citation commit")
		}
	}
	if closeErr != nil {
		t.Fatalf("native Tool Runner close failed: %v", closeErr)
	}
	if early {
		t.Fatal("Close returned while the RTW citation acceptor was still in-flight")
	}
	if _, err := boundary.Search(context.Background(), q); !errors.Is(err, btwruntime.ErrClosed) {
		t.Fatalf("closed Tool boundary started a new Runner: %v", err)
	}
}
