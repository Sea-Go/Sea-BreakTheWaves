// Package agent orchestrator.go — OrchestratorAgent 双路径编排（Task 12.1）。
//
// 该文件实现 OrchestratorAgent：作为多 Agent 架构的编排核心（GraphAgent 风格），
// 基于 intent.complexity + RecommendConfig.PathMode 决策 fast/slow/hybrid 三路径，
// 编排 8 个子 Agent（Intent/RecallPlanner/Graph/Rerank/Quality/Explain/Profile/Channel）
// 与传统 Recaller/Ranker 协作，融合快慢路径候选。
//
// 职责：
//   - 路由决策：fast（无 LLM，0 token，P99<100ms）/ slow（6 Agent 串行）/ hybrid（fast Top-50 → slow Rerank Top-20 → Top-10）
//   - fast 路径：HybridRecaller + Ranker，零 LLM token
//   - slow 路径：IntentAgent → RecallPlannerAgent → GraphAgent → RerankAgent → QualityAgent → ExplainAgent
//   - hybrid 路径：fast 出 Top-50 → slow Rerank Top-20 → 返回 Top-10
//   - 实现 domain.Orchestrator interface（Recommend）与 domain.Agent interface（Run + Name）
//   - 失败兜底：主路径失败且 Config.FallbackEnabled 时降级到 fast
//
// 二开扩展点：
//   - 替换 Recaller/Ranker：注入自研召回/排序实现
//   - 替换任意子 Agent：通过 AgentBundle 注入
//   - 调整路由阈值：修改 route 方法的 complexity 分段
//   - 调整 hybrid 截断：修改 hybridFastTopN/hybridRerankTopN 常量
//
// 不直接 import trpc-agent-go / neo4j / milvus，所有依赖通过 interface 注入。
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"sea/internal/domain"
)

// ----------------------------------------------------------------------------
// 常量
// ----------------------------------------------------------------------------

// 路径决策与成本相关常量。
const (
	// orchestratorAgentName Agent 名称。
	orchestratorAgentName = "orchestrator"
	// defaultOrchTopK 默认返回数量（req.TopK<=0 时使用）。
	defaultOrchTopK = 10
	// hybridFastTopN hybrid 路径 fast 阶段截断数量。
	hybridFastTopN = 50
	// hybridRerankTopN hybrid 路径 slow rerank 阶段截断数量。
	hybridRerankTopN = 20
	// mockTokensInPerAgent 每个 Agent 调用的 mock 输入 token 数（LLM 为 interface，无法获取真实值）。
	mockTokensInPerAgent = 100
	// mockTokensOutPerAgent 每个 Agent 调用的 mock 输出 token 数。
	mockTokensOutPerAgent = 50
	// mockCostPerAgentYuan 每个 Agent 调用的 mock 费用（元）。
	mockCostPerAgentYuan = 0.001
)

// ----------------------------------------------------------------------------
// AgentBundle
// ----------------------------------------------------------------------------

// AgentBundle 持有 8 个子 Agent 引用，供 OrchestratorAgent 编排。
//
// 字段语义：
//   - Intent：意图理解 Agent（slow 路径首步，输出 intent）
//   - RecallPlanner：召回规划 Agent（输出 recall_plan）
//   - Graph：图谱推理 Agent（输出 graph_knowledge/graph_trace）
//   - Rerank：重排 Agent（输入 candidates，输出 rerank_items）
//   - Quality：质量评判 Agent（输入 rerank_items，输出 quality_scores/quality_filtered）
//   - Explain：解释生成 Agent（输出 explanation）
//   - Profile：画像更新 Agent（异步，输入 events）
//   - Channel：频道路由 Agent（输出 channel_route/channel）
//
// 二开扩展点：通过 AgentBundle 注入自研 Agent 实现替换任意子 Agent。
type AgentBundle struct {
	// Intent 意图理解 Agent（slow 路径用）。
	Intent domain.Agent
	// RecallPlanner 召回规划 Agent。
	RecallPlanner domain.Agent
	// Graph 图谱推理 Agent。
	Graph domain.Agent
	// Rerank 重排 Agent。
	Rerank domain.Agent
	// Quality 质量评判 Agent。
	Quality domain.Agent
	// Explain 解释生成 Agent。
	Explain domain.Agent
	// Profile 画像更新 Agent。
	Profile domain.Agent
	// Channel 频道路由 Agent。
	Channel domain.Agent
}

