// Package agent graph_agent_test.go — GraphAgent 单元测试（Task 11.4）。
//
// 该文件用 stdlib 手写 stub（不引入 testify）覆盖 GraphAgent 的：
//   - 正常路径（LLM 生成 Cypher + 工具执行返回节点）
//   - LLM 失败回退规则兜底（按前缀关键词生成参数化 Cypher）
//   - tools 为 nil 跳过图谱执行
//   - Cypher 必须参数化（$param 占位符）
//   - 空 query 直接返回空 GraphKnowledge
//   - 工具执行失败仍返回 Cypher 与实体
//   - State/Trace/Result 字段写入正确
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"sea/service/recommend/rpc/internal/domain"
)

// ----------------------------------------------------------------------------
// stub 实现
// ----------------------------------------------------------------------------

// stubLLMForGraph 测试用 LLMClient stub（GraphAgent 专用）。
type stubLLMForGraph struct {
	structuredOut json.RawMessage
	structuredErr error
	completeOut   string
	completeErr   error
	logprobsOut   string
	logprobsErr   error
	calls         int
}

func (s *stubLLMForGraph) Complete(_ context.Context, _ string, _ LLMOptions) (string, error) {
	s.calls++
	return s.completeOut, s.completeErr
}

func (s *stubLLMForGraph) CompleteWithStructuredOutput(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	s.calls++
	return s.structuredOut, s.structuredErr
}

func (s *stubLLMForGraph) CompleteWithLogprobs(_ context.Context, _ string, _ int) (string, [][]LogprobEntry, error) {
	return s.logprobsOut, nil, s.logprobsErr
}

// stubToolsForGraph 测试用 ToolExecutor stub（GraphAgent 专用）。
type stubToolsForGraph struct {
	out       json.RawMessage
	err       error
	lastName  string
	lastInput json.RawMessage
	calls     int
}

func (s *stubToolsForGraph) Invoke(_ context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	s.calls++
	s.lastName = name
	s.lastInput = input
	if s.err != nil {
		return nil, s.err
	}
	return s.out, nil
}

func (s *stubToolsForGraph) List(_ context.Context) ([]string, error) {
	return []string{"graph.query"}, nil
}

// wrapNodes 包装 GraphNode 列表为 {"nodes":[...]} JSON。
func wrapNodes(nodes []domain.GraphNode) json.RawMessage {
	out, _ := json.Marshal(map[string]any{"nodes": nodes})
	return out
}

// ----------------------------------------------------------------------------
// 测试用例
// ----------------------------------------------------------------------------

// TestGraphAgent_Name 验证 Name 返回 "graph"。
func TestGraphAgent_Name(t *testing.T) {
	a := NewGraphAgent(&stubLLMForGraph{}, nil, domain.AgentOptions{})
	if a.Name() != "graph" {
		t.Errorf("Name = %q, 期望 graph", a.Name())
	}
}

