package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const testManifest = `{
 "schema_version": 1, "graph": "course-computer-network",
 "nodes": {
  "layers": [{"id":"L-transport","title":"运输层","order":2,"book_chapter":"第3章","interview_weight":"core","milestone":"能画出三次握手"}],
  "units": [{"id":"U-tcp","title":"TCP 连接管理","sessions":2,"session_minutes":90,"micro_action":"手画三次握手时序图"}],
  "concepts": [{"id":"C-3way","name":"三次握手","one_liner":"同步双方初始序号","interview_freq":"high","black_book_ref":"§3.5"}],
  "questions": [{"id":"Q-3way-why","text":"为什么握手是三次?","difficulty":"base","interview_freq":"high"}],
  "materials": [{"id":"M-x-tcp","kind":"article","title":"TCP 面试题","ref_or_url":"https://xiaolincoding.com/network/3_tcp/tcp_interview.html","verified":true}]
 },
 "edges": {
  "belongs_to": [{"unit":"U-tcp","layer":"L-transport","order":1}],
  "prerequisite": [],
  "covers": [{"unit":"U-tcp","concept":"C-3way","depth":"core"}],
  "tests": [{"question":"Q-3way-why","concept":"C-3way"}],
  "anchors": [{"material":"M-x-tcp","concept":"C-3way","verified":true,"ref":""}],
  "reads": [{"unit":"U-tcp","material":"M-x-tcp","order":1,"book_ref":""}]
 }
}`

func baseGraph(t *testing.T) *GraphContent {
	t.Helper()
	g, err := ReplayGraph([]byte(testManifest), "course:test")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestReplayDerivesValidGraph(t *testing.T) {
	g := baseGraph(t)
	if len(g.Nodes) != 5 || len(g.Edges) != 5 {
		t.Fatalf("派生数量不符: nodes=%d edges=%d", len(g.Nodes), len(g.Edges))
	}
	byType := map[string]int{}
	for _, n := range g.Nodes {
		byType[n.Type]++
	}
	if byType["layer"] != 1 || byType["unit"] != 1 || byType["concept"] != 1 || byType["question"] != 1 || byType["material"] != 1 {
		t.Fatalf("五类节点分型不符: %v", byType)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	a, _ := ReplayGraph([]byte(testManifest), "course:test")
	b, _ := ReplayGraph([]byte(testManifest), "course:test")
	ja, _ := CanonicalGraphJSON(a)
	jb, _ := CanonicalGraphJSON(b)
	if string(ja) != string(jb) {
		t.Fatal("同输入重放哈希不一致")
	}
}

func TestUnknownNodePropertyRejected(t *testing.T) {
	g := baseGraph(t)
	g.Nodes[2].Props["hack"] = "x"
	if err := ValidateGraph(g); err == nil || !strings.Contains(err.Error(), "未知属性") {
		t.Fatalf("未知属性未被拒绝: %v", err)
	}
}

func TestNodeEdgePropertyMixingRejected(t *testing.T) {
	g := baseGraph(t)
	// 边上挂节点 schema 的保留键(milestone 属于 layer 节点)
	g.Edges[0].Props["milestone"] = "x"
	err := ValidateGraph(g)
	if err == nil || !strings.Contains(err.Error(), "不可混用") {
		t.Fatalf("属性混用未被拒绝: %v", err)
	}
	// 节点上挂边类型名
	g2 := baseGraph(t)
	g2.Nodes[0].Props["covers"] = "x"
	err = ValidateGraph(g2)
	if err == nil || !strings.Contains(err.Error(), "不可混用") {
		t.Fatalf("节点挂边类型名未被拒绝: %v", err)
	}
}

func TestDanglingEdgeRejected(t *testing.T) {
	g := baseGraph(t)
	g.Edges = append(g.Edges, GraphEdge{Type: "covers", From: "U-tcp", To: "C-nonexistent", Props: map[string]any{"depth": "core"}})
	if err := ValidateGraph(g); err == nil || !strings.Contains(err.Error(), "悬空") {
		t.Fatalf("悬空边未被拒绝: %v", err)
	}
}

func TestEdgeEndpointTypeMismatchRejected(t *testing.T) {
	g := baseGraph(t)
	// covers 必须 unit→concept;这里接 layer→concept
	g.Edges = append(g.Edges, GraphEdge{Type: "covers", From: "L-transport", To: "C-3way", Props: map[string]any{"depth": "core"}})
	if err := ValidateGraph(g); err == nil || !strings.Contains(err.Error(), "from 必须是") {
		t.Fatalf("边端点类型错误未被拒绝: %v", err)
	}
}

func TestPrerequisiteCycleRejected(t *testing.T) {
	g := baseGraph(t)
	// 构造两层的环:L-transport → L2 → L-transport
	g.Nodes = append(g.Nodes, GraphNode{Type: "layer", ID: "L2", Props: map[string]any{"title": "二层", "order": float64(3), "book_chapter": "x", "interview_weight": "core", "milestone": "y"}})
	g.Edges = append(g.Edges,
		GraphEdge{Type: "prerequisite", From: "L-transport", To: "L2"},
		GraphEdge{Type: "prerequisite", From: "L2", To: "L-transport"})
	if err := ValidateGraph(g); err == nil || !strings.Contains(err.Error(), "环") {
		t.Fatalf("先修环未被拒绝: %v", err)
	}
}

func TestInvalidEnumAndRangeRejected(t *testing.T) {
	g := baseGraph(t)
	g.Nodes[0].Props["interview_weight"] = "mega"
	if err := ValidateGraph(g); err == nil {
		t.Fatal("枚举越界未被拒绝")
	}
	g2 := baseGraph(t)
	g2.Nodes[1].Props["session_minutes"] = float64(30)
	if err := ValidateGraph(g2); err == nil || !strings.Contains(err.Error(), "session_minutes") {
		t.Fatalf("会话时长越界未被拒绝: %v", err)
	}
}

func TestApplyOpsValidModificationAndImmutability(t *testing.T) {
	base := baseGraph(t)
	baseRaw, _ := json.Marshal(base)
	next, err := ApplyOps(base, []GraphOp{{
		Kind: "set_node",
		Node: &GraphNode{Type: "concept", ID: "C-3way", Props: map[string]any{
			"name": "三次握手", "one_liner": "修订后的本质", "interview_freq": "high", "black_book_ref": "§3.5"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	nextRaw, _ := json.Marshal(next)
	if string(baseRaw) == string(nextRaw) {
		t.Fatal("操作集未生效")
	}
	// base 未被就地修改
	if base.Nodes[2].Props["one_liner"] != "同步双方初始序号" {
		t.Fatalf("base 被就地修改: %v", base.Nodes[2].Props["one_liner"])
	}
}

func TestApplyOpsRejectsInvalidResult(t *testing.T) {
	base := baseGraph(t)
	_, err := ApplyOps(base, []GraphOp{{
		Kind: "set_node",
		Node: &GraphNode{Type: "concept", ID: "C-new", Props: map[string]any{"name": "x", "one_liner": "y", "interview_freq": "high", "bogus": 1}},
	}})
	if err == nil {
		t.Fatal("含未知属性的操作集未被拒绝")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