// ----------------------------------------------------------------------------
// OrchestratorAgent
// ----------------------------------------------------------------------------

// OrchestratorAgent 编排 Agent，基于 GraphAgent 风格实现双路径（fast/slow/hybrid）。
//
// 职责：
//   - 接收 RecommendRequest，路由决策 fast/slow/hybrid
//   - fast 分支：调 HybridRecaller + Ranker（无 LLM，0 token）
//   - slow 分支：IntentAgent → RecallPlannerAgent → GraphAgent → RerankAgent → QualityAgent → ExplainAgent
//   - hybrid 分支：fast 出 Top-50 → slow Rerank Top-20 → 返回 Top-10
//   - 实现 domain.Orchestrator interface（Recommend 方法）
//   - 实现 domain.Agent interface（Run + Name）
//
// 二开扩展点：替换 Recaller/Ranker/各 Agent（均通过 interface 注入）。
type OrchestratorAgent struct {
	// recaller 混合召回器（fast 路径用）。
	recaller domain.Recaller
	// ranker 传统排序器（fast 路径用）。
	ranker domain.Ranker
	// intentAgent 意图理解 Agent（slow 路径用）。
	intentAgent domain.Agent
	// recallPlannerAgent 召回规划 Agent。
	recallPlannerAgent domain.Agent
	// graphAgent 图谱推理 Agent。
	graphAgent domain.Agent
	// rerankAgent 重排 Agent。
	rerankAgent domain.Agent
	// qualityAgent 质量评判 Agent。
	qualityAgent domain.Agent
	// explainAgent 解释生成 Agent。
	explainAgent domain.Agent
	// profileAgent 画像更新 Agent。
	profileAgent domain.Agent
	// channelAgent 频道路由 Agent。
	channelAgent domain.Agent
	// opts Agent 选项。
	opts domain.AgentOptions
}

// 编译期断言：OrchestratorAgent 同时实现 domain.Orchestrator 与 domain.Agent。
var (
	_ domain.Orchestrator = (*OrchestratorAgent)(nil)
	_ domain.Agent        = (*OrchestratorAgent)(nil)
)

// NewOrchestratorAgent 构造 OrchestratorAgent。
// recaller 混合召回器（fast 路径用）；ranker 传统排序器（fast 路径用）；
// agents AgentBundle 持有 8 个子 Agent 引用（可为 nil，nil 时 slow/hybrid 降级）；
// opts Agent 构造选项。返回 *OrchestratorAgent。
func NewOrchestratorAgent(recaller domain.Recaller, ranker domain.Ranker, agents *AgentBundle, opts domain.AgentOptions) *OrchestratorAgent {
	a := &OrchestratorAgent{
		recaller: recaller,
		ranker:   ranker,
		opts:     opts,
	}
	if agents != nil {
		a.intentAgent = agents.Intent
		a.recallPlannerAgent = agents.RecallPlanner
		a.graphAgent = agents.Graph
		a.rerankAgent = agents.Rerank
		a.qualityAgent = agents.Quality
		a.explainAgent = agents.Explain
		a.profileAgent = agents.Profile
		a.channelAgent = agents.Channel
	}
	return a
}

// Name 返回 Agent 名称 "orchestrator"。
func (a *OrchestratorAgent) Name() string { return orchestratorAgentName }

