package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"io"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/artifact"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/tree"
)

// ---- 测试替身 ---------------------------------------------------------------

type sliceSource struct {
	events []ReleaseEvent
}

func (s *sliceSource) Next(ctx context.Context) (ReleaseEvent, error) {
	if err := ctx.Err(); err != nil {
		return ReleaseEvent{}, err
	}
	if len(s.events) == 0 {
		return ReleaseEvent{}, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

type errSource struct{ err error }

func (s errSource) Next(context.Context) (ReleaseEvent, error) { return ReleaseEvent{}, s.err }

type mapSink struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMapSink() *mapSink { return &mapSink{objects: make(map[string][]byte)} }

func (s *mapSink) Put(_ context.Context, key string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), b...)
	return nil
}

func (s *mapSink) keySet() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.objects))
	for k := range s.objects {
		out[k] = true
	}
	return out
}

func (s *mapSink) get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[key]
}

func (s *mapSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

type recordingReceipts struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingReceipts) Ready(_ context.Context, manifestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, manifestID)
	return nil
}

func (r *recordingReceipts) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// stubEncoder 按_doc key 确定性产出三路 Repr，可注入失败。
type stubEncoder struct {
	failOn func(doc ReleaseDoc) error
}

var errInjected = errors.New("injected encoder failure")

func (e *stubEncoder) Encode(_ context.Context, doc ReleaseDoc) (Repr, error) {
	if e.failOn != nil {
		if err := e.failOn(doc); err != nil {
			return Repr{}, err
		}
	}
	seed := sha256.Sum256([]byte(doc.DocKey))
	dense := make([]float32, 8)
	for i := range dense {
		dense[i] = float32(seed[i%len(seed)]) / 256
	}
	multi := make([]float32, 8) // 2 rows x 4 dims
	for i := range multi {
		multi[i] = float32(seed[(i+8)%len(seed)]) / 512
	}
	return Repr{
		Dense: dense,
		Sparse: []artifact.Term{
			{TermID: binary.LittleEndian.Uint32(seed[0:4]), Weight: 200},
			{TermID: binary.LittleEndian.Uint32(seed[4:8]), Weight: 100},
		},
		Multi:     multi,
		MultiDim:  4,
		EncoderID: "stub-encoder.v1",
	}, nil
}

type mapStructures map[string][]byte

func (s mapStructures) Structure(_ context.Context, ref string) ([]byte, error) {
	b, ok := s[ref]
	if !ok {
		return nil, fmt.Errorf("structure %q not found", ref)
	}
	return b, nil
}

func structureJSON(revision string) []byte {
	b, _ := json.Marshal(wholeindex.TreeJSON{
		RevisionID: revision,
		Nodes: []wholeindex.NodeJSON{{
			NodeID: "n1", Level: 1, Title: "标题", ParaIndex: -1, CharStart: 0, CharEnd: 10,
		}},
	})
	return b
}

func testEvent(eventID string, docKeys ...string) ReleaseEvent {
	ev := ReleaseEvent{
		EventID:     eventID,
		ModuleID:    "mod-a",
		ReleaseID:   "rel-" + eventID,
		PublishedAt: "2026-10-06T15:03:32+08:00",
	}
	for _, k := range docKeys {
		ev.Docs = append(ev.Docs, ReleaseDoc{
			DocKey:        k,
			RevisionID:    "rev-" + k,
			ContentSHA256: "sha256-" + k,
			CharLen:       100 + len(k),
			StructureRef:  "structure/" + k,
		})
	}
	return ev
}

func newPipeline(p *Pipeline, sink *mapSink, rcpt *recordingReceipts) *Pipeline {
	p.Sink = sink
	p.Receipts = rcpt
	if p.Seen == nil {
		p.Seen = NewMemSeen()
	}
	if p.Encoder == nil {
		p.Encoder = &stubEncoder{}
	}
	return p
}

