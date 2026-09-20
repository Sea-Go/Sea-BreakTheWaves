package recommendationv2

import "context"

type ConfigProvider interface {
	ConfigFor(ctx context.Context, scope ConfigScope) (*RecommendConfig, error)
}

type StaticConfigProvider struct {
	tenantChannel map[string]RecommendConfig
	scenario      map[string]RecommendConfig
}

func NewStaticConfigProvider() *StaticConfigProvider {
	return &StaticConfigProvider{
		tenantChannel: make(map[string]RecommendConfig),
		scenario:      make(map[string]RecommendConfig),
	}
}

func (p *StaticConfigProvider) SetTenantChannelConfig(tenantID, channel string, cfg RecommendConfig) {
	if p == nil {
		return
	}
	p.tenantChannel[tenantID+"/"+channel] = cfg
}

func (p *StaticConfigProvider) SetScenarioConfig(scenario string, cfg RecommendConfig) {
	if p == nil {
		return
	}
	p.scenario[scenario] = cfg
}

func (p *StaticConfigProvider) ConfigFor(_ context.Context, scope ConfigScope) (*RecommendConfig, error) {
	if p == nil {
		return nil, nil
	}
	var out *RecommendConfig
	if cfg, ok := p.tenantChannel[scope.TenantID+"/"+scope.Channel]; ok {
		copied := cfg
		out = &copied
	}
	if cfg, ok := p.scenario[scope.Scenario]; ok {
		if out == nil {
			copied := cfg
			out = &copied
		} else {
			merged := MergeConfig(*out, &cfg)
			out = &merged
		}
	}
	return out, nil
}

func DefaultConfig() RecommendConfig {
	path := PathAuto
	recallTopK := 50
	recallTimeout := 80
	rerankTopN := 20
	rerankBudget := 0.01
	rerankTimeout := 300
	maxTokens := 1200
	maxAmount := 0.03
	traceSampleRate := 1.0
	returnSteps := true
	returnExplain := true
	return RecommendConfig{
		PathMode: &path,
		Recall: RecallConfig{
			Sources:        []string{"rule", "content", "cf", "graph", "channel"},
			TopK:           &recallTopK,
			Weights:        map[string]float64{"rule": 0.15, "content": 0.25, "cf": 0.2, "graph": 0.25, "channel": 0.15},
			TimeoutMillis:  &recallTimeout,
			FallbackSource: "rule",
		},
		Rank: RankConfig{
			FeatureWeights: map[string]float64{"profile": 0.35, "freshness": 0.2, "quality": 0.25, "diversity": 0.2},
			ModelVersion:   "weighted-v1",
			ABBucket:       "default",
		},
		Rerank: RerankConfig{
			Model:         "self",
			TopN:          &rerankTopN,
			Budget:        &rerankBudget,
			TimeoutMillis: &rerankTimeout,
		},
		Cost: CostConfig{
			MaxTokens:     &maxTokens,
			MaxAmount:     &maxAmount,
			ModelTier:     "small",
			CacheStrategy: "prompt-cache",
		},
		Obs: ObservabilityConfig{
			TraceSampleRate: &traceSampleRate,
			ReturnSteps:     &returnSteps,
			ReturnExplain:   &returnExplain,
		},
	}
}

