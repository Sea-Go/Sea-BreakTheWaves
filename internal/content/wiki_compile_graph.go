package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	wikiCompileGraphName    = "wiki_compile_candidate"
	wikiCompileAgentName    = "draft_wiki_from_sources"
	wikiCompilePromptNode   = "fix_source_paragraphs"
	wikiCompileValidateNode = "validate_wiki_candidate"
	wikiCompileInputKey     = "wiki_compile_fixed_input"
	wikiCompileModelKey     = "wiki_compile_model_response"
	wikiCompileOutputKey    = "wiki_compile_candidate"
)

const wikiCompileInstruction = `Draft one editable Wiki page only from the selected immutable source paragraphs in fixed_source_pack. Do not call tools, select other sources or publish a release. Return exactly one JSON object with keys "title", "markdown", and "source_refs". Each source_refs entry has only "revision_id" and "locator"; copy a displayed paragraph:N locator and revision_id exactly. Cite the source paragraphs used; if there is insufficient source content, do not invent facts. Output Markdown body without fences around the JSON.`

// WikiCompileRunOption freezes the ticket and the bounded RTW-owned source
// bytes before a native Runner sees them. The caller must independently pin
// its DC lease and retrieve these specific revisions by RTW revision ID/hash.
func WikiCompileRunOption(input WikiCompileInput) (agent.RunOption, error) {
	owned, err := normalizeWikiCompileInput(input)
	if err != nil {
		return nil, err
	}
	return agent.MergeRuntimeState(map[string]any{wikiCompileInputKey: owned}), nil
}

// NewWikiCompileGraphAgent is an explicit local candidate seam: one native
// GraphAgent, one no-tool LLMAgent model call and a server-validated output.
// It borrows the DC-configured model; no object, WikiRevision or release is
// written here. The app layer owns Runner, Session and the attempt lifecycle.
func NewWikiCompileGraphAgent(m model.Model) (*graphagent.GraphAgent, error) {
	if m == nil {
		return nil, ErrWikiCompileContract
	}
	kind := reflect.ValueOf(m)
	if (kind.Kind() == reflect.Pointer || kind.Kind() == reflect.Interface) && kind.IsNil() {
		return nil, ErrWikiCompileContract
	}
	configuration := model.GenerationConfig{Stream: false, MaxTokens: model.IntPtr(1024)}
	drafter := llmagent.New(wikiCompileAgentName, llmagent.WithModel(m),
		llmagent.WithInstruction(wikiCompileInstruction), llmagent.WithTools([]tool.Tool{}),
		llmagent.WithGenerationConfig(configuration), llmagent.WithMaxLLMCalls(1),
		llmagent.WithEnableCodeExecutionResponseProcessor(false))
	schema := graph.NewStateSchema().
		AddField(wikiCompileInputKey, graph.StateField{Type: reflect.TypeOf(WikiCompileInput{}), Reducer: graph.DefaultReducer}).
		AddField(wikiCompileModelKey, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).
		AddField(wikiCompileOutputKey, graph.StateField{Type: reflect.TypeOf(WikiCompileCandidate{}), Reducer: graph.DefaultReducer}).
		AddField(graph.StateKeyLastResponse, graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer})
	compiled, err := graph.NewStateGraph(schema).
		AddNode(wikiCompilePromptNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			input, ok := graph.GetStateValue[WikiCompileInput](state, wikiCompileInputKey)
			input, err := normalizeWikiCompileInput(input)
			if !ok || err != nil {
				return nil, ErrWikiCompileContract
			}
			type sourceParagraph struct {
				Locator string `json:"locator"`
				Text    string `json:"text"`
			}
			type fixedSource struct {
				RevisionID string            `json:"revision_id"`
				Title      string            `json:"title"`
				Hash       string            `json:"content_hash"`
				Paragraphs []sourceParagraph `json:"paragraphs"`
			}
			sources := make([]fixedSource, len(input.Sources))
			for i, source := range input.Sources {
				parts := sourceParagraphs(source.Content)
				paragraphs := make([]sourceParagraph, len(parts))
				for index, body := range parts {
					paragraphs[index] = sourceParagraph{Locator: fmt.Sprintf("paragraph:%d", index+1), Text: body}
				}
				sources[i] = fixedSource{RevisionID: source.RevisionID, Title: source.Title,
					Hash: source.SHA256, Paragraphs: paragraphs}
			}
			prompt, err := json.Marshal(struct {
				ModuleID string        `json:"module_id"`
				PageID   string        `json:"page_id"`
				Guidance string        `json:"guidance"`
				Sources  []fixedSource `json:"fixed_source_pack"`
			}{input.ModuleID, input.PageID, input.Guidance, sources})
			if err != nil {
				return nil, fmt.Errorf("encode fixed wiki source: %w", err)
			}
			return graph.State{graph.StateKeyLastResponse: string(prompt)}, nil
		}).
		AddAgentNode(wikiCompileAgentName, graph.WithSubgraphInputFromLastResponse(),
			graph.WithSubgraphIsolatedMessages(true),
			graph.WithSubgraphOutputMapper(func(_ graph.State, result graph.SubgraphResult) graph.State {
				return graph.State{wikiCompileModelKey: result.LastResponse}
			})).
		AddNode(wikiCompileValidateNode, func(ctx context.Context, state graph.State) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			input, ok := graph.GetStateValue[WikiCompileInput](state, wikiCompileInputKey)
			raw, outputOK := graph.GetStateValue[string](state, wikiCompileModelKey)
			if !ok || !outputOK {
				return nil, ErrWikiCompileContract
			}
			candidate, err := ParseWikiCompileCandidate(raw, input)
			if err != nil {
				return nil, err
			}
			return graph.State{wikiCompileOutputKey: candidate}, nil
		}).
		SetEntryPoint(wikiCompilePromptNode).
		AddEdge(wikiCompilePromptNode, wikiCompileAgentName).
		AddEdge(wikiCompileAgentName, wikiCompileValidateNode).
		SetFinishPoint(wikiCompileValidateNode).
		Compile()
	if err != nil {
		return nil, fmt.Errorf("compile native wiki candidate graph: %w", err)
	}
	return graphagent.New(wikiCompileGraphName, compiled, graphagent.WithSubAgents([]agent.Agent{drafter}))
}

