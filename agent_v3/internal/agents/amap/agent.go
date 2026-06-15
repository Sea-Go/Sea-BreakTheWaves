package amap

import (
	"agent_v3/internal/agents/modelrouter"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent_v3/internal/config"
	amaptools "agent_v3/internal/tools/amap"

	agentcore "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
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

func AmapAgent() agentcore.Agent {
	thinkingEnabled := true
	temperature := 0.0
	topP := 0.3

	alimodel := modelrouter.NewModelForLevel("amap-agent", modelrouter.ModelLevelMedium)

	amapTools := amaptools.NewDefaultAmapTools()

	// BuiltinPlanner 适合支持原生 thinking/reasoning 的模型：
	// 1. 它会在请求前把 ThinkingEnabled 等参数写入 LLM Request；
	// 2. 不额外注入 ReAct 标签，避免污染本 Agent 要求的最终 JSON 输出。
	amapPlanner := builtin.New(builtin.Options{
		ThinkingEnabled: &thinkingEnabled,
	})

	skillRepo, err := skill.NewFSRepository("skills")
	if err != nil {
		log.Errorf("[amap-agent] 加载 skills 仓库失败: %v", err)
	}

	opts := []llmagent.Option{
		llmagent.WithModel(alimodel),
		llmagent.WithPlanner(amapPlanner),
		llmagent.WithGenerationConfig(model.GenerationConfig{
			Temperature: &temperature,
			TopP:        &topP,
		}),
		llmagent.WithTools(amapTools),
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
			"一个高德地图 Agent，使用 planner 模式逐步推理，基于高德地图 API 提供地理信息查询、POI 搜索、路线规划等服务。",
		),
		llmagent.WithInstruction(`
你是一个"高德地图 Agent"，使用 planner 模式逐步推理，主动调用工具获取地理信息后回答用户问题。

## Planner 流程 — 严格执行以下四步

### 第一步：理解问题
- 读取用户输入，理解用户想要查询的地理信息类型
- 识别问题中涉及的地点、区域、出行方式等关键信息

### 第二步：信息缺口分析 & 工具选择
思考"要完整回答这个问题，我还缺少什么信息？"，然后针对性地选择工具：

| 需求场景 | 缺什么信息 | 调用工具 |
|---------|-----------|---------|
| 搜索某地点的 POI（如餐厅、酒店等） | POI 数据 | amap_poi_keyword_search |
| 搜索某位置周边的 POI | 周边 POI | amap_poi_around_search |
| 查询 POI 详细信息 | POI 详情 | amap_poi_detail |
| 输入提示/地址补全 | 候选地址 | amap_input_tips |
| 地址转坐标 | 经纬度 | amap_geocode |
| 坐标转地址 | 地址描述 | amap_regeocode |
| 测量两点距离 | 距离 | amap_distance |
| 步行路线规划 | 步行路径 | amap_route_walking |
| 公交/地铁路线规划 | 公共交通路径 | amap_route_transit |
| 驾车路线规划 | 驾车路径 | amap_route_driving |
| 骑行路线规划 | 骑行路径 | amap_route_bicycling |
| 行政区划查询 | 行政区信息 | amap_district_search |
| IP 定位 | 当前位置 | amap_ip_location |
| 生成静态地图图片 | 地图图片 | amap_static_map |

**关键原则**：
- 只要有不确定的地理信息，就应该调用工具获取，不要凭记忆编造。
- 宁可多调用工具确保信息充足，也不要跳过导致回答不准确。
- 可以连续多次调用不同工具，每次工具的结果都会追加到上下文。
- 如需要同时获取多个信息（如先地理编码获取坐标，再周边搜索），按顺序依次调用。

**高德 API QPS 限制**：
- 高德地图 API 有严格的 QPS（每秒请求数）限制，系统的工具调用已内置自动限速。
- 每次只调用一个工具，等工具返回结果后再决定是否继续调用下一个。
- 如果工具返回 QPS 超限错误（infocode=10021），说明当前请求频率过高，请稍等 1-2 秒后再重试，不要立即连续重试。
- 合理规划工具调用顺序，避免不必要的重复请求。

### 第三步：执行工具调用
- 按需依次调用工具
- 每次调用后判断：是否还需要更多信息？
- 信息足够后不要再调用工具

### 第四步：按格式规范输出
- 基于所有工具返回的结果和上下文，组织答案。
- 回答需准确引用工具提供的信息。
- 最终只输出自然、准确的用户答案，不要提及你用了什么工具。

你必须输出合法 JSON，格式如下：

{
  "query": "用户原始问题",
  "normalized_query": "你理解后的标准化查询",
  "planning_process": "面向用户的简要规划摘要：说明地点识别、信息缺口、采用的查询/路线规划策略、工具结果结论。不要输出逐字隐藏推理链路。",
  "answer": "基于工具调用结果的回答内容",
  "insufficient_information": false
}

补充要求：
- 如果工具调用结果不足以回答用户问题，insufficient_information 设为 true，同时在 answer 中用自然语言明确告诉用户还需要补充哪些信息，并主动向用户提问。
- 必须只输出单个 JSON object。
- 禁止输出 markdown 代码块、前后缀说明或额外文本。
- planning_process 只输出可解释的规划摘要，不要暴露模型内部逐字思考或无关推理。

## 主动询问用户规则
当遇到以下情况时，不要猜测或使用默认值，必须在 answer 中主动、清晰地列出缺失信息并向用户提问：
- **位置不明确**：用户说"附近"、"周边"但未提供当前位置 → 请用户提供城市或具体地点。
- **出发地/目的地缺失**：路线规划缺少任一端点 → 请用户补充缺失的端点。
- **出行偏好未指定**：用户问"怎么去"但没说交通方式 → 列出驾车、公交、步行等选项让用户选择。
- **时间/日期缺失**：用户想规划行程但没说时间 → 请用户提供日期和游玩天数。
- **预算/人群等信息缺失**：影响推荐结果 → 简要询问用户是否需要考虑这些因素。
- **工具返回结果不足**：工具调用结果为空或无法确定目标地点 → 告知用户当前结果并请求更精确的描述。
- **歧义地点**：地名可能在多个城市（如"鼓楼"在北京和南京都有）→ 列出候选城市让用户确认。

## 旅游路线规划专项规则
- 优先识别：出发地、目的地/城市、日期与时间、游玩天数、交通方式、预算、同行人群、兴趣偏好、必须去/不想去地点。
- 缺少关键信息时，先用工具补足可查询信息；仍缺少用户偏好时，在 answer 中以结构化方式列出缺失项并向用户提问，例如："为您规划路线还需要确认以下几点：1) 您的出发地是？ 2) 计划游玩几天？ 3) 偏好步行还是公共交通？"
- 规划多日路线时，按地理邻近性聚类景点，减少折返；每一天给出推荐顺序、交通方式、预计路程/耗时、备选方案。
- 涉及具体地址、距离、路线、行政区、周边 POI 时，必须优先调用高德工具确认，不要凭记忆编造。
`),
	)

	return llmagent.New("amap-agent", opts...)
}

