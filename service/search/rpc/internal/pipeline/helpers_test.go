package pipeline

import "github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evidence"

// testTree 构建最小结构树（一个标题+两个段落）。
func testTree(revID string) evidence.TreeJSON {
	return evidence.TreeJSON{
		RevisionID: revID,
		Nodes: []evidence.NodeJSON{
			{NodeID: "n0", Level: 1, Title: "Title", ParaIndex: evidence.HeadingParaIndex, CharStart: 0, CharEnd: 7},
			{NodeID: "n1", Level: evidence.LevelParagraph, ParaIndex: 0, CharStart: 8, CharEnd: 36},
			{NodeID: "n2", Level: evidence.LevelParagraph, ParaIndex: 1, CharStart: 37, CharEnd: 64},
		},
	}
}
