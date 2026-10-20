package tree

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func twoTopicDocs() []DocVec {
	return append(
		axisDocs("chat/a", 6, 1, 0),
		axisDocs("wiki/b", 6, 0, 1)...,
	)
}

func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

type failingSummarizer struct{}

func (failingSummarizer) Summarize(docTexts []string) (string, error) {
	return "", fmt.Errorf("summarizer unavailable")
}

type failingEmbedder struct{}

func (failingEmbedder) Embed(text string) ([]float32, error) {
	return nil, fmt.Errorf("embedder unavailable")
}

type fixedEmbedder struct{ vec []float32 }

func (f fixedEmbedder) Embed(text string) ([]float32, error) {
	return append([]float32(nil), f.vec...), nil
}

func TestBuildProducesValidTree(t *testing.T) {
	docs := twoTopicDocs()
	tr, err := Build("sea.knowledge", "enc-bge-m3-20261001", docs, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if tr.ModuleScope != "sea.knowledge" || tr.EncoderID != "enc-bge-m3-20261001" || tr.BuildEpoch != 1 {
		t.Fatalf("header fields wrong: %+v", *tr)
	}
	if !isHex16(tr.TreeID) || tr.TreeID == tr.RootID {
		t.Fatalf("tree_id %q / root_id %q not distinct 16-hex", tr.TreeID, tr.RootID)
	}
	if len(tr.Nodes) != 15 { // root + 2 clusters + 12 leaves
		t.Fatalf("nodes = %d, want 15", len(tr.Nodes))
	}
	ids := map[string]bool{}
	byID := map[string]Node{}
	for _, n := range tr.Nodes {
		if !isHex16(n.NodeID) {
			t.Fatalf("node id %q is not 16 hex chars", n.NodeID)
		}
		if ids[n.NodeID] {
			t.Fatalf("duplicate node id %s", n.NodeID)
		}
		ids[n.NodeID] = true
		byID[n.NodeID] = n
	}
	root := byID[tr.RootID]
	if !root.IsCluster || root.ParentID != "" {
		t.Fatalf("root must be a parentless cluster: %+v", root)
	}
	wantMembers := make([]string, 0, len(docs))
	for _, d := range docs {
		wantMembers = append(wantMembers, d.DocKey)
	}
	sort.Strings(wantMembers)
	if !reflect.DeepEqual(root.Members, wantMembers) {
		t.Fatalf("root members = %v, want %v", root.Members, wantMembers)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children = %v, want 2 clusters", root.Children)
	}
	clusterA := byID[root.Children[0]]
	clusterB := byID[root.Children[1]]
	if !reflect.DeepEqual(clusterA.Members, []string{"chat/a1", "chat/a2", "chat/a3", "chat/a4", "chat/a5", "chat/a6"}) {
		t.Fatalf("cluster A members = %v", clusterA.Members)
	}
	if !reflect.DeepEqual(clusterB.Members, []string{"wiki/b1", "wiki/b2", "wiki/b3", "wiki/b4", "wiki/b5", "wiki/b6"}) {
		t.Fatalf("cluster B members = %v", clusterB.Members)
	}
	for _, cluster := range []Node{clusterA, clusterB} {
		if len(cluster.Children) != 6 {
			t.Fatalf("cluster %s leaf children = %d, want 6", cluster.NodeID, len(cluster.Children))
		}
		for _, c := range cluster.Children {
			leaf := byID[c]
			if leaf.IsCluster || leaf.ParentID != cluster.NodeID || leaf.DocKey == "" {
				t.Fatalf("bad leaf %+v under %s", leaf, cluster.NodeID)
			}
		}
	}
	// Mean vector: root sees both axes, each cluster sees only its own.
	rootVec, err := decodeDenseRef(root.ClusterDenseRef)
	if err != nil {
		t.Fatalf("root dense ref: %v", err)
	}
	if rootVec[0] != 0.5 || rootVec[1] != 0.5 {
		t.Fatalf("root mean vector = %v, want [0.5 0.5]", rootVec)
	}
	aVec, _ := decodeDenseRef(clusterA.ClusterDenseRef)
	if aVec[0] != 1 || aVec[1] != 0 {
		t.Fatalf("cluster A mean vector = %v, want [1 0]", aVec)
	}
	// Deterministic keywords: chat(6) outranks the singleton tokens a1..a6.
	if !reflect.DeepEqual(clusterA.Keywords, []string{"chat", "a1", "a2", "a3", "a4"}) {
		t.Fatalf("cluster A keywords = %v", clusterA.Keywords)
	}
}

func TestBuildDeterministicAcrossRunsAndInputOrder(t *testing.T) {
	docs := twoTopicDocs()
	first, err := Build("sea.knowledge", "enc-x", docs, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	second, err := Build("sea.knowledge", "enc-x", docs, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same input built different trees")
	}
	shuffled := append([]DocVec(nil), docs...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	third, err := Build("sea.knowledge", "enc-x", shuffled, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !reflect.DeepEqual(first, third) {
		t.Fatal("input order influenced the built tree")
	}
}

func TestBuildSmallInputIsShallow(t *testing.T) {
	tr, err := Build("scope", "enc", axisDocs("a", 3, 1, 0), DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(tr.Nodes) != 4 {
		t.Fatalf("nodes = %d, want root + 3 leaves", len(tr.Nodes))
	}
	root := tr.Nodes[0]
	if len(root.Children) != 3 || len(root.Members) != 3 {
		t.Fatalf("root children = %v members = %v", root.Children, root.Members)
	}
	for _, n := range tr.Nodes[1:] {
		if n.IsCluster || n.ParentID != root.NodeID {
			t.Fatalf("expected leaves under root, got %+v", n)
		}
	}
}

func TestBuildEpochChangesIdentifiers(t *testing.T) {
	docs := twoTopicDocs()
	epoch1, err := Build("scope", "enc", docs, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	epoch2, err := buildTree("scope", "enc", 2, DefaultStopSize, docs, DeterministicSummarizer{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if epoch1.RootID == epoch2.RootID || epoch1.TreeID == epoch2.TreeID {
		t.Fatal("epoch must change derived identifiers")
	}
	if len(epoch1.Nodes) != len(epoch2.Nodes) {
		t.Fatalf("structure changed between epochs: %d vs %d", len(epoch1.Nodes), len(epoch2.Nodes))
	}
	for i := range epoch1.Nodes {
		if epoch1.Nodes[i].Members == nil {
			continue
		}
		if !reflect.DeepEqual(epoch1.Nodes[i].Members, epoch2.Nodes[i].Members) {
			t.Fatalf("membership drifted at node %d", i)
		}
	}
	if err := epoch2.Validate(); err != nil {
		t.Fatalf("validate epoch 2: %v", err)
	}
}

func TestBuildEmbedderReplacesMeanVector(t *testing.T) {
	emb := fixedEmbedder{vec: []float32{0.9, 0.1}}
	tr, err := Build("scope", "enc", twoTopicDocs(), DeterministicSummarizer{}, emb)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, n := range tr.Nodes {
		if !n.IsCluster {
			continue
		}
		v, err := decodeDenseRef(n.ClusterDenseRef)
		if err != nil {
			t.Fatalf("dense ref: %v", err)
		}
		if v[0] != 0.9 || v[1] != 0.1 {
			t.Fatalf("cluster %s vector = %v, want embedded [0.9 0.1]", n.NodeID, v)
		}
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	docs := twoTopicDocs()
	cases := []struct {
		name      string
		scope     string
		encoderID string
		docs      []DocVec
		summarize Summarizer
		embed     Embedder
		want      string
	}{
		{"empty scope", "", "enc", docs, DeterministicSummarizer{}, nil, "module_scope required"},
		{"empty encoder", "scope", "", docs, DeterministicSummarizer{}, nil, "encoder_id required"},
		{"no docs", "scope", "enc", nil, DeterministicSummarizer{}, nil, "at least one doc"},
		{"nil summarizer", "scope", "enc", docs, nil, nil, "Summarizer seam required"},
		{"duplicate doc key", "scope", "enc", []DocVec{{DocKey: "a", Vec: []float32{1}}, {DocKey: "a", Vec: []float32{1}}}, DeterministicSummarizer{}, nil, "duplicate doc_key"},
		{"empty vector", "scope", "enc", []DocVec{{DocKey: "a", Vec: nil}}, DeterministicSummarizer{}, nil, "empty vector"},
		{"dim mismatch", "scope", "enc", []DocVec{{DocKey: "a", Vec: []float32{1}}, {DocKey: "b", Vec: []float32{1, 2}}}, DeterministicSummarizer{}, nil, "dim 2 differs"},
		{"non finite vector", "scope", "enc", []DocVec{{DocKey: "a", Vec: []float32{float32(math.Inf(1))}}, {DocKey: "b", Vec: []float32{1}}}, DeterministicSummarizer{}, nil, "non-finite"},
		{"summarizer failure", "scope", "enc", docs, failingSummarizer{}, nil, "summarizer unavailable"},
		{"embedder failure", "scope", "enc", docs, DeterministicSummarizer{}, failingEmbedder{}, "embedder unavailable"},
		{"embedder dim mismatch", "scope", "enc", docs, DeterministicSummarizer{}, fixedEmbedder{vec: []float32{1, 2, 3}}, "dim 3, want 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.scope, tc.encoderID, tc.docs, tc.summarize, tc.embed)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDeterministicSummarizer(t *testing.T) {
	s := DeterministicSummarizer{}
	a, err := s.Summarize([]string{"b.md", "a.md", "c.md"})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	b, err := s.Summarize([]string{"c.md", "b.md", "a.md"})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if a != b {
		t.Fatalf("member order influenced summary: %q vs %q", a, b)
	}
	again, _ := s.Summarize([]string{"a.md", "b.md", "c.md"})
	if again != a {
		t.Fatal("summary not deterministic")
	}
	other, _ := s.Summarize([]string{"a.md", "b.md"})
	if other == a {
		t.Fatal("different member sets produced the same summary")
	}
	if !strings.HasPrefix(a, "stub-summary:v1:") {
		t.Fatalf("summary %q lacks stub prefix", a)
	}
	if _, err := s.Summarize(nil); err == nil {
		t.Fatal("empty member set must fail")
	}
}

func TestDenseRefRoundTrip(t *testing.T) {
	vec := []float32{0.5, -1.25, 0, 3.75, 1e-9}
	decoded, err := decodeDenseRef(encodeDenseRef(vec))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, vec) {
		t.Fatalf("round trip = %v, want %v", decoded, vec)
	}
	for _, bad := range []string{"", "nope", "dense.v1:zz", "dense.v1:010203"} {
		if _, err := decodeDenseRef(bad); err == nil {
			t.Fatalf("decodeDenseRef(%q) unexpectedly succeeded", bad)
		}
	}
}
