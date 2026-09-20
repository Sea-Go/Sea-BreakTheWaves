package searchclient

import (
	"context"

	trpcrunner "trpc.group/trpc-go/trpc-agent-go/runner"

	"sea/service/common/config"
	"sea/service/common/infra"
	"sea/service/common/skillsys"
	trpcmodel "sea/service/common/trpcagent/model"
	logic "sea/service/search/rpc/internal/logic"
	model "sea/service/search/rpc/internal/model"
	searchagent "sea/service/search/rpc/internal/trpcagent/agent"
	agent "sea/service/search/rpc/internal/trpcagent/agent/llm/legacyagent"
	searchprompt "sea/service/search/rpc/internal/trpcagent/prompt"
	runnerfactory "sea/service/search/rpc/internal/trpcagent/runner"
	milvusSearch "sea/service/search/rpc/internal/trpcagent/skill/milvus_search"
)

var (
	ErrSourceMetadataUnavailable   = logic.ErrSourceMetadataUnavailable
	ErrOnboardingMemoryUnavailable = logic.ErrOnboardingMemoryUnavailable
	ErrInvalidOnboardingAnswer     = logic.ErrInvalidOnboardingAnswer
)

type (
	Registry                        = skillsys.Registry
	ContentSearchAgent              = agent.ContentSearchAgent
	ContentSearchRequest            = agent.ContentSearchRequest
	ContentSearchResponse           = agent.ContentSearchResponse
	StructuredSearchRequest         = logic.StructuredSearchRequest
	ArticleTitleSearchResponse      = logic.ArticleTitleSearchResponse
	AuthorNameSearchResponse        = logic.AuthorNameSearchResponse
	ArticleTitleSearchService       = logic.ArticleTitleSearchService
	AuthorNameSearchService         = logic.AuthorNameSearchService
	OnboardingQuestionnaireService  = logic.OnboardingQuestionnaireService
	OnboardingQuestionnaireRequest  = logic.OnboardingQuestionnaireRequest
	OnboardingQuestionnaireResponse = logic.OnboardingQuestionnaireResponse
)

// Dependencies are created once by the RPC process and handed to the API
// process through the generated transport. The current compatibility adapter
// keeps the same in-process types while establishing the client boundary.
type Dependencies struct {
	ContentSearch *ContentSearchAgent
	TitleSearch   *ArticleTitleSearchService
	AuthorSearch  *AuthorNameSearchService
	Onboarding    *OnboardingQuestionnaireService
	Tools         *Registry
	Runner        trpcrunner.Runner
}

type Client struct {
	deps Dependencies
}

func New(deps Dependencies) *Client { return &Client{deps: deps} }

func (c *Client) Close() error {
	if c != nil && c.deps.Runner != nil {
		return c.deps.Runner.Close()
	}
	return nil
}

func (c *Client) Search(ctx context.Context, req ContentSearchRequest) (ContentSearchResponse, error) {
	return c.deps.ContentSearch.Search(ctx, req)
}

func (c *Client) SearchTitle(ctx context.Context, req StructuredSearchRequest) (logic.ArticleTitleSearchResponse, error) {
	return c.deps.TitleSearch.Search(ctx, req)
}

func (c *Client) SearchAuthors(ctx context.Context, req StructuredSearchRequest) (logic.AuthorNameSearchResponse, error) {
	return c.deps.AuthorSearch.Search(ctx, req)
}

func (c *Client) SubmitOnboarding(ctx context.Context, req OnboardingQuestionnaireRequest) (OnboardingQuestionnaireResponse, error) {
	return c.deps.Onboarding.Submit(ctx, req)
}

func (c *Client) Tools() []string {
	if c.deps.Tools == nil {
		return nil
	}
	return c.deps.Tools.List()
}

// NewRuntime assembles both the current compatibility dependencies and the
// framework Runner that owns the model-driven Search lifecycle.
func NewRuntime() *Client {
	registry := skillsys.NewRegistry()
	registry.Register(milvusSearch.New())
	articleRepo := model.NewArticleRepo(infra.Postgres())
	sourceDB := infra.SourcePostgres()
	gateway, err := trpcmodel.NewGateway(trpcmodel.Options{
		Name:    configOrDefault(config.Cfg.Agent.Model, config.Cfg.Ali.TextModel),
		BaseURL: config.Cfg.Ali.BaseURL,
		APIKey:  config.Cfg.Ali.APIKey,
	})
	if err != nil {
		panic(err)
	}
	searchAgent, err := searchagent.NewSearchAgent(searchagent.Dependencies{
		Model:        gateway,
		Instruction:  searchprompt.EvidenceInstructionV1,
		MaxToolCalls: config.Cfg.Agent.MaxToolCalls,
	})
	if err != nil {
		panic(err)
	}
	runner, err := runnerfactory.New(runnerfactory.Dependencies{AppName: "sea-search", Agent: searchAgent, SessionService: true})
	if err != nil {
		panic(err)
	}
	deps := Dependencies{
		ContentSearch: agent.NewContentSearchAgent(infra.NewAIClient(), registry, articleRepo),
		TitleSearch:   logic.NewArticleTitleSearchService(model.NewSourceArticleRepo(sourceDB)),
		AuthorSearch:  logic.NewAuthorNameSearchService(model.NewSourceUserRepo(sourceDB)),
		Onboarding: logic.NewOnboardingQuestionnaireService(
			model.NewMemoryRepo(infra.Postgres()),
			model.NewMemoryChunkRepo(infra.Postgres()),
		),
		Tools:  registry,
		Runner: runner,
	}
	return New(deps)
}

func configOrDefault(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
