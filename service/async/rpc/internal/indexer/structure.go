package indexer

import (
	"encoding/json"
	"fmt"
)

// evalseed 结构树契约的镜像类型。源头契约：service/search/rpc/internal/
// evidence/types.go 的 TreeJSON / NodeJSON（其自身又是 RTW structure 契约的
// 镜像）。跨服务 internal 包不可导入（架构红线），故此处按字段级一致镜像，
// 任何一侧改动字段或 JSON tag 必须双边同步。
//
// structure_ref 指向的对象即此格式：一次冻结修订的结构树，节点按文档序
// 排列，CharStart/CharEnd 为源文本字节偏移（区间 [start, end)）。

// StructureTree 是 evalseed TreeJSON 的镜像。
type StructureTree struct {
	// RevisionID 冻结修订 ID（结构树的派生键），必须与事件 doc.revision_id
	// 一致。
	RevisionID string `json:"revision_id"`
	// Nodes 结构节点列表（标题 + 段落），按文档序。
	Nodes []StructureNode `json:"nodes"`
}

// StructureNode 是 evalseed NodeJSON 的镜像：一个标题或段落节点。
type StructureNode struct {
	NodeID    string `json:"node_id"`
	Level     int    `json:"level"`      // 标题 1..6，段落 7
	Title     string `json:"title"`      // 标题文本；段落节点为空
	ParaIndex int    `json:"para_index"` // 全局段落序号；标题为 -1
	CharStart int    `json:"char_start"` // 源文本起始字节偏移（含）
	CharEnd   int    `json:"char_end"`   // 结束字节偏移（不含）
}

// ParseStructureTree 解析结构树 JSON（structure_ref 指向对象的字节）。
// 要求 revision_id 非空；节点形状的完整校验属于结构树生产方（RTW
// structure.Derive），本包只消费。
func ParseStructureTree(b []byte) (StructureTree, error) {
	var st StructureTree
	if err := json.Unmarshal(b, &st); err != nil {
		return StructureTree{}, fmt.Errorf("structure tree json: %w", err)
	}
	if st.RevisionID == "" {
		return StructureTree{}, fmt.Errorf("structure tree revision_id required")
	}
	return st, nil
}