// Recommend 执行推荐主流程，实现 domain.Orchestrator interface。
//
// 流程：
//  1. 调用 route 决策路径（fast/slow/hybrid）
//  2. 按路径调用 runFast/runSlow/runHybrid
//  3. 失败兜底：若主路径失败且 req.Config.FallbackEnabled，降级到 fast
//  4. 调用 finish 组装响应（含 PathTaken/Cost/GraphTrace/QualityScores/CFScores/RerankScores/Explanation）
func (a *OrchestratorAgent) Recommend(ctx context.Context, req domain.RecommendRequest) (domain.RecommendResponse, error) {
	if a == nil {
		return domain.RecommendResponse{}, fmt.Errorf("orchestrator: nil agent")
	}

	// 1. 路由决策。
	path, intent, routeCost, _ := a.route(ctx, req)

	// 2. 按路径执行。
	var result pathResult
	var runErr error
	switch path {
	case "fast":
		result, runErr = a.runFast(ctx, req)
	case "slow":
		result, runErr = a.runSlow(ctx, req)
	case "hybrid":
		result, runErr = a.runHybrid(ctx, req)
	default:
		path = "hybrid"
		result, runErr = a.runHybrid(ctx, req)
	}

	// 3. 失败兜底：主路径失败且 FallbackEnabled 时降级到 fast。
	if runErr != nil && req.Config.FallbackEnabled {
		if fbResult, fbErr := a.runFast(ctx, req); fbErr == nil {
			result = fbResult
			path = "fast"
			runErr = nil
		}
	}
	if runErr != nil {
		return domain.RecommendResponse{}, runErr
	}

	// 4. 累加成本并组装响应。
	totalCost := addCost(routeCost, result.cost)
	state := result.state
	if state == nil {
		state = map[string]any{}
	}
	state["path_decision"] = path
	state["cost"] = totalCost
	if intent.Label != "" || intent.Complexity > 0 {
		state["intent"] = intent
	}
	return a.finish(ctx, req, result.candidates, path, totalCost, state)
}

// Run 执行编排 Agent，实现 domain.Agent interface。
//
// 流程：
//  1. 从 input.State["recommend_request"] 读取 RecommendRequest
//  2. 调用 Recommend
//  3. 输出 State 写入 recommend_response；Result 写入 response；
//     Trace 追加 "orchestrator.route"/"orchestrator.{path}"/"orchestrator.finish"
func (a *OrchestratorAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	if a == nil {
		return domain.AgentOutput{}, fmt.Errorf("orchestrator: nil agent")
	}
	req, ok := extractRecommendRequest(input.State)
	if !ok {
		return domain.AgentOutput{
			State: map[string]any{"recommend_error": "state missing recommend_request"},
			Trace: []string{"orchestrator.error"},
		}, fmt.Errorf("orchestrator: state missing recommend_request")
	}
	resp, err := a.Recommend(ctx, req)
	if err != nil {
		return domain.AgentOutput{
			State: map[string]any{"recommend_error": err.Error()},
			Trace: []string{"orchestrator.route", "orchestrator.error"},
		}, err
	}
	state := copyState(input.State)
	state["recommend_response"] = resp
	state["path_decision"] = resp.PathTaken
	return domain.AgentOutput{
		State:  state,
		Result: resp,
		Trace:  []string{"orchestrator.route", "orchestrator." + resp.PathTaken, "orchestrator.finish"},
	}, nil
}

// ----------------------------------------------------------------------------
// 路由决策
// ----------------------------------------------------------------------------

