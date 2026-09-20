package recommendclient

import (
	"context"
	"time"

	trpcrunner "trpc.group/trpc-go/trpc-agent-go/runner"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/config"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/infra"
	trpcmodel "github.com/Sea-Go/Sea-BreakTheWaves/service/common/trpcagent/model"
	logic "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/logic"
	model "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/model"
	recommendagent "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/agent"
	core "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/core"
	recommendprompt "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/prompt"
	runnerfactory "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/runner"
)

type (
	RecommendationService = core.RecommendationService
	RecommendRequest      = core.RecommendRequest
	RecommendResponse     = core.RecommendResponse
	EventBatchRequest     = core.EventBatchRequest
	EventBatchResponse    = core.EventBatchResponse
	TraceQueryRequest     = core.TraceQueryRequest
	StreamEvent           = core.StreamEvent
	RecoEvaluationService = logic.RecoEvaluationService
)

type Dependencies struct {
	Recommendation *RecommendationService
	Evaluation     *RecoEvaluationService
	Runner         trpcrunner.Runner
}

type Client struct{ deps Dependencies }

func (c *Client) Close() error {
	if c != nil && c.deps.Runner != nil {
		return c.deps.Runner.Close()
	}
	return nil
}

func New(deps Dependencies) *Client { return &Client{deps: deps} }

func (c *Client) Recommend(ctx context.Context, req RecommendRequest) (RecommendResponse, error) {
	return c.deps.Recommendation.Recommend(ctx, req)
}

func (c *Client) RecordEvents(ctx context.Context, req EventBatchRequest) EventBatchResponse {
	return c.deps.Recommendation.RecordEvents(ctx, req)
}

func (c *Client) StreamRecommend(ctx context.Context, req RecommendRequest) (<-chan core.StreamEvent, error) {
	return c.deps.Recommendation.StreamRecommend(ctx, req)
}

func (c *Client) Summary() core.ObservationSummary {
	if c.deps.Recommendation == nil {
		return core.ObservationSummary{}
	}
	return c.deps.Recommendation.Summary()
}

func (c *Client) Trace(req TraceQueryRequest) core.TraceQueryResponse {
	if c.deps.Recommendation == nil {
		return core.TraceQueryResponse{}
	}
	return c.deps.Recommendation.Trace(req)
}

func (c *Client) ListSkills() []core.SkillDefinition {
	if c.deps.Recommendation == nil {
		return nil
	}
	return c.deps.Recommendation.ListSkills()
}

func (c *Client) EvaluationSummary(ctx context.Context, surface, window string) (any, error) {
	if c.deps.Evaluation == nil {
		return core.ObservationSummary{}, nil
	}
	return c.deps.Evaluation.Summary(ctx, surface, window)
}

// NewRuntime assembles recommendation dependencies and the framework Runner.
func NewRuntime(skillDir string) *Client {
	db := infra.Postgres()
	activityWorker, err := core.NewActivityWorkerClient(core.ActivityAnalysisConfig{
		Enabled:           config.Cfg.ActivityWorker.Enabled,
		Endpoint:          config.Cfg.ActivityWorker.Endpoint,
		BearerToken:       config.Cfg.ActivityWorker.BearerToken,
		Timeout:           time.Duration(config.Cfg.ActivityWorker.TimeoutSeconds) * time.Second,
		ScoreThreshold:    config.Cfg.ActivityWorker.ScoreThreshold,
		DecisionCacheSize: config.Cfg.ActivityWorker.DecisionCacheSize,
	})
	if err != nil {
		panic(err)
	}
	runtime := core.NewRecommendationRuntime(
		core.WithSkillDirectory(skillDir),
		core.WithProductionProviders(model.NewArticleRepo(db), model.NewPoolRepo(db)),
		core.WithActivityAnalyzer(activityWorker),
	)
	var evaluation *logic.RecoEvaluationService
	if db != nil {
		evaluation = logic.NewRecoEvaluationService(model.NewRecoEvaluationRepo(db))
	}
	modelName := config.Cfg.Agent.Model
	if modelName == "" {
		modelName = config.Cfg.Ali.TextModel
	}
	gateway, err := trpcmodel.NewGateway(trpcmodel.Options{Name: modelName, BaseURL: config.Cfg.Ali.BaseURL, APIKey: config.Cfg.Ali.APIKey})
	if err != nil {
		panic(err)
	}
	agent, err := recommendagent.NewRecommendAgent(recommendagent.Dependencies{
		Model: gateway, Instruction: recommendprompt.RecommendInstructionV1, MaxToolCalls: config.Cfg.Agent.MaxToolCalls,
	})
	if err != nil {
		panic(err)
	}
	runner, err := runnerfactory.New(runnerfactory.Dependencies{AppName: "sea-recommend", Agent: agent, SessionService: true})
	if err != nil {
		panic(err)
	}
	return New(Dependencies{
		Recommendation: core.NewRecommendationService(runtime),
		Evaluation:     evaluation,
		Runner:         runner,
	})
}
