// Package agent graph_agent.go — GraphAgent 大模型图谱推理（Task 11.4）。
//
// 该文件实现 GraphAgent：作为多 Agent 架构中的"大模型 + 知识图谱"分支，
// 负责将自然语言查询转换为参数化 Cypher，调用 graph.query 工具执行图谱查询，
// 解析结果填充 domain.GraphKnowledge 供下游 RecallPlanner / ExplainAgent 复用。
// 不直接 import trpc-agent-go / neo4j，所有外部依赖通过 LLMClient / ToolExecutor
// interface 注入，Cypher 全程参数化（$param 占位符）防注入。
//
// 二开扩展点：
//   - 替换 LLMClient：实现该 interface 接入自研/第三方 LLM 提升 Cypher 生成质量
//   - 替换 ToolExecutor：实现该 interface 接入自研工具调用框架或直连 Neo4j
//   - 扩展规则兜底：在 fallbackCypher 中追加业务专属实体类型与模板
//   - 调整 schema：在 cypherSchema 中描述自有图谱节点/边以引导 LLM 生成
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"sea/internal/domain"
)

// ----------------------------------------------------------------------------
// 常量与内置 Cypher 模板（全部参数化，禁止字符串拼接用户输入）
// ----------------------------------------------------------------------------

// graphAgentName Agent 名称。
const graphAgentName = "graph"

// cypherSchema 描述图谱节点 schema，用于引导 LLM 生成参数化 Cypher。
// 节点类型：Article / Tag / Author / IP / Channel / Entity。
var cypherSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"cypher": map[string]any{
			"type":        "string",
			"description": "参数化 Cypher 查询语句，用户输入必须用 $param 占位符，禁止字符串拼接",
		},
		"params": map[string]any{
			"type":        "object",
			"description": "Cypher 参数绑定键值对，键名与 $param 一一对应",
		},
		"entities": map[string]any{
			"type":        "array",
			"description": "从查询中识别出的实体列表（含 name 与 type）",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"type": map[string]any{"type": "string"},
				},
			},
		},
	},
	"required": []string{"cypher"},
}

// 内置参数化 Cypher 模板（基于实体类型）。
// 所有用户输入通过 $param 占位符绑定，杜绝 Cypher 注入。
const (
	// cypherTplAuthor 作者→文章：以作者为起点，沿 WROTE 扩展到文章。
	cypherTplAuthor = `MATCH (au:Author {name: $author_name})-[:WROTE]->(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`
	// cypherTplIP IP→文章：以 IP 为起点，沿 BELONGS_IP 反向扩展到文章。
	cypherTplIP = `MATCH (ip:IP {name: $ip_name})<-[:BELONGS_IP]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`
	// cypherTplTag Tag→文章：以 Tag 为起点，沿 HAS_TAG 反向扩展到文章。
	cypherTplTag = `MATCH (t:Tag {name: $tag_name})<-[:HAS_TAG]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`
	// cypherTplTitle Title→文章：按标题模糊匹配文章。
	cypherTplTitle = `MATCH (rec:Article)
WHERE rec.title CONTAINS $title_keyword
RETURN DISTINCT rec
LIMIT $topk`
	// cypherTplTopic Topic→文章：以 Topic 实体为起点，沿 MENTIONED_IN 扩展到文章。
	cypherTplTopic = `MATCH (e:Entity {name: $topic_name, type: 'Topic'})-[:MENTIONED_IN]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`
	// cypherTplDefault 默认模板：实体关键词 MentionedIn 文章。
	cypherTplDefault = `MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(rec:Article)
RETURN DISTINCT rec
LIMIT $topk`
)

// defaultTopK 默认返回上限。
const defaultTopK = 20

// ----------------------------------------------------------------------------
// GraphAgent 实现
// ----------------------------------------------------------------------------