func MergeConfig(base RecommendConfig, override *RecommendConfig) RecommendConfig {
	if override == nil {
		return base
	}
	out := base
	if override.PathMode != nil {
		out.PathMode = cloneStringPtr(override.PathMode)
	}
	if override.Recall.TopK != nil {
		out.Recall.TopK = cloneIntPtr(override.Recall.TopK)
	}
	if len(override.Recall.Sources) > 0 {
		out.Recall.Sources = append([]string(nil), override.Recall.Sources...)
	}
	if len(override.Recall.Weights) > 0 {
		out.Recall.Weights = cloneFloatMap(override.Recall.Weights)
	}
	if override.Recall.TimeoutMillis != nil {
		out.Recall.TimeoutMillis = cloneIntPtr(override.Recall.TimeoutMillis)
	}
	if override.Recall.FallbackSource != "" {
		out.Recall.FallbackSource = override.Recall.FallbackSource
	}
	if len(override.Rank.FeatureWeights) > 0 {
		out.Rank.FeatureWeights = cloneFloatMap(override.Rank.FeatureWeights)
	}
	if override.Rank.ModelVersion != "" {
		out.Rank.ModelVersion = override.Rank.ModelVersion
	}
	if override.Rank.ABBucket != "" {
		out.Rank.ABBucket = override.Rank.ABBucket
	}
	if override.Rerank.Model != "" {
		out.Rerank.Model = override.Rerank.Model
	}
	if override.Rerank.TopN != nil {
		out.Rerank.TopN = cloneIntPtr(override.Rerank.TopN)
	}
	if override.Rerank.Budget != nil {
		out.Rerank.Budget = cloneFloatPtr(override.Rerank.Budget)
	}
	if override.Rerank.TimeoutMillis != nil {
		out.Rerank.TimeoutMillis = cloneIntPtr(override.Rerank.TimeoutMillis)
	}
	if override.Cost.MaxTokens != nil {
		out.Cost.MaxTokens = cloneIntPtr(override.Cost.MaxTokens)
	}
	if override.Cost.MaxAmount != nil {
		out.Cost.MaxAmount = cloneFloatPtr(override.Cost.MaxAmount)
	}
	if override.Cost.ModelTier != "" {
		out.Cost.ModelTier = override.Cost.ModelTier
	}
	if override.Cost.CacheStrategy != "" {
		out.Cost.CacheStrategy = override.Cost.CacheStrategy
	}
	if override.Obs.TraceSampleRate != nil {
		out.Obs.TraceSampleRate = cloneFloatPtr(override.Obs.TraceSampleRate)
	}
	if override.Obs.ReturnSteps != nil {
		out.Obs.ReturnSteps = cloneBoolPtr(override.Obs.ReturnSteps)
	}
	if override.Obs.ReturnExplain != nil {
		out.Obs.ReturnExplain = cloneBoolPtr(override.Obs.ReturnExplain)
	}
	return out
}

func EffectiveConfig(cfg RecommendConfig) EffectiveRecommendConfig {
	out := EffectiveRecommendConfig{
		PathMode: derefString(cfg.PathMode, PathHybrid),
		Recall: EffectiveRecallConfig{
			Sources:        append([]string(nil), cfg.Recall.Sources...),
			TopK:           derefInt(cfg.Recall.TopK, 50),
			Weights:        cloneFloatMap(cfg.Recall.Weights),
			TimeoutMillis:  derefInt(cfg.Recall.TimeoutMillis, 80),
			FallbackSource: cfg.Recall.FallbackSource,
		},
		Rank: cfg.Rank,
		Rerank: EffectiveRerankConfig{
			Model:         cfg.Rerank.Model,
			TopN:          derefInt(cfg.Rerank.TopN, 20),
			Budget:        derefFloat(cfg.Rerank.Budget, 0.01),
			TimeoutMillis: derefInt(cfg.Rerank.TimeoutMillis, 300),
		},
		Cost: EffectiveCostConfig{
			MaxTokens:     derefInt(cfg.Cost.MaxTokens, 1200),
			MaxAmount:     derefFloat(cfg.Cost.MaxAmount, 0.03),
			ModelTier:     cfg.Cost.ModelTier,
			CacheStrategy: cfg.Cost.CacheStrategy,
		},
		Obs: EffectiveObservabilityConfig{
			TraceSampleRate: derefFloat(cfg.Obs.TraceSampleRate, 1),
			ReturnSteps:     derefBool(cfg.Obs.ReturnSteps, true),
			ReturnExplain:   derefBool(cfg.Obs.ReturnExplain, true),
		},
	}
	if len(out.Recall.Sources) == 0 {
		out.Recall.Sources = []string{"rule"}
	}
	if out.Recall.FallbackSource == "" {
		out.Recall.FallbackSource = "rule"
	}
	if out.Rerank.Model == "" {
		out.Rerank.Model = "self"
	}
	if out.Cost.ModelTier == "" {
		out.Cost.ModelTier = "small"
	}
	if out.Cost.CacheStrategy == "" {
		out.Cost.CacheStrategy = "prompt-cache"
	}
	return out
}

func cloneFloatMap(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneFloatPtr(in *float64) *float64 {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func derefString(in *string, fallback string) string {
	if in == nil {
		return fallback
	}
	return *in
}

func derefInt(in *int, fallback int) int {
	if in == nil {
		return fallback
	}
	return *in
}

func derefFloat(in *float64, fallback float64) float64 {
	if in == nil {
		return fallback
	}
	return *in
}

func derefBool(in *bool, fallback bool) bool {
	if in == nil {
		return fallback
	}
	return *in
}