func mustHandle(t *testing.T, p *Pipeline, ev ReleaseEvent) {
	t.Helper()
	if err := p.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("HandleEvent(%s): %v", ev.EventID, err)
	}
}

// ---- 集成测试 ---------------------------------------------------------------

// TestRunThreeDocsArtifactsAndKeys：3 文档假事件 → 工件键集合精确断言 +
// manifest/树往返 + 载荷可解码 + READY 恰一次。
func TestRunThreeDocsArtifactsAndKeys(t *testing.T) {
	ev := testEvent("evt-1", "doc-a", "doc-b", "doc-c")
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{Source: &sliceSource{events: []ReleaseEvent{ev}}}, sink, rcpt)

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ids := rcpt.list()
	if len(ids) != 1 {
		t.Fatalf("READY receipts = %v, want exactly 1", ids)
	}
	mid := ids[0]

	var m artifact.WholeDocIndexManifest
	if b := sink.get(wholeindex.ManifestKey(mid)); b == nil {
		t.Fatalf("manifest object %s missing", wholeindex.ManifestKey(mid))
	} else if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.ManifestID != mid {
		t.Fatalf("stored manifest_id %q != receipt %q", m.ManifestID, mid)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest validate: %v", err)
	}
	if got := artifact.ManifestID(m); got != mid {
		t.Fatalf("recomputed manifest_id %q != %q (round-trip drift)", got, mid)
	}
	if len(m.Docs) != 3 {
		t.Fatalf("manifest docs = %d, want 3", len(m.Docs))
	}

	// 工件键集合：3 文档 × 3 路 + manifest + tree = 11，键全部带 manifest_id 前缀。
	want := map[string]bool{wholeindex.ManifestKey(mid): true, wholeindex.TreeKey(mid): true}
	for i, d := range m.Docs {
		if d.DocKey != ev.Docs[i].DocKey {
			t.Fatalf("docs[%d].doc_key = %q, want %q（docs 保序）", i, d.DocKey, ev.Docs[i].DocKey)
		}
		if d.StructureRef != ev.Docs[i].StructureRef {
			t.Fatalf("docs[%d].structure_ref = %q, want %q", i, d.StructureRef, ev.Docs[i].StructureRef)
		}
		if d.SourceChars != ev.Docs[i].CharLen {
			t.Fatalf("docs[%d].source_chars = %d, want %d", i, d.SourceChars, ev.Docs[i].CharLen)
		}
		if d.EncoderID != "stub-encoder.v1" {
			t.Fatalf("docs[%d].encoder_id = %q", i, d.EncoderID)
		}
		want[wholeindex.ObjectKey(mid, d.DenseRef)] = true
		want[wholeindex.ObjectKey(mid, d.SparseRef)] = true
		want[wholeindex.ObjectKey(mid, d.MultiRef)] = true

		dense := sink.get(wholeindex.ObjectKey(mid, d.DenseRef))
		sparse := sink.get(wholeindex.ObjectKey(mid, d.SparseRef))
		multi := sink.get(wholeindex.ObjectKey(mid, d.MultiRef))
		if artifact.DequantizeI8(dense, 8) == nil {
			t.Fatalf("dense payload of %s failed to decode", d.DocKey)
		}
		terms, err := artifact.DecodeImpact(sparse)
		if err != nil || len(terms) != 2 {
			t.Fatalf("sparse payload of %s: terms=%v err=%v, want 2 terms", d.DocKey, terms, err)
		}
		if d.MultiTokens != 2 {
			t.Fatalf("docs[%d].multi_tokens = %d, want 2", i, d.MultiTokens)
		}
		if artifact.DequantizeMulti(multi, d.MultiTokens, 4) == nil {
			t.Fatalf("multi payload of %s failed to decode", d.DocKey)
		}
		if d.BudgetBytes != len(dense)+len(sparse)+len(multi) {
			t.Fatalf("docs[%d].budget_bytes = %d, want %d", i, d.BudgetBytes, len(dense)+len(sparse)+len(multi))
		}
	}
	if got := sink.keySet(); !reflect.DeepEqual(got, want) {
		t.Fatalf("artifact key set mismatch:\n got  %v\n want %v", sortedKeys(got), sortedKeys(want))
	}

	var tr tree.RetrievalTree
	if b := sink.get(wholeindex.TreeKey(mid)); b == nil {
		t.Fatalf("tree object %s missing", wholeindex.TreeKey(mid))
	} else if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatalf("unmarshal tree: %v", err)
	}
	if err := tr.Validate(); err != nil {
		t.Fatalf("tree validate: %v", err)
	}
	if tr.ModuleScope != ev.ModuleID {
		t.Fatalf("tree module_scope = %q, want %q", tr.ModuleScope, ev.ModuleID)
	}
	if tr.EncoderID != "stub-encoder.v1" {
		t.Fatalf("tree encoder_id = %q", tr.EncoderID)
	}
	if p.Seen.(*MemSeen).Len() != 1 {
		t.Fatalf("seen ledger = %d, want 1", p.Seen.(*MemSeen).Len())
	}
}

