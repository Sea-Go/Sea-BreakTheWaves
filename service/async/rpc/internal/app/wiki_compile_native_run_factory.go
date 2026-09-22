package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/content"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

const wikiNativeRunApp = "wiki_compile_native_run"
const wikiNativeTechnicalAuthority = "ridethewind.knowledge"
const wikiNativeTechnicalScope = "wiki-compile-run"
const wikiNativeRunMessage = "compile the fixed RTW Wiki source revisions"

var ErrWikiCompileNativeFactory = errors.New("wiki compile native run factory is unbound, closed or invalid")

// WikiCompileNativeSessionProvider is supplied by the DC native app-user
// session owner. Its technical jobs token and RTW JWT are never model bearer
// substitutes. The factory borrows this provider and does not close it.
type WikiCompileNativeSessionProvider interface {
	CurrentNativeSession(context.Context) (WikiCompileModelSession, error)
}

type WikiCompileNativeSessionFunc func(context.Context) (WikiCompileModelSession, error)

func (fn WikiCompileNativeSessionFunc) CurrentNativeSession(ctx context.Context) (WikiCompileModelSession, error) {
	return fn(ctx)
}

// WikiCompileNativeRunFactoryConfig fixes the DC Gateway root. The callpoint
// is the registered knowledge-wiki-compiler and cannot be changed per job.
type WikiCompileNativeRunFactoryConfig struct {
	DCGatewayRoot string
	HTTPClient    *http.Client
}

// WikiCompileNativeRunFactory first freezes the provider's native AccountID
// and TokenDigest. The cmd owner then installs one telemetry Bundle and calls
// BindTelemetry once. Open only borrows the supplied frozen session's bearer;
// it never refreshes the provider or writes a candidate object.
type WikiCompileNativeRunFactory struct {
	config    WikiCompileNativeRunFactoryConfig
	provider  WikiCompileNativeSessionProvider
	mu        sync.Mutex
	proof     WikiCompileModelSessionProof
	bound     bool
	observed  *telemetry.Bundle
	closed    bool
	active    map[*wikiCompileNativeRun]struct{}
	closeOnce sync.Once
	closeErr  error
}

var _ WikiCompileRunFactory = (*WikiCompileNativeRunFactory)(nil)

func NewWikiCompileNativeRunFactory(cfg WikiCompileNativeRunFactoryConfig,
	provider WikiCompileNativeSessionProvider) (*WikiCompileNativeRunFactory, error) {
	u, err := url.Parse(cfg.DCGatewayRoot)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || nilDependency(provider) {
		return nil, ErrWikiCompileNativeFactory
	}
	return &WikiCompileNativeRunFactory{config: cfg, provider: provider,
		active: make(map[*wikiCompileNativeRun]struct{})}, nil
}