// GraphAgent 大模型图谱推理 Agent。
//
// 职责：
//   - 调用 LLM 生成参数化 Cypher（schema 描述 Article/Tag/Author/IP/Channel/Entity 节点）
//   - LLM 失败时按关键词匹配实体类型生成参数化 Cypher 兜底
//   - 调用 tools.Invoke(ctx, "graph.query", ...) 执行 Cypher（tools 为 nil 跳过）
//   - 解析结果填充 domain.GraphKnowledge（Entities/Articles/Authors/IPs/Cypher）
//
// 二开扩展点：替换 LLMClient / ToolExecutor / 扩展 fallbackCypher 实体规则。
type GraphAgent struct {
	llm   LLMClient
	tools ToolExecutor
	opts  domain.AgentOptions
}

// NewGraphAgent 构造 GraphAgent，返回 domain.Agent 接口。
// llm LLM 客户端；tools 工具执行器（可为 nil，nil 时跳过图谱执行）；opts Agent 选项。
func NewGraphAgent(llm LLMClient, tools ToolExecutor, opts domain.AgentOptions) domain.Agent {
	return &GraphAgent{llm: llm, tools: tools, opts: opts}
}

// Name 返回 Agent 名称 "graph"。
func (a *GraphAgent) Name() string { return graphAgentName }

// Run 执行图谱推理主流程。
//
// 流程：
//  1. 从 input.Message 或 input.State["query"] 取查询文本
//  2. 调用 llm.CompleteWithStructuredOutput 生成参数化 Cypher
//  3. LLM 失败时用规则兜底（基于关键词匹配实体类型）
//  4. 调用 tools.Invoke(ctx, "graph.query", cypherInput) 执行 Cypher（tools 为 nil 跳过）
//  5. 解析结果填充 domain.GraphKnowledge
//  6. State 写入 graph_knowledge/graph_trace；Result 写入 GraphKnowledge；
//     Trace 追加 "graph.generate_cypher"/"graph.execute"
func (a *GraphAgent) Run(ctx context.Context, input domain.AgentInput) (domain.AgentOutput, error) {
	if a == nil {
		return domain.AgentOutput{}, fmt.Errorf("graph agent: nil agent")
	}
	query := extractQuery(input)
	trace := make([]string, 0, 2)
	graphTrace := make([]map[string]any, 0, 2)

	// 1. 调用 LLM 生成参数化 Cypher。
	cypher, params, entities, llmErr := a.generateCypher(ctx, query)
	trace = append(trace, "graph.generate_cypher")
	graphTrace = append(graphTrace, map[string]any{
		"step":    "generate_cypher",
		"source":  llmSource(llmErr),
		"query":   query,
		"cypher":  cypher,
		"params":  params,
		"entityN": len(entities),
	})
	if cypher == "" {
		// LLM 失败且兜底也未生成（如 query 为空）。
		gk := domain.GraphKnowledge{Entities: entities, Cypher: cypher}
		return domain.AgentOutput{
			State: map[string]any{
				"graph_knowledge": gk,
				"graph_trace":     graphTrace,
			},
			Result: gk,
			Trace:  trace,
		}, nil
	}

	// 2. 调用 graph.query 工具执行 Cypher（tools 为 nil 跳过）。
	nodes, execErr := a.executeCypher(ctx, cypher, params)
	trace = append(trace, "graph.execute")
	graphTrace = append(graphTrace, map[string]any{
		"step":    "execute",
		"nodeN":   len(nodes),
		"execErr": errString(execErr),
	})

	// 3. 解析结果填充 GraphKnowledge（执行失败也保留 Cypher 与 LLM 抽取的实体）。
	gk := buildGraphKnowledge(cypher, entities, nodes)

	// 4. 组装输出。
	return domain.AgentOutput{
		State: map[string]any{
			"graph_knowledge": gk,
			"graph_trace":     graphTrace,
		},
		Result: gk,
		Trace:  trace,
	}, nil
}

