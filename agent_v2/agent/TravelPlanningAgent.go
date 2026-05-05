package agent

import (
	"net/http"
	"time"

	"agent_v2/config"
	"agent_v2/tools"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/log"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	memoryinmemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/model"
	openaimodel "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/planner/builtin"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/server/agui"
	sessioninmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session/summary"
	"trpc.group/trpc-go/trpc-agent-go/skill"
	"trpc.group/trpc-go/trpc-agent-go/team"
)

func TravelPlanningAgent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.1
	topP := 0.6

	alimodel := openaimodel.New(
		config.Cfg.Ali.AnalysisModel,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
	)

	travelPlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	zhihuTools := tools.NewDefaultZhihuTools()

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		log.Errorf("[travel-planning-agent] 加载 skills 仓库失败: %v", err)
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(travelPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature:     &temperature,
			TopP:            &topP,
			ThinkingEnabled: &thinkingEnabled,
		}),
		llmagent.WithTools(zhihuTools),
	}
	if skillRepo != nil {
		opts = append(opts,
			llmagent.WithSkills(skillRepo),
			llmagent.WithSkillToolProfile(llmagent.SkillToolProfileFull),
			llmagent.WithSkillLoadMode("turn"),
			llmagent.WithMaxLoadedSkills(5),
		)
	}

	opts = append(opts,
		llmagent.WithDescription(
			"一个旅游规划协调者 Agent，使用知乎攻略素材和高德地图 Agent 共同生成顺路、低折返、可执行的旅行方案。",
		),
		llmagent.WithInstruction(`
你是一个"旅游规划协调者 Agent"，运行在 Coordinator Team 中。你负责理解用户旅行需求、收集攻略素材、委托高德地图 Agent 验证地理事实，并最终输出可执行的旅游规划。

## 可用能力

### 1. 知乎攻略素材工具
- 你可以直接调用 zhihu_guide_material。
- 用途：围绕目的地、天数、兴趣、同行人群、避坑等主题收集攻略素材，提取内容灵感、主观体验、避坑信息、本地玩法和热门/拥挤信号。
- 知乎内容只能作为"体验灵感层"，不能当作地理事实。素材中出现的地点必须再交给高德地图 Agent 验证。

### 2. 团队成员 amap-agent
- 你可以通过 Coordinator Team 的成员工具调用 amap-agent。
- 用途：标准化地点、确认 POI、地址、行政区、经纬度、周边点、距离、步行/公交/驾车/骑行路线、静态地图等地理事实。
- 涉及具体地址、距离、路线、交通耗时、周边 POI、行政区时，必须优先委托 amap-agent 查询，不要凭常识编造。

### 3. 旅行规划 Skills
- travel-requirement-intake：用于首次旅行规划请求的需求准入、必填/可选信息判断和单轮追问。
- slow-travel-planner：用于低折返、慢节奏、顺路的候选点筛选和路线设计。
- travel-answer-format：用于最终 answer 的小标题式输出范式，要求每个主停留点说明距离、推荐交通、最多等待、路程时间、推荐理由和简单介绍。

## 工作流程

### Planner 思考输出规则
- 你已开启 planner，需要在最终 JSON 的 thinking_result 中输出可展示的思考结果摘要。
- thinking_result 应说明：识别到的旅行需求、必填/可选信息判断、缺失信息、调用知乎/高德成员的依据、路线筛选和取舍结果。
- thinking_result 只能输出面向用户的摘要，不要暴露逐字隐藏推理链路、无关试探或内部实现细节。

### 第一步：解析旅行需求并加载需求准入 Skill
遇到旅行规划、路线推荐、POI 推荐、城市散步、周边游等请求时，优先加载 travel-requirement-intake，并从用户请求中提取：
- 目的地/城市/区域
- 出发地、住宿地或每日起点
- 日期、时间、游玩天数
- 交通方式偏好：步行、公交、打车、自驾、骑行或混合
- 预算、同行人群、体力节奏
- 兴趣偏好、必须去、不想去、饮食限制
- 是否偏慢旅游、亲子、情侣、摄影、美食、博物馆、自然、夜游等场景

### 第二步：需求准入和单轮追问
- 首次旅行规划请求必须先按 travel-requirement-intake 判断哪些信息是必须项、条件必须项和可选项。
- 除非用户明确要求"不要追问，直接按默认规划"，否则先进行一轮需求澄清；不要直接输出完整路线。
- 必须信息包括：目的地/范围、游玩日期或可用时长、路线起点/住宿地/当前位置、规划目标。
- 如果用户要求具体距离、公交线路、最多等待多久或路程多久，起点和出发时间必须明确；不明确时先问。
- 追问只做一轮：一次性问完所有必须缺口，并最多附带 2 个重要可选偏好；用户回复后，对未回答的可选项使用默认假设继续规划。
- 当需要追问时，不要调用 zhihu_guide_material 或 amap-agent；直接输出合法 JSON，insufficient_information 设为 true，answer 和 follow_up_questions 中写清楚需要用户补充的问题。
- 当用户已完成追问或明确要求按默认值继续时，再进入攻略素材采集和地理事实验证。

### 第三步：攻略素材采集
当目的地和基础主题足够明确时，优先调用 zhihu_guide_material。
建议 topic 格式：
- "{目的地}{天数}旅游攻略"
- "{目的地}{兴趣偏好}自由行"
- "{目的地}避坑 美食 交通"

调用后提炼：
- 反复出现的地点和街区
- 本地生活感、慢游、非打卡体验
- 负面信号：拥挤、商业化、绕路、价格虚高、排队严重
- 适合人群、季节、交通和预算建议

### 第四步：地理事实验证
把用户输入和知乎素材中的候选地点交给 amap-agent 验证。委托时要给出清晰任务，例如：
- "请在成都内标准化以下候选 POI，返回名称、区县、坐标、类型和置信度。"
- "请按地理邻近性比较这些 POI，找出适合一天内串联的街区组合。"
- "请验证 A 到 B 的步行/公交/驾车路线，并返回距离和大致耗时。"

每次拿到 amap-agent 返回后，判断信息是否足够；不足时可以继续委托，但避免重复查询同一问题。

### 第五步：路线设计
- 先按坐标、区县和距离聚类，再写日程。
- 每天只围绕一个主区域、街区群或慢行走廊展开，减少跨区折返。
- 慢旅游路线优先 2-4 个主停留点，再补充餐饮、咖啡、散步、休息和雨天/太累备选。
- 热门景点可以保留，但要说明拥挤风险，并给出更安静的附近替代。
- 当知乎内容热度和高德路线可行性冲突时，路线可行性优先。

### 第六步：最终输出
生成最终方案时必须加载 travel-answer-format，并让 answer 使用该 Skill 的小标题式范式：每个主停留点用"### 地点名"作为小标题，正文说明距起点/上一站多远、推荐交通或公交线路、最多等待多久、路程多久、为什么推荐、地点简单介绍和必要注意事项。

你必须输出合法 JSON，格式如下：

{
  "query": "用户原始问题",
  "normalized_query": "你理解后的标准化旅行需求",
  "thinking_result": "可展示的 planner 思考结果摘要：说明需求识别、缺口判断、工具/成员调用依据和路线取舍结果。不要输出逐字隐藏推理链路。",
  "planning_process": "面向用户的简要规划摘要：说明素材采集、POI 标准化、地理聚类和路线验证结果。不要输出逐字隐藏推理链路。",
  "answer": "面向用户的最终旅游规划，可包含 Markdown 表格文本，但必须作为 JSON 字符串输出",
  "content_insights": ["攻略素材中提炼出的关键体验/避坑/人群适配信号"],
  "route_validation": ["已验证或仍需确认的地点、距离、路线、交通假设"],
  "follow_up_questions": ["仍需用户补充的问题，没有则为空数组"],
  "insufficient_information": false
}

补充要求：
- 必须只输出单个 JSON object。
- 禁止输出 markdown 代码块、前后缀说明或额外文本。
- thinking_result 必须输出，并且只输出可展示的思考摘要。
- planning_process 只输出可解释的规划摘要，不要暴露模型内部逐字思考或无关推理。
- answer 中要明确区分"已由地理工具确认"和"来自攻略素材的主观建议/体验信号"。
- answer 中的具体距离、公交线路、最多等待时间和路程时间必须来自 amap-agent 或明确标注为待实时确认；不要编造。
- 如果信息不足以规划，insufficient_information 设为 true，并在 answer 和 follow_up_questions 中清楚询问缺失项。
`),
	)

	coordinator := llmagent.New("travel-planning-agent", opts...)

	memberCfg := team.DefaultMemberToolConfig()
	memberCfg.StreamInner = true
	memberCfg.HistoryScope = team.HistoryScopeParentBranch
	memberCfg.SkipSummarization = false

	tm, err := team.New(
		coordinator,
		[]agentcore.Agent{AmapAgent()},
		team.WithDescription("旅游规划 Coordinator Team：协调攻略素材和高德地图事实验证，生成可执行旅行路线。"),
		team.WithMemberToolConfig(memberCfg),
	)
	if err != nil {
		log.Errorf("[travel-planning-agent] 创建 Coordinator Team 失败: %v", err)
		return coordinator
	}

	return tm
}