// CurrentNativeSession is the same factory's only provider read. A changed
// account or rotated bearer digest needs a new worker/factory instance.
func (f *WikiCompileNativeRunFactory) CurrentNativeSession(ctx context.Context) (WikiCompileModelSession, error) {
	if f == nil || ctx == nil {
		return WikiCompileModelSession{}, ErrWikiCompileNativeFactory
	}
	if err := ctx.Err(); err != nil {
		return WikiCompileModelSession{}, err
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return WikiCompileModelSession{}, ErrWikiCompileNativeFactory
	}
	current, err := f.provider.CurrentNativeSession(ctx)
	if err != nil || !current.valid() {
		return WikiCompileModelSession{}, ErrWikiCompileModelIdentity
	}
	if err := ctx.Err(); err != nil {
		return WikiCompileModelSession{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return WikiCompileModelSession{}, ErrWikiCompileNativeFactory
	}
	if !f.bound {
		f.proof, f.bound = current.Proof(), true
	} else if f.proof != current.Proof() {
		return WikiCompileModelSession{}, ErrWikiCompileModelIdentity
	}
	return current, nil
}

// BindTelemetry is called once after the authoritative process Bundle has
// installed framework globals. The factory borrows it; Close does not close it.
func (f *WikiCompileNativeRunFactory) BindTelemetry(observed *telemetry.Bundle) error {
	if f == nil || observed == nil || !observed.Installed() || observed.Closed() {
		return ErrWikiCompileNativeFactory
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.bound || f.observed != nil {
		return ErrWikiCompileNativeFactory
	}
	f.observed = observed
	return nil
}

// Open rechecks the exact RTW-selected revision order and original body hashes
// before building a request-specific official model, Graph, Runner and private
// Session. The model's logical key intentionally omits DC attempt/lease fields.
func (f *WikiCompileNativeRunFactory) Open(ctx context.Context, input content.WikiCompileInput,
	session WikiCompileModelSession) (WikiCompileRun, error) {
	if f == nil || ctx == nil {
		return nil, ErrWikiCompileNativeFactory
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !session.valid() {
		return nil, ErrWikiCompileModelIdentity
	}
	f.mu.Lock()
	proof, bound, observed, closed := f.proof, f.bound, f.observed, f.closed
	f.mu.Unlock()
	if closed || !bound || observed == nil || observed.Closed() {
		return nil, ErrWikiCompileNativeFactory
	}
	if session.Proof() != proof {
		return nil, ErrWikiCompileModelIdentity
	}
	option, err := content.WikiCompileRunOption(input)
	if err != nil || len(input.SourceIDs) != len(input.Sources) {
		return nil, ErrWikiCompileSource
	}
	identities := make([]btwruntime.WikiCompileSourceIdentity, len(input.SourceIDs))
	for i, id := range input.SourceIDs {
		if input.Sources[i].RevisionID != id {
			return nil, ErrWikiCompileSource
		}
		identities[i] = btwruntime.WikiCompileSourceIdentity{
			RevisionID: id, ContentHash: input.Sources[i].SHA256}
	}
	sourceSHA, err := btwruntime.WikiCompileSourceRefsSHA(identities)
	if err != nil {
		return nil, ErrWikiCompileSource
	}
	fixed := input
	fixed.SourceIDs = append([]string(nil), input.SourceIDs...)
	fixed.Sources = append([]content.WikiCompileSource(nil), input.Sources...)
	ref := btwruntime.WikiCompileModelInvocationRef{CompileID: fixed.CompileID,
		InputHash: fixed.InputHash, Generation: fixed.Generation, SourceRefsSHA: sourceSHA,
		ModelStage: btwruntime.WikiCompileModelStageWikiDraft}
	m, err := btwruntime.NewWikiCompileCallPointModel(btwruntime.WikiCompileCallPointModelConfig{
		BaseURL: f.config.DCGatewayRoot, NativeBearer: session.NativeBearer(),
		CallPoint: btwruntime.WikiCompileCallPoint, HTTPClient: f.config.HTTPClient}, ref)
	if err != nil {
		return nil, fmt.Errorf("construct DC Wiki model: %w", err)
	}
	ag, err := content.NewWikiCompileGraphAgent(m)
	if err != nil {
		return nil, fmt.Errorf("construct native Wiki Graph: %w", err)
	}
	sessions := inmemory.NewSessionService()
	r, err := btwruntime.New(wikiNativeRunApp, ag, sessions, observed)
	if err != nil {
		_ = sessions.Close()
		return nil, fmt.Errorf("construct native Wiki Runner: %w", err)
	}
	opened := &wikiCompileNativeRun{runtime: r, sessions: sessions, observed: observed,
		input: fixed, fixedOption: option, proof: proof, owner: f,
		nodes: &wikiNativeNodeLifetime{activeKeys: make(map[wikiNativeNodeKey]int)}}
	f.mu.Lock()
	if f.closed || f.observed != observed || observed.Closed() {
		f.mu.Unlock()
		_ = r.Close()
		_ = sessions.Close()
		return nil, ErrWikiCompileNativeFactory
	}
	f.active[opened] = struct{}{}
	f.mu.Unlock()
	return opened, nil
}

// Close stops every still-open request Runner and waits for its complete Run
// lifecycle. The provider, HTTP client and process telemetry are borrowed.
func (f *WikiCompileNativeRunFactory) Close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed = true
		runs := make([]*wikiCompileNativeRun, 0, len(f.active))
		for run := range f.active {
			runs = append(runs, run)
		}
		f.mu.Unlock()
		var errs []error
		for _, run := range runs {
			errs = append(errs, run.Close())
		}
		f.closeErr = errors.Join(errs...)
	})
	return f.closeErr
}

type wikiCompileNativeRun struct {
	runtime     *btwruntime.Runtime
	sessions    *inmemory.SessionService
	observed    *telemetry.Bundle
	input       content.WikiCompileInput
	fixedOption agent.RunOption
	proof       WikiCompileModelSessionProof
	owner       *WikiCompileNativeRunFactory
	nodes       *wikiNativeNodeLifetime
	mu          sync.Mutex
	closed      bool
	used        bool
	active      sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

type wikiNativeNodeKey struct {
	invocationID string
	nodeID       string
	step         int
}

// The framework Runner can finish a cancelled stream before a Graph node's
// final event/log emission returns. Public request-level NodeCallbacks give
// this run a precise barrier without modifying the shared Content Graph.
type wikiNativeNodeLifetime struct {
	mu         sync.Mutex
	closed     bool
	activeKeys map[wikiNativeNodeKey]int
	active     sync.WaitGroup
	started    int
	finished   int
}

func wikiNativeNodeIdentity(ctx *graph.NodeCallbackContext) wikiNativeNodeKey {
	if ctx == nil {
		return wikiNativeNodeKey{}
	}
	return wikiNativeNodeKey{ctx.InvocationID, ctx.NodeID, ctx.StepNumber}
}

func (n *wikiNativeNodeLifetime) before(_ context.Context, callbackCtx *graph.NodeCallbackContext,
	_ graph.State) (any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, ErrWikiCompileNativeFactory
	}
	key := wikiNativeNodeIdentity(callbackCtx)
	n.activeKeys[key]++
	n.active.Add(1)
	n.started++
	return nil, nil
}

