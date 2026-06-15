package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"agent_v3/internal/config"
	"agent_v3/internal/graph"
	workflowtrace "agent_v3/internal/workflow/trace"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

type NamedReviewResult struct {
	AgentName string
	Review    graph.ReviewInput
}

func RunPOIReview(ctx context.Context, subgraph *graph.DaySubgraph, poi graph.POINode) *graph.ReviewInput {
	agent := ConstraintReviewAgent("poi")
	prompt := fmt.Sprintf(`请审查以下 POI 的约束合规性：

POI: %s
类型: %s
坐标: (%.6f, %.6f)
城市: %s
费用: %.2f
已验证来源: %s
是否雨天备选: %v

请加载 review-poi skill，并只输出 JSON 审查结果。`,
		poi.Name,
		poi.Type,
		poi.Lat,
		poi.Lng,
		poi.City,
		poi.EstimatedCost,
		poi.VerifiedBy,
		poi.IsRainyDayBackup,
	)

	output, err := runReviewAgentStandalone(ctx, agent, "review-poi-"+poi.ID, prompt)
	if err != nil || output == "" {
		return nil
	}
	return parseReviewOutput(output)
}

func RunDayContentReviews(ctx context.Context, reviewAgents []NamedAgent, subgraph *graph.DaySubgraph) []NamedReviewResult {
	dayData, _ := json.Marshal(subgraph)
	prompt := fmt.Sprintf(`请审查以下旅行日的规划质量：

日期: %s
主题: %s
区域: %s
POI 数量: %d

完整子图数据:
%s

请加载对应的 review skill，并只输出 JSON 审查结果。`,
		subgraph.Day.Date,
		subgraph.Day.Theme,
		subgraph.Day.PrimaryArea,
		len(subgraph.POIs),
		string(dayData),
	)

	var wg sync.WaitGroup
	results := make(chan NamedReviewResult, len(reviewAgents))

	for _, ra := range reviewAgents {
		wg.Add(1)
		go func(name string, ag agentcore.Agent) {
			defer wg.Done()
			output, err := runReviewAgentStandalone(ctx, ag, fmt.Sprintf("review-%s-%s", name, subgraph.Day.Date), prompt)
			if err != nil || output == "" {
				return
			}
			if r := parseReviewOutput(output); r != nil {
				results <- NamedReviewResult{
					AgentName: workflowtrace.PublicReviewAgentLabel(name),
					Review:    *r,
				}
			}
		}(ra.Name, ra.Agent)
	}

	wg.Wait()
	close(results)

	var reviews []NamedReviewResult
	for r := range results {
		reviews = append(reviews, r)
	}
	return reviews
}

func RunWeekReview(ctx context.Context, weekID string) *graph.ReviewInput {
	agent := ConstraintReviewAgent("week")
	prompt := fmt.Sprintf(`请审查 Week 节点 %s 的约束合规性，包括休息日底线、转移日上限、高强度日上限和 POI 密度。请加载 review-week skill，并只输出 JSON 审查结果。`, weekID)

	output, err := runReviewAgentStandalone(ctx, agent, "review-week-"+weekID, prompt)
	if err != nil || output == "" {
		return nil
	}
	return parseReviewOutput(output)
}

func runReviewAgentStandalone(ctx context.Context, ag agentcore.Agent, sessionID, prompt string) (string, error) {
	appName := config.Cfg.Agent.AppName + "review-standalone"

	rn := runner.NewRunner(appName, ag)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "review-system", sessionID, model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", err
	}

	var result strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				result.WriteString(choice.Delta.Content)
			}
			if choice.Message.Content != "" && result.Len() == 0 {
				result.WriteString(choice.Message.Content)
			}
		}
	}
	return result.String(), nil
}

func parseReviewOutput(output string) *graph.ReviewInput {
	cleaned := extractJSONBlock(output)
	if cleaned == "" {
		return nil
	}
	var r graph.ReviewInput
	if err := json.Unmarshal([]byte(cleaned), &r); err != nil {
		return nil
	}
	if r.Level == "" || r.Dimension == "" {
		return nil
	}
	return &r
}

func extractJSONBlock(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}
