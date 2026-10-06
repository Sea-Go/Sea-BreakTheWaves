// Package runtime assembles the go-zero API with DataCenter model access.
package runtime

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/handler"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/btw/api/internal/svc"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/datacenter"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type App struct {
	Server *rest.Server
	Host   string
	Port   int
	owner  *svc.ServiceContext
	once   sync.Once
}

// New keeps the injectable generation model for isolated host tests. The
// production entrypoint supplies a DataCenter workload identity instead.
func New(ctx context.Context, filename string, generation model.Model) (*App, error) {
	return newApp(ctx, filename, generation, nil)
}

func NewWithDataCenterIdentity(ctx context.Context, filename string, identity datacenter.TokenSource) (*App, error) {
	return newApp(ctx, filename, nil, identity)
}

func newApp(ctx context.Context, filename string, generation model.Model, identity datacenter.TokenSource) (*App, error) {
	var c config.Config
	if err := conf.Load(filename, &c, conf.UseEnv()); err != nil {
		return nil, err
	}
	owner, err := svc.OpenWithDataCenter(ctx, c, generation, identity)
	if err != nil {
		return nil, err
	}
	server, err := rest.NewServer(c.RestConf)
	if err != nil {
		_ = owner.Close()
		return nil, err
	}
	server.Use(owner.Middleware)
	httpx.SetErrorHandlerCtx(func(_ context.Context, err error) (int, any) {
		status := http.StatusInternalServerError
		code := "internal_error"
		switch {
		case errors.Is(err, item.ErrInvalid):
			status, code = http.StatusBadRequest, "invalid_request"
		case errors.Is(err, item.ErrNotFound):
			status, code = http.StatusNotFound, "not_found"
		case errors.Is(err, item.ErrConflict):
			status, code = http.StatusConflict, "revision_conflict"
		case errors.Is(err, item.ErrUnavailable):
			status, code = http.StatusServiceUnavailable, "unavailable"
		case errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusGatewayTimeout, "deadline_exceeded"
		case errors.Is(err, context.Canceled):
			status, code = 499, "cancelled"
		}
		return status, map[string]any{"status": "failed", "error": code, "message": err.Error()}
	})
	handler.RegisterHandlers(server, owner)
	return &App{Server: server, Host: c.Host, Port: c.Port, owner: owner}, nil
}

func (a *App) Close() error {
	var result error
	a.once.Do(func() {
		result = a.owner.Close()
		a.Server.Stop()
	})
	return result
}