func (n *wikiNativeNodeLifetime) after(_ context.Context, callbackCtx *graph.NodeCallbackContext,
	_ graph.State, _ any, _ error) (any, error) {
	n.mu.Lock()
	key := wikiNativeNodeIdentity(callbackCtx)
	if n.activeKeys[key] > 0 {
		n.activeKeys[key]--
		if n.activeKeys[key] == 0 {
			delete(n.activeKeys, key)
		}
		n.active.Done()
		n.finished++
	}
	n.mu.Unlock()
	return nil, nil
}

func (n *wikiNativeNodeLifetime) closeAndWait() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.active.Wait()
}

func (n *wikiNativeNodeLifetime) callbacks() *graph.NodeCallbacks {
	return graph.NewNodeCallbacks().RegisterBeforeNode(n.before).RegisterAfterNode(n.after)
}

var _ WikiCompileRun = (*wikiCompileNativeRun)(nil)

func (r *wikiCompileNativeRun) ModelSessionProof() WikiCompileModelSessionProof {
	if r == nil {
		return WikiCompileModelSessionProof{}
	}
	return r.proof
}

// Run overrides the caller's opaque Graph option with the fixed source state
// from Open. The native Runner consumes all events internally; the caller's
// sink receives only one validated Graph candidate after successful EOF.
// A model stream can emit Graph completion before its late terminal error.
func (r *wikiCompileNativeRun) Run(ctx context.Context, q btwruntime.Request,
	sink btwruntime.Sink) (btwruntime.Result, error) {
	if r == nil || r.runtime == nil || ctx == nil {
		return btwruntime.Result{}, ErrWikiCompileRun
	}
	if err := ctx.Err(); err != nil {
		return btwruntime.Result{}, err
	}
	if q.Subject.AuthorityID != wikiNativeTechnicalAuthority ||
		q.Subject.TenantID != wikiNativeTechnicalScope || q.Subject.SubjectID != r.input.CompileID ||
		q.SessionID != "wiki-compile/"+r.input.CompileID+"/"+r.input.AttemptID ||
		!strings.HasPrefix(q.RunID, "wiki-compile:") || !strings.HasSuffix(q.RunID, ":"+r.input.AttemptID) {
		return btwruntime.Result{}, ErrWikiCompileRun
	}
	r.mu.Lock()
	if r.closed || r.used || r.observed == nil || r.observed.Closed() {
		r.mu.Unlock()
		return btwruntime.Result{}, ErrWikiCompileNativeFactory
	}
	r.used = true
	r.active.Add(1)
	r.mu.Unlock()
	defer r.active.Done()
	q.Message = model.NewUserMessage(wikiNativeRunMessage)
	q.Options = []agent.RunOption{r.fixedOption,
		agent.MergeRuntimeState(map[string]any{graph.StateKeyNodeCallbacks: r.nodes.callbacks()})}
	var graphDone int
	var completion *event.Event
	result, err := r.runtime.Run(ctx, q, func(_ context.Context, e *event.Event) error {
		candidate, done, checkErr := content.WikiCompileCandidateFromCompletion(e, r.input)
		if checkErr != nil {
			return checkErr
		}
		if done {
			graphDone++
			if graphDone != 1 || candidate.CompileID != r.input.CompileID ||
				candidate.AttemptID != r.input.AttemptID || candidate.ContentSHA256 == "" {
				return ErrWikiCompileRun
			}
			completion = e.Clone()
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("run fixed Wiki candidate: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !result.Completed || graphDone != 1 || completion == nil {
		return result, ErrWikiCompileRun
	}
	if sink != nil {
		if err := sink(ctx, completion); err != nil {
			return result, fmt.Errorf("deliver validated Wiki candidate: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return result, ErrWikiCompileNativeFactory
	}
	return result, nil
}

// Close cancels and waits for Runtime.Run/Graph consumption before closing the
// request-owned private Session. The immutable proof remains readable after it.
func (r *wikiCompileNativeRun) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		var errs []error
		if r.runtime != nil {
			errs = append(errs, r.runtime.Close())
		}
		r.nodes.closeAndWait()
		r.active.Wait()
		if r.sessions != nil {
			errs = append(errs, r.sessions.Close())
		}
		if r.owner != nil {
			r.owner.mu.Lock()
			delete(r.owner.active, r)
			r.owner.mu.Unlock()
		}
		r.closeErr = errors.Join(errs...)
	})
	return r.closeErr
}
