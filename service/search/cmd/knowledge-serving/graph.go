// 内容知识图谱:图模型 schema(节点/边分型分离)+ 严格校验 + manifest 重放派生 + 修订操作。
// 原则见 Sea-Docs/文档/技术/内容知识图谱方案.md:原始数据(RTW manifest)并存、
// 修订链可修改、重放可验证、节点与边属性严格分离(互不混用、未知属性拒绝)。
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ---------- 图内容模型 ----------

const graphSchemaVersion = 1

type GraphNode struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Props map[string]any `json:"props"`
}

type GraphEdge struct {
	Type  string         `json:"type"`
	From  string         `json:"from"`
	To    string         `json:"to"`
	Props map[string]any `json:"props,omitempty"`
}

type GraphContent struct {
	SchemaVersion int         `json:"schema_version"`
	GraphID       string      `json:"graph_id"`
	Nodes         []GraphNode `json:"nodes"`
	Edges         []GraphEdge `json:"edges"`
}

// ---------- 节点 schema(分型定义)----------

type propSpec struct {
	kind     string // "string" | "int" | "bool"
	required bool
	enum     []string
	enumInt  []int
	minInt   *int
	maxInt   *int
}

var nodeSchemas = map[string]map[string]propSpec{
	"layer": {
		"title":            {kind: "string", required: true},
		"order":            {kind: "int", required: true, minInt: ptrInt(1), maxInt: ptrInt(99)},
		"book_chapter":     {kind: "string", required: true},
		"interview_weight": {kind: "string", required: true, enum: []string{"core", "important", "general"}},
		"milestone":        {kind: "string", required: true},
	},
	"unit": {
		"title":           {kind: "string", required: true},
		"sessions":        {kind: "int", required: true, minInt: ptrInt(1), maxInt: ptrInt(8)},
		"session_minutes": {kind: "int", required: true, enumInt: []int{45, 60, 90, 120}},
		"micro_action":    {kind: "string", required: true},
	},
	"concept": {
		"name":           {kind: "string", required: true},
		"one_liner":      {kind: "string", required: true},
		"interview_freq": {kind: "string", required: true, enum: []string{"high", "mid", "low"}},
		"black_book_ref": {kind: "string", required: false},
	},
	"question": {
		"text":           {kind: "string", required: true},
		"difficulty":     {kind: "string", required: true, enum: []string{"base", "advanced"}},
		"interview_freq": {kind: "string", required: true, enum: []string{"high", "mid", "low"}},
	},
	"material": {
		"kind":       {kind: "string", required: true, enum: []string{"book", "article"}},
		"title":      {kind: "string", required: true},
		"ref_or_url": {kind: "string", required: true},
		"verified":   {kind: "bool", required: true},
	},
}

