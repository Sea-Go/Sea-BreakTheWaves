// Package tree is the pure domain layer of the C6 btw-tree-builder: it turns
// a fixed set of encoded documents into a deterministic retrieval tree via
// agglomerative clustering, and computes incremental assignments for late
// arrivals. LLM calls, vector stores, and rebuild orchestration stay outside;
// summaries and re-encoding enter through the Summarizer and Embedder seams
// in build.go.
package tree

import (
	"fmt"
	"sort"
)

// Node is one vertex of a retrieval tree. Contract snapshot (JSON snake_case):
// a node is either a cluster (IsCluster, Members, SummaryRef,
// ClusterDenseRef, Children) or a leaf document (DocKey, no children).
type Node struct {
	NodeID          string   `json:"node_id"`
	IsCluster       bool     `json:"is_cluster"`
	DocKey          string   `json:"doc_key,omitempty"`
	Members         []string `json:"members,omitempty"`
	SummaryRef      string   `json:"summary_ref,omitempty"`
	ClusterDenseRef string   `json:"cluster_dense_ref,omitempty"`
	Keywords        []string `json:"keywords,omitempty"`
	ParentID        string   `json:"parent_id,omitempty"`
	Children        []string `json:"children,omitempty"`
}

// RetrievalTree is the persisted cluster hierarchy for one module scope.
// EncoderID is recorded verbatim at build time; binding it against the index
// manifest (mismatch refuses load) belongs to the loading side, which owns
// the manifest.
type RetrievalTree struct {
	TreeID      string `json:"tree_id"`
	ModuleScope string `json:"module_scope"`
	RootID      string `json:"root_id"`
	BuildEpoch  int64  `json:"build_epoch"`
	EncoderID   string `json:"encoder_id"`
	Nodes       []Node `json:"nodes"`
}

