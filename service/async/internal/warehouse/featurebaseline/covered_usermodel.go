package featurebaseline

import (
	"context"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/sourcecoverage"
)

// UsermodelCoveredReader is the thin adapter for WS08's independently
// verified PG history fold. It does not infer W from Current.Active, repeat
// VerifyPrefix, or modify the source's accepted coverage state.
type UsermodelCoveredReader struct{ Store *usermodel.Store }

func (r UsermodelCoveredReader) CoveredStateAt(ctx context.Context, subject usermodel.SubjectRef,
	prefix sourcecoverage.GlobalPrefixRef, cutoff time.Time) (CoveredState, error) {
	if r.Store == nil {
		return CoveredState{}, ErrCoveredContract
	}
	state, err := r.Store.CoveredStateAt(ctx, subject, prefix, cutoff)
	if err != nil {
		return CoveredState{}, err
	}
	tail := make([]CoveredTailEvent, len(state.Tail))
	for i, event := range state.Tail {
		tail[i] = CoveredTailEvent{EventKey: event.EventKey, Offset: event.Offset, Status: event.Status}
	}
	return CoveredState{Coverage: state.Coverage, StateVersion: state.StateVersion,
		ActiveAtPrefix: state.ActiveAtPrefix, Tail: tail, CurrentComplete: state.CurrentComplete}, nil
}