func AmapAgentRun(userID, sessionID, userMessage string) (<-chan *event.Event, context.CancelFunc, error) {
	if userID == "" {
		return nil, nil, errors.New("userID 不能为空")
	}
	if sessionID == "" {
		return nil, nil, errors.New("sessionID 不能为空")
	}
	if userMessage == "" {
		return nil, nil, errors.New("userMessage 不能为空")
	}

	cfg := config.Cfg
	appName := cfg.Agent.AppName + "amap"

	rn := runner.NewRunner(
		appName,
		AmapAgent(),
	)

	ctx, stop := context.WithCancel(context.Background())

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			stop()
			_ = rn.Close()
		})
	}

	log.Infof(
		"[amap-agent] before rn.Run app_name=%s user_id=%s session_id=%s user_message=%s",
		appName,
		userID,
		sessionID,
		userMessage,
	)

	eventCh, err := rn.Run(
		ctx,
		userID,
		sessionID,
		model.NewUserMessage(userMessage),
		agentcore.WithStream(true),
		agentcore.MergeRuntimeState(map[string]any{
			"userID":       userID,
			"sessionID":    sessionID,
			"userMessage":  userMessage,
			"agentRunMode": "amap",
		}),
	)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	log.Infof(
		"[amap-agent] rn.Run started app_name=%s user_id=%s session_id=%s",
		appName,
		userID,
		sessionID,
	)

	outCh := make(chan *event.Event, 16)

	go func() {
		var (
			finalContent strings.Builder
			reasoningBuf strings.Builder
			toolCallsBuf strings.Builder
			firstTool    = true
		)

		defer func() {
			log.Infof(
				"[amap-agent] finished final_content_len=%d reasoning_len=%d tool_calls_len=%d",
				finalContent.Len(),
				reasoningBuf.Len(),
				toolCallsBuf.Len(),
			)

			if finalContent.Len() > 0 {
				log.Infof(
					"[amap-agent] final_content=%s",
					finalContent.String(),
				)
			}
			if reasoningBuf.Len() > 0 {
				log.Infof(
					"[amap-agent] reasoning=%s",
					reasoningBuf.String(),
				)
			}
			if toolCallsBuf.Len() > 0 {
				log.Infof(
					"[amap-agent] tool_calls=%s",
					toolCallsBuf.String(),
				)
			}

			close(outCh)
			cleanup()
		}()

		for {
			select {
			case <-ctx.Done():
				log.Infof(
					"[amap-agent] context done err=%v",
					ctx.Err(),
				)
				return

			case evt, ok := <-eventCh:
				if !ok {
					log.Infof("[amap-agent] eventCh closed")
					return
				}

				if evt == nil {
					select {
					case outCh <- evt:
					case <-ctx.Done():
					}
					continue
				}

				if evt.Response != nil {
					for _, choice := range evt.Response.Choices {
						// Final answer content
						if strings.TrimSpace(choice.Message.Content) != "" {
							log.Infof(
								"[amap-agent][message_content] content=%s",
								strings.TrimSpace(choice.Message.Content),
							)
						}

						if choice.Delta.Content != "" {
							finalContent.WriteString(choice.Delta.Content)
						} else if choice.Message.Content != "" && finalContent.Len() == 0 {
							finalContent.WriteString(choice.Message.Content)
						}

						// Planner 思考过程
						if choice.Delta.ReasoningContent != "" {
							reasoningBuf.WriteString(choice.Delta.ReasoningContent)
						} else if choice.Message.ReasoningContent != "" {
							reasoningBuf.WriteString(choice.Message.ReasoningContent)
						}

						// 工具调用记录
						for _, tc := range choice.Message.ToolCalls {
							if !firstTool {
								toolCallsBuf.WriteString("\n")
							}
							firstTool = false
							tcJSON, _ := json.Marshal(tc)
							toolCallsBuf.Write(tcJSON)
						}
						for _, tc := range choice.Delta.ToolCalls {
							if !firstTool {
								toolCallsBuf.WriteString("\n")
							}
							firstTool = false
							tcJSON, _ := json.Marshal(tc)
							toolCallsBuf.Write(tcJSON)
						}
					}
				}

				select {
				case outCh <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return outCh, cleanup, nil
}

func NewAmapAGUIHandler() (http.Handler, func(), error) {
	appName := config.Cfg.Agent.AppName + "amap"

	// 为 summarizer 和 memory extractor 创建一个轻量模型实例
	summaryModel := modelrouter.NewSummaryModel("amap-summary")

	// 短期记忆：session 服务 + summarizer，自动压缩长对话历史
	sessSvc := sessioninmemory.NewSessionService(
		sessioninmemory.WithSessionEventLimit(1000),
		sessioninmemory.WithSessionTTL(30*time.Minute),
		sessioninmemory.WithSummarizer(summary.NewSummarizer(summaryModel)),
		sessioninmemory.WithAsyncSummaryNum(2),
	)

	// 长期记忆：自动从对话中提取用户偏好、常去地点等
	memSvc := memoryinmemory.NewMemoryService(
		memoryinmemory.WithMemoryLimit(100),
		memoryinmemory.WithExtractor(extractor.NewExtractor(summaryModel)),
		memoryinmemory.WithAsyncMemoryNum(2),
	)

	rn := runner.NewRunner(
		appName,
		AmapAgent(),
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