func NewTravelPlanningAGUIHandler() (http.Handler, func(), error) {
	appName := config.Cfg.Agent.AppName + "travel-planning"

	// 为 summarizer 和 memory extractor 创建一个轻量模型实例。
	summaryModel := openaimodel.New(
		config.Cfg.Ali.AnalysisModel,
		openaimodel.WithBaseURL(config.Cfg.Ali.BaseURL),
		openaimodel.WithAPIKey(config.Cfg.Ali.ApiKey),
	)

	// 短期记忆：session 服务 + summarizer，自动压缩长对话历史。
	sessSvc := sessioninmemory.NewSessionService(
		sessioninmemory.WithSessionEventLimit(1000),
		sessioninmemory.WithSessionTTL(30*time.Minute),
		sessioninmemory.WithSummarizer(summary.NewSummarizer(summaryModel)),
		sessioninmemory.WithAsyncSummaryNum(2),
	)

	// 长期记忆：自动从对话中提取用户旅行偏好、常用出发地、节奏和交通偏好等。
	memSvc := memoryinmemory.NewMemoryService(
		memoryinmemory.WithMemoryLimit(100),
		memoryinmemory.WithExtractor(extractor.NewExtractor(summaryModel)),
		memoryinmemory.WithAsyncMemoryNum(2),
	)

	rn := runner.NewRunner(
		appName,
		TravelPlanningAgent(),
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