// generateCypher 调用 LLM 生成参数化 Cypher，失败时走规则兜底。
// 返回 (cypher, params, entities, err)；err 非 nil 表示 LLM 失败已走兜底。
func (a *GraphAgent) generateCypher(ctx context.Context, query string) (string, map[string]any, []domain.Entity, error) {
	// 空查询直接返回空。
	if strings.TrimSpace(query) == "" {
		return "", nil, nil, nil
	}
	// 先尝试 LLM 生成。
	if a.llm != nil {
		prompt := buildCypherPrompt(query)
		raw, err := a.llm.CompleteWithStructuredOutput(ctx, prompt, cypherSchema)
		if err == nil && len(raw) > 0 {
			cy, ps, ents, parseErr := parseLLMCypher(raw)
			if parseErr == nil && cy != "" {
				return cy, ps, ents, nil
			}
		}
		// LLM 失败 → 走规则兜底。
		cy, ps, ents := fallbackCypher(query)
		return cy, ps, ents, fmt.Errorf("llm generate cypher failed: %v", err)
	}
	// 无 LLM → 直接走规则兜底。
	cy, ps, ents := fallbackCypher(query)
	return cy, ps, ents, fmt.Errorf("llm client not injected")
}

// executeCypher 调用 graph.query 工具执行 Cypher。
// tools 为 nil 时返回空节点列表（不报错），由上层根据 trace 判断是否跳过。
func (a *GraphAgent) executeCypher(ctx context.Context, cypher string, params map[string]any) ([]domain.GraphNode, error) {
	if a.tools == nil {
		return nil, nil
	}
	input := map[string]any{
		"cypher": cypher,
		"params": params,
	}
	raw, err := a.tools.Invoke(ctx, "graph.query", mustMarshal(input))
	if err != nil {
		return nil, fmt.Errorf("graph.query invoke: %w", err)
	}
	return parseGraphNodes(raw)
}

// buildCypherPrompt 构造 LLM 生成 Cypher 的 prompt，包含 schema 描述与查询。
func buildCypherPrompt(query string) string {
	var sb strings.Builder
	sb.WriteString("你是知识图谱查询专家。基于以下图谱 schema 生成参数化 Cypher 查询。\n\n")
	sb.WriteString("图谱节点类型：Article/Tag/Author/IP/Channel/Entity\n")
	sb.WriteString("关系类型：WROTE_BY/HAS_TAG/BELONGS_IP/MENTIONED_IN/CO_OCCURRED_WITH/SIMILAR_TO\n")
	sb.WriteString("强约束：用户输入必须用 $param 占位符绑定，禁止字符串拼接，避免 Cypher 注入。\n")
	sb.WriteString("返回 JSON：{cypher, params, entities}\n\n")
	sb.WriteString("查询：")
	sb.WriteString(query)
	return sb.String()
}

// fallbackCypher 基于关键词匹配实体类型的规则兜底，生成参数化 Cypher。
// 识别前缀关键词：作者:/IP:/标签:/标题:/话题: ；无前缀时按默认实体模板。
func fallbackCypher(query string) (string, map[string]any, []domain.Entity) {
	q := strings.TrimSpace(query)
	params := map[string]any{"topk": defaultTopK}
	var entities []domain.Entity

	switch {
	case strings.HasPrefix(q, "作者:") || strings.HasPrefix(q, "作者："):
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "作者:"), "作者："))
		params["author_name"] = name
		entities = []domain.Entity{{Name: name, Type: "Author"}}
		return cypherTplAuthor, params, entities
	case strings.HasPrefix(q, "IP:") || strings.HasPrefix(q, "IP："):
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "IP:"), "IP："))
		params["ip_name"] = name
		entities = []domain.Entity{{Name: name, Type: "IP"}}
		return cypherTplIP, params, entities
	case strings.HasPrefix(q, "标签:") || strings.HasPrefix(q, "标签："):
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "标签:"), "标签："))
		params["tag_name"] = name
		entities = []domain.Entity{{Name: name, Type: "Tag"}}
		return cypherTplTag, params, entities
	case strings.HasPrefix(q, "标题:") || strings.HasPrefix(q, "标题："):
		kw := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "标题:"), "标题："))
		params["title_keyword"] = kw
		entities = []domain.Entity{{Name: kw, Type: "Title"}}
		return cypherTplTitle, params, entities
	case strings.HasPrefix(q, "话题:") || strings.HasPrefix(q, "话题："):
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "话题:"), "话题："))
		params["topic_name"] = name
		entities = []domain.Entity{{Name: name, Type: "Topic"}}
		return cypherTplTopic, params, entities
	default:
		params["entity_name"] = q
		entities = []domain.Entity{{Name: q, Type: "Entity"}}
		return cypherTplDefault, params, entities
	}
}

