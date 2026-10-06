package tree

import (
	"strings"
	"testing"
)

// handBuiltTree constructs a minimal two-leaf tree by hand (no Build) so the
// counterexample tests mutate a known-good baseline.
func handBuiltTree() *RetrievalTree {
	root := &Node{
		NodeID:          "root0000000000001",
		IsCluster:       true,
		Members:         []string{"doc-a", "doc-b"},
		SummaryRef:      "stub-summary:v1:doc-a+doc-b",
		ClusterDenseRef: encodeDenseRef([]float32{1, 0}),
		Children:        []string{"leaf000000000000a", "leaf000000000000b"},
	}
	leafA := &Node{NodeID: "leaf000000000000a", DocKey: "doc-a", ParentID: root.NodeID}
	leafB := &Node{NodeID: "leaf000000000000b", DocKey: "doc-b", ParentID: root.NodeID}
	return &RetrievalTree{
		TreeID:      "tree0000000000001",
		ModuleScope: "scope",
		RootID:      root.NodeID,
		BuildEpoch:  1,
		EncoderID:   "enc",
		Nodes:       []Node{*root, *leafA, *leafB},
	}
}

func TestValidateAcceptsHandBuiltTree(t *testing.T) {
	if err := handBuiltTree().Validate(); err != nil {
		t.Fatalf("hand-built tree rejected: %v", err)
	}
	tr, err := Build("scope", "enc", axisDocs("a", 3, 1, 0), DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("built tree rejected: %v", err)
	}
}

func TestValidateCycleCounterexample(t *testing.T) {
	tr := handBuiltTree()
	// A detached two-node cycle: parent/children links are mutually
	// consistent, so only the DFS reachability pass can catch it.
	cycleA := Node{
		NodeID:          "cyc00000000000a",
		IsCluster:       true,
		Members:         []string{"doc-x"},
		SummaryRef:      "s",
		ClusterDenseRef: encodeDenseRef([]float32{1}),
		Children:        []string{"cyc00000000000b"},
		ParentID:        "cyc00000000000b",
	}
	cycleB := cycleA
	cycleB.NodeID = "cyc00000000000b"
	cycleB.Children = []string{"cyc00000000000a"}
	cycleB.ParentID = "cyc00000000000a"
	tr.Nodes = append(tr.Nodes, cycleA, cycleB)
	err := tr.Validate()
	if err == nil || !strings.Contains(err.Error(), "unreachable from root") {
		t.Fatalf("err = %v, want unreachable-from-root cycle rejection", err)
	}
}

func TestValidateRejectsBrokenShapes(t *testing.T) {
	mutate := func(f func(t *RetrievalTree)) *RetrievalTree {
		tr := handBuiltTree()
		f(tr)
		return tr
	}
	cases := []struct {
		name string
		tree *RetrievalTree
		want string
	}{
		{"missing tree_id", mutate(func(t *RetrievalTree) { t.TreeID = "" }), "tree_id required"},
		{"zero epoch", mutate(func(t *RetrievalTree) { t.BuildEpoch = 0 }), "build_epoch"},
		{"root not found", mutate(func(t *RetrievalTree) { t.RootID = "ghost00000000000" }), "not found"},
		{"two roots", mutate(func(t *RetrievalTree) {
			t.Nodes = append(t.Nodes, Node{NodeID: "extra00000000000", DocKey: "doc-c"})
		}), "exactly one root"},
		{"duplicate node id", mutate(func(t *RetrievalTree) { t.Nodes[2].NodeID = t.Nodes[1].NodeID }), "duplicate node_id"},
		{"parent pointer mismatch", mutate(func(t *RetrievalTree) { t.Nodes[1].ParentID = "ghost00000000000" }), "disagrees"},
		{"child not listed", mutate(func(t *RetrievalTree) { t.Nodes[0].Children = t.Nodes[0].Children[:1] }), "disagree"},
		{"cluster without members", mutate(func(t *RetrievalTree) { t.Nodes[0].Members = nil }), "requires members"},
		{"unsorted members", mutate(func(t *RetrievalTree) { t.Nodes[0].Members = []string{"doc-b", "doc-a"} }), "sorted"},
		{"members mismatch subtree", mutate(func(t *RetrievalTree) { t.Nodes[0].Members = []string{"doc-a", "doc-c"} }), "disagree"},
		{"leaf without doc_key", mutate(func(t *RetrievalTree) { t.Nodes[1].DocKey = "" }), "requires doc_key"},
		{"leaf with children", mutate(func(t *RetrievalTree) {
			t.Nodes[1].Children = []string{"leaf000000000000b"}
			t.Nodes[1].Members = []string{"doc-b"}
			t.Nodes[1].SummaryRef = "s"
			t.Nodes[1].ClusterDenseRef = encodeDenseRef([]float32{1})
		}), "must not have children"},
		{"cluster with doc_key", mutate(func(t *RetrievalTree) { t.Nodes[0].DocKey = "doc-a" }), "must not carry doc_key"},
		{"cluster without summary", mutate(func(t *RetrievalTree) { t.Nodes[0].SummaryRef = "" }), "requires summary_ref"},
		{"undecodable dense ref", mutate(func(t *RetrievalTree) { t.Nodes[0].ClusterDenseRef = "garbage" }), "dense ref"},
		{"duplicate doc_key", mutate(func(t *RetrievalTree) {
			t.Nodes[2].DocKey = "doc-a"
			t.Nodes[0].Members = []string{"doc-a"}
		}), "owned by leaves"},
		{"empty nodes", mutate(func(t *RetrievalTree) { t.Nodes = nil }), "nodes required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tree.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateNilTree(t *testing.T) {
	var tr *RetrievalTree
	if err := tr.Validate(); err == nil || !strings.Contains(err.Error(), "nil tree") {
		t.Fatalf("err = %v, want nil tree rejection", err)
	}
}
