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
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/skill"
)

type TravelPlanningOutput struct {
	Query                   string   `json:"query"`
	NormalizedQuery         string   `json:"normalized_query"`
	ThinkingResult          string   `json:"thinking_result"`
	PlanningProcess         string   `json:"planning_process"`
	Answer                  string   `json:"answer"`
	ContentInsights         []string `json:"content_insights"`
	RouteValidation         []string `json:"route_validation"`
	FollowUpQuestions       []string `json:"follow_up_questions"`
	InsufficientInformation bool     `json:"insufficient_information"`
}

type TravelArticleBrief struct {
	Topic          string   `json:"topic"`
	TargetAudience string   `json:"target_audience"`
	WritingGoal    string   `json:"writing_goal"`
	Style          string   `json:"style"`
	Constraints    []string `json:"constraints"`
}

type TravelArticleWriterRequest struct {
	Brief          TravelArticleBrief          `json:"brief"`
	TravelPlan     TravelPlanningOutput        `json:"travel_plan"`
	FeedbackMemory tools.ContentStrategyMemory `json:"feedback_memory"`
}

type TravelArticleOutput struct {
	Title                     string   `json:"title"`
	Summary                   string   `json:"summary"`
	ArticleOutline            []string `json:"article_outline"`
	Article                   string   `json:"article"`
	FactCitations             []string `json:"fact_citations"`
	InsightCitations          []string `json:"insight_citations"`
	AppliedFeedbackStrategies []string `json:"applied_feedback_strategies"`
	FollowUpQuestions         []string `json:"follow_up_questions"`
	InsufficientInformation   bool     `json:"insufficient_information"`
}

