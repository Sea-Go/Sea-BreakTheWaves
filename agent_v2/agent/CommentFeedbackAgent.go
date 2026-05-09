package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent_v2/config"
	"agent_v2/tools"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/skill"
)

type CommentFeedbackInput struct {
	ArticleID      string                      `json:"article_id"`
	ArticleTitle   string                      `json:"article_title"`
	Comments       []string                    `json:"comments"`
	ExistingMemory tools.ContentStrategyMemory `json:"existing_memory"`
}

type CommentFeedbackOutput struct {
	CommentSummary         string                      `json:"comment_summary"`
	PositiveSignals        []string                    `json:"positive_signals"`
	NegativeSignals        []string                    `json:"negative_signals"`
	HighFrequencyQuestions []string                    `json:"high_frequency_questions"`
	WritingImprovements    []string                    `json:"writing_improvements"`
	StrategyMemoryUpdate   tools.ContentStrategyMemory `json:"strategy_memory_update"`
}

func CommentFeedbackAgent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.1
	topP := 0.5

	alimodel := openaimodel.New(
		config.Cfg.Ali.AnalysisModel,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
	)

	feedbackPlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		skillRepo = nil
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(feedbackPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithDescription("一个评论反馈 Agent，负责把文章评论提炼成稳定的写作改进策略。"),
		llmagent.WithInstruction(`
你是一个"评论反馈 Agent"。你的任务是从一批旅游攻略文章评论中提炼稳定、可执行的写作策略。

规则：
- 区分高频问题和个别情绪化抱怨
- 只保留对后续写作有帮助的稳定策略
- 不要把单条极端评论升级为全局规则
- 重点观察：结构、交通说明、体力成本、适用人群、避坑、信息缺口

你必须输出合法 JSON，格式如下：

{
  "comment_summary": "评论整体摘要",
  "positive_signals": ["读者明确喜欢的点"],
  "negative_signals": ["读者明确不满意的点"],
  "high_frequency_questions": ["评论中高频追问"],
  "writing_improvements": ["下一篇文章应如何调整"],
  "strategy_memory_update": {
    "preferred_structure": ["稳定的结构偏好"],
    "do_more": ["应增加的内容"],
    "avoid": ["应减少或避免的写法"],
    "unanswered_questions": ["仍值得持续补充的问题"]
  }
}

补充要求：
- 只能输出单个 JSON object
- 不要输出代码块或额外说明
- 如果评论太少，仍要谨慎给出低置信度、保守的建议
`),
	}

	if skillRepo != nil {
		opts = append(opts,
			llmagent.WithSkills(skillRepo),
			llmagent.WithSkillToolProfile(llmagent.SkillToolProfileFull),
			llmagent.WithSkillLoadMode("turn"),
			llmagent.WithMaxLoadedSkills(2),
		)
	}

	return llmagent.New("comment-feedback-agent", opts...)
}

func CommentFeedbackAgentRun(
	userID, sessionID string,
	input CommentFeedbackInput,
	memoryKey string,
) (CommentFeedbackOutput, error) {
	if strings.TrimSpace(userID) == "" {
		return CommentFeedbackOutput{}, errors.New("userID 不能为空")
	}
	if strings.TrimSpace(sessionID) == "" {
		return CommentFeedbackOutput{}, errors.New("sessionID 不能为空")
	}
	if len(input.Comments) == 0 {
		return CommentFeedbackOutput{}, errors.New("comments 不能为空")
	}

	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return CommentFeedbackOutput{}, err
	}

	prompt := fmt.Sprintf(`
<comment_feedback_input>
%s
</comment_feedback_input>

请根据上面的评论生成反馈策略。
`, string(payload))

	raw, err := runAgentString(
		config.Cfg.Agent.AppName+"comment-feedback",
		CommentFeedbackAgent(),
		userID,
		sessionID,
		prompt,
	)
	if err != nil {
		return CommentFeedbackOutput{}, err
	}

	var out CommentFeedbackOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return CommentFeedbackOutput{}, fmt.Errorf("解析 Feedback Agent JSON 失败: %w; raw=%s", err, raw)
	}

	existing := tools.NormalizeContentStrategyMemory(input.ExistingMemory)
	if isEmptyContentStrategyMemory(existing) {
		var err error
		existing, err = tools.LoadContentStrategyMemory(memoryKey)
		if err != nil {
			return CommentFeedbackOutput{}, err
		}
	}
	merged := tools.MergeContentStrategyMemory(existing, out.StrategyMemoryUpdate)
	if _, err := tools.SaveContentStrategyMemory(memoryKey, merged); err != nil {
		return CommentFeedbackOutput{}, err
	}
	out.StrategyMemoryUpdate = merged
	return out, nil
}

func CommentFeedbackAgentRunFromBackend(
	ctx context.Context,
	userID, sessionID, articleID, articleTitle, memoryKey string,
	client *tools.BackendClient,
) (CommentFeedbackOutput, error) {
	if client == nil {
		return CommentFeedbackOutput{}, errors.New("backend client 不能为空")
	}
	comments, err := client.ListArticleComments(ctx, articleID, 1, 50)
	if err != nil {
		return CommentFeedbackOutput{}, err
	}
	if len(comments) == 0 {
		return CommentFeedbackOutput{}, errors.New("后端文章评论为空")
	}

	existing, err := tools.LoadContentStrategyMemory(memoryKey)
	if err != nil {
		return CommentFeedbackOutput{}, err
	}

	return CommentFeedbackAgentRun(userID, sessionID, CommentFeedbackInput{
		ArticleID:      articleID,
		ArticleTitle:   articleTitle,
		Comments:       comments,
		ExistingMemory: existing,
	}, memoryKey)
}

func isEmptyContentStrategyMemory(memory tools.ContentStrategyMemory) bool {
	return len(memory.PreferredStructure) == 0 &&
		len(memory.DoMore) == 0 &&
		len(memory.Avoid) == 0 &&
		len(memory.UnansweredQuestions) == 0
}