// parseLLMCypher 解析 LLM 返回的 JSON，提取 cypher/params/entities。
func parseLLMCypher(raw json.RawMessage) (string, map[string]any, []domain.Entity, error) {
	var out struct {
		Cypher   string          `json:"cypher"`
		Params   map[string]any  `json:"params"`
		Entities []domain.Entity `json:"entities"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, nil, fmt.Errorf("unmarshal llm cypher: %w", err)
	}
	if out.Params == nil {
		out.Params = map[string]any{}
	}
	return out.Cypher, out.Params, out.Entities, nil
}

// parseGraphNodes 解析 graph.query 工具返回的 JSON 为 GraphNode 列表。
// 支持两种格式：{"nodes":[...]} 或直接 [...]。
func parseGraphNodes(raw json.RawMessage) ([]domain.GraphNode, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// 优先尝试 {"nodes":[...]} 包装格式。
	var wrapped struct {
		Nodes []domain.GraphNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Nodes != nil {
		return wrapped.Nodes, nil
	}
	// 退化尝试直接 [...]。
	var nodes []domain.GraphNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("parse graph nodes: %w", err)
	}
	return nodes, nil
}

// buildGraphKnowledge 组装 GraphKnowledge，从节点列表抽取 Articles/Authors/IPs。
func buildGraphKnowledge(cypher string, entities []domain.Entity, nodes []domain.GraphNode) domain.GraphKnowledge {
	gk := domain.GraphKnowledge{
		Entities: entities,
		Cypher:   cypher,
	}
	seenArticle := map[string]struct{}{}
	seenAuthor := map[string]struct{}{}
	seenIP := map[string]struct{}{}
	for _, n := range nodes {
		switch n.Type {
		case "Article":
			id := pickStringProp(n.Props, "id", n.ID)
			if id != "" {
				if _, ok := seenArticle[id]; !ok {
					seenArticle[id] = struct{}{}
					gk.Articles = append(gk.Articles, id)
				}
			}
		case "Author":
			name := pickStringProp(n.Props, "name", n.ID)
			if name != "" {
				if _, ok := seenAuthor[name]; !ok {
					seenAuthor[name] = struct{}{}
					gk.Authors = append(gk.Authors, name)
				}
			}
		case "IP":
			name := pickStringProp(n.Props, "name", n.ID)
			if name != "" {
				if _, ok := seenIP[name]; !ok {
					seenIP[name] = struct{}{}
					gk.IPs = append(gk.IPs, name)
				}
			}
		}
	}
	return gk
}

// extractQuery 从 AgentInput 取查询文本：优先 Message，其次 State["query"]。
func extractQuery(input domain.AgentInput) string {
	if strings.TrimSpace(input.Message) != "" {
		return input.Message
	}
	if input.State != nil {
		if q, ok := input.State["query"].(string); ok {
			return q
		}
	}
	return ""
}

// pickStringProp 从节点 Props 取字符串字段，缺省回退到 fallback。
func pickStringProp(props map[string]any, key, fallback string) string {
	if props == nil {
		return fallback
	}
	if v, ok := props[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// mustMarshal 将任意值序列化为 JSON，失败返回空 JSON 对象。
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// llmSource 返回 Cypher 生成来源标签（用于 trace）。
func llmSource(err error) string {
	if err == nil {
		return "llm"
	}
	return "rule_fallback"
}

// errString 安全转 error 为字符串（nil 返回空）。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// 编译期断言：GraphAgent 实现 domain.Agent。
var _ domain.Agent = (*GraphAgent)(nil)
