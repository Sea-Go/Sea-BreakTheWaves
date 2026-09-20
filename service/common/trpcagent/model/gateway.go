package model

import (
	"fmt"

	trpcmodel "trpc.group/trpc-go/trpc-agent-go/model"
	trpcopenai "trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// Options configures an OpenAI-compatible gateway endpoint. The endpoint is
// expected to be the Sea DataCenter model gateway, not a direct provider.
type Options struct {
	Name    string
	BaseURL string
	APIKey  string
}

// NewGateway returns a framework Model backed by the configured DataCenter
// OpenAI-compatible gateway.
func NewGateway(opts Options) (trpcmodel.Model, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("model gateway base URL is required")
	}
	if opts.APIKey == "" {
		return nil, fmt.Errorf("model gateway API key is required")
	}
	return trpcopenai.New(opts.Name, trpcopenai.WithBaseURL(opts.BaseURL), trpcopenai.WithAPIKey(opts.APIKey)), nil
}