// TestManifestIDDeterminism：同一发布内容（不同 event_id）两次独立处理必得
// 同一 manifest_id、同一键集、逐字节相同工件。
func TestManifestIDDeterminism(t *testing.T) {
	run := func(eventID string) (string, *mapSink) {
		ev := testEvent(eventID, "doc-a", "doc-b", "doc-c")
		ev.ReleaseID = "rel-same" // 两事件仅 event_id 不同，发布内容相同
		sink, rcpt := newMapSink(), &recordingReceipts{}
		p := newPipeline(&Pipeline{Source: &sliceSource{events: []ReleaseEvent{ev}}}, sink, rcpt)
		if err := p.Run(context.Background()); err != nil {
			t.Fatalf("Run(%s): %v", eventID, err)
		}
		ids := rcpt.list()
		if len(ids) != 1 {
			t.Fatalf("Run(%s): receipts = %v", eventID, ids)
		}
		return ids[0], sink
	}
	mid1, sink1 := run("evt-a")
	mid2, sink2 := run("evt-b")
	if mid1 != mid2 {
		t.Fatalf("manifest_id differs across identical content: %q vs %q", mid1, mid2)
	}
	k1, k2 := sink1.keySet(), sink2.keySet()
	if !reflect.DeepEqual(k1, k2) {
		t.Fatalf("key sets differ across identical content")
	}
	for k := range k1 {
		if string(sink1.get(k)) != string(sink2.get(k)) {
			t.Fatalf("object %s bytes differ across identical content", k)
		}
	}
}

// TestReplayIdempotent：同 event_id 重放直接返回 nil，不产生新工件/回执。
func TestReplayIdempotent(t *testing.T) {
	ev := testEvent("evt-1", "doc-a", "doc-b", "doc-c")
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{}, sink, rcpt)
	mustHandle(t, p, ev)
	before := sink.keySet()
	if len(before) != 11 {
		t.Fatalf("artifact count = %d, want 11", len(before))
	}

	if err := p.HandleEvent(context.Background(), ev); err != nil {
		t.Fatalf("replay HandleEvent: %v", err)
	}
	if got := sink.keySet(); !reflect.DeepEqual(got, before) {
		t.Fatalf("replay changed artifact key set")
	}
	if got := sink.len(); got != 11 {
		t.Fatalf("replay rewrote objects: count = %d, want 11", got)
	}
	if ids := rcpt.list(); len(ids) != 1 {
		t.Fatalf("replay emitted extra READY: %v", ids)
	}
	if p.Seen.(*MemSeen).Len() != 1 {
		t.Fatalf("seen ledger = %d, want 1", p.Seen.(*MemSeen).Len())
	}
}

