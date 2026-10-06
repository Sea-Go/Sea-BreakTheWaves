// Code scaffolded by goctl. Safe to edit.
package stream

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/logic/stream"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/types"
	"net/http"
)

func AnswerStreamHandler(s *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, func(ctx context.Context, req *types.QueryRequest, events chan<- *types.StreamEvent) error {
			return stream.NewAnswerStreamLogic(ctx, s).AnswerStream(req, events)
		})
	}
}
