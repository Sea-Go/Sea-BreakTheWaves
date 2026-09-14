package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/internal/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/telemetry"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

var ErrSummary = errors.New("summary output failed evidence contract")

const summaryInstruction = `Answer only from the factual content of the quotes in fixed_evidence_pack. Do not define query terms or add outside background; if a quote lacks detail, say the source does not provide that detail. Do not search or ask for tools. Return exactly one JSON object, without Markdown, with only keys "answer" (nonempty string) and "citations" (array of evidence_id strings). Copy citation IDs exactly from fixed_evidence_pack.evidence and include each cited ID once. Cite the evidence used in the answer; do not duplicate IDs. If evidence is partial, describe the gaps without inventing evidence.`

type SummaryRequest struct {
	SearchID  string
	AnswerID  string
	Subject   btwruntime.SubjectRef
	SessionID string
	Search    Request
}

type SummaryResult struct {
	Search        SearchResult `json:"search"`
	AnswerID      string       `json:"answer_id"`
	Answer        string       `json:"answer,omitempty"`
	Citations     []string     `json:"citations"`
	SummaryStatus string       `json:"summary_status"`
}

// SummaryModelLimits is supplied by an explicit product profile. The generic
// Summary Agent has no hidden output cap or silent tier downgrade.
type SummaryModelLimits struct {
	MaxOutputTokens int
}

type Summarizer struct {
	delivery *Delivery
	runtime  *btwruntime.Runtime
}

// NewSummarizer assembles an actual tRPC-Agent-Go LLMAgent and Runner through
// the shared runtime/telemetry owner. No search Tool, ToolSet or sub-Agent is
// registered on this LLMAgent. The caller owns sessions and telemetry shutdown.
func NewSummarizer(d *Delivery, m model.Model, sessions session.Service, observed *telemetry.Bundle) (*Summarizer, error) {
	if d == nil || isNil(m) || isNil(sessions) || observed == nil || !observed.Installed() {
		return nil, ErrInvalid
	}
	ag := llmagent.New("search_summary", llmagent.WithModel(m), llmagent.WithInstruction(summaryInstruction),
		llmagent.WithTools([]tool.Tool{}), llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: false}))
	r, err := btwruntime.New("search_summary", ag, sessions, observed)
	if err != nil {
		return nil, err
	}
	return &Summarizer{delivery: d, runtime: r}, nil
}

// Summarize emits no answer until RTW citation acceptance, Runner completion,
// and a typed citation check have all succeeded. Search and summary statuses
// stay separate. The first public answer is the returned, validated object.
func (s *Summarizer) Summarize(ctx context.Context, q SummaryRequest) (out SummaryResult, err error) {
	out.AnswerID, out.SummaryStatus = q.AnswerID, "failed"
	if s == nil || q.AnswerID == "" || q.SearchID == "" || q.SessionID == "" {
		return out, ErrInvalid
	}
	if _, err := q.Subject.UserKey(); err != nil {
		return out, err
	}
	out.Search, err = s.delivery.Search(ctx, q.SearchID, q.Search)
	if err != nil {
		return out, err
	}
	if len(out.Search.Pack.Evidence) == 0 {
		out.SummaryStatus = "insufficient"
		return out, nil
	}
	prompt, err := json.Marshal(struct {
		Question string       `json:"question"`
		Pack     EvidencePack `json:"fixed_evidence_pack"`
	}{q.Search.Query, out.Search.Pack})
	if err != nil {
		return out, err
	}
	var final string
	_, err = s.runtime.Run(ctx, btwruntime.Request{Subject: q.Subject, SessionID: q.SessionID,
		RunID: q.AnswerID, Message: model.NewUserMessage(string(prompt))}, func(_ context.Context, e *event.Event) error {
		if e.Response == nil || e.IsPartial {
			return nil
		}
		for _, choice := range e.Choices {
			if choice.Message.Role == model.RoleAssistant && choice.Message.Content != "" {
				final = choice.Message.Content
			}
		}
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("run summary agent: %w", err)
	}
	var body struct {
		Answer    string   `json:"answer"`
		Citations []string `json:"citations"`
	}
	decoder := json.NewDecoder(strings.NewReader(final))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(body.Answer) == "" || len(body.Citations) == 0 {
		return out, ErrSummary
	}
	allowed := make(map[string]bool, len(out.Search.Pack.Evidence))
	for _, e := range out.Search.Pack.Evidence {
		allowed[e.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range body.Citations {
		if !allowed[id] || seen[id] {
			return out, ErrSummary
		}
		seen[id] = true
	}
	out.Answer, out.Citations, out.SummaryStatus = body.Answer, body.Citations, "succeeded"
	return out, nil
}

func (s *Summarizer) Close() error {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.Close()
}
