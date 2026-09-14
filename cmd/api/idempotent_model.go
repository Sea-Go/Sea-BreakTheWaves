package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/internal/search"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// gatewayModel gives DataCenter one stable idempotency key per exact Agent
// request. The accepted EvidencePack embeds RTW's unique SearchID, so a retry
// reuses the key while a distinct logical search gets a distinct key. The
// framework's official OpenAI adapter remains the model implementation.
type gatewayModel struct {
	name string
	url  string
	key  string
}

func (m gatewayModel) Info() model.Info { return model.Info{Name: m.name} }

func (m gatewayModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	if request == nil || len(request.Messages) == 0 {
		return nil, errors.New("model request must contain a fixed Agent message")
	}
	invocation, ok := searchdomain.ModelInvocationFromContext(ctx)
	if !ok {
		return nil, errors.New("DataCenter model call missing RTW fixed invocation scope")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	requestSum := sha256.Sum256(raw)
	keyInput, err := json.Marshal(struct {
		Ref        searchdomain.ModelInvocationRef `json:"invocation"`
		RequestSHA string                          `json:"request_sha256"`
		Model      string                          `json:"model"`
	}{invocation, hex.EncodeToString(requestSum[:]), m.name})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(keyInput)
	idempotencyKey := "btw-summary-" + hex.EncodeToString(sum[:])
	client := openai.New(m.name, openai.WithBaseURL(m.url), openai.WithAPIKey(m.key),
		openai.WithHeaders(map[string]string{"Idempotency-Key": idempotencyKey}),
		openai.WithExtraFields(map[string]any{"reasoning_effort": "none"}))
	return client.GenerateContent(ctx, request)
}

var _ model.Model = gatewayModel{}
