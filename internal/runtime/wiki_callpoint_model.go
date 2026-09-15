package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	openaioptions "github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

const (
	WikiCompileCallPoint           = "knowledge-wiki-compiler"
	WikiCompileModelStageWikiDraft = "WikiDraft"
	wikiCompileOutputTokens        = 1024
	wikiCompileMaxWireBytes        = 128 << 10
)

var (
	ErrWikiCompileCallPoint          = errors.New("wiki compile model callpoint unavailable")
	ErrWikiCompileInvocationConflict = errors.New("wiki compile model invocation changed under one key")
	wikiCompileRefID                 = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	wikiCompileRefSHA                = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// NativeBearer is a raw DC native app_user access token, never a DC jobs
// service token or RTW JWT. The real DC Gateway still verifies its session,
// user UUID, auth epoch and active status on every invocation.
type WikiCompileCallPointModelConfig struct {
	BaseURL      string
	NativeBearer string
	CallPoint    string
	HTTPClient   *http.Client
}

// These fields must be frozen by the RTW/DC Wiki worker after it verifies the
// RTW source revision bytes and ContentHash. The Agent prompt cannot set them.
// AttemptID is deliberately absent: a fresh DC lease with unchanged logical
// Compile/source content reuses the same durable model invocation.
type WikiCompileModelInvocationRef struct {
	CompileID     string
	InputHash     string
	Generation    int64
	SourceRefsSHA string
	ModelStage    string
}

type WikiCompileSourceIdentity struct {
	RevisionID  string `json:"revision_id"`
	ContentHash string `json:"content_hash"`
}

// WikiCompileSourceRefsSHA preserves RTW's frozen SourceRevisionIDs order.
// Object member keys use JCS lexical order (content_hash, revision_id), not
// Go's struct field order; a different order changes the model receipt key.
func WikiCompileSourceRefsSHA(refs []WikiCompileSourceIdentity) (string, error) {
	if len(refs) == 0 || len(refs) > 16 {
		return "", ErrWikiCompileCallPoint
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if !wikiCompileRefID.MatchString(ref.RevisionID) ||
			!wikiCompileRefSHA.MatchString(ref.ContentHash) || seen[ref.RevisionID] {
			return "", ErrWikiCompileCallPoint
		}
		seen[ref.RevisionID] = true
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

type wikiCompileCallPointModel struct {
	inner        model.Model
	key          string
	callPoint    string
	nativeBearer string
	gate         chan struct{}
	boundHash    [sha256.Size]byte
	bound        bool
}

// NewWikiCompileCallPointModel keeps the official tRPC-Agent-Go OpenAI model,
// request/response conversion, and completeModel finish_reason gate. It only
// injects DC's typed callpoint/idempotency/budget headers at the SDK middleware
// boundary, after the SDK has serialized the actual body.
func NewWikiCompileCallPointModel(cfg WikiCompileCallPointModelConfig,
	ref WikiCompileModelInvocationRef) (model.Model, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil ||
		cfg.CallPoint != WikiCompileCallPoint || !nativeWikiBearer(cfg.NativeBearer) ||
		!wikiCompileRefID.MatchString(ref.CompileID) ||
		!wikiCompileRefSHA.MatchString(ref.InputHash) || ref.Generation < 1 ||
		!wikiCompileRefSHA.MatchString(ref.SourceRefsSHA) ||
		ref.ModelStage != WikiCompileModelStageWikiDraft {
		return nil, ErrWikiCompileCallPoint
	}
	rawRef, err := json.Marshal(struct {
		CompileID     string `json:"compile_id"`
		InputHash     string `json:"input_hash"`
		Generation    int64  `json:"generation"`
		SourceRefsSHA string `json:"source_refs_sha"`
		ModelStage    string `json:"model_stage"`
	}{ref.CompileID, ref.InputHash, ref.Generation, ref.SourceRefsSHA, ref.ModelStage})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(rawRef)
	key := "wiki-compile-" + hex.EncodeToString(sum[:])
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	m := &wikiCompileCallPointModel{
		key: key, callPoint: cfg.CallPoint, nativeBearer: cfg.NativeBearer,
		gate: make(chan struct{}, 1),
	}
	m.gate <- struct{}{}
	base := strings.TrimRight(cfg.BaseURL, "/") + "/v1"
	official := openai.New(cfg.CallPoint,
		openai.WithBaseURL(base), openai.WithAPIKey(cfg.NativeBearer),
		openai.WithOpenAIOptions(
			openaioptions.WithHTTPClient(&cloned),
			openaioptions.WithMaxRetries(0),
			openaioptions.WithHeader("X-Sea-Model-Callpoint", cfg.CallPoint),
			openaioptions.WithHeader("Idempotency-Key", key),
			openaioptions.WithMiddleware(m.boundRequest),
		),
	)
	m.inner = &completeModel{inner: official}
	return m, nil
}

func nativeWikiBearer(token string) bool {
	if !strings.HasPrefix(token, "wh_access_") ||
		strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
		return false
	}
	suffix := strings.TrimPrefix(token, "wh_access_")
	decoded, err := base64.RawURLEncoding.DecodeString(suffix)
	return err == nil && len(decoded) == 32 &&
		base64.RawURLEncoding.EncodeToString(decoded) == suffix
}

func (m *wikiCompileCallPointModel) Info() model.Info { return m.inner.Info() }

func (m *wikiCompileCallPointModel) GenerateContent(ctx context.Context,
	q *model.Request) (<-chan *model.Response, error) {
	if ctx == nil || q == nil || q.MaxTokens == nil || *q.MaxTokens != wikiCompileOutputTokens ||
		q.Stream || len(q.Tools) != 0 || len(q.Messages) == 0 || len(q.Messages) > 16 {
		return nil, ErrWikiCompileCallPoint
	}
	total := 0
	for _, msg := range q.Messages {
		if msg.Role != model.RoleSystem && msg.Role != model.RoleUser &&
			msg.Role != model.RoleAssistant {
			return nil, ErrWikiCompileCallPoint
		}
		if strings.TrimSpace(msg.Content) == "" || !utf8.ValidString(msg.Content) ||
			len(msg.ContentParts) != 0 || len(msg.ToolCalls) != 0 ||
			msg.ToolID != "" || msg.ToolName != "" || msg.ReasoningContent != "" {
			return nil, ErrWikiCompileCallPoint
		}
		total += len(msg.Content)
		if total > wikiCompileMaxWireBytes {
			return nil, ErrWikiCompileCallPoint
		}
	}
	return m.inner.GenerateContent(ctx, q)
}

func (m *wikiCompileCallPointModel) boundRequest(r *http.Request,
	next openaioptions.MiddlewareNext) (*http.Response, error) {
	if r == nil || r.URL == nil || r.Context() == nil || r.Method != http.MethodPost ||
		!strings.HasSuffix(r.URL.Path, "/v1/chat/completions") ||
		r.Header.Get("Authorization") != "Bearer "+m.nativeBearer ||
		r.Header.Get("X-Sea-Model-Callpoint") != m.callPoint ||
		r.Header.Get("Idempotency-Key") != m.key ||
		len(r.Header.Values("X-Sea-Model-Callpoint")) != 1 ||
		len(r.Header.Values("Idempotency-Key")) != 1 || r.Body == nil {
		return nil, ErrWikiCompileCallPoint
	}
	select {
	case <-r.Context().Done():
		return nil, r.Context().Err()
	case <-m.gate:
	}
	defer func() { m.gate <- struct{}{} }()
	body, err := io.ReadAll(io.LimitReader(r.Body, wikiCompileMaxWireBytes+1))
	if err != nil || len(body) > wikiCompileMaxWireBytes {
		return nil, ErrWikiCompileCallPoint
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	// Go's Transport can otherwise replay an Idempotency-Key POST on a stale
	// connection when GetBody is available, even with SDK MaxRetries(0).
	r.GetBody = nil
	var wire struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens           *int            `json:"max_tokens"`
		MaxCompletionTokens *int            `json:"max_completion_tokens"`
		Stream              *bool           `json:"stream"`
		N                   *int            `json:"n"`
		Tools               json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Model != m.callPoint ||
		len(wire.Messages) == 0 || len(wire.Messages) > 16 ||
		wire.MaxCompletionTokens == nil || *wire.MaxCompletionTokens != wikiCompileOutputTokens ||
		(wire.MaxTokens != nil && *wire.MaxTokens != wikiCompileOutputTokens) ||
		(wire.Stream != nil && *wire.Stream) || (wire.N != nil && *wire.N != 1) ||
		(len(wire.Tools) != 0 && string(wire.Tools) != "null" && string(wire.Tools) != "[]") {
		return nil, ErrWikiCompileCallPoint
	}
	for _, msg := range wire.Messages {
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" {
			return nil, ErrWikiCompileCallPoint
		}
		var text string
		if json.Unmarshal(msg.Content, &text) != nil || strings.TrimSpace(text) == "" {
			return nil, ErrWikiCompileCallPoint
		}
	}
	inputBudget := int64(len(body)) + 512 + 64*int64(len(wire.Messages))
	budget := strconv.FormatInt(inputBudget, 10)
	r.Header.Set("X-Sea-Model-Input-Tokens", budget)
	r.Header.Set("X-Sea-Model-Context-Tokens", budget)
	r.Header.Set("X-Sea-Model-Output-Tokens", strconv.Itoa(wikiCompileOutputTokens))
	otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(r.Header))
	bodyHash := sha256.Sum256(body)
	material, err := json.Marshal(struct {
		CallPoint     string `json:"callpoint"`
		BodyHash      string `json:"body_hash"`
		InputTokens   int64  `json:"input_tokens"`
		ContextTokens int64  `json:"context_tokens"`
		OutputTokens  int    `json:"output_tokens"`
	}{m.callPoint, hex.EncodeToString(bodyHash[:]), inputBudget, inputBudget, wikiCompileOutputTokens})
	if err != nil {
		return nil, ErrWikiCompileCallPoint
	}
	current := sha256.Sum256(material)
	if m.bound && m.boundHash != current {
		return nil, ErrWikiCompileInvocationConflict
	}
	m.boundHash = current
	m.bound = true
	return next(r)
}
