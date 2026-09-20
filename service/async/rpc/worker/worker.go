package worker

import (
	"context"

	asynclogic "github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/logic"
)

func Start(ctx context.Context) (func() error, error) {
	return asynclogic.StartWorkers(ctx)
}