// route 基于 intent.complexity + req.PathMode 决策路径。
//
// 决策规则：
//   - 若 req.PathMode 为 fast/slow/hybrid，直接使用
//   - 否则（空或 auto）运行 IntentAgent 获取 complexity：
//     simple<0.4→fast, 0.4≤medium<0.7→hybrid, complex≥0.7→slow
//   - 默认 hybrid（IntentAgent 不可用或失败时）
//
// 返回 (路径, 意图, 路由阶段成本, error)。
func (a *OrchestratorAgent) route(ctx context.Context, req domain.RecommendRequest) (string, Intent, domain.CostReport, error) {
	// PathMode 显式指定时直接使用。
	switch req.PathMode {
	case "fast", "slow", "hybrid":
		return req.PathMode, Intent{}, domain.CostReport{}, nil
	}
	// PathMode 空或 auto：运行 IntentAgent 获取复杂度。
	if a.intentAgent == nil {
		return "hybrid", Intent{}, domain.CostReport{}, nil
	}
	out, err := a.intentAgent.Run(ctx, domain.AgentInput{
		UserID: req.UserKey.UserID,
		State:  map[string]any{"recommend_request": req},
	})
	if err != nil {
		return "hybrid", Intent{}, domain.CostReport{}, nil
	}
	intent, ok := readIntent(out.State)
	if !ok {
		return "hybrid", Intent{}, domain.CostReport{}, nil
	}
	cost := mockAgentCost(1) // IntentAgent LLM 调用
	switch {
	case intent.Complexity < 0.4:
		return "fast", intent, cost, nil
	case intent.Complexity < 0.7:
		return "hybrid", intent, cost, nil
	default:
		return "slow", intent, cost, nil
	}
}

// ----------------------------------------------------------------------------
// fast 路径
// ----------------------------------------------------------------------------

// runFast 执行 fast 路径：调 recaller.Recall + ranker.Rank（无 LLM，0 token）。
//
// 流程：
//  1. 调 recaller.Recall 召回候选（TopK*3 过召）
//  2. 调 ranker.Rank 排序（ranker 为 nil 时跳过）
//  3. 截断到 req.TopK
//  4. 成本：所有 token = 0
func (a *OrchestratorAgent) runFast(ctx context.Context, req domain.RecommendRequest) (pathResult, error) {
	if a.recaller == nil {
		return pathResult{}, fmt.Errorf("orchestrator fast: recaller nil")
	}
	topK := req.TopK
	if topK <= 0 {
		topK = defaultOrchTopK
	}
	candidates, err := a.fastRecall(ctx, req, topK*3)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator fast: %w", err)
	}
	candidates = truncateCandidates(candidates, topK)
	return pathResult{
		candidates: candidates,
		cost:       domain.CostReport{}, // fast 路径 0 token
		state:      map[string]any{"fast_candidates": candidates},
	}, nil
}

// fastRecall 执行 recaller.Recall + ranker.Rank，返回未截断的候选列表。
// topK 为召回数量（过召用，通常为返回数量的 3 倍）。
func (a *OrchestratorAgent) fastRecall(ctx context.Context, req domain.RecommendRequest, topK int) ([]domain.Candidate, error) {
	recallReq := domain.RecallRequest{
		UserKey:   req.UserKey,
		Channel:   req.Channel,
		TagFilter: req.TagFilter,
		TopK:      topK,
	}
	recallRes, err := a.recaller.Recall(ctx, recallReq)
	if err != nil {
		return nil, fmt.Errorf("recall: %w", err)
	}
	candidates := recallRes.Candidates
	if a.ranker != nil && len(candidates) > 0 {
		rankCtx := domain.RankContext{
			Channel:    req.Channel,
			Candidates: candidates,
		}
		rankRes, err := a.ranker.Rank(ctx, rankCtx)
		if err != nil {
			return nil, fmt.Errorf("rank: %w", err)
		}
		candidates = rankRes.Candidates
	}
	return candidates, nil
}

// ----------------------------------------------------------------------------
// slow 路径
// ----------------------------------------------------------------------------

