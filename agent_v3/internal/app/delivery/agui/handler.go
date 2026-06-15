package agui

import (
	"net/http"
	"time"

	"agent_v3/internal/agents/travel"
	"agent_v3/internal/config"

	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	serveragui "trpc.group/trpc-go/trpc-agent-go/server/agui"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session/summary"
)

func NewHandler() (http.Handler, func(), error) {
	appName := config.Cfg.Agent.AppName + "travel-planning"

	// 为 summarizer 和 memory extractor 创建一个轻量模型实例。
	summaryModel := travel.NewSummaryModel("summary")

	// 短期记忆：session 服务 + summarizer，自动压缩长对话历史。
	sessSvc := sessioninmemory.NewSessionService(
		sessioninmemory.WithSessionEventLimit(500),
		sessioninmemory.WithSessionTTL(30*time.Minute),
		sessioninmemory.WithSummarizer(summary.NewSummarizer(summaryModel)),
		sessioninmemory.WithAsyncSummaryNum(4),
	)

	// 长期记忆：自动从对话中提取用户旅行偏好、常用出发地、节奏和交通偏好等。
	memSvc := memoryinmemory.NewMemoryService(
		memoryinmemory.WithMemoryLimit(100),
		memoryinmemory.WithExtractor(extractor.NewExtractor(summaryModel)),
		memoryinmemory.WithAsyncMemoryNum(2),
	)

	rn := runner.NewRunner(
		appName,
		travel.TravelPlanningAgent(),
		runner.WithSessionService(sessSvc),
		runner.WithMemoryService(memSvc),
	)

	server, err := serveragui.New(
		rn,
		serveragui.WithPath("/agui"),
		serveragui.WithReasoningContentEnabled(true),
	)
	if err != nil {
		_ = rn.Close()
		return nil, nil, err
	}

	cleanup := func() {
		_ = memSvc.Close()
		_ = sessSvc.Close()
		_ = rn.Close()
	}

	return server.Handler(), cleanup, nil
}
