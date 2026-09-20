package worker

import (
	"context"

	asynclogic "sea/service/async/rpc/internal/logic"
)

func Start(ctx context.Context) (func() error, error) {
	return asynclogic.StartWorkers(ctx)
}