// runSlow 执行 slow 路径：串行调用 6 个 Agent。
//
// 流程：IntentAgent → RecallPlannerAgent → GraphAgent →（recaller.Recall 取候选）→
// RerankAgent → QualityAgent → ExplainAgent
//
// 任一 Agent 失败则返回 error（供兜底机制降级到 fast）。
// 成本：累加各 Agent 的 mock token 消耗。
func (a *OrchestratorAgent) runSlow(ctx context.Context, req domain.RecommendRequest) (pathResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultOrchTopK
	}
	state := map[string]any{
		"recommend_request": req,
		"config":            req.Config,
		"query":             "",
		"topk":              topK,
	}
	userID := req.UserKey.UserID
	agentCount := 0

	// 1. IntentAgent → State["intent"]
	var err error
	state, err = a.runAgent(ctx, a.intentAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: intent: %w", err)
	}
	agentCount++

	// 2. RecallPlannerAgent → State["recall_plan"]
	state, err = a.runAgent(ctx, a.recallPlannerAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: recall_planner: %w", err)
	}
	agentCount++

	// 3. GraphAgent → State["graph_knowledge"]/State["graph_trace"]
	state, err = a.runAgent(ctx, a.graphAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: graph: %w", err)
	}
	agentCount++

	// 中间步：调 recaller.Recall 获取候选（非 Agent，用召回规划结果引导）。
	candidates := a.recallForSlow(ctx, req, state)
	state["candidates"] = candidates
	state["slow_candidates"] = candidates

	// 4. RerankAgent → State["rerank_items"]
	state, err = a.runAgent(ctx, a.rerankAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: rerank: %w", err)
	}
	agentCount++

	// 5. QualityAgent → State["quality_scores"]/State["quality_filtered"]
	state, err = a.runAgent(ctx, a.qualityAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: quality: %w", err)
	}
	agentCount++

	// 6. ExplainAgent → State["explanation"]
	state, err = a.runAgent(ctx, a.explainAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator slow: explain: %w", err)
	}
	agentCount++

	// 提取最终候选：优先 quality_filtered → rerank_items → candidates。
	final := extractFinalCandidates(state)
	if len(final) == 0 {
		final = candidates
	}
	final = truncateCandidates(final, topK)
	state["merged_candidates"] = final

	cost := mockAgentCost(agentCount)
	state["cost"] = cost
	return pathResult{candidates: final, cost: cost, state: state}, nil
}

// recallForSlow 为 slow 路径获取候选：调 recaller.Recall，失败返回 nil（不阻断）。
// state 用于读取 intent（若存在）引导召回。
func (a *OrchestratorAgent) recallForSlow(ctx context.Context, req domain.RecommendRequest, state map[string]any) []domain.Candidate {
	if a.recaller == nil {
		return nil
	}
	topK := req.TopK
	if topK <= 0 {
		topK = defaultOrchTopK
	}
	recallReq := domain.RecallRequest{
		UserKey:   req.UserKey,
		Channel:   req.Channel,
		TagFilter: req.TagFilter,
		TopK:      topK * 3,
	}
	if intent, ok := readIntent(state); ok {
		di := intentToDomain(intent)
		recallReq.Intent = &di
	}
	res, err := a.recaller.Recall(ctx, recallReq)
	if err != nil {
		return nil
	}
	return res.Candidates
}

// ----------------------------------------------------------------------------
// hybrid 路径
// ----------------------------------------------------------------------------

// runHybrid 执行 hybrid 路径：fast 出 Top-50 → slow Rerank Top-20 → 返回 Top-10。
//
// 流程：
//  1. 调 runFast（TopK=hybridFastTopN）获取 Top-50 候选
//  2. 调 RerankAgent 重排 → 截断到 Top-20
//  3. 调 QualityAgent 过滤
//  4. 截断到 req.TopK（Top-10）
//
// 成本：fast 部分 0 + slow 部分（Rerank+Quality）累加。
func (a *OrchestratorAgent) runHybrid(ctx context.Context, req domain.RecommendRequest) (pathResult, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultOrchTopK
	}

	// 1. fast 出 Top-50。
	fastReq := req
	fastReq.TopK = hybridFastTopN
	fastResult, err := a.runFast(ctx, fastReq)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator hybrid: fast: %w", err)
	}
	fastCands := fastResult.candidates
	state := mergeState(fastResult.state, map[string]any{
		"candidates":        fastCands,
		"fast_candidates":   fastCands,
		"merged_candidates": fastCands,
		"recommend_request": req,
		"config":            req.Config,
	})
	userID := req.UserKey.UserID
	agentCount := 0

	// 2. slow Rerank → Top-20。
	state, err = a.runAgent(ctx, a.rerankAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator hybrid: rerank: %w", err)
	}
	agentCount++
	rerankItems := candidatesFromState(state, "rerank_items")
	if len(rerankItems) == 0 {
		rerankItems = fastCands
	}
	rerankItems = truncateCandidates(rerankItems, hybridRerankTopN)
	state["rerank_items"] = rerankItems

	// 3. Quality 过滤。
	state, err = a.runAgent(ctx, a.qualityAgent, userID, state)
	if err != nil {
		return pathResult{}, fmt.Errorf("orchestrator hybrid: quality: %w", err)
	}
	agentCount++

	// 4. 提取最终候选 → Top-10。
	final := extractFinalCandidates(state)
	if len(final) == 0 {
		final = rerankItems
	}
	final = truncateCandidates(final, topK)
	state["merged_candidates"] = final

	cost := addCost(fastResult.cost, mockAgentCost(agentCount))
	state["cost"] = cost
	return pathResult{candidates: final, cost: cost, state: state}, nil
}

