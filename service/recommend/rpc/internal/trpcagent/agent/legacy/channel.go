// Package agent channel.go — ChannelAgent IP 频道路由（Task 11.9）。
//
// 职责：基于 LLM 结构化输出对查询/消息进行 IP 频道路由，LLM 失败时用关键词
// 规则兜底。路由结果可通过 tools.Invoke("channel.route") 写入频道独立池。
//
// 二开扩展点：
//   - 替换 LLMClient：接入自研/第三方 LLM 做频道路由
//   - 扩展关键词规则：修改 fallbackRoute 或注入自定义 ChannelRegistry
//   - 替换 ToolExecutor：接入自研工具框架管理频道独立池
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ChannelRoute 频道路由结果（LLM 结构化输出）。
//
// 字段语义：
//   - Channel：频道名（travel/game/food/tech/default）
//   - IPName：IP 名称（如"旅游 IP"/"游戏 IP"）
//   - Exclusive：是否独占频道（true 时仅返回该频道候选）
//   - Reason：路由原因（LLM 解释或关键词命中说明）
type ChannelRoute struct {
	// Channel 频道名。
	Channel string `json:"channel"`
	// IPName IP 名称。
	IPName string `json:"ip_name"`
	// Exclusive 是否独占。
	Exclusive bool `json:"exclusive"`
	// Reason 路由原因。
	Reason string `json:"reason"`
}

// ChannelConfig 频道配置（内置频道注册表条目）。
type ChannelConfig struct {
	// Name 频道名。
	Name string
	// TypeTag 主类型标签。
	TypeTag string
	// IPName IP 名称。
	IPName string
	// Keywords 关键词列表（用于规则兜底匹配）。
	Keywords []string
	// QualityThreshold 独立质量阈值。
	QualityThreshold float64
}

// defaultChannelRegistry 内置频道注册表。
//
// 包含 5 个内置频道：travel/game/food/tech/default。
// 二开扩展点：业务方可在此 map 追加自定义频道，或通过注入覆盖。
var defaultChannelRegistry = map[string]ChannelConfig{
	"travel": {
		Name:             "travel",
		TypeTag:          "travel",
		IPName:           "旅游 IP",
		Keywords:         []string{"旅游", "旅行", "景点", "酒店", "机票", "度假"},
		QualityThreshold: 0.7,
	},
	"game": {
		Name:             "game",
		TypeTag:          "game",
		IPName:           "游戏 IP",
		Keywords:         []string{"游戏", "电竞", "手游", "主机", "PS5", "Steam"},
		QualityThreshold: 0.6,
	},
	"food": {
		Name:             "food",
		TypeTag:          "food",
		IPName:           "美食 IP",
		Keywords:         []string{"美食", "餐饮", "菜谱", "餐厅", "做饭", "料理"},
		QualityThreshold: 0.65,
	},
	"tech": {
		Name:             "tech",
		TypeTag:          "tech",
		IPName:           "科技 IP",
		Keywords:         []string{"科技", "AI", "编程", "代码", "互联网", "数码"},
		QualityThreshold: 0.75,
	},
	"default": {
		Name:             "default",
		TypeTag:          "default",
		IPName:           "默认 IP",
		Keywords:         nil,
		QualityThreshold: 0.5,
	},
}

// ChannelAgent 频道路由 Agent。
//
// 职责：
//   - 调用 LLM.CompleteWithStructuredOutput 输出 ChannelRoute
//   - LLM 失败时用关键词规则兜底（旅游→travel/游戏→game/美食→food/其他→default）
//   - 调用 tools.Invoke("channel.route") 路由到独立池（tools 为 nil 时跳过）
//
// 二开扩展点：替换 LLMClient 接入自研 LLM；扩展 defaultChannelRegistry 注册自定义频道。
type ChannelAgent struct {
	// llm LLM 客户端（小模型，结构化输出 ChannelRoute）。
	llm LLMClient
	// tools 工具执行器（用于 channel.route 调用，可为 nil）。
	tools ToolExecutor
	// opts Agent 选项。
	opts domain.AgentOptions
}

// 编译期断言：ChannelAgent 实现 domain.Agent。
var _ domain.Agent = (*ChannelAgent)(nil)