// TestGraphAgent_Run_Normal 验证正常路径：LLM 生成 Cypher + 工具执行返回节点。
func TestGraphAgent_Run_Normal(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(rec:Article) RETURN rec LIMIT $topk",
			"params": map[string]any{"entity_name": "旅游", "topk": 10},
			"entities": []map[string]any{
				{"name": "旅游", "type": "Topic"},
			},
		}),
	}
	tools := &stubToolsForGraph{out: wrapNodes([]domain.GraphNode{
		{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}},
		{ID: "a2", Type: "Article", Props: map[string]any{"id": "a2"}},
	})}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "旅游"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("期望 LLM 调用 1 次, 实际 %d", llm.calls)
	}
	if tools.calls != 1 {
		t.Errorf("期望 tools 调用 1 次, 实际 %d", tools.calls)
	}
	if tools.lastName != "graph.query" {
		t.Errorf("工具名 = %q, 期望 graph.query", tools.lastName)
	}
	gk, ok := got.Result.(domain.GraphKnowledge)
	if !ok {
		t.Fatalf("Result 类型断言失败: %T", got.Result)
	}
	if len(gk.Articles) != 2 {
		t.Errorf("期望 2 篇文章, 实际 %d", len(gk.Articles))
	}
	if gk.Articles[0] != "a1" {
		t.Errorf("Articles[0] = %q, 期望 a1", gk.Articles[0])
	}
	if len(gk.Entities) != 1 {
		t.Errorf("期望 1 个实体, 实际 %d", len(gk.Entities))
	}
	if gk.Entities[0].Name != "旅游" {
		t.Errorf("Entities[0].Name = %q, 期望 旅游", gk.Entities[0].Name)
	}
	if gk.Cypher == "" {
		t.Errorf("Cypher 不应为空")
	}
	// Trace 应包含 generate_cypher 与 execute。
	if len(got.Trace) != 2 {
		t.Errorf("期望 2 步 trace, 实际 %d", len(got.Trace))
	}
	if got.Trace[0] != "graph.generate_cypher" || got.Trace[1] != "graph.execute" {
		t.Errorf("Trace = %v, 期望 [graph.generate_cypher graph.execute]", got.Trace)
	}
	// State 字段。
	if _, ok := got.State["graph_knowledge"]; !ok {
		t.Errorf("State 缺少 graph_knowledge")
	}
	if _, ok := got.State["graph_trace"]; !ok {
		t.Errorf("State 缺少 graph_trace")
	}
}

// TestGraphAgent_Run_LLMFailFallback 验证 LLM 失败回退规则兜底生成参数化 Cypher。
func TestGraphAgent_Run_LLMFailFallback(t *testing.T) {
	llm := &stubLLMForGraph{structuredErr: errors.New("llm down")}
	tools := &stubToolsForGraph{out: wrapNodes([]domain.GraphNode{
		{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}},
	})}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "作者:张三"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if !strings.Contains(gk.Cypher, "$author_name") {
		t.Errorf("Cypher 应参数化包含 $author_name, 实际: %s", gk.Cypher)
	}
	if !strings.Contains(gk.Cypher, "Author") {
		t.Errorf("Cypher 应包含 Author 节点, 实际: %s", gk.Cypher)
	}
	// 校验工具收到的 params 包含 author_name=张三。
	var in map[string]any
	if err := json.Unmarshal(tools.lastInput, &in); err != nil {
		t.Fatalf("解析工具输入失败: %v", err)
	}
	params, _ := in["params"].(map[string]any)
	if params["author_name"] != "张三" {
		t.Errorf("params.author_name = %v, 期望 张三", params["author_name"])
	}
	// 验证实体类型为 Author。
	if len(gk.Entities) != 1 || gk.Entities[0].Type != "Author" {
		t.Errorf("Entities = %v, 期望 [{张三 Author}]", gk.Entities)
	}
}

// TestGraphAgent_Run_NilTools 验证 tools 为 nil 时跳过图谱执行，仍返回 Cypher 与实体。
func TestGraphAgent_Run_NilTools(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(rec:Article) RETURN rec LIMIT $topk",
			"params": map[string]any{"entity_name": "AI", "topk": 10},
		}),
	}
	a := NewGraphAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "AI"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if gk.Cypher == "" {
		t.Errorf("Cypher 不应为空")
	}
	if len(gk.Articles) != 0 {
		t.Errorf("tools 为 nil 时 Articles 应为空, 实际 %d", len(gk.Articles))
	}
	// Trace 仍应包含 execute 步骤（跳过但记录）。
	if len(got.Trace) != 2 {
		t.Errorf("期望 2 步 trace, 实际 %d", len(got.Trace))
	}
}