// TestEncoderFailureNoReadyRetryable：第二个 doc 编码失败 → 事件失败、零工件、
// 无 READY、账本已撤销；修复后重试同一事件成功。
func TestEncoderFailureNoReadyRetryable(t *testing.T) {
	ev := testEvent("evt-1", "doc-a", "doc-b", "doc-c")
	enc := &stubEncoder{failOn: func(doc ReleaseDoc) error {
		if doc.DocKey == "doc-b" {
			return errInjected
		}
		return nil
	}}
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{Encoder: enc}, sink, rcpt)

	err := p.HandleEvent(context.Background(), ev)
	if !errors.Is(err, errInjected) {
		t.Fatalf("HandleEvent err = %v, want injected failure", err)
	}
	if n := sink.len(); n != 0 {
		t.Fatalf("failed event wrote %d objects, want 0（不半提交）", n)
	}
	if ids := rcpt.list(); len(ids) != 0 {
		t.Fatalf("failed event emitted READY %v", ids)
	}
	if p.Seen.(*MemSeen).Len() != 0 {
		t.Fatalf("failed event stayed claimed, blocking retry")
	}

	enc.failOn = nil
	mustHandle(t, p, ev)
	if ids := rcpt.list(); len(ids) != 1 {
		t.Fatalf("retry receipts = %v, want 1", ids)
	}
	if n := sink.len(); n != 11 {
		t.Fatalf("retry artifact count = %d, want 11", n)
	}
	// 成功后的重放仍是 no-op。
	mustHandle(t, p, ev)
	if ids := rcpt.list(); len(ids) != 1 {
		t.Fatalf("post-success replay emitted extra READY: %v", ids)
	}
}

// TestContextCancelled：取消的 context 干净返回，零工件、无 READY、不记账。
func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ev := testEvent("evt-1", "doc-a")
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{}, sink, rcpt)

	if err := p.HandleEvent(ctx, ev); !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleEvent err = %v, want context.Canceled", err)
	}
	if n := sink.len(); n != 0 || len(rcpt.list()) != 0 || p.Seen.(*MemSeen).Len() != 0 {
		t.Fatalf("cancelled run left state: objects=%d receipts=%d seen=%d", n, len(rcpt.list()), p.Seen.(*MemSeen).Len())
	}
}

// TestStructureValidation：注入 StructureSource 时 revision 一致放行、
// 不一致拒绝且不 emit READY、取树失败按可重试错误返回。
func TestStructureValidation(t *testing.T) {
	match := testEvent("evt-ok", "doc-a")
	mismatch := testEvent("evt-bad", "doc-a")
	missing := testEvent("evt-miss", "doc-z")
	structures := mapStructures{
		"structure/doc-a": structureJSON("rev-doc-a"), // 与事件的 revision_id 一致
		"structure/doc-b": structureJSON("rev-OTHER"), // 故意不一致
	}
	mismatch.Docs[0].StructureRef = "structure/doc-b"

	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{Structures: structures}, sink, rcpt)

	mustHandle(t, p, match)
	if ids := rcpt.list(); len(ids) != 1 {
		t.Fatalf("matching structure rejected: receipts=%v", ids)
	}

	if err := p.HandleEvent(context.Background(), mismatch); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("mismatch err = %v, want ErrInvalidEvent", err)
	}
	if err := p.HandleEvent(context.Background(), missing); err == nil || err.Error() == "" {
		t.Fatalf("missing structure err = %v, want fetch failure", err)
	}
	if ids := rcpt.list(); len(ids) != 1 {
		t.Fatalf("structure failures emitted READY %v", ids)
	}
}

