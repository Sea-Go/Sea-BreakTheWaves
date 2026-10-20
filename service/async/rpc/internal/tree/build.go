package tree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ErrInvalid marks rejected inputs: malformed trees, unusable doc vectors, or
// seam failures that leave no complete tree to return.
var ErrInvalid = errors.New("invalid tree input")

// Summarizer produces the summary reference stored on a cluster node for the
// given member texts (build passes sorted member doc_keys). It is the single
// seam where a real LLM summarizer plugs in; the domain layer never calls a
// model itself.
type Summarizer interface {
	Summarize(docTexts []string) (string, error)
}

// Embedder optionally re-encodes a cluster summary into a dense vector. When
// Build receives a nil Embedder the cluster vector is the mean of the member
// vectors.
type Embedder interface {
	Embed(text string) ([]float32, error)
}

// DeterministicSummarizer is the stub Summarizer used by tests and until the
// DC wiring provides a real LLM implementation: the summary is the canonical
// member-key concatenation plus a length and content hash, so identical
// member sets always summarize identically regardless of member order.
type DeterministicSummarizer struct{}

// Summarize implements Summarizer. docTexts are canonicalized (copied and
// sorted) before joining, so caller order is irrelevant.
func (DeterministicSummarizer) Summarize(docTexts []string) (string, error) {
	if len(docTexts) == 0 {
		return "", fmt.Errorf("%w: summarize requires at least one member text", ErrInvalid)
	}
	texts := append([]string(nil), docTexts...)
	sort.Strings(texts)
	joined := strings.Join(texts, "+")
	digest := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("stub-summary:v1:%s|%d|%s", joined, len(joined), hex.EncodeToString(digest[:8])), nil
}

// Build assembles the retrieval tree for one module scope. The recursion is:
// cluster the doc set (stopSize default), create a cluster node per group
// with a summary from the Summarizer seam and a dense vector that is either
// the mean of member vectors or an Embedder re-encoding of the summary, and
// stop recursing once a group is within stopSize members — such groups get
// one leaf child per document. node_id is the first 16 hex chars of
// sha256(moduleScope ‖ epoch ‖ path) where path is the cluster's index path
// from the root. Build sorts docs by DocKey first, so the output tree is a
// pure function of (moduleScope, encoderID, epoch, docs, seam behaviour).
func Build(moduleScope, encoderID string, docs []DocVec, summarize Summarizer, embed Embedder) (*RetrievalTree, error) {
	return buildTree(moduleScope, encoderID, 1, DefaultStopSize, docs, summarize, embed)
}

func buildTree(moduleScope, encoderID string, epoch int64, stopSize int, docs []DocVec, summarize Summarizer, embed Embedder) (*RetrievalTree, error) {
	if moduleScope == "" {
		return nil, fmt.Errorf("%w: module_scope required", ErrInvalid)
	}
	if encoderID == "" {
		return nil, fmt.Errorf("%w: encoder_id required", ErrInvalid)
	}
	if epoch < 1 {
		return nil, fmt.Errorf("%w: build_epoch must be >= 1", ErrInvalid)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("%w: at least one doc required", ErrInvalid)
	}
	if summarize == nil {
		return nil, fmt.Errorf("%w: Summarizer seam required (use DeterministicSummarizer)", ErrInvalid)
	}
	dim := len(docs[0].Vec)
	if dim == 0 {
		return nil, fmt.Errorf("%w: doc %s has an empty vector", ErrInvalid, docs[0].DocKey)
	}
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		d := &docs[i]
		if d.DocKey == "" {
			return nil, fmt.Errorf("%w: doc %d missing doc_key", ErrInvalid, i)
		}
		if seen[d.DocKey] {
			return nil, fmt.Errorf("%w: duplicate doc_key %s", ErrInvalid, d.DocKey)
		}
		seen[d.DocKey] = true
		if len(d.Vec) != dim {
			return nil, fmt.Errorf("%w: doc %s vector dim %d differs from %d", ErrInvalid, d.DocKey, len(d.Vec), dim)
		}
		if !finiteVec(d.Vec) {
			return nil, fmt.Errorf("%w: doc %s vector has non-finite values", ErrInvalid, d.DocKey)
		}
	}
	sortedDocs := append([]DocVec(nil), docs...)
	sort.SliceStable(sortedDocs, func(x, y int) bool { return sortedDocs[x].DocKey < sortedDocs[y].DocKey })

	b := &builder{
		scope:     moduleScope,
		epoch:     epoch,
		stop:      stopSize,
		docs:      sortedDocs,
		dim:       dim,
		summarize: summarize,
		embed:     embed,
	}
	group := make([]int, len(sortedDocs))
	for i := range group {
		group[i] = i
	}
	root, err := b.node(group, "")
	if err != nil {
		return nil, err
	}
	t := &RetrievalTree{
		// "#tree" cannot collide with any node path (paths are "" or
		// slash-joined cluster indices), so tree_id stays distinct from
		// the root node_id that shares the hash inputs.
		TreeID:      idHash(moduleScope, epoch, "#tree"),
		ModuleScope: moduleScope,
		RootID:      root.NodeID,
		BuildEpoch:  epoch,
		EncoderID:   encoderID,
		Nodes:       make([]Node, 0, len(b.nodes)),
	}
	for _, n := range b.nodes {
		t.Nodes = append(t.Nodes, *n)
	}
	if err := t.Validate(); // the builder must never emit a shape Validate rejects
	err != nil {
		return nil, err
	}
	return t, nil
}

type builder struct {
	scope     string
	epoch     int64
	stop      int
	docs      []DocVec
	dim       int
	summarize Summarizer
	embed     Embedder
	nodes     []*Node
}

