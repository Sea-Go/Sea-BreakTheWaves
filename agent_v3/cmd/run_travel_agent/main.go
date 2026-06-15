package main

import (
	"context"
	"fmt"
	"os"
	"time"

	agent "agent_v3/internal/agents/travel"
	"agent_v3/internal/config"
	"agent_v3/internal/graph"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	log.SetLevel(log.LevelInfo)

	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "config init failed: %v\n", err)
		os.Exit(1)
	}

	graph.Init()

	travelAgent := agent.TravelPlanningAgent()

	msg := "全国365天的旅游，预算20万，一人行，自驾游，更喜好自然风光。出发城市北京，6月1日开始，节奏均衡。"

	invocation := &agentcore.Invocation{
		Message: model.Message{
			Role:    model.RoleUser,
			Content: msg,
		},
	}

	fmt.Println("Starting agent run...")
	eventCh, err := travelAgent.Run(ctx, invocation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var fullOutput string
	count := 0
	for evt := range eventCh {
		count++
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				fullOutput += choice.Delta.Content
			}
			if choice.Message.Content != "" {
				fullOutput += choice.Message.Content
			}
		}
	}

	fmt.Printf("Received %d events\n", count)
	fmt.Printf("Output length: %d chars\n", len(fullOutput))

	err = os.WriteFile("/Users/edy/Sea-BreakTheWaves/doc/365-agent-final-output.txt", []byte(fullOutput), 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Output written to doc/365-agent-final-output.txt")
}
