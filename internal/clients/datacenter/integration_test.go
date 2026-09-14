package datacenter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/jobs"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime/httpclient"
)

func TestRealPlatformHTTP(t *testing.T) {
	endpoint := os.Getenv("SEA_TEST_DC_URL")
	if endpoint == "" {
		t.Skip("run internal/runtime/acceptance.sh for isolated DC HTTP")
	}
	c, e := New(httpclient.Config{BaseURL: endpoint, Token: "runtime-fixture"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unique := time.Now().UTC().Format("20060102T150405.000000000")
	producer := "runtime-" + unique
	event := eventing.Event{EventID: "e1", EventType: "knowledge.runtime.fixture.v1", SchemaVersion: 1, Producer: producer, AggregateID: "module", AggregateVersion: 1, OperationID: "source", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Payload: json.RawMessage(`{"value":"9007199254740993"}`)}
	receipt, e := c.PublishEvent(ctx, event)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := c.PublishEvent(ctx, event)
	if e != nil || receipt != replay {
		t.Fatalf("unstable receipt: %+v %+v %v", receipt, replay, e)
	}
	stored, e := c.EventReceipt(ctx, producer, "e1")
	if e != nil || stored != receipt {
		t.Fatalf("receipt read: %v", e)
	}
	event.Payload = json.RawMessage(`{"value":"9007199254740992"}`)
	_, e = c.PublishEvent(ctx, event)
	var conflict *httpclient.HTTPError
	if !errors.As(e, &conflict) || conflict.StatusCode != 409 {
		t.Fatalf("lost fixed input conflict: %v", e)
	}
	batch, e := c.ReadEvents(ctx, "consumer-"+unique, producer, 10)
	if e != nil || len(batch.Events) != 1 {
		t.Fatalf("batch %+v %v", batch, e)
	}
	if _, e = c.AcknowledgeEvents(ctx, batch.Consumer, eventing.Acknowledge{Producer: producer, FromOffset: batch.FromOffset, ToOffset: batch.ToOffset, BatchHash: batch.BatchHash}); e != nil {
		t.Fatal(e)
	}
	mark, e := c.SourceWatermark(ctx, producer, "module")
	if e != nil || mark.ContiguousVersion != 1 || mark.HasGap {
		t.Fatalf("watermark %+v %v", mark, e)
	}
	submit := jobs.Submit{Producer: producer, OperationID: "job", RunRef: "run", JobType: producer, ResourceProfile: "cpu", Input: json.RawMessage(`{"release":"fixed"}`), Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), MaxAttempts: 2}
	created, e := c.SubmitJob(ctx, submit)
	if e != nil {
		t.Fatal(e)
	}
	claim, e := c.ClaimJob(ctx, jobs.Claim{WorkerID: "worker", JobType: producer, ResourceProfile: "cpu", LeaseSeconds: 20})
	if e != nil || claim.ID != created.ID || claim.InputHash != created.InputHash {
		t.Fatalf("claim %+v %v", claim, e)
	}
	lease := jobs.Lease{WorkerID: claim.WorkerID, AttemptID: claim.AttemptID, LeaseEpoch: claim.LeaseEpoch, CancelVersion: claim.CancelVersion}
	if _, e = c.RenewJob(ctx, claim.ID, jobs.Renew{Lease: lease, LeaseSeconds: 30}); e != nil {
		t.Fatal(e)
	}
	result := jobs.Complete{Lease: lease, Result: jobs.Result{State: "succeeded", Ref: &jobs.ResultRef{URI: "fixture://result", Hash: strings.Repeat("a", 64), MediaType: "application/json"}}}
	completed, e := c.CompleteJob(ctx, claim.ID, result)
	if e != nil {
		t.Fatal(e)
	}
	again, e := c.CompleteJob(ctx, claim.ID, result)
	if e != nil || !reflect.DeepEqual(again, completed) {
		t.Fatalf("completion replay %v", e)
	}
	if reread, e := c.GetJob(ctx, claim.ID); e != nil || reread.State != "succeeded" {
		t.Fatalf("job reread %+v %v", reread, e)
	}
	if _, e = c.ClaimJob(ctx, jobs.Claim{WorkerID: "worker", JobType: producer, ResourceProfile: "cpu", LeaseSeconds: 20}); !errors.Is(e, ErrNoWork) {
		t.Fatalf("empty claim %v", e)
	}
	submit.OperationID = "cancel-job"
	second, e := c.SubmitJob(ctx, submit)
	if e != nil {
		t.Fatal(e)
	}
	claim, e = c.ClaimJob(ctx, jobs.Claim{WorkerID: "worker", JobType: producer, ResourceProfile: "cpu", LeaseSeconds: 20})
	if e != nil {
		t.Fatal(e)
	}
	cancelled, e := c.CancelJob(ctx, second.ID, jobs.Cancel{OperationID: "stop", ExpectedCancelVersion: claim.CancelVersion, Reason: "fixture cancellation"})
	if e != nil {
		t.Fatal(e)
	}
	lease = jobs.Lease{WorkerID: claim.WorkerID, AttemptID: claim.AttemptID, LeaseEpoch: claim.LeaseEpoch, CancelVersion: cancelled.CancelVersion}
	ack, e := c.AcknowledgeCancellation(ctx, second.ID, lease)
	if e != nil || ack.State != "cancelled" {
		t.Fatalf("cancel ack %+v %v", ack, e)
	}
	t.Logf("real DC HTTP event offset=%d, completed job=%s, cancelled job=%s", receipt.Offset, created.ID, second.ID)
}