// ----------------------------------------------------------------------------
// finish 组装响应
// ----------------------------------------------------------------------------

// finish 组装 RecommendResponse。
//
// 从 state 提取 quality_scores/explanation/graph_knowledge 等字段，
// 截断候选到 req.TopK，填充 PathTaken/Cost/GraphTrace/QualityScores/CFScores/RerankScores。
func (a *OrchestratorAgent) finish(_ context.Context, req domain.RecommendRequest, candidates []domain.Candidate, path string, cost domain.CostReport, state map[string]any) (domain.RecommendResponse, error) {
	topK := req.TopK
	if topK <= 0 {
		topK = defaultOrchTopK
	}
	candidates = truncateCandidates(candidates, topK)

	// 质量评分：从 State["quality_scores"]（map[string]float64）转换为 map[string]ArticleQuality。
	qualityScores := map[string]domain.ArticleQuality{}
	if scores := floatMapFromState(state, "quality_scores"); scores != nil {
		for id, s := range scores {
			qualityScores[id] = domain.ArticleQuality{ArticleID: id, Overall: s}
		}
	}

	// 重排分数：从最终候选 Score 字段构建。
	rerankScores := make(map[string]float64, len(candidates))
	for _, c := range candidates {
		rerankScores[c.ArticleID] = c.Score
	}

	// CF 分数：从候选 Scores["cf_score"] 构建。
	cfScores := make(map[string]float64)
	for _, c := range candidates {
		if v, ok := c.Scores["cf_score"]; ok {
			cfScores[c.ArticleID] = v
		}
	}

	// 解释：仅 req.Explain 时填充。
	explanation := ""
	if req.Explain {
		if s, ok := state["explanation"].(string); ok {
			explanation = s
		}
	}

	// 图谱 trace：从 State["graph_knowledge"] 构建（slow 路径有，fast 路径无）。
	var graphTrace *domain.GraphTrace
	if gt := buildGraphTrace(state); gt != nil {
		graphTrace = gt
	}

	return domain.RecommendResponse{
		Candidates:    candidates,
		Cost:          cost,
		Config:        req.Config,
		Channel:       req.Channel,
		QualityScores: qualityScores,
		CFScores:      cfScores,
		RerankScores:  rerankScores,
		PathTaken:     path,
		GraphTrace:    graphTrace,
		Explain:       explanation,
	}, nil
}

// ----------------------------------------------------------------------------
// 候选融合与去重
// ----------------------------------------------------------------------------