var nodeTypes = func() []string {
	out := make([]string, 0, len(nodeSchemas))
	for t := range nodeSchemas {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}()

// ---------- 边 schema(分型定义,与节点属性严格互斥)----------

type edgeSpec struct {
	fromType string
	toType   string
	props    map[string]propSpec
}

var edgeSchemas = map[string]edgeSpec{
	"belongs_to":   {fromType: "unit", toType: "layer", props: map[string]propSpec{"order": {kind: "int", required: true, minInt: ptrInt(1), maxInt: ptrInt(99)}}},
	"prerequisite": {fromType: "layer", toType: "layer", props: map[string]propSpec{}},
	"covers":       {fromType: "unit", toType: "concept", props: map[string]propSpec{"depth": {kind: "string", required: true, enum: []string{"core", "extended"}}}},
	"tests":        {fromType: "question", toType: "concept", props: map[string]propSpec{}},
	"anchors":      {fromType: "material", toType: "concept", props: map[string]propSpec{"verified": {kind: "bool", required: true}, "ref": {kind: "string", required: false}}},
	"reads":        {fromType: "unit", toType: "material", props: map[string]propSpec{"order": {kind: "int", required: true, minInt: ptrInt(1), maxInt: ptrInt(99)}, "book_ref": {kind: "string", required: false}}},
}

// nodeReservedKeys/edgeReservedKeys:属性混用检测的保留键集合。
func schemaKeys(m map[string]propSpec) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// ---------- 校验 ----------

// ValidateGraph 校验整份图内容:节点分型、边分型、悬空边、重复 id、属性混用。
func ValidateGraph(g *GraphContent) error {
	if g.SchemaVersion != graphSchemaVersion {
		return fmt.Errorf("schema_version 必须为 %d", graphSchemaVersion)
	}
	if g.GraphID == "" || len(g.Nodes) == 0 {
		return fmt.Errorf("graph_id 与节点不可为空")
	}
	nodeByID := map[string]*GraphNode{}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if err := validateNode(n); err != nil {
			return fmt.Errorf("node[%d] %s: %w", i, n.ID, err)
		}
		if _, dup := nodeByID[n.ID]; dup {
			return fmt.Errorf("节点 id 重复: %s", n.ID)
		}
		nodeByID[n.ID] = n
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		spec, ok := edgeSchemas[e.Type]
		if !ok {
			return fmt.Errorf("edge[%d]: 未知边类型 %q", i, e.Type)
		}
		from, okF := nodeByID[e.From]
		if !okF {
			return fmt.Errorf("edge[%d] %s: 悬空边(from=%s 不存在)", i, e.Type, e.From)
		}
		to, okT := nodeByID[e.To]
		if !okT {
			return fmt.Errorf("edge[%d] %s: 悬空边(to=%s 不存在)", i, e.Type, e.To)
		}
		if from.Type != spec.fromType {
			return fmt.Errorf("edge[%d] %s: from 必须是 %s,实际 %s(%s)", i, e.Type, spec.fromType, from.Type, e.From)
		}
		if to.Type != spec.toType {
			return fmt.Errorf("edge[%d] %s: to 必须是 %s,实际 %s(%s)", i, e.Type, spec.toType, to.Type, e.To)
		}
		if err := validateProps(e.Props, spec.props, "edge "+e.Type); err != nil {
			return fmt.Errorf("edge[%d] %s(%s→%s): %w", i, e.Type, e.From, e.To, err)
		}
		// 属性混用:边属性里出现任何节点 schema 保留键即拒绝(仅当该键不属于本边 schema)。
		ownKeys := schemaKeys(spec.props)
		for _, nt := range nodeTypes {
			for k := range nodeSchemas[nt] {
				if !ownKeys[k] {
					if _, present := e.Props[k]; present {
						return fmt.Errorf("edge[%d] %s: 属性 %q 属于节点 schema(%s),节点/边属性不可混用", i, e.Type, k, nt)
					}
				}
			}
		}
	}
	// 图级:先修关系成环检测(仅层间)
	layers := map[string]map[string]bool{}
	for _, e := range g.Edges {
		if e.Type != "prerequisite" {
			continue
		}
		if layers[e.From] == nil {
			layers[e.From] = map[string]bool{}
		}
		layers[e.From][e.To] = true
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var dfs func(string) bool
	dfs = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for next := range layers[id] {
			if dfs(next) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for id := range layers {
		if dfs(id) {
			return fmt.Errorf("prerequisite 关系存在环(涉及 %s)", id)
		}
	}
	return nil
}

func validateNode(n *GraphNode) error {
	schema, ok := nodeSchemas[n.Type]
	if !ok {
		return fmt.Errorf("未知节点类型 %q(允许: %s)", n.Type, strings.Join(nodeTypes, "/"))
	}
	if n.ID == "" || len(n.ID) > 100 {
		return fmt.Errorf("id 必须为 1..100 字符")
	}
	ownKeys := schemaKeys(schema)
	for k := range n.Props {
		if !ownKeys[k] {
			// 未知属性:同时检查它是否是边类型名(常见混用)以给出可读错误。
			if _, isEdge := edgeSchemas[k]; isEdge {
				return fmt.Errorf("属性 %q 是边类型名,节点/边数据不可混用", k)
			}
			return fmt.Errorf("未知属性 %q(类型 %s)", k, n.Type)
		}
	}
	return validateProps(n.Props, schema, "node "+n.Type)
}

func validateProps(props map[string]any, schema map[string]propSpec, where string) error {
	for k, spec := range schema {
		v, present := props[k]
		if !present {
			if spec.required {
				return fmt.Errorf("%s: 缺必填属性 %q", where, k)
			}
			continue
		}
		switch spec.kind {
		case "string":
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s: 属性 %q 必须为字符串", where, k)
			}
			if strings.TrimSpace(s) == "" {
				if spec.required {
					return fmt.Errorf("%s: 属性 %q 必须为非空字符串", where, k)
				}
				continue // 可选字符串允许为空
			}
			if len(spec.enum) > 0 && !containsStr(spec.enum, s) {
				return fmt.Errorf("%s: 属性 %q 必须为 %s 之一", where, k, strings.Join(spec.enum, "/"))
			}
		case "int":
			f, ok := v.(float64) // JSON 数值统一为 float64
			if !ok || f != float64(int(f)) {
				return fmt.Errorf("%s: 属性 %q 必须为整数", where, k)
			}
			iv := int(f)
			if len(spec.enumInt) > 0 && !containsInt(spec.enumInt, iv) {
				return fmt.Errorf("%s: 属性 %q 必须为 %v 之一", where, k, spec.enumInt)
			}
			if spec.minInt != nil && iv < *spec.minInt {
				return fmt.Errorf("%s: 属性 %q 最小 %d", where, k, *spec.minInt)
			}
			if spec.maxInt != nil && iv > *spec.maxInt {
				return fmt.Errorf("%s: 属性 %q 最大 %d", where, k, *spec.maxInt)
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s: 属性 %q 必须为布尔", where, k)
			}
		}
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func ptrInt(v int) *int { return &v }

// ---------- canonical JSON(修订工件哈希的确定性基础)----------

func CanonicalGraphJSON(g *GraphContent) ([]byte, error) {
	// 结构体 → map → 再 marshal:Go 对 map 键排序,得到字节稳定输出。
	raw, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// ---------- manifest 重放派生 ----------

type courseManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Graph         string `json:"graph"`
	Nodes         struct {
		Layers    []map[string]any `json:"layers"`
		Units     []map[string]any `json:"units"`
		Concepts  []map[string]any `json:"concepts"`
		Questions []map[string]any `json:"questions"`
		Materials []map[string]any `json:"materials"`
	} `json:"nodes"`
	Edges struct {
		BelongsTo    []map[string]any `json:"belongs_to"`
		Prerequisite []map[string]any `json:"prerequisite"`
		Covers       []map[string]any `json:"covers"`
		Tests        []map[string]any `json:"tests"`
		Anchors      []map[string]any `json:"anchors"`
		Reads        []map[string]any `json:"reads"`
	} `json:"edges"`
}

// ReplayGraph 从 RTW manifest 原文(JSON)派生图内容——修订 1 的唯一来源,
// 也是重放一致性校验的基础。
func ReplayGraph(manifestJSON []byte, graphID string) (*GraphContent, error) {
	var m courseManifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("解析 manifest: %w", err)
	}
	g := &GraphContent{SchemaVersion: graphSchemaVersion, GraphID: graphID}
	// 节点:域键 → 分型 props(id 之外全部进 props)
	pick := func(src map[string]any, drop ...string) map[string]any {
		out := map[string]any{}
		dropSet := map[string]bool{"id": true}
		for _, d := range drop {
			dropSet[d] = true
		}
		for k, v := range src {
			if !dropSet[k] {
				out[k] = v
			}
		}
		return out
	}
	addNodes := func(typ string, list []map[string]any) error {
		for _, src := range list {
			id, _ := src["id"].(string)
			g.Nodes = append(g.Nodes, GraphNode{Type: typ, ID: id, Props: pick(src)})
		}
		return nil
	}
	_ = addNodes("layer", m.Nodes.Layers)
	_ = addNodes("unit", m.Nodes.Units)
	_ = addNodes("concept", m.Nodes.Concepts)
	_ = addNodes("question", m.Nodes.Questions)
	_ = addNodes("material", m.Nodes.Materials)
	// 边:域键名(from/to 语义各异)→ 统一 from/to
	addEdge := func(typ, fromKey, toKey string, list []map[string]any, drop ...string) {
		for _, src := range list {
			from, _ := src[fromKey].(string)
			to, _ := src[toKey].(string)
			g.Edges = append(g.Edges, GraphEdge{Type: typ, From: from, To: to, Props: pick(src, append(drop, fromKey, toKey)...)})
		}
	}
	addEdge("belongs_to", "unit", "layer", m.Edges.BelongsTo)
	addEdge("prerequisite", "from", "to", m.Edges.Prerequisite)
	addEdge("covers", "unit", "concept", m.Edges.Covers)
	addEdge("tests", "question", "concept", m.Edges.Tests)
	addEdge("anchors", "material", "concept", m.Edges.Anchors)
	addEdge("reads", "unit", "material", m.Edges.Reads)
	if err := ValidateGraph(g); err != nil {
		return nil, fmt.Errorf("派生图未通过结构校验: %w", err)
	}
	return g, nil
}

// ---------- 修订操作 ----------

type GraphOp struct {
	Kind string     `json:"kind"` // set_node | remove_node | set_edge | remove_edge
	Node *GraphNode `json:"node,omitempty"`
	Edge *GraphEdge `json:"edge,omitempty"`
	// remove_edge 按 (type,from,to) 定位;同向同类型多条时拒绝操作(需先拆分语义)。
}

// ApplyOps 在 base 副本上应用操作集,返回新内容(调用方负责再校验+落工件)。
func ApplyOps(base *GraphContent, ops []GraphOp) (*GraphContent, error) {
	next := deepCopyGraph(base)
	nodeIdx := map[string]int{}
	for i, n := range next.Nodes {
		nodeIdx[n.ID] = i
	}
	findEdge := func(e GraphEdge) (int, bool) {
		for i := range next.Edges {
			x := next.Edges[i]
			if x.Type == e.Type && x.From == e.From && x.To == e.To {
				return i, true
			}
		}
		return 0, false
	}
	for i, op := range ops {
		switch op.Kind {
		case "set_node":
			if op.Node == nil {
				return nil, fmt.Errorf("op[%d]: set_node 缺 node", i)
			}
			if idx, ok := nodeIdx[op.Node.ID]; ok {
				next.Nodes[idx].Type = op.Node.Type
				next.Nodes[idx].Props = op.Node.Props
			} else {
				next.Nodes = append(next.Nodes, *op.Node)
				nodeIdx[op.Node.ID] = len(next.Nodes) - 1
			}
		case "remove_node":
			if op.Node == nil || op.Node.ID == "" {
				return nil, fmt.Errorf("op[%d]: remove_node 缺 node.id", i)
			}
			if _, ok := nodeIdx[op.Node.ID]; !ok {
				return nil, fmt.Errorf("op[%d]: 节点 %s 不存在", i, op.Node.ID)
			}
			// 级联:删节点必须同时删除其全部关联边,否则产生悬空边。
			kept := next.Edges[:0]
			for _, e := range next.Edges {
				if e.From != op.Node.ID && e.To != op.Node.ID {
					kept = append(kept, e)
				}
			}
			next.Edges = kept
			idx := nodeIdx[op.Node.ID]
			next.Nodes = append(next.Nodes[:idx], next.Nodes[idx+1:]...)
			delete(nodeIdx, op.Node.ID)
			// 重建索引
			nodeIdx = map[string]int{}
			for j, n := range next.Nodes {
				nodeIdx[n.ID] = j
			}
		case "set_edge":
			if op.Edge == nil {
				return nil, fmt.Errorf("op[%d]: set_edge 缺 edge", i)
			}
			if idx, ok := findEdge(*op.Edge); ok {
				next.Edges[idx].Props = op.Edge.Props
			} else {
				next.Edges = append(next.Edges, *op.Edge)
			}
		case "remove_edge":
			if op.Edge == nil {
				return nil, fmt.Errorf("op[%d]: remove_edge 缺 edge", i)
			}
			idx, ok := findEdge(*op.Edge)
			if !ok {
				return nil, fmt.Errorf("op[%d]: 边 %s(%s→%s) 不存在", i, op.Edge.Type, op.Edge.From, op.Edge.To)
			}
			next.Edges = append(next.Edges[:idx], next.Edges[idx+1:]...)
		default:
			return nil, fmt.Errorf("op[%d]: 未知操作 %q", i, op.Kind)
		}
	}
	if err := ValidateGraph(next); err != nil {
		return nil, fmt.Errorf("操作后图未通过结构校验: %w", err)
	}
	return next, nil
}

func deepCopyGraph(g *GraphContent) *GraphContent {
	raw, _ := json.Marshal(g)
	var out GraphContent
	_ = json.Unmarshal(raw, &out)
	return &out
}
