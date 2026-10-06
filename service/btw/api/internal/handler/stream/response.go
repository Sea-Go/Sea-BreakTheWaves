package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/zeromicro/go-zero/rest/httpx"
	"net/http"
)

// Both public streams share transport and cancellation; the logic owns its result.
func respond(w http.ResponseWriter, r *http.Request, run func(context.Context, *types.QueryRequest, chan<- *types.StreamEvent) error) {
	var req types.QueryRequest
	if err := httpx.Parse(r, &req); err != nil {
		httpx.ErrorCtx(r.Context(), w, fmt.Errorf("%w: %v", item.ErrInvalid, err))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	events := make(chan *types.StreamEvent, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		if err := run(ctx, &req, events); err != nil {
			svc.RecordFailure(ctx, err)
		}
	}()
	defer func() { cancel(); <-done }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	controller := http.NewResponseController(w)
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				svc.RecordFailure(ctx, err)
				return
			}
			if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
				svc.RecordFailure(ctx, err)
				return
			}
			if err = controller.Flush(); err != nil {
				svc.RecordFailure(ctx, err)
				return
			}
		}
	}
}