// mergeCandidates 融合两个候选列表，按 ArticleID 去重，分数相加。
//
// 语义：相同 ArticleID 的候选合并为一个，Score 与 Scores map 中的特征值相加。
// 首次出现的候选保留 Source 字段。
func mergeCandidates(fast, slow []domain.Candidate) []domain.Candidate {
	merged := make([]domain.Candidate, 0, len(fast)+len(slow))
	idx := map[string]int{}
	addOne := func(c domain.Candidate) {
		if i, ok := idx[c.ArticleID]; ok {
			merged[i] = addCandidateScore(merged[i], c)
			return
		}
		idx[c.ArticleID] = len(merged)
		merged = append(merged, c)
	}
	for _, c := range fast {
		addOne(c)
	}
	for _, c := range slow {
		addOne(c)
	}
	return merged
}

// addCandidateScore 合并两个候选的分数（Score 相加，Scores map 特征值相加）。
func addCandidateScore(a, b domain.Candidate) domain.Candidate {
	a.Score += b.Score
	if a.Scores == nil {
		a.Scores = map[string]float64{}
	}
	for k, v := range b.Scores {
		a.Scores[k] += v
	}
	return a
}

// dedupCandidates 按 ArticleID 去重，保留首次出现的候选。
func dedupCandidates(candidates []domain.Candidate) []domain.Candidate {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]domain.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if _, ok := seen[c.ArticleID]; ok {
			continue
		}
		seen[c.ArticleID] = struct{}{}
		out = append(out, c)
	}
	return out
}

// ----------------------------------------------------------------------------
// 辅助函数
// ----------------------------------------------------------------------------

// pathResult 路径执行结果（内部传递用）。
type pathResult struct {
	candidates []domain.Candidate
	cost       domain.CostReport
	state      map[string]any
}

// runAgent 调用单个 Agent 并合并 State。
// agent 为 nil 时直接返回原 state（不报错）。
// 返回合并后的 state 与 error。
func (a *OrchestratorAgent) runAgent(ctx context.Context, agent domain.Agent, userID string, state map[string]any) (map[string]any, error) {
	if agent == nil {
		return state, nil
	}
	out, err := agent.Run(ctx, domain.AgentInput{UserID: userID, State: state})
	if err != nil {
		return state, err
	}
	return mergeState(state, out.State), nil
}

// mergeState 合并两个 state map（overlay 覆盖 base 同名键），返回新 map。
func mergeState(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay)+2)
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// truncateCandidates 截断候选列表到前 n 个（n<=0 或长度不足时原样返回）。
func truncateCandidates(candidates []domain.Candidate, n int) []domain.Candidate {
	if n <= 0 || len(candidates) <= n {
		return candidates
	}
	out := make([]domain.Candidate, n)
	copy(out, candidates[:n])
	return out
}

// extractFinalCandidates 从 state 提取最终候选，优先级：
// quality_filtered → rerank_items → candidates。
func extractFinalCandidates(state map[string]any) []domain.Candidate {
	if c := candidatesFromState(state, "quality_filtered"); len(c) > 0 {
		return c
	}
	if c := candidatesFromState(state, "rerank_items"); len(c) > 0 {
		return c
	}
	return candidatesFromState(state, "candidates")
}

// candidatesFromState 从 state[key] 提取 []domain.Candidate。
// 支持 []domain.Candidate 与 []any（JSON 反序列化场景）两种形态。
func candidatesFromState(state map[string]any, key string) []domain.Candidate {
	if state == nil {
		return nil
	}
	v, ok := state[key]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case []domain.Candidate:
		return vv
	case []any:
		if len(vv) == 0 {
			return nil
		}
		b, err := json.Marshal(vv)
		if err != nil {
			return nil
		}
		var cands []domain.Candidate
		if err := json.Unmarshal(b, &cands); err != nil {
			return nil
		}
		return cands
	}
	return nil
}

// floatMapFromState 从 state[key] 提取 map[string]float64。
// 支持 map[string]float64 与 map[string]any 两种形态。
func floatMapFromState(state map[string]any, key string) map[string]float64 {
	if state == nil {
		return nil
	}
	v, ok := state[key]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case map[string]float64:
		return vv
	case map[string]any:
		out := make(map[string]float64, len(vv))
		for k, val := range vv {
			if f, ok := val.(float64); ok {
				out[k] = f
			}
		}
		return out
	}
	return nil
}

