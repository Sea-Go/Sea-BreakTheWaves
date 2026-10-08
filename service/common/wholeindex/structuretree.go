// structuretree.go —— 结构树契约的共享类型（唯一实现）。
//
// 源头契约是 RTW（Sea-RideTheWind）service/knowledge/internal/structure
// 的 Tree/Node：跨仓不可导入 internal 包，因此 BTW 侧以 wire 契约镜像。
// 此前该镜像在 search/internal/evidence 与 async/internal/indexer 各有一
// 份（同仓、同字段、同 JSON tag），2026-10-08 复用审计后上提到本包统一；
// 与 RTW 源头的同步义务不变（改字段/tag 需双边同步）。
//
// 语义：一次冻结修订的结构树，节点按文档序排列；CharStart/CharEnd 为源
// 文本字节偏移（区间 [start, end)）。段落序号与标题常量与 evidence 侧
// 口径一致。
package wholeindex

import (
	"encoding/json"
	"fmt"
)

// 段落层级与标题段落的段落序号哨兵（与 evidence 侧常量口径一致）。
const (
	// LevelParagraph 段落节点的层级值（标题为 1..6）。
	LevelParagraph = 7
	// HeadingParaIndex 标题节点的段落序号（段落节点从 0 递增）。
	HeadingParaIndex = -1
)

// TreeJSON 是一次冻结修订的结构树（RTW structure.Tree 的 wire 契约）。
type TreeJSON struct {
	// RevisionID 冻结修订 ID（结构树的派生键）。
	RevisionID string `json:"revision_id"`
	// Nodes 结构节点列表（标题 + 段落），按文档序。
	Nodes []NodeJSON `json:"nodes"`
}

// NodeJSON 是一个标题或段落节点（RTW structure.Node 的 wire 契约）。
type NodeJSON struct {
	// NodeID 节点 ID（RTW 派生，本包视为不透明）。
	NodeID string `json:"node_id"`
	// Level 层级：标题 1..6，段落为 LevelParagraph(7)。
	Level int `json:"level"`
	// Title 标题文本（已 trim）；段落节点为空字符串。
	Title string `json:"title"`
	// ParaIndex 全局段落序号；标题节点为 HeadingParaIndex(-1)。
	ParaIndex int `json:"para_index"`
	// CharStart 节点覆盖源文本的起始字节偏移（含）。
	CharStart int `json:"char_start"`
	// CharEnd 节点覆盖源文本的结束字节偏移（不含）。
	CharEnd int `json:"char_end"`
}

// ParseStructureTree 解析结构树 JSON（structure_ref 指向对象的字节）。
// 要求 revision_id 非空；节点形状的完整校验属于结构树生产方（RTW
// structure.Derive），消费侧只做最低限度检查。
func ParseStructureTree(b []byte) (TreeJSON, error) {
	var st TreeJSON
	if err := json.Unmarshal(b, &st); err != nil {
		return TreeJSON{}, fmt.Errorf("structure tree json: %w", err)
	}
	if st.RevisionID == "" {
		return TreeJSON{}, fmt.Errorf("structure tree revision_id required")
	}
	return st, nil
}
