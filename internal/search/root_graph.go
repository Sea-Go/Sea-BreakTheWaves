package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	rootSummaryRequestKey = "root_summary_request"
	rootSearchResultKey   = "root_search_result"
	rootModelOutputKey    = "root_model_output"
	rootSummaryResultKey  = "root_summary_result"
	rootSearchNode        = "search_and_accept"
	rootSummaryAgent      = "search_summary"
	rootFinishNode        = "validate_answer"
)

var ErrRootSummaryOutput = errors.New("root search summary graph output missing or invalid")

// RootSummarizer owns one framework Runner over the whole search, citation and
// summary sequence. The source, citation acceptor, model, sessions and telemetry
// are borrowed; only this Runner is closed by Close.
type RootSummarizer struct {
	runtime *btwruntime.Runtime
}

// RootSummaryRunOption fixes the authoritative snapshot and public identity for
// one Runner invocation. The caller must obtain that snapshot before this call.
func RootSummaryRunOption(q SummaryRequest) (agent.RunOption, error) {
	if err := validateRootSummaryRequest(q); err != nil {
		return nil, err
	}
	q.Search.Snapshot = cloneSnapshot(q.Search.Snapshot)
	return agent.MergeRuntimeState(map[string]any{rootSummaryRequestKey: q}), nil
}

func validateRootSummaryRequest(q SummaryRequest) error {
	if q.SearchID == "" || q.AnswerID == "" || q.SessionID == "" ||
		strings.TrimSpace(q.Search.Query) == "" ||
		(q.Search.Depth != Fast && q.Search.Depth != Detailed) ||
		(q.Search.Intelligence != Low && q.Search.Intelligence != Medium && q.Search.Intelligence != High) ||
		!validSnapshot(q.Search.Snapshot) {
		return ErrInvalid
	}
	if _, err := q.Subject.UserKey(); err != nil {
		return err
	}
	return nil
}

// NewRootSummarizer assembles the v1.8.1 GraphAgent with one deterministic
// delivery node, a conditional no-evidence branch, a real no-tool LLMAgent
// sub-agent and a final contract-check node. Search itself remains a domain
// operation; the Graph and Runner own Agent execution and native observability.
func NewRootSummarizer(d *Delivery, m model.Model, sessions session.Service, observed *telemetry.Bundle) (*RootSummarizer, error) {
	if d == nil || isNil(m) || isNil(sessions) || observed == nil || !observed.Installed() {
		return nil, ErrInvalid
	}
	summaryAgent := llmagent.New(rootSummaryAgent, llmagent.WithModel(m), llmagent.WithInstruction(summaryInstruction),
		llmagent.WithTools([]tool.Tool{}), llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: false}))
	schema := graph.NewStateSchema().
		AddField(rootSummaryRequestKey, graph.StateField{Type: reflect.TypeOf(SummaryRequest{}), Reducer: graph.DefaultReducer}).
		AddField(rootSearchResultKey, graph.StateField{Type: reflect.TypeOf(SearchResult{}), Reducer: graph.DefaultReducer}).
		AddField(rootModelOutputKey, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).
		AddField(rootSummaryResultKey, graph.StateField{Type: reflect.TypeOf(SummaryResult{}), Reducer: graph.DefaultReducer}).
		AddField(graph.StateKeyLastResponse, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(rootSearchNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
			if !ok || validateRootSummaryRequest(q) != nil {
				return nil, ErrInvalid
			}
			found, err := d.Search(ctx, q.SearchID, q.Search)
			if err != nil {
				return nil, fmt.Errorf("search and accept citations: %w", err)
			}
			update := graph.State{rootSearchResultKey: found}
			if len(found.Pack.Evidence) == 0 {
				return update, nil
			}
			if err := validateAcceptedPack(found, q.SearchID); err != nil {
				return nil, err
			}
			prompt, err := json.Marshal(struct {
				Question string       `json:"question"`
				Pack     EvidencePack `json:"fixed_evidence_pack"`
			}{q.Search.Query, found.Pack})
			if err != nil {
				return nil, fmt.Errorf("encode accepted evidence: %w", err)
			}
			update[graph.StateKeyLastResponse] = string(prompt)
			return update, nil
		}).
		AddAgentNode(rootSummaryAgent, graph.WithSubgraphInputFromLastResponse(),
			graph.WithSubgraphIsolatedMessages(true),
			graph.WithSubgraphOutputMapper(func(_ graph.State, result graph.SubgraphResult) graph.State {
				return graph.State{rootModelOutputKey: result.LastResponse}
			})).
		AddNode(rootFinishNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			q, ok := graph.GetStateValue[SummaryRequest](state, rootSummaryRequestKey)
			if !ok || validateRootSummaryRequest(q) != nil {
				return nil, ErrInvalid
			}
			found, ok := graph.GetStateValue[SearchResult](state, rootSearchResultKey)
			if !ok {
				return nil, ErrRootSummaryOutput
			}
			out := SummaryResult{Search: found, AnswerID: q.AnswerID, SummaryStatus: "insufficient"}
			if len(found.Pack.Evidence) == 0 {
				return graph.State{rootSummaryResultKey: out}, nil
			}
			if err := validateAcceptedPack(found, q.SearchID); err != nil {
				return nil, err
			}
			raw, ok := graph.GetStateValue[string](state, rootModelOutputKey)
			if !ok {
				return nil, ErrSummary
			}
			answer, citations, err := validateSummaryJSON(raw, found.Pack)
			if err != nil {
				return nil, err
			}
			out.Answer, out.Citations, out.SummaryStatus = answer, citations, "succeeded"
			return graph.State{rootSummaryResultKey: out}, nil
		}).
		AddConditionalEdges(rootSearchNode, func(_ context.Context, state graph.State) (string, error) {
			found, ok := graph.GetStateValue[SearchResult](state, rootSearchResultKey)
			if !ok {
				return "", ErrRootSummaryOutput
			}
			if len(found.Pack.Evidence) == 0 {
				return "insufficient", nil
			}
			return "summary", nil
		}, map[string]string{"insufficient": rootFinishNode, "summary": rootSummaryAgent}).
		AddEdge(rootSummaryAgent, rootFinishNode).
		SetEntryPoint(rootSearchNode).
		SetFinishPoint(rootFinishNode).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile root search summary graph: %w", err)
	}
	ag, err := graphagent.New("search_summary_root", compiled, graphagent.WithSubAgents([]agent.Agent{summaryAgent}))
	if err != nil {
		return nil, fmt.Errorf("create root search summary agent: %w", err)
	}
	r, err := btwruntime.New("search_summary_root", ag, sessions, observed)
	if err != nil {
		return nil, err
	}
	return &RootSummarizer{runtime: r}, nil
}

