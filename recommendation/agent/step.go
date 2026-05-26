package agent

import (
	"context"

	"sea/zlog"
)

func runStep[T any](ctx context.Context, name string, fn func(context.Context) (T, error)) (T, error) {
	ctx, sp := zlog.StartSpan(ctx, name)
	val, err := fn(ctx)
	if err != nil {
		sp.End(zlog.StatusError, err)
		var zero T
		return zero, err
	}
	sp.End(zlog.StatusOK, nil)
	return val, nil
}

func failRecommend(expl *explainBuilder, stepName string, err error, respOut *RecommendResponse, explain bool) (RecommendResponse, error) {
	expl.Add(stepName+".error", map[string]any{"error": err.Error()})
	respOut.Status = "error"
	respOut.Explanation = expl.Text()
	if explain {
		respOut.ExplainTrace = expl.Trace()
	}
	return *respOut, err
}
