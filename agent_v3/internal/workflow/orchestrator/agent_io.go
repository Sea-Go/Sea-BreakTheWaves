package orchestrator

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"strings"
	workflowruntime "agent_v3/internal/workflow/runtime"
	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
)

func (o *Orchestrator) runAgentAndCollect(
	ctx context.Context,
	ag agentcore.Agent,
	sessionID string,
	prompt string,
) (string, error) {
	rn := runner.NewRunner("orchestrator-"+sessionID, ag)
	defer rn.Close()

	eventCh, err := rn.Run(ctx, "orchestrator-system",
		sessionID+"-"+uuid.NewString()[:8],
		model.NewUserMessage(prompt), agentcore.WithStream(true))
	if err != nil {
		return "", err
	}

	var out strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil {
			continue
		}
		for _, c := range evt.Response.Choices {
			if c.Delta.Content != "" {
				out.WriteString(c.Delta.Content)
			}
			if c.Message.Content != "" && out.Len() == 0 {
				out.WriteString(c.Message.Content)
			}
		}
	}
	return out.String(), nil
}

// parseSkillResult 从 LLM 文本输出中解析 SkillResult JSON。
func parseSkillResult(output string) *workflowruntime.SkillResult {
	s := strings.Index(output, "{")
	e := strings.LastIndex(output, "}")
	if s < 0 || e <= s {
		return nil
	}
	var r workflowruntime.SkillResult
	if json.Unmarshal([]byte(output[s:e+1]), &r) != nil {
		return nil
	}
	if r.SkillName == "" {
		return nil
	}
	return &r
}

func ParseSkillResult(output string) *workflowruntime.SkillResult {
	return parseSkillResult(output)
}

// startCleanupLoop 定期清理过期的 runtime。
