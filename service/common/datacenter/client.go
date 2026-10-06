// Package datacenter connects BTW to DataCenter's active model contract.
package datacenter

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// TokenSource is supplied by the deployment identity layer, not BTW settings.
type TokenSource func(context.Context) (string, error)

type Connector struct {
	address   string
	inference *inference.Client
	transport http.RoundTripper
}

type requestKey struct{}
type requestIdentity struct {
	idempotency string
	purpose     string
}

type transport struct {
	base  http.RoundTripper
	token TokenSource
}

func (t transport) RoundTrip(original *http.Request) (*http.Response, error) {
	request := original.Clone(original.Context())
	request.Header.Del("Authorization")
	if t.token != nil {
		value, err := t.token(original.Context())
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("DataCenter workload identity is unavailable")
		}
		request.Header.Set("Authorization", "Bearer "+value)
	}
	if identity, ok := original.Context().Value(requestKey{}).(requestIdentity); ok {
		request.Header.Set("Idempotency-Key", identity.idempotency)
		request.Header.Set("X-WhaleHall-Model-Purpose", identity.purpose)
	}
	return t.base.RoundTrip(request)
}

func New(address string, token TokenSource) (*Connector, error) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("DataCenter address must be an HTTP(S) origin without credentials or path")
	}
	base := strings.TrimRight(parsed.String(), "/")
	clientTransport := transport{base: http.DefaultTransport, token: token}
	encoder := inference.New(base+"/v1/model", "", 120*time.Second)
	encoder.HTTP.Transport = clientTransport
	return &Connector{address: base, inference: encoder, transport: clientTransport}, nil
}

func (c *Connector) Manifest(ctx context.Context) (inference.Manifest, error) {
	return c.inference.Manifest(ctx)
}

func (c *Connector) Inference() *inference.Client { return c.inference }

type chatModel struct {
	model.Model
	purpose string
}

func (m *chatModel) GenerateContent(ctx context.Context, request *model.Request) (<-chan *model.Response, error) {
	ctx = context.WithValue(ctx, requestKey{}, requestIdentity{idempotency: uuid.NewString(), purpose: m.purpose})
	return m.Model.GenerateContent(ctx, request)
}

// Chat selects only a DataCenter-advertised logical alias. Physical provider
// selection and credentials remain in DataCenter.
func (c *Connector) Chat(alias, purpose string) (model.Model, error) {
	if c == nil || alias == "" || purpose == "" {
		return nil, errors.New("DataCenter chat route is unavailable")
	}
	m := openai.New(alias, openai.WithBaseURL(c.address+"/v1/model"), openai.WithAPIKey("datacenter"), openai.WithHTTPClientOptions(openai.WithHTTPClientTransport(c.transport)))
	return &chatModel{Model: m, purpose: purpose}, nil
}