// Summarize consumes the complete framework event stream before returning an
// answer. Raw model events never leave this method and cannot become public
// output before the citation receipt and final validation are both complete.
func (s *RootSummarizer) Summarize(ctx context.Context, q SummaryRequest) (SummaryResult, error) {
	out := SummaryResult{AnswerID: q.AnswerID, SummaryStatus: "failed"}
	if s == nil || s.runtime == nil || ctx == nil {
		return out, ErrInvalid
	}
	option, err := RootSummaryRunOption(q)
	if err != nil {
		return out, err
	}
	fixed := q
	fixed.Search.Snapshot = cloneSnapshot(q.Search.Snapshot)
	var completed int
	var candidate SummaryResult
	_, err = s.runtime.Run(ctx, btwruntime.Request{Subject: q.Subject, SessionID: q.SessionID,
		RunID: q.AnswerID, Message: model.NewUserMessage(q.Search.Query), Options: []agent.RunOption{option}},
		func(_ context.Context, e *event.Event) error {
			if !graph.IsGraphCompletionEvent(e) {
				return nil
			}
			completed++
			raw, ok := e.StateDelta[rootSummaryResultKey]
			if !ok {
				return ErrRootSummaryOutput
			}
			if err := json.Unmarshal(raw, &candidate); err != nil {
				return fmt.Errorf("decode root graph result: %w", err)
			}
			if !validRootSummaryResult(candidate, fixed) {
				return ErrRootSummaryOutput
			}
			return nil
		})
	if err != nil {
		return out, fmt.Errorf("run root search summary graph: %w", err)
	}
	if completed != 1 || !validRootSummaryResult(candidate, fixed) {
		return out, ErrRootSummaryOutput
	}
	return candidate, nil
}

func validateAcceptedPack(found SearchResult, searchID string) error {
	if found.Pack.SearchID != searchID || len(found.Pack.Evidence) == 0 {
		return ErrReceipt
	}
	hash, err := found.Pack.Hash()
	if err != nil || found.Receipt.SearchID != searchID ||
		found.Receipt.PackHash != hash || found.Receipt.DurableRef == "" {
		return ErrReceipt
	}
	return nil
}

func validateSummaryJSON(raw string, pack EvidencePack) (string, []string, error) {
	var body struct {
		Answer    string   `json:"answer"`
		Citations []string `json:"citations"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(body.Answer) == "" || len(body.Citations) == 0 {
		return "", nil, ErrSummary
	}
	allowed := make(map[string]bool, len(pack.Evidence))
	for _, evidence := range pack.Evidence {
		allowed[evidence.ID] = true
	}
	seen := make(map[string]bool, len(body.Citations))
	for _, id := range body.Citations {
		if !allowed[id] || seen[id] {
			return "", nil, ErrSummary
		}
		seen[id] = true
	}
	return body.Answer, body.Citations, nil
}

func validRootSummaryResult(out SummaryResult, q SummaryRequest) bool {
	if out.AnswerID != q.AnswerID || !validGraphResult(out.Search.Retrieval, q.Search) ||
		out.Search.Pack.SearchID != q.SearchID ||
		!reflect.DeepEqual(out.Search.Pack.Snapshot, q.Search.Snapshot) {
		return false
	}
	if len(out.Search.Pack.Evidence) == 0 {
		return out.Search.Pack.Status == "empty" && out.Search.Receipt == (CitationReceipt{}) &&
			out.SummaryStatus == "insufficient" && out.Answer == "" && len(out.Citations) == 0
	}
	if validateAcceptedPack(out.Search, q.SearchID) != nil || out.SummaryStatus != "succeeded" {
		return false
	}
	allowed := make(map[string]bool, len(out.Search.Pack.Evidence))
	for _, evidence := range out.Search.Pack.Evidence {
		allowed[evidence.ID] = true
	}
	if strings.TrimSpace(out.Answer) == "" || len(out.Citations) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range out.Citations {
		if !allowed[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *RootSummarizer) Close() error {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Close()
}
