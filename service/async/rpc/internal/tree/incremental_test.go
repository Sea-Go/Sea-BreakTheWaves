package tree

import (
	"reflect"
	"strings"
	"testing"
)

func builtTwoTopicTree(t *testing.T) *RetrievalTree {
	t.Helper()
	tr, err := Build("scope", "enc", twoTopicDocs(), DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return tr
}

func leafParentClusters(tr *RetrievalTree) (string, string) {
	root := tr.Nodes[0]
	a := tr.Nodes[indexOf(tr, root.Children[0])]
	b := tr.Nodes[indexOf(tr, root.Children[1])]
	return a.NodeID, b.NodeID
}

func indexOf(tr *RetrievalTree, id string) int {
	for i := range tr.Nodes {
		if tr.Nodes[i].NodeID == id {
			return i
		}
	}
	return -1
}

func TestAssignNearestLeafParentCluster(t *testing.T) {
	tr := builtTwoTopicTree(t)
	clusterA, clusterB := leafParentClusters(tr)
	before := append([]Node(nil), tr.Nodes...)

	docs := []DocVec{
		{DocKey: "chat/new1", Vec: []float32{1, 0}},
		{DocKey: "wiki/new1", Vec: []float32{0, 1}},
	}
	assignment, dirty, err := Assign(tr, docs)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	want := map[string]string{"chat/new1": clusterA, "wiki/new1": clusterB}
	if !reflect.DeepEqual(assignment, want) {
		t.Fatalf("assignment = %v, want %v", assignment, want)
	}
	if !reflect.DeepEqual(dirty, []string{min(clusterA, clusterB), max(clusterA, clusterB)}) {
		t.Fatalf("dirty = %v, want both affected clusters sorted", dirty)
	}
	// Assign is pure: the tree is untouched and still valid.
	if !reflect.DeepEqual(before, tr.Nodes) {
		t.Fatal("assign mutated the tree")
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("validate after assign: %v", err)
	}
}

func TestAssignTieBreaksTowardSmallestNodeID(t *testing.T) {
	tr := builtTwoTopicTree(t)
	clusterA, clusterB := leafParentClusters(tr)
	// A vector orthogonal to both cluster centroids: cosine 0 everywhere,
	// so the smallest node_id must win deterministically.
	assignment, dirty, err := Assign(tr, []DocVec{{DocKey: "tie/doc", Vec: []float32{0.0001, 0.0001}}})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	want := min(clusterA, clusterB)
	if assignment["tie/doc"] != want {
		t.Fatalf("assignment = %v, want tie broken to %s", assignment, want)
	}
	if !reflect.DeepEqual(dirty, []string{want}) {
		t.Fatalf("dirty = %v, want [%s]", dirty, want)
	}
}

func TestAssignEmptyInput(t *testing.T) {
	tr := builtTwoTopicTree(t)
	assignment, dirty, err := Assign(tr, nil)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if len(assignment) != 0 || len(dirty) != 0 {
		t.Fatalf("assignment = %v dirty = %v, want empty", assignment, dirty)
	}
}

func TestAssignRejectsBadInput(t *testing.T) {
	tr := builtTwoTopicTree(t)
	cases := []struct {
		name string
		docs []DocVec
		want string
	}{
		{"existing doc key", []DocVec{{DocKey: "chat/a1", Vec: []float32{1, 0}}}, "already present"},
		{"duplicate new keys", []DocVec{{DocKey: "n1", Vec: []float32{1, 0}}, {DocKey: "n1", Vec: []float32{1, 0}}}, "duplicate new doc_key"},
		{"missing doc key", []DocVec{{DocKey: "", Vec: []float32{1, 0}}}, "missing doc_key"},
		{"empty vector", []DocVec{{DocKey: "n1", Vec: nil}}, "empty vector"},
		{"dim mismatch", []DocVec{{DocKey: "n1", Vec: []float32{1, 0, 0}}}, "dim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Assign(tr, tc.docs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	broken := handBuiltTree()
	broken.Nodes[0].Members = nil
	if _, _, err := Assign(broken, []DocVec{{DocKey: "n1", Vec: []float32{1, 0}}}); err == nil || !strings.Contains(err.Error(), "requires members") {
		t.Fatalf("err = %v, want validation rejection", err)
	}
}

func TestPruneMarksDirtyWithoutRebuilding(t *testing.T) {
	tr := builtTwoTopicTree(t)
	clusterA, clusterB := leafParentClusters(tr)
	before := append([]Node(nil), tr.Nodes...)

	dirty, err := Prune(tr, []string{"chat/a1", "chat/a3"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !reflect.DeepEqual(dirty, []string{clusterA}) {
		t.Fatalf("dirty = %v, want [%s]", dirty, clusterA)
	}
	dirty, err = Prune(tr, []string{"chat/a1", "wiki/b2"})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	want := []string{min(clusterA, clusterB), max(clusterA, clusterB)}
	if !reflect.DeepEqual(dirty, want) {
		t.Fatalf("dirty = %v, want %v", dirty, want)
	}
	if !reflect.DeepEqual(before, tr.Nodes) {
		t.Fatal("prune mutated the tree")
	}
	if _, err := Prune(tr, nil); err != nil {
		t.Fatalf("empty prune: %v", err)
	}
	if _, err := Prune(tr, []string{"ghost/doc"}); err == nil || !strings.Contains(err.Error(), "unknown doc_key") {
		t.Fatalf("err = %v, want unknown doc_key rejection", err)
	}
}
