package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	openaioptions "github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// ModelConfig addresses DC's legacy OpenAI-compatible chat gateway by model
// name. NewModel does not sign a registered CallPoint or idempotency budget;
// Wiki compilation must use NewWikiCompileCallPointModel instead.
// Representation requests have a distinct typed client, never this chat model.
type ModelConfig struct {
	BaseURL    string
	Token      string
	CallPoint  string
	HTTPClient *http.Client
}

func NewModel(cfg ModelConfig) (model.Model, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.TrimSpace(cfg.CallPoint) == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("model requires valid DC base URL and call point")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := strings.TrimRight(cfg.BaseURL, "/") + "/v1"
	inner := openai.New(cfg.CallPoint, openai.WithBaseURL(base), openai.WithAPIKey(cfg.Token), openai.WithOpenAIOptions(openaioptions.WithHTTPClient(&cloned), openaioptions.WithMaxRetries(0)))
	return &completeModel{inner: inner}, nil
}

// The locked SDK treats a clean transport EOF without a finish_reason as success.
// This narrow public Model adapter keeps ordinary deltas and marks that case as
// an incomplete model response. It does not implement another agent/tool loop.
type completeModel struct{ inner model.Model }

func (m *completeModel) Info() model.Info { return m.inner.Info() }
func (m *completeModel) GenerateContent(parent context.Context, q *model.Request) (<-chan *model.Response, error) {
	if q == nil {
		return nil, errors.New("nil model request")
	}
	ctx, cancel := context.WithCancel(parent)
	input, err := m.inner.GenerateContent(ctx, q)
	if err != nil {
		cancel()
		return nil, err
	}
	if input == nil {
		cancel()
		return nil, errors.New("nil model response stream")
	}
	output := make(chan *model.Response)
	go func() {
		defer close(output)
		defer cancel()
		finished := false
		emit := func(v *model.Response) bool {
			select {
			case output <- v:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-input:
				if !ok {
					if !finished {
						emit(&model.Response{Done: true, Error: &model.ResponseError{Type: model.ErrorTypeStreamError, Message: "model stream ended without final response"}})
					}
					return
				}
				if v == nil {
					continue
				}
				if v.Error != nil {
					finished = true
				}
				if v.Done && !v.IsPartial && v.Error == nil {
					valid := len(v.Choices) > 0
					for _, c := range v.Choices {
						valid = valid && c.FinishReason != nil && *c.FinishReason != ""
					}
					if !valid {
						copy := *v
						copy.Error = &model.ResponseError{Type: model.ErrorTypeStreamError, Message: "model stream ended without finish_reason"}
						v = &copy
					}
					finished = true
				}
				if !emit(v) {
					return
				}
			}
		}
	}()
	return output, nil
}
