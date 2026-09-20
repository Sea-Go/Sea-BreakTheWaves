package datacenter

import (
	"context"
	"errors"
	"reflect"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/representation"
)

// Representer is the typed DataCenter representation boundary used by all
// three retrieval lanes. A shared gate limits this process's concurrent model
// calls without changing their identity, payload, errors or usage receipts.
type Representer interface {
	Represent(context.Context, representation.Request, representation.Contract, string) (representation.Response, error)
}

type RepresentationGate struct {
	upstream Representer
	slots    chan struct{}
}

func NewRepresentationGate(upstream Representer, maxInFlight int) (*RepresentationGate, error) {
	if upstream == nil || reflect.ValueOf(upstream).Kind() == reflect.Pointer && reflect.ValueOf(upstream).IsNil() ||
		maxInFlight < 1 || maxInFlight > 32 {
		return nil, errors.New("DataCenter representation gate requires upstream and capacity 1..32")
	}
	return &RepresentationGate{upstream: upstream, slots: make(chan struct{}, maxInFlight)}, nil
}

func (g *RepresentationGate) Represent(ctx context.Context, request representation.Request,
	contract representation.Contract, physicalModel string) (representation.Response, error) {
	if g == nil || ctx == nil {
		return representation.Response{}, errors.New("DataCenter representation gate requires context")
	}
	if err := ctx.Err(); err != nil {
		return representation.Response{}, err
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return representation.Response{}, ctx.Err()
	}
	defer func() { <-g.slots }()
	if err := ctx.Err(); err != nil {
		return representation.Response{}, err
	}
	return g.upstream.Represent(ctx, request, contract, physicalModel)
}

var _ Representer = (*RepresentationGate)(nil)