// TestGraphAgent_Run_NilLLM 验证 llm 为 nil 时直接走规则兜底。
func TestGraphAgent_Run_NilLLM(t *testing.T) {
	tools := &stubToolsForGraph{out: wrapNodes(nil)}
	a := NewGraphAgent(nil, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "IP:旅游频道"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if !strings.Contains(gk.Cypher, "$ip_name") {
		t.Errorf("Cypher 应参数化包含 $ip_name, 实际: %s", gk.Cypher)
	}
}

// TestGraphAgent_Run_EmptyQuery 验证空 query 返回空 GraphKnowledge。
func TestGraphAgent_Run_EmptyQuery(t *testing.T) {
	llm := &stubLLMForGraph{structuredOut: mustMarshal(map[string]any{"cypher": ""})}
	a := NewGraphAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: ""})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if gk.Cypher != "" {
		t.Errorf("空 query 时 Cypher 应为空, 实际: %s", gk.Cypher)
	}
	if llm.calls != 0 {
		t.Errorf("空 query 时不应调用 LLM, 实际 %d 次", llm.calls)
	}
	// Trace 仅含 generate_cypher（无 execute）。
	if len(got.Trace) != 1 || got.Trace[0] != "graph.generate_cypher" {
		t.Errorf("Trace = %v, 期望 [graph.generate_cypher]", got.Trace)
	}
}

// TestGraphAgent_Run_StateQuery 验证从 State["query"] 取查询文本。
func TestGraphAgent_Run_StateQuery(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (rec:Article) WHERE rec.title CONTAINS $title_keyword RETURN rec LIMIT $topk",
			"params": map[string]any{"title_keyword": "AI", "topk": 10},
		}),
	}
	a := NewGraphAgent(llm, nil, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{
		State: map[string]any{"query": "AI"},
	})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	if llm.calls != 1 {
		t.Errorf("期望 LLM 调用 1 次, 实际 %d", llm.calls)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if gk.Cypher == "" {
		t.Errorf("Cypher 不应为空")
	}
}

// TestGraphAgent_Run_ToolExecFail 验证工具执行失败仍返回 Cypher 与实体。
func TestGraphAgent_Run_ToolExecFail(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (e:Entity {name: $entity_name})-[:MENTIONED_IN]-(rec:Article) RETURN rec LIMIT $topk",
			"params": map[string]any{"entity_name": "旅游", "topk": 10},
		}),
	}
	tools := &stubToolsForGraph{err: errors.New("graph down")}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "旅游"})
	if err != nil {
		t.Fatalf("工具失败不应返回错误, 实际 %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if gk.Cypher == "" {
		t.Errorf("工具失败时 Cypher 仍应保留")
	}
	if len(gk.Articles) != 0 {
		t.Errorf("工具失败时 Articles 应为空, 实际 %d", len(gk.Articles))
	}
}

// TestGraphAgent_Run_CypherParameterized 验证所有内置模板均使用 $param 参数化。
func TestGraphAgent_Run_CypherParameterized(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		paramKey string
	}{
		{"作者", "作者:张三", "$author_name"},
		{"IP", "IP:旅游", "$ip_name"},
		{"标签", "标签:科技", "$tag_name"},
		{"标题", "标题:AI", "$title_keyword"},
		{"话题", "话题:深度学习", "$topic_name"},
		{"默认", "AI", "$entity_name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewGraphAgent(nil, nil, domain.AgentOptions{})
			got, err := a.Run(context.Background(), domain.AgentInput{Message: c.query})
			if err != nil {
				t.Fatalf("Run 错误: %v", err)
			}
			gk := got.Result.(domain.GraphKnowledge)
			if !strings.Contains(gk.Cypher, c.paramKey) {
				t.Errorf("Cypher 应包含 %s, 实际: %s", c.paramKey, gk.Cypher)
			}
			// 严禁出现字符串拼接用户输入（无单引号包裹的 query 内容）。
			if strings.Contains(gk.Cypher, "'"+c.query+"'") {
				t.Errorf("Cypher 不应字符串拼接用户输入: %s", gk.Cypher)
			}
		})
	}
}