// Validate enforces the structural contract of a retrieval tree:
// exactly one root; bidirectional parent/children consistency; acyclicity
// via depth-first traversal from the root with every node reachable; leaves
// are non-cluster nodes with a non-empty doc_key; cluster nodes carry
// non-empty members that exactly match the leaf doc_keys of their subtree.
func (t *RetrievalTree) Validate() error {
	if t == nil {
		return fmt.Errorf("%w: nil tree", ErrInvalid)
	}
	if t.TreeID == "" {
		return fmt.Errorf("%w: tree_id required", ErrInvalid)
	}
	if t.ModuleScope == "" {
		return fmt.Errorf("%w: module_scope required", ErrInvalid)
	}
	if t.RootID == "" {
		return fmt.Errorf("%w: root_id required", ErrInvalid)
	}
	if t.EncoderID == "" {
		return fmt.Errorf("%w: encoder_id required", ErrInvalid)
	}
	if t.BuildEpoch < 1 {
		return fmt.Errorf("%w: build_epoch must be >= 1", ErrInvalid)
	}
	if len(t.Nodes) == 0 {
		return fmt.Errorf("%w: nodes required", ErrInvalid)
	}
	byID := make(map[string]*Node, len(t.Nodes))
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if n.NodeID == "" {
			return fmt.Errorf("%w: node %d missing node_id", ErrInvalid, i)
		}
		if _, dup := byID[n.NodeID]; dup {
			return fmt.Errorf("%w: duplicate node_id %s", ErrInvalid, n.NodeID)
		}
		byID[n.NodeID] = n
	}
	root := byID[t.RootID]
	if root == nil {
		return fmt.Errorf("%w: root_id %s not found", ErrInvalid, t.RootID)
	}
	roots := 0
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if n.ParentID == "" {
			roots++
		}
		if n.ParentID == n.NodeID {
			return fmt.Errorf("%w: node %s is its own parent", ErrInvalid, n.NodeID)
		}
		if n.ParentID != "" && byID[n.ParentID] == nil {
			return fmt.Errorf("%w: node %s references unknown parent %s", ErrInvalid, n.NodeID, n.ParentID)
		}
		if err := validateNodeShape(n); err != nil {
			return err
		}
		seenChild := make(map[string]bool, len(n.Children))
		for _, c := range n.Children {
			if seenChild[c] {
				return fmt.Errorf("%w: node %s repeats child %s", ErrInvalid, n.NodeID, c)
			}
			seenChild[c] = true
			child := byID[c]
			if child == nil {
				return fmt.Errorf("%w: node %s references unknown child %s", ErrInvalid, n.NodeID, c)
			}
			if child.ParentID != n.NodeID {
				return fmt.Errorf("%w: child %s parent %q disagrees with holder %s", ErrInvalid, c, child.ParentID, n.NodeID)
			}
		}
	}
	if roots != 1 {
		return fmt.Errorf("%w: expected exactly one root, found %d", ErrInvalid, roots)
	}
	if root.ParentID != "" {
		return fmt.Errorf("%w: root %s must not have a parent", ErrInvalid, root.NodeID)
	}
	leafOwner := make(map[string]string, len(t.Nodes))
	state := make(map[string]uint8, len(t.Nodes))
	var walk func(n *Node) ([]string, error)
	walk = func(n *Node) ([]string, error) {
		state[n.NodeID] = 1
		keys := make([]string, 0, len(n.Members))
		for _, c := range n.Children {
			child := byID[c]
			if state[child.NodeID] == 1 {
				return nil, fmt.Errorf("%w: cycle reached through node %s", ErrInvalid, child.NodeID)
			}
			if state[child.NodeID] == 2 {
				return nil, fmt.Errorf("%w: node %s visited twice", ErrInvalid, child.NodeID)
			}
			sub, err := walk(child)
			if err != nil {
				return nil, err
			}
			keys = append(keys, sub...)
		}
		if !n.IsCluster {
			if prev, dup := leafOwner[n.DocKey]; dup {
				return nil, fmt.Errorf("%w: doc_key %s owned by leaves %s and %s", ErrInvalid, n.DocKey, prev, n.NodeID)
			}
			leafOwner[n.DocKey] = n.NodeID
			keys = append(keys, n.DocKey)
		}
		sort.Strings(keys)
		if n.IsCluster && !equalSortedStrings(keys, n.Members) {
			return nil, fmt.Errorf("%w: cluster %s members disagree with subtree leaf doc_keys", ErrInvalid, n.NodeID)
		}
		state[n.NodeID] = 2
		return keys, nil
	}
	if _, err := walk(root); err != nil {
		return err
	}
	for i := range t.Nodes {
		if state[t.Nodes[i].NodeID] != 2 {
			return fmt.Errorf("%w: node %s unreachable from root (cycle or orphan)", ErrInvalid, t.Nodes[i].NodeID)
		}
	}
	return nil
}

func validateNodeShape(n *Node) error {
	if n.IsCluster {
		if n.DocKey != "" {
			return fmt.Errorf("%w: cluster %s must not carry doc_key", ErrInvalid, n.NodeID)
		}
		if len(n.Members) == 0 {
			return fmt.Errorf("%w: cluster %s requires members", ErrInvalid, n.NodeID)
		}
		if !sort.StringsAreSorted(n.Members) {
			return fmt.Errorf("%w: cluster %s members must be sorted and unique", ErrInvalid, n.NodeID)
		}
		if len(n.Children) == 0 {
			return fmt.Errorf("%w: cluster %s requires children", ErrInvalid, n.NodeID)
		}
		if n.SummaryRef == "" {
			return fmt.Errorf("%w: cluster %s requires summary_ref", ErrInvalid, n.NodeID)
		}
		if _, err := decodeDenseRef(n.ClusterDenseRef); err != nil {
			return fmt.Errorf("%w: cluster %s dense ref: %v", ErrInvalid, n.NodeID, err)
		}
		return nil
	}
	if n.DocKey == "" {
		return fmt.Errorf("%w: leaf %s requires doc_key", ErrInvalid, n.NodeID)
	}
	if len(n.Children) != 0 {
		return fmt.Errorf("%w: leaf %s must not have children", ErrInvalid, n.NodeID)
	}
	if len(n.Members) != 0 {
		return fmt.Errorf("%w: leaf %s must not have members", ErrInvalid, n.NodeID)
	}
	if n.SummaryRef != "" || n.ClusterDenseRef != "" {
		return fmt.Errorf("%w: leaf %s must not carry cluster refs", ErrInvalid, n.NodeID)
	}
	return nil
}

func equalSortedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
