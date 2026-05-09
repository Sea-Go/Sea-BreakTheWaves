package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"agent_v2/agent"
	"agent_v2/config"
	"agent_v2/tools"
)

func main() {
	mode := flag.String("mode", "writer", "writer, feedback-backend, or publish-backend")
	userID := flag.String("user", "demo-user", "user id")
	sessionID := flag.String("session", "demo-session", "session id")
	memoryKey := flag.String("memory-key", "default", "strategy memory key")
	planPath := flag.String("plan", "", "path to TravelPlanningAgent JSON output")
	articleID := flag.String("article-id", "", "backend article id for comment feedback")
	articleTitle := flag.String("article-title", "", "backend article title for comment feedback")
	topic := flag.String("topic", "", "override article topic")
	audience := flag.String("audience", "", "override target audience")
	goal := flag.String("goal", "", "override writing goal")
	style := flag.String("style", "", "override writing style")
	manualTypeTag := flag.String("manual-type-tag", "旅游攻略", "backend article manual type tag")
	secondaryTags := flag.String("secondary-tags", "", "comma-separated backend article secondary tags")
	flag.Parse()

	if err := config.Load(resolveConfigPath()); err != nil {
		fatalf("初始化配置失败: %v", err)
	}

	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "writer":
		if strings.TrimSpace(*planPath) == "" {
			fatalf("-plan is required in writer mode")
		}
		plan, err := loadTravelPlanningOutput(*planPath)
		if err != nil {
			fatalf("读取规划 JSON 失败: %v", err)
		}

		out, err := agent.TravelArticleWriterAgentRun(
			*userID,
			*sessionID,
			agent.TravelArticleBrief{
				Topic:          *topic,
				TargetAudience: *audience,
				WritingGoal:    *goal,
				Style:          *style,
			},
			plan,
			*memoryKey,
		)
		if err != nil {
			fatalf("writer mode failed: %v", err)
		}
		printJSON(out)

	case "feedback-backend":
		if strings.TrimSpace(*articleID) == "" {
			fatalf("-article-id is required in feedback-backend mode")
		}
		backendClient := toolsBackendClient()
		out, err := agent.CommentFeedbackAgentRunFromBackend(
			context.Background(),
			*userID,
			*sessionID,
			*articleID,
			*articleTitle,
			*memoryKey,
			backendClient,
		)
		if err != nil {
			fatalf("feedback-backend mode failed: %v", err)
		}
		printJSON(out)

	case "publish-backend":
		if strings.TrimSpace(*planPath) == "" {
			fatalf("-plan is required in publish-backend mode")
		}
		plan, err := loadTravelPlanningOutput(*planPath)
		if err != nil {
			fatalf("读取规划 JSON 失败: %v", err)
		}
		backendClient := toolsBackendClient()
		resp, articleOut, err := agent.TravelArticleWriterAgentRunAndPublish(
			context.Background(),
			*userID,
			*sessionID,
			agent.TravelArticleBrief{
				Topic:          *topic,
				TargetAudience: *audience,
				WritingGoal:    *goal,
				Style:          *style,
			},
			plan,
			*memoryKey,
			backendClient,
			*manualTypeTag,
			splitCSV(*secondaryTags),
		)
		if err != nil {
			fatalf("publish-backend mode failed: %v", err)
		}
		printJSON(map[string]any{
			"backend": resp,
			"article": articleOut,
		})

	default:
		fatalf("unsupported mode: %s", *mode)
	}
}

func toolsBackendClient() *tools.BackendClient {
	return tools.NewBackendClientFromConfig(config.Cfg.Backend)
}

func resolveConfigPath() string {
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}
	return "agent_v2/config.yaml"
}

func loadTravelPlanningOutput(path string) (agent.TravelPlanningOutput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agent.TravelPlanningOutput{}, err
	}
	var out agent.TravelPlanningOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return agent.TravelPlanningOutput{}, err
	}
	return out, nil
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fatalf("marshal output failed: %v", err)
	}
	fmt.Println(string(data))
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