// TestGraphAgent_Run_BuildGraphKnowledge 验证从节点抽取 Authors/IPs。
func TestGraphAgent_Run_BuildGraphKnowledge(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (au:Author {name: $author_name})-[:WROTE]->(rec:Article) RETURN rec LIMIT $topk",
			"params": map[string]any{"author_name": "张三", "topk": 10},
		}),
	}
	tools := &stubToolsForGraph{out: wrapNodes([]domain.GraphNode{
		{ID: "a1", Type: "Article", Props: map[string]any{"id": "a1"}},
		{ID: "au1", Type: "Author", Props: map[string]any{"name": "张三"}},
		{ID: "ip1", Type: "IP", Props: map[string]any{"name": "旅游IP"}},
	})}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, _ := a.Run(context.Background(), domain.AgentInput{Message: "作者:张三"})
	gk := got.Result.(domain.GraphKnowledge)
	if len(gk.Articles) != 1 || gk.Articles[0] != "a1" {
		t.Errorf("Articles = %v, 期望 [a1]", gk.Articles)
	}
	if len(gk.Authors) != 1 || gk.Authors[0] != "张三" {
		t.Errorf("Authors = %v, 期望 [张三]", gk.Authors)
	}
	if len(gk.IPs) != 1 || gk.IPs[0] != "旅游IP" {
		t.Errorf("IPs = %v, 期望 [旅游IP]", gk.IPs)
	}
}

// TestGraphAgent_Run_PartialLLMOutput 验证 LLM 仅返回 cypher（无 params）时正常工作。
func TestGraphAgent_Run_PartialLLMOutput(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: mustMarshal(map[string]any{
			"cypher": "MATCH (rec:Article) RETURN rec LIMIT 10",
		}),
	}
	tools := &stubToolsForGraph{out: wrapNodes(nil)}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "AI"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	if gk.Cypher == "" {
		t.Errorf("Cypher 不应为空")
	}
}

// TestGraphAgent_Run_LLMInvalidJSON 验证 LLM 返回无效 JSON 时走规则兜底。
func TestGraphAgent_Run_LLMInvalidJSON(t *testing.T) {
	llm := &stubLLMForGraph{
		structuredOut: json.RawMessage("{invalid json}"),
	}
	tools := &stubToolsForGraph{out: wrapNodes(nil)}
	a := NewGraphAgent(llm, tools, domain.AgentOptions{})

	got, err := a.Run(context.Background(), domain.AgentInput{Message: "AI"})
	if err != nil {
		t.Fatalf("Run 错误: %v", err)
	}
	gk := got.Result.(domain.GraphKnowledge)
	// 走规则兜底，Cypher 应包含 $entity_name。
	if !strings.Contains(gk.Cypher, "$entity_name") {
		t.Errorf("LLM 无效 JSON 应走规则兜底, Cypher: %s", gk.Cypher)
	}
}

// TestFallbackCypher_AllPrefixes 验证 fallbackCypher 各前缀分支。
func TestFallbackCypher_AllPrefixes(t *testing.T) {
	cases := []struct {
		query    string
		typ      string
		paramKey string
	}{
		{"作者:张三", "Author", "author_name"},
		{"作者：李四", "Author", "author_name"},
		{"IP:旅游", "IP", "ip_name"},
		{"IP：科技", "IP", "ip_name"},
		{"标签:AI", "Tag", "tag_name"},
		{"标签：ML", "Tag", "tag_name"},
		{"标题:Go", "Title", "title_keyword"},
		{"标题：Rust", "Title", "title_keyword"},
		{"话题:深度学习", "Topic", "topic_name"},
		{"话题：聚类", "Topic", "topic_name"},
		{"裸查询", "Entity", "entity_name"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			cy, ps, ents := fallbackCypher(c.query)
			if _, ok := ps[c.paramKey]; !ok {
				t.Errorf("params 缺少 %s, 实际: %v", c.paramKey, ps)
			}
			if len(ents) != 1 || ents[0].Type != c.typ {
				t.Errorf("Entities = %v, 期望 Type=%s", ents, c.typ)
			}
			if cy == "" {
				t.Errorf("Cypher 不应为空")
			}
		})
	}
}
