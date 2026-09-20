package datacenter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
)

type gatedRepresentation struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *gatedRepresentation) Represent(ctx context.Context, _ representation.Request,
	_ representation.Contract, _ string) (representation.Response, error) {
	s.calls.Add(1)
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return representation.Response{}, nil
	case <-ctx.Done():
		return representation.Response{}, ctx.Err()
	}
}

func TestRepresentationGateSharesCapacityAndCancelsQueuedCall(t *testing.T) {
	upstream := &gatedRepresentation{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	gate, err := NewRepresentationGate(upstream, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := gate.Represent(context.Background(), representation.Request{}, representation.Contract{}, "model")
		first <- err
	}()
	select {
	case <-upstream.entered:
	case <-time.After(time.Second):
		t.Fatal("first call did not enter DataCenter")
	}
	queued, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := gate.Represent(queued, representation.Request{}, representation.Contract{}, "model")
		second <- err
	}()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) || upstream.calls.Load() != 1 {
		t.Fatalf("cancelled queued call reached upstream: calls=%d err=%v", upstream.calls.Load(), err)
	}
	third := make(chan error, 1)
	go func() {
		_, err := gate.Represent(context.Background(), representation.Request{}, representation.Contract{}, "model")
		third <- err
	}()
	select {
	case <-upstream.entered:
		t.Fatal("second in-flight model call bypassed capacity")
	case <-time.After(20 * time.Millisecond):
	}
	upstream.release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstream.entered:
	case <-time.After(time.Second):
		t.Fatal("queued call did not enter after release")
	}
	upstream.release <- struct{}{}
	if err := <-third; err != nil || upstream.calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", upstream.calls.Load(), err)
	}
}