func TravelArticleWriterAgent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.3
	topP := 0.8

	alimodel := openaimodel.New(
		config.Cfg.Ali.AnalysisModel,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
	)

	articlePlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		// Skill loading is optional for the MVP.
		skillRepo = nil
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(articlePlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithDescription("一个旅游攻略成文 Agent，基于既有规划素材和反馈策略输出适合发布的文章。"),
		llmagent.WithInstruction(`
你是一个"旅游攻略成文 Agent"。你的输入已经被拆成三层：

1. brief：写作任务、目标读者、风格和硬约束
2. travel_plan：上游旅游规划 Agent 的完整 JSON 输出
3. feedback_memory：从历史评论中提炼出的稳定写作策略

你的职责：
- 只基于 travel_plan 写作，不补充未经验证的地理事实
- 优先遵守 feedback_memory 中的结构和表达偏好
- route_validation 只能转述为已验证事实
- content_insights 只能作为体验建议、避坑提示和推荐理由
- answer 与 planning_process 只能作为路线组织参考，不要整段照抄
- follow_up_questions 不为空且会影响准确性时，要明确说明信息缺口
- travel_plan.insufficient_information=true 时，不要生成正式成文

你必须输出合法 JSON，格式如下：

{
  "title": "文章标题",
  "summary": "100 字左右摘要",
  "article_outline": ["文章结构提纲"],
  "article": "Markdown 正文",
  "fact_citations": ["本次引用的已验证事实"],
  "insight_citations": ["本次引用的主观体验/避坑信号"],
  "applied_feedback_strategies": ["本次实际应用的反馈策略"],
  "follow_up_questions": ["仍需补充的问题，没有则为空数组"],
  "insufficient_information": false
}

补充要求：
- 只能输出单个 JSON object
- 不要输出代码块或额外说明
- 如果 travel_plan 明显不足以成文，insufficient_information 设为 true
- article 中优先给总览，再按天/按片区展开
- article 中必须区分"高德已验证"和"攻略/主观建议"
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

	return llmagent.New("travel-article-writer-agent", opts...)
}

func TravelArticleWriterAgentRun(
	userID, sessionID string,
	brief TravelArticleBrief,
	plan TravelPlanningOutput,
	memoryKey string,
) (TravelArticleOutput, error) {
	if strings.TrimSpace(userID) == "" {
		return TravelArticleOutput{}, errors.New("userID 不能为空")
	}
	if strings.TrimSpace(sessionID) == "" {
		return TravelArticleOutput{}, errors.New("sessionID 不能为空")
	}

	feedbackMemory, err := tools.LoadContentStrategyMemory(memoryKey)
	if err != nil {
		return TravelArticleOutput{}, err
	}

	brief = normalizeTravelArticleBrief(brief, plan)
	if plan.InsufficientInformation {
		return TravelArticleOutput{
			Title:                     strings.TrimSpace(brief.Topic),
			Summary:                   "上游旅行规划信息不足，当前只返回待补充问题，不生成正式文章。",
			ArticleOutline:            nil,
			Article:                   "当前旅行规划信息不足，建议先补齐以下信息后再生成正式攻略文章。",
			FactCitations:             nil,
			InsightCitations:          nil,
			AppliedFeedbackStrategies: nil,
			FollowUpQuestions:         normalizeStringSlice(plan.FollowUpQuestions),
			InsufficientInformation:   true,
		}, nil
	}

	request := TravelArticleWriterRequest{
		Brief:          brief,
		TravelPlan:     plan,
		FeedbackMemory: tools.NormalizeContentStrategyMemory(feedbackMemory),
	}
	payload, err := json.MarshalIndent(map[string]any{
		"brief":           request.Brief,
		"travel_plan":     request.TravelPlan,
		"feedback_memory": request.FeedbackMemory,
	}, "", "  ")
	if err != nil {
		return TravelArticleOutput{}, err
	}

	prompt := fmt.Sprintf(`
<travel_article_input>
%s
</travel_article_input>

请根据上面的输入生成文章。
`, string(payload))

	raw, err := runAgentString(
		config.Cfg.Agent.AppName+"travel-article-writer",
		TravelArticleWriterAgent(),
		userID,
		sessionID,
		prompt,
	)
	if err != nil {
		return TravelArticleOutput{}, err
	}

	var out TravelArticleOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return TravelArticleOutput{}, fmt.Errorf("解析 Writer Agent JSON 失败: %w; raw=%s", err, raw)
	}
	return out, nil
}

func TravelArticleOutputToBackendDraft(out TravelArticleOutput, manualTypeTag string, secondaryTags []string) tools.BackendArticleDraft {
	title := strings.TrimSpace(out.Title)
	summary := strings.TrimSpace(out.Summary)
	content := strings.TrimSpace(out.Article)

	if content == "" && len(out.FollowUpQuestions) > 0 {
		content = "当前信息不足，仍需补充：\n"
		for _, question := range normalizeStringSlice(out.FollowUpQuestions) {
			content += "- " + question + "\n"
		}
	}

	return tools.BackendArticleDraft{
		Title:         title,
		Brief:         summary,
		Content:       content,
		ManualTypeTag: strings.TrimSpace(manualTypeTag),
		SecondaryTags: normalizeStringSlice(secondaryTags),
	}
}

func TravelArticleWriterAgentRunAndPublish(
	ctx context.Context,
	userID, sessionID string,
	brief TravelArticleBrief,
	plan TravelPlanningOutput,
	memoryKey string,
	client *tools.BackendClient,
	manualTypeTag string,
	secondaryTags []string,
) (tools.BackendCreateArticleResponse, TravelArticleOutput, error) {
	if client == nil {
		return tools.BackendCreateArticleResponse{}, TravelArticleOutput{}, errors.New("backend client 不能为空")
	}
	out, err := TravelArticleWriterAgentRun(userID, sessionID, brief, plan, memoryKey)
	if err != nil {
		return tools.BackendCreateArticleResponse{}, TravelArticleOutput{}, err
	}
	if out.InsufficientInformation {
		return tools.BackendCreateArticleResponse{}, out, errors.New("上游旅行规划信息不足，不发布文章")
	}
	resp, err := client.CreateArticle(ctx, TravelArticleOutputToBackendDraft(out, manualTypeTag, secondaryTags))
	if err != nil {
		return tools.BackendCreateArticleResponse{}, out, err
	}
	return resp, out, nil
}

func runAgentString(appName string, a agentcore.Agent, userID, sessionID, userMessage string) (string, error) {
	rn := runner.NewRunner(appName, a)
	defer rn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh, err := rn.Run(
		ctx,
		userID,
		sessionID,
		model.NewUserMessage(userMessage),
		agentcore.WithStream(true),
	)
	if err != nil {
		return "", err
	}

	var final strings.Builder
	for evt := range eventCh {
		if evt == nil || evt.Response == nil || len(evt.Response.Choices) == 0 {
			continue
		}

		choice := evt.Response.Choices[0]
		if choice.Delta.Content != "" {
			final.WriteString(choice.Delta.Content)
			continue
		}
		if choice.Message.Content != "" && final.Len() == 0 {
			final.WriteString(choice.Message.Content)
		}
	}

	return strings.TrimSpace(final.String()), nil
}

func normalizeTravelArticleBrief(
	brief TravelArticleBrief,
	plan TravelPlanningOutput,
) TravelArticleBrief {
	if strings.TrimSpace(brief.Topic) == "" {
		switch {
		case strings.TrimSpace(plan.NormalizedQuery) != "":
			brief.Topic = strings.TrimSpace(plan.NormalizedQuery)
		default:
			brief.Topic = strings.TrimSpace(plan.Query)
		}
	}
	if strings.TrimSpace(brief.TargetAudience) == "" {
		brief.TargetAudience = "第一次去该目的地的自由行游客"
	}
	if strings.TrimSpace(brief.WritingGoal) == "" {
		brief.WritingGoal = "写一篇可直接照着执行的旅行攻略文章"
	}
	if strings.TrimSpace(brief.Style) == "" {
		brief.Style = "具体、克制、少空话"
	}

	baseConstraints := []string{
		"不要编造地图事实",
		"区分已验证事实与主观建议",
		"优先写执行信息，不堆空泛描述",
	}
	brief.Constraints = normalizeStringSlice(append(baseConstraints, brief.Constraints...))
	return brief
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
