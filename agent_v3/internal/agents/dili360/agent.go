package dili360

import (
	"agent_v3/internal/agents/modelrouter"
	"net/http"
	"time"

	"agent_v3/internal/config"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/server/agui"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session/summary"
	"trpc.group/trpc-go/trpc-agent-go/skill"
)

func Dili360Agent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.0
	topP := 0.3

	alimodel := modelrouter.NewModelForLevel("dili360-agent", modelrouter.ModelLevelMedium)

	dili360Planner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		log.Errorf("[dili360-agent] 加载 skills 仓库失败: %v", err)
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(dili360Planner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature: &temperature,
			TopP:        &topP,
		}),
	}
	if skillRepo != nil {
		opts = append(opts,
			llmagent.WithSkills(skillRepo),
			llmagent.WithSkillToolProfile(llmagent.SkillToolProfileFull),
			llmagent.WithSkillLoadMode("turn"),
			llmagent.WithMaxLoadedSkills(3),
		)
	}

	opts = append(opts,
		llmagent.WithDescription(
			"一个中国国家地理杂志（dili360.com）内容检索 Agent，使用 planner 模式逐步推理，通过 global-search skill 从中国国家地理官网搜索权威地理资讯。",
		),
		llmagent.WithInstruction(`
你是一个"中国国家地理杂志内容检索 Agent"，使用 planner 模式逐步推理，通过 global-search skill 从中国国家地理官网（dili360.com）搜索权威地理资讯。

## Planner 流程 — 严格执行以下四步

### 第一步：理解需求
- 读取上游 Team 或用户输入的委托
- 提取关键信息：目的地/区域、关注方向、具体问题

### 第二步：搜索策略 & 执行
- 使用 global-search skill 进行搜索
- 搜索词构造原则：
  - 尝试 site:dili360.com + 关键词（如 "site:dili360.com 新疆自驾"）
  - 同时尝试 "中国国家地理" + 关键词作为备选
  - 根据委托内容生成多组不同角度的搜索词
- 多轮搜索：
  - 每轮搜索后评估结果质量和覆盖度
  - 如果信息不充分或存在明显缺口，调整搜索词继续下一轮
  - 持续搜索直到信息覆盖充分或结果高度重复
- 最多执行 6 轮搜索，每次 count 用 10-20

### 第三步：结果筛选与提炼
- 去重：排除重复 URL
- 域名过滤：只保留 dili360.com 的结果
- 相关性评估：排除与委托无关的内容
- 提炼每个条目的核心信息

### 第四步：按格式规范输出

你必须输出合法 JSON，格式如下：

{
  "query": "委托的原始问题",
  "normalized_query": "你理解后的标准化搜索需求",
  "planning_process": "搜索过程摘要：说明采用的搜索策略、搜索轮次、结果筛选逻辑",
  "answer": "基于从 dili360.com 搜索到的内容，组织回答委托的答案，包含核心事实、来源标注、综合判断",
  "findings": [
    {
      "title": "文章标题",
      "url": "文章链接",
      "author": "作者/编辑",
      "summary": "文章摘要",
      "key_info": "提炼的核心信息",
      "credibility": "editorial|contributor"
    }
  ],
  "insufficient_information": false
}

补充要求：
- 如果搜索不到任何相关内容，insufficient_information 设为 true，同时在 answer 中明确说明在 dili360.com 上未找到相关信息。
- 必须只输出单个 JSON object。
- 禁止输出 markdown 代码块、前后缀说明或额外文本。
- 必须标注每条信息的来源 URL。
- credibility 标注：editorial=官方编辑部/专题策划，contributor=自由撰稿/用户投稿。
- 听从上游 Team 的具体委托指令，灵活调整搜索方向和输出重点。
- planning_process 只输出可解释的搜索过程摘要，不要暴露模型内部逐字思考或无关推理。
`),
	)

	return llmagent.New("dili360-agent", opts...)
}

func NewDili360AGUIHandler() (http.Handler, func(), error) {
	appName := config.Cfg.Agent.AppName + "dili360"

	summaryModel := modelrouter.NewSummaryModel("dili360-summary")

	sessSvc := sessioninmemory.NewSessionService(
		sessioninmemory.WithSessionEventLimit(1000),
		sessioninmemory.WithSessionTTL(30*time.Minute),
		sessioninmemory.WithSummarizer(summary.NewSummarizer(summaryModel)),
		sessioninmemory.WithAsyncSummaryNum(2),
	)

	memSvc := memoryinmemory.NewMemoryService(
		memoryinmemory.WithMemoryLimit(100),
		memoryinmemory.WithExtractor(extractor.NewExtractor(summaryModel)),
		memoryinmemory.WithAsyncMemoryNum(2),
	)

	rn := runner.NewRunner(
		appName,
		Dili360Agent(),
		runner.WithSessionService(sessSvc),
		runner.WithMemoryService(memSvc),
	)

	server, err := agui.New(
		rn,
		agui.WithPath("/agui"),
		agui.WithReasoningContentEnabled(true),
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
