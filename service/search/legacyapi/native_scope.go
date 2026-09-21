package main

import (
	"context"
	"net/http"

	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
	searchhttp "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/transport/http/search"
)

// RTW signature validation happens first. The physical backend then checks
// the very same live publication and all three lane projections before a
// Runner, BGE query or citation write can begin.
type nativeSummaryScope struct {
	inner   searchhttp.ScopeResolver
	backend *nativeBackend
	policy  searchdomain.Policy
}

func (r nativeSummaryScope) ResolveSearch(ctx context.Context, request *http.Request,
	body searchhttp.PublicRequest) (searchhttp.TrustedScope, error) {
	scope, err := r.inner.ResolveSearch(ctx, request, body)
	if err != nil {
		return scope, err
	}
	if _, supported := r.policy.Profiles[body.Depth][body.Intelligence]; !supported {
		return scope, nil // existing Summary preflight returns its profile 503.
	}
	if err := r.backend.Prepare(ctx, scope.Snapshot); err != nil {
		if ctx.Err() != nil {
			return searchhttp.TrustedScope{}, ctx.Err()
		}
		return searchhttp.TrustedScope{}, searchhttp.ErrScopeUnavailable
	}
	return scope, nil
}

type nativeToolsScope struct {
	inner         searchhttp.ToolsScopeResolver
	backend       *nativeBackend
	mediumEnabled bool
}

func (r nativeToolsScope) ResolveTools(ctx context.Context, request *http.Request,
	body searchhttp.ToolsRequest) (searchhttp.TrustedToolsScope, error) {
	scope, err := r.inner.ResolveTools(ctx, request, body)
	if err != nil {
		return scope, err
	}
	if body.Depth != searchdomain.Fast || body.Intelligence != searchdomain.Low &&
		(body.Intelligence != searchdomain.Medium || !r.mediumEnabled) {
		return scope, nil // existing Tools handler returns its profile 503.
	}
	if err := r.backend.Prepare(ctx, scope.Snapshot); err != nil {
		if ctx.Err() != nil {
			return searchhttp.TrustedToolsScope{}, ctx.Err()
		}
		return searchhttp.TrustedToolsScope{}, searchhttp.ErrScopeUnavailable
	}
	return scope, nil
}