// NewChannelAgent 构造 ChannelAgent。
// llm LLM 客户端；tools 工具执行器（用于 channel.route，可为 nil）；
// opts Agent 选项。返回 domain.Agent 接口。
func NewChannelAgent(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &ChannelAgent{llm: llm, tools: tools, opts: opts}
}

// Name 返回 Agent 名称。
func (a *ChannelAgent) Name() string { return "channel" }

// Run 实现 domain.Agent.Run。
//
// 流程：
//  1. 从 input.Message 或 input.State["query"] 取查询/频道名
//  2. 调用 llm.CompleteWithStructuredOutput 输出 ChannelRoute
//  3. LLM 失败时用规则兜底（关键词匹配）
//  4. 调用 tools.Invoke("channel.route", routeInput) 路由到独立池（tools 为 nil 跳过）
//  5. State 写入 channel_route（ChannelRoute）/channel（string）；Result 写入 ChannelRoute
func (a *ChannelAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	trace := make([]string, 0, 1)

	// 1. 取查询/频道名（优先 Message，其次 State["query"]）。
	query := input.Message
	if query == "" {
		if q, ok := input.State["query"].(string); ok {
			query = q
		}
	}

	// 2. LLM 路由，失败时规则兜底。
	route, err := a.routeWithLLM(ctx, query)
	if err != nil || route.Channel == "" {
		route = fallbackRoute(query)
	}

	// 4. 频道独立池管理：调用 tools.Invoke("channel.route") 路由到独立池。
	if a.tools != nil {
		routeInput, _ := json.Marshal(map[string]any{
			"channel":   route.Channel,
			"ip_name":   route.IPName,
			"exclusive": route.Exclusive,
			"query":     query,
			"user_id":   input.UserID,
		})
		// best-effort，失败不阻断主流程。
		_, _ = a.tools.Invoke(ctx, "channel.route", routeInput)
	}
	trace = append(trace, "channel.route")

	state := copyState(input.State)
	state["channel_route"] = route
	state["channel"] = route.Channel
	return domain.AgentOutput{
		State:  state,
		Result: route,
		Trace:  trace,
	}, nil
}

// routeWithLLM 调用 LLM 结构化输出 ChannelRoute。
// ctx 上下文；query 查询文本。返回 ChannelRoute 与 error。
func (a *ChannelAgent) routeWithLLM(ctx context.Context, query string) (ChannelRoute, error) {
	if a.llm == nil {
		return ChannelRoute{}, fmt.Errorf("llm nil")
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"channel":   map[string]any{"type": "string"},
			"ip_name":   map[string]any{"type": "string"},
			"exclusive": map[string]any{"type": "boolean"},
			"reason":    map[string]any{"type": "string"},
		},
		"required": []string{"channel"},
	}
	prompt := fmt.Sprintf("根据用户查询进行 IP 频道路由。可选频道：travel/game/food/tech/default。查询：%s", query)
	raw, err := a.llm.CompleteWithStructuredOutput(ctx, prompt, schema)
	if err != nil {
		return ChannelRoute{}, err
	}
	var route ChannelRoute
	if err := json.Unmarshal(raw, &route); err != nil {
		return ChannelRoute{}, err
	}
	// 校验频道名是否在注册表中，不在则返回空 Channel 触发兜底。
	if _, ok := defaultChannelRegistry[route.Channel]; !ok {
		return ChannelRoute{}, fmt.Errorf("unknown channel: %s", route.Channel)
	}
	if route.IPName == "" {
		route.IPName = defaultChannelRegistry[route.Channel].IPName
	}
	return route, nil
}

// fallbackRoute 规则兜底路由：基于关键词匹配。
// query 查询文本。返回 ChannelRoute。
//
// 匹配规则：旅游→travel；游戏→game；美食→food；科技→tech；其他→default。
func fallbackRoute(query string) ChannelRoute {
	for name, cfg := range defaultChannelRegistry {
		if name == "default" {
			continue
		}
		for _, kw := range cfg.Keywords {
			if strings.Contains(query, kw) {
				return ChannelRoute{
					Channel:   cfg.Name,
					IPName:    cfg.IPName,
					Exclusive: false,
					Reason:    fmt.Sprintf("keyword match: %s", kw),
				}
			}
		}
	}
	cfg := defaultChannelRegistry["default"]
	return ChannelRoute{
		Channel:   cfg.Name,
		IPName:    cfg.IPName,
		Exclusive: false,
		Reason:    "no keyword match, fallback to default",
	}
}