// node builds the cluster node covering group (ascending positions into
// b.docs) at the given index path, appending itself and its subtree pre-order.
func (b *builder) node(group []int, path string) (*Node, error) {
	id := idHash(b.scope, b.epoch, path)
	members := make([]string, 0, len(group))
	for _, p := range group {
		members = append(members, b.docs[p].DocKey)
	}
	summary, err := b.summarize.Summarize(append([]string(nil), members...))
	if err != nil {
		return nil, fmt.Errorf("%w: summarize cluster %s: %v", ErrInvalid, id, err)
	}
	vec, err := b.clusterVec(group, summary)
	if err != nil {
		return nil, fmt.Errorf("%w: cluster %s dense vector: %v", ErrInvalid, id, err)
	}
	n := &Node{
		NodeID:          id,
		IsCluster:       true,
		Members:         members,
		SummaryRef:      summary,
		ClusterDenseRef: encodeDenseRef(vec),
		Keywords:        keywordsOf(members),
		Children:        []string{},
	}
	b.nodes = append(b.nodes, n)
	if len(group) <= b.stop {
		for i, p := range group {
			leaf := &Node{
				NodeID:   idHash(b.scope, b.epoch, path+"/"+strconv.Itoa(i)),
				DocKey:   b.docs[p].DocKey,
				ParentID: id,
				Keywords: []string{},
			}
			b.nodes = append(b.nodes, leaf)
			n.Children = append(n.Children, leaf.NodeID)
		}
		return n, nil
	}
	sub := make([]DocVec, 0, len(group))
	for _, p := range group {
		sub = append(sub, b.docs[p])
	}
	groups := Cluster(sub, b.stop)
	if len(groups) < 2 {
		return nil, fmt.Errorf("%w: clustering failed to split group of %d docs at %s", ErrInvalid, len(group), path)
	}
	for gi, local := range groups {
		childGroup := make([]int, 0, len(local))
		for _, li := range local {
			childGroup = append(childGroup, group[li])
		}
		child, err := b.node(childGroup, path+"/"+strconv.Itoa(gi))
		if err != nil {
			return nil, err
		}
		child.ParentID = id
		n.Children = append(n.Children, child.NodeID)
	}
	return n, nil
}

// clusterVec is the member-vector mean, or the Embedder re-encoding of the
// summary when one is injected. The embedder must keep the encoder dimension.
func (b *builder) clusterVec(group []int, summary string) ([]float32, error) {
	if b.embed != nil {
		v, err := b.embed.Embed(summary)
		if err != nil {
			return nil, err
		}
		if len(v) != b.dim {
			return nil, fmt.Errorf("embedder returned dim %d, want %d", len(v), b.dim)
		}
		if !finiteVec(v) {
			return nil, fmt.Errorf("embedder returned non-finite values")
		}
		return append([]float32(nil), v...), nil
	}
	mean := make([]float64, b.dim)
	for _, p := range group {
		for i, x := range b.docs[p].Vec {
			mean[i] += float64(x)
		}
	}
	out := make([]float32, b.dim)
	for i, s := range mean {
		out[i] = float32(s / float64(len(group)))
	}
	return out, nil
}

// idHash derives the deterministic 16-hex-char identifier from
// moduleScope, build epoch, and node path, joined with unit separators so
// field boundaries cannot be forged by hostile inputs.
func idHash(scope string, epoch int64, path string) string {
	h := sha256.New()
	h.Write([]byte(scope))
	h.Write([]byte{0x1f})
	h.Write([]byte(strconv.FormatInt(epoch, 10)))
	h.Write([]byte{0x1f})
	h.Write([]byte(path))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// keywordsOf extracts up to five deterministic keywords from the member doc
// keys: alphanumeric tokens (lowercased, length >= 2) ranked by frequency
// with ties broken lexicographically.
func keywordsOf(members []string) []string {
	counts := make(map[string]int)
	for _, key := range members {
		for _, tok := range tokenizeKey(key) {
			counts[tok]++
		}
	}
	toks := make([]string, 0, len(counts))
	for tok := range counts {
		toks = append(toks, tok)
	}
	sort.Slice(toks, func(x, y int) bool {
		if counts[toks[x]] != counts[toks[y]] {
			return counts[toks[x]] > counts[toks[y]]
		}
		return toks[x] < toks[y]
	})
	if len(toks) > 5 {
		toks = toks[:5]
	}
	return toks
}

func tokenizeKey(key string) []string {
	var toks []string
	var cur []rune
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, unicode.ToLower(r))
			continue
		}
		if len(cur) >= 2 {
			toks = append(toks, string(cur))
		}
		cur = nil
	}
	if len(cur) >= 2 {
		toks = append(toks, string(cur))
	}
	return toks
}

const densePrefix = "dense.v1:"

// encodeDenseRef inlines a cluster dense vector as hex float32 bits so the
// tree stays self-contained and Assign can compare against it without any
// store round-trip. A production artifact ref can replace this format as
// long as decoding stays available to the assignment path.
func encodeDenseRef(v []float32) string {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		putUint32LE(buf[i*4:], math.Float32bits(x))
	}
	return densePrefix + hex.EncodeToString(buf)
}

func decodeDenseRef(ref string) ([]float32, error) {
	if !strings.HasPrefix(ref, densePrefix) {
		return nil, fmt.Errorf("missing %s prefix", densePrefix)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(ref, densePrefix))
	if err != nil {
		return nil, err
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("truncated vector payload")
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(uint32LE(raw[i*4:]))
	}
	return out, nil
}

func putUint32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func uint32LE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