// WikiCompileCandidateFromCompletion reads only a native Graph completion,
// then verifies the bounded candidate still matches the fixed RTW/DC attempt.
// Runner EOF and RTW AcceptCompile are separate, later obligations.
func WikiCompileCandidateFromCompletion(e *event.Event, expected WikiCompileInput) (WikiCompileCandidate, bool, error) {
	if !graph.IsGraphCompletionEvent(e) {
		return WikiCompileCandidate{}, false, nil
	}
	expected, err := normalizeWikiCompileInput(expected)
	if err != nil {
		return WikiCompileCandidate{}, true, err
	}
	raw, ok := e.StateDelta[wikiCompileOutputKey]
	if !ok {
		return WikiCompileCandidate{}, true, ErrWikiCompileContract
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result WikiCompileCandidate
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF ||
		result.CompileID != expected.CompileID ||
		result.ModuleID != expected.ModuleID || result.PageID != expected.PageID ||
		result.BaseRevision != expected.BaseRevision || result.Generation != expected.Generation ||
		result.InputHash != expected.InputHash || result.AttemptID != expected.AttemptID ||
		result.LeaseEpoch != expected.LeaseEpoch || result.CancelVersion != expected.CancelVersion ||
		result.ContentSHA256 != artifacts.Hash([]byte(result.Markdown)) ||
		strings.TrimSpace(result.Title) == "" || len(result.Title) > 200 ||
		strings.TrimSpace(result.Markdown) == "" || len(result.Markdown) > 48<<10 ||
		len(result.SourceRefs) == 0 {
		return WikiCompileCandidate{}, true, ErrWikiCompileContract
	}
	if _, err := checkedWikiSourceRefs(result.SourceRefs, expected.Sources); err != nil {
		return WikiCompileCandidate{}, true, err
	}
	return result, true, nil
}
