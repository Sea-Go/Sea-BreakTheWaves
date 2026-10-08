package tree

import (
	"fmt"
	"sort"
)

// Assign maps each new document to its nearest leaf-parent cluster — the
// cluster node whose children are all leaves — by cosine similarity against
// the cluster dense vectors. Ties break toward the smallest node_id, so the
// result is a pure function of (tree, docs). The tree is not mutated: the
// returned dirty list names the clusters whose membership would change, and
// deciding between in-place growth and a rebuild belongs to the DC
// orchestration layer that owns persistence.
func Assign(t *RetrievalTree, docs []DocVec) (assignment map[string]string, dirty []string, err error) {
	if err := t.Validate(); err != nil {
		return nil, nil, err
	}
	assignment = map[string]string{}
	if len(docs) == 0 {
		return assignment, []string{}, nil
	}
	byID := indexNodes(t)
	leafKeys := make(map[string]bool, len(t.Nodes))
	type candidate struct {
		id  string
		vec []float32
	}
	candidates := make([]candidate, 0, len(t.Nodes))
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if !n.IsCluster {
			leafKeys[n.DocKey] = true
			continue
		}
		leafParent := true
		for _, c := range n.Children {
			if byID[c].IsCluster {
				leafParent = false
				break
			}
		}
		if !leafParent {
			continue
		}
		vec, err := decodeDenseRef(n.ClusterDenseRef)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: cluster %s: %v", ErrInvalid, n.NodeID, err)
		}
		candidates = append(candidates, candidate{id: n.NodeID, vec: vec})
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("%w: tree has no leaf-parent cluster to assign to", ErrInvalid)
	}
	dim := len(docs[0].Vec)
	if dim == 0 {
		return nil, nil, fmt.Errorf("%w: doc %s has an empty vector", ErrInvalid, docs[0].DocKey)
	}
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		d := &docs[i]
		if d.DocKey == "" {
			return nil, nil, fmt.Errorf("%w: doc %d missing doc_key", ErrInvalid, i)
		}
		if seen[d.DocKey] {
			return nil, nil, fmt.Errorf("%w: duplicate new doc_key %s", ErrInvalid, d.DocKey)
		}
		seen[d.DocKey] = true
		if leafKeys[d.DocKey] {
			return nil, nil, fmt.Errorf("%w: doc_key %s already present in tree", ErrInvalid, d.DocKey)
		}
		if len(d.Vec) != dim {
			return nil, nil, fmt.Errorf("%w: doc %s vector dim %d differs from %d", ErrInvalid, d.DocKey, len(d.Vec), dim)
		}
		if !finiteVec(d.Vec) {
			return nil, nil, fmt.Errorf("%w: doc %s vector has non-finite values", ErrInvalid, d.DocKey)
		}
		if len(d.Vec) != len(candidates[0].vec) {
			return nil, nil, fmt.Errorf("%w: doc dim %d disagrees with cluster dim %d (encoder mismatch?)", ErrInvalid, len(d.Vec), len(candidates[0].vec))
		}
		best := -1
		bestSim := 0.0
		for ci := range candidates {
			if len(candidates[ci].vec) != dim {
				return nil, nil, fmt.Errorf("%w: cluster %s dim %d disagrees with doc dim %d", ErrInvalid, candidates[ci].id, len(candidates[ci].vec), dim)
			}
			s := cosine(d.Vec, candidates[ci].vec)
			if best < 0 || s > bestSim || (s == bestSim && candidates[ci].id < candidates[best].id) {
				best, bestSim = ci, s
			}
		}
		assignment[d.DocKey] = candidates[best].id
	}
	return assignment, dirtyOf(assignment), nil
}

// Prune reports which leaf-parent clusters own the given doc_keys, marking
// them dirty for a later rebuild without touching the tree. Rebuild timing
// and persistence belong to the caller; the pure domain layer only answers
// which clusters are affected.
func Prune(t *RetrievalTree, docKeys []string) (dirty []string, err error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if len(docKeys) == 0 {
		return []string{}, nil
	}
	byID := indexNodes(t)
	owner := make(map[string]string, len(t.Nodes))
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if n.IsCluster {
			continue
		}
		owner[n.DocKey] = n.ParentID
		if byID[n.ParentID] == nil {
			return nil, fmt.Errorf("%w: leaf %s has unknown parent %s", ErrInvalid, n.NodeID, n.ParentID)
		}
	}
	affected := map[string]bool{}
	for _, key := range docKeys {
		parent, ok := owner[key]
		if !ok {
			return nil, fmt.Errorf("%w: unknown doc_key %s", ErrInvalid, key)
		}
		affected[parent] = true
	}
	dirty = make([]string, 0, len(affected))
	for id := range affected {
		dirty = append(dirty, id)
	}
	sort.Strings(dirty)
	return dirty, nil
}

func indexNodes(t *RetrievalTree) map[string]*Node {
	byID := make(map[string]*Node, len(t.Nodes))
	for i := range t.Nodes {
		byID[t.Nodes[i].NodeID] = &t.Nodes[i]
	}
	return byID
}

func dirtyOf(assignment map[string]string) []string {
	set := make(map[string]bool, len(assignment))
	for _, id := range assignment {
		set[id] = true
	}
	dirty := make([]string, 0, len(set))
	for id := range set {
		dirty = append(dirty, id)
	}
	sort.Strings(dirty)
	return dirty
}