// extractRecommendRequest 从 state["recommend_request"] 提取 RecommendRequest。
// 支持 domain.RecommendRequest 与 *domain.RecommendRequest 两种形态。
func extractRecommendRequest(state map[string]any) (domain.RecommendRequest, bool) {
	if state == nil {
		return domain.RecommendRequest{}, false
	}
	v, ok := state["recommend_request"]
	if !ok || v == nil {
		return domain.RecommendRequest{}, false
	}
	switch vv := v.(type) {
	case domain.RecommendRequest:
		return vv, true
	case *domain.RecommendRequest:
		if vv != nil {
			return *vv, true
		}
	}
	return domain.RecommendRequest{}, false
}

// intentToDomain 将 agent 包内 Intent（Complexity 为 float64）转为 domain.Intent（Complexity 为 string）。
// 转换规则：<0.4→simple, <0.7→medium, ≥0.7→complex。
func intentToDomain(in Intent) domain.Intent {
	var complexity string
	switch {
	case in.Complexity < 0.4:
		complexity = "simple"
	case in.Complexity < 0.7:
		complexity = "medium"
	default:
		complexity = "complex"
	}
	return domain.Intent{
		Label:      in.Label,
		Confidence: in.Confidence,
		Complexity: complexity,
		Entities:   in.Entities,
		TimeIntent: in.TimeIntent,
	}
}

// buildGraphTrace 从 state["graph_knowledge"] 构建 *domain.GraphTrace。
// 无 graph_knowledge 或 Cypher 为空时返回 nil。
func buildGraphTrace(state map[string]any) *domain.GraphTrace {
	gk := extractGraphKnowledgeFromState(state)
	if gk.Cypher == "" {
		return nil
	}
	return &domain.GraphTrace{
		Cypher:  gk.Cypher,
		Results: len(gk.Articles),
	}
}

// extractGraphKnowledgeFromState 从 state["graph_knowledge"] 提取 domain.GraphKnowledge。
// 支持 domain.GraphKnowledge、*domain.GraphKnowledge 与 map[string]any 多种形态。
func extractGraphKnowledgeFromState(state map[string]any) domain.GraphKnowledge {
	if state == nil {
		return domain.GraphKnowledge{}
	}
	v, ok := state["graph_knowledge"]
	if !ok || v == nil {
		return domain.GraphKnowledge{}
	}
	switch vv := v.(type) {
	case domain.GraphKnowledge:
		return vv
	case *domain.GraphKnowledge:
		if vv != nil {
			return *vv
		}
		return domain.GraphKnowledge{}
	case map[string]any:
		b, err := json.Marshal(vv)
		if err != nil {
			return domain.GraphKnowledge{}
		}
		var gk domain.GraphKnowledge
		if err := json.Unmarshal(b, &gk); err != nil {
			return domain.GraphKnowledge{}
		}
		return gk
	}
	return domain.GraphKnowledge{}
}

// mockAgentCost 按 Agent 调用次数生成 mock 成本报告。
// fast 路径 agentCount=0 → 0 token；slow/hybrid 路径按实际调用次数累加。
func mockAgentCost(agentCount int) domain.CostReport {
	return domain.CostReport{
		TokensIn:      agentCount * mockTokensInPerAgent,
		TokensOut:     agentCount * mockTokensOutPerAgent,
		LLMCalls:      agentCount,
		EstimatedCost: float64(agentCount) * mockCostPerAgentYuan,
	}
}

// addCost 累加两个成本报告。
func addCost(a, b domain.CostReport) domain.CostReport {
	return domain.CostReport{
		TokensIn:      a.TokensIn + b.TokensIn,
		TokensOut:     a.TokensOut + b.TokensOut,
		CachedTokens:  a.CachedTokens + b.CachedTokens,
		LLMCalls:      a.LLMCalls + b.LLMCalls,
		EstimatedCost: a.EstimatedCost + b.EstimatedCost,
	}
}
