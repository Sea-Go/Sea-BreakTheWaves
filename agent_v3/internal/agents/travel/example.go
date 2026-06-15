package travel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agent_v3/internal/config"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

func ExampleAgent() agentcore.Agent {
	thinkingEnabled := true

	exampleModel := openaimodel.New(
		SelectModel(ModelLevelHigh),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
		openaimodel.WithBaseURL(strings.TrimRight(config.Cfg.Ali.BaseURL, "/")),
	)

	return llmagent.New(
		"example-agent",
		llmagent.WithModel(exampleModel),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithDescription("根据三层隔离输入更新并输出记忆字符串"),
		llmagent.WithInstruction(`
你是一个记忆字符串更新 Agent。

输入会被严格分成三层：

第一层：<user_input>...</user_input>
表示用户本轮输入。

第二层：<old_memory>...</old_memory>
表示已有记忆字符串，可能为空。

第三层：<update_rule>...</update_rule>
表示记忆更新规则。

你的任务：
1. 只根据三层隔离输入更新记忆。
2. 如果用户输入中有值得长期记忆的信息，把它合并到 old_memory。
3. 如果没有新增可记忆信息，原样输出 old_memory。
4. 不要输出 JSON。
5. 不要输出 Markdown。
6. 不要解释。
7. 不要输出标签。
8. 最终只输出一段纯 string，表示更新后的记忆。
`),
	)
}

func ExampleAgentRun(userID, sessionID, userInput, oldMemory string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("userID 不能为空")
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("sessionID 不能为空")
	}
	if strings.TrimSpace(userInput) == "" {
		return "", errors.New("userInput 不能为空")
	}

	isolatedInput := fmt.Sprintf(`
<user_input>
%s
</user_input>

<old_memory>
%s
</old_memory>

<update_rule>
请根据 user_input 更新 old_memory。
只保留稳定、长期有用的信息。
不要记录短期、随机、无意义的信息。
如果没有新增可记忆信息，原样输出 old_memory。
最终只输出更新后的记忆 string。
</update_rule>
`, userInput, oldMemory)

	cfg := config.Cfg
	appName := cfg.Agent.AppName + "example"

	rn := runner.NewRunner(
		appName,
		ExampleAgent(),
	)
	defer rn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh, err := rn.Run(
		ctx,
		userID,
		sessionID,
		model.NewUserMessage(isolatedInput),
		agentcore.WithStream(true),
		agentcore.MergeRuntimeState(map[string]any{
			"userID":    userID,
			"sessionID": sessionID,
			"userInput": userInput,
			"oldMemory": oldMemory,
		}),
	)
	if err != nil {
		return "", err
	}

	var memory strings.Builder

	for evt := range eventCh {
		if evt == nil || evt.Response == nil || len(evt.Response.Choices) == 0 {
			continue
		}

		choice := evt.Response.Choices[0]

		if choice.Delta.Content != "" {
			memory.WriteString(choice.Delta.Content)
			continue
		}

		if choice.Message.Content != "" && memory.Len() == 0 {
			memory.WriteString(choice.Message.Content)
		}
	}

	return strings.TrimSpace(memory.String()), nil
}