// TestConcurrentEvents：并发处理互异事件 + 重放竞争，-race 下账本与工件
// 写入均安全，恰产出一批互异 manifest。
func TestConcurrentEvents(t *testing.T) {
	const n = 8
	events := make([]ReleaseEvent, n)
	for i := range events {
		events[i] = testEvent(fmt.Sprintf("evt-c%d", i), fmt.Sprintf("doc-c%d-a", i), fmt.Sprintf("doc-c%d-b", i))
	}
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{}, sink, rcpt)

	var wg sync.WaitGroup
	for _, ev := range events {
		ev := ev
		wg.Add(2)
		go func() { defer wg.Done(); mustHandle(t, p, ev) }() // 处理者
		go func() { defer wg.Done(); mustHandle(t, p, ev) }() // 重放竞争者
	}
	wg.Wait()

	ids := rcpt.list()
	if len(ids) != n {
		t.Fatalf("receipts = %d, want %d", len(ids), n)
	}
	distinct := make(map[string]bool)
	for _, id := range ids {
		distinct[id] = true
	}
	if len(distinct) != n {
		t.Fatalf("distinct manifest ids = %d, want %d", len(distinct), n)
	}
	if got, want := sink.len(), n*(2*3+2); got != want {
		t.Fatalf("objects = %d, want %d", got, want)
	}
	if p.Seen.(*MemSeen).Len() != n {
		t.Fatalf("seen ledger = %d, want %d", p.Seen.(*MemSeen).Len(), n)
	}
}

// TestRunSourceError：事件源错误终止 Run 并向上传播。
func TestRunSourceError(t *testing.T) {
	boom := errors.New("source boom")
	sink, rcpt := newMapSink(), &recordingReceipts{}
	p := newPipeline(&Pipeline{Source: errSource{err: boom}}, sink, rcpt)
	if err := p.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run err = %v, want source error", err)
	}
}

// TestValidateEvent：C-1 字段级契约的拒绝面。
func TestValidateEvent(t *testing.T) {
	good := testEvent("evt-1", "doc-a")
	cases := map[string]func(*ReleaseEvent){
		"event_id":   func(e *ReleaseEvent) { e.EventID = "" },
		"module_id":  func(e *ReleaseEvent) { e.ModuleID = "" },
		"release_id": func(e *ReleaseEvent) { e.ReleaseID = "" },
		"docs":       func(e *ReleaseEvent) { e.Docs = nil },
		"doc_key":    func(e *ReleaseEvent) { e.Docs[0].DocKey = "" },
		"revision":   func(e *ReleaseEvent) { e.Docs[0].RevisionID = "" },
		"content":    func(e *ReleaseEvent) { e.Docs[0].ContentSHA256 = "" },
		"char_len":   func(e *ReleaseEvent) { e.Docs[0].CharLen = 0 },
	}
	for name, mutate := range cases {
		ev := good
		ev.Docs = append([]ReleaseDoc(nil), good.Docs...)
		mutate(&ev)
		if err := validateEvent(ev); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	if err := validateEvent(good); err != nil {
		t.Errorf("good event rejected: %v", err)
	}
}

// TestValidateRepr：Encoder 输出形状契约的拒绝面。
func TestValidateRepr(t *testing.T) {
	good := Repr{Dense: []float32{1}, Sparse: nil, Multi: []float32{1, 2, 3, 4}, MultiDim: 4, EncoderID: "e"}
	if err := validateRepr(good); err != nil {
		t.Fatalf("good repr rejected: %v", err)
	}
	cases := map[string]func(*Repr){
		"empty dense":     func(r *Repr) { r.Dense = nil },
		"zero dim":        func(r *Repr) { r.MultiDim = 0 },
		"misaligned":      func(r *Repr) { r.Multi = []float32{1, 2, 3} },
		"no rows":         func(r *Repr) { r.Multi = nil },
		"missing encoder": func(r *Repr) { r.EncoderID = "" },
	}
	for name, mutate := range cases {
		r := good
		mutate(&r)
		if err := validateRepr(r); !errors.Is(err, ErrInvalidRepr) {
			t.Errorf("%s: err = %v, want ErrInvalidRepr", name, err)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
