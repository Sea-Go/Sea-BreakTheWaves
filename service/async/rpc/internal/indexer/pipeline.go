// Package indexer 是 M2 indexer 三段 worker 的进程骨架：把 release 事件
// （C-1 镜像）加工成全文档索引工件（manifest + 三路载荷 + 检索树），并以
// C-2 READY 回执提交。
//
// 本包只做编排，传输与计算全部走可替换接缝：
//   - EventSource：事件来源（真实 DC 事件后续接入；dev 形态为 JSONL 文件）；
//   - Encoder：单文档 → 三路原始表示（Repr），字段对齐 artifact 包量化输入；
//   - Sink：工件字节写（对象存储 / 内存）；
//   - ReceiptSink：C-2 READY 回执发射；
//   - SeenStore：event_id 幂等账本；
//   - StructureSource：可选的结构树 JSON 来源（evalseed TreeJSON 契约镜像，
//     用于 revision 一致性校验）。
//
// 确定性与不半提交：manifest_id 内容寻址（artifact.ManifestID），同一事件
// 内容重算必得同一 id；所有工件键都带 manifest_id 前缀，READY 是最终提交点
// ——任一 doc 失败即整事件失败（不 emit READY、不记账本），重试覆盖同一批
// 键，天然幂等。
package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"io"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/artifact"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/tree"
)

// ============================================================================
// C-1 事件契约镜像（JSON snake_case）。真实 DC 事件的字段级镜像，改动需与
// 事件生产方同步。
// ============================================================================

// ReleaseEvent 镜像 C-1 release 发布事件：一次模块发布的全部文档清单。
type ReleaseEvent struct {
	// EventID 事件唯一 ID，幂等账本（SeenStore）的键。
	EventID string `json:"event_id"`
	// ModuleID 发布所属模块 ID，兼作检索树的 module_scope。
	ModuleID string `json:"module_id"`
	// ReleaseID 发布 ID，记入 manifest。
	ReleaseID string `json:"release_id"`
	// PublishedAt 发布时间戳（原样透传，不参与 manifest_id）。
	PublishedAt string `json:"published_at"`
	// Docs 本次发布的文档列表（docs 顺序进入 manifest，顺序不同即
	// manifest_id 不同）。
	Docs []ReleaseDoc `json:"docs"`
}

// ReleaseDoc 镜像 C-1 事件的单文档条目：内容的引用与指纹，不含正文本体
// （正文由 Encoder 侧按引用取用）。
type ReleaseDoc struct {
	DocKey        string `json:"doc_key"`
	RevisionID    string `json:"revision_id"`
	ContentSHA256 string `json:"content_sha256"`
	CharLen       int    `json:"char_len"`
	StructureRef  string `json:"structure_ref"`
}

// ============================================================================
// 接缝（seams）
// ============================================================================

// EventSource 是事件传输接缝。Next 返回 io.EOF 表示流正常结束；其余错误
// 终止整个 Run（生产形态可在外层换重试策略）。
type EventSource interface {
	Next(ctx context.Context) (ReleaseEvent, error)
}

// Encoder 是编码接缝：单文档 → 三路原始表示。真实实现（DC representation）
// 后续接入；字段与 artifact 包量化输入一一对应：
//
//	Dense  []float32        → artifact.QuantizeF32
//	Sparse []artifact.Term  → artifact.EncodeImpact
//	Multi  []float32+MultiDim → artifact.QuantizeMulti
type Encoder interface {
	Encode(ctx context.Context, doc ReleaseDoc) (Repr, error)
}

// Repr 是 Encoder 的输出：三路（dense/sparse/multi）原始表示。Multi 为
// 行主序矩阵，行宽 MultiDim，行数即 manifest 的 multi_tokens。
type Repr struct {
	Dense     []float32
	Sparse    []artifact.Term
	Multi     []float32
	MultiDim  int
	EncoderID string
}

// Sink 是工件写接缝：key → 字节。实现必须支持对同一 key 的幂等覆盖写
// （重试路径会重写同一批键）。
type Sink interface {
	Put(ctx context.Context, key string, b []byte) error
}

// ReceiptSink 是 C-2 回执接缝：manifest 就绪通知。Ready 在该 manifest 的
// 全部工件（含检索树）落位之后才会被调用，且必须幂等（重试可能重发）。
type ReceiptSink interface {
	Ready(ctx context.Context, manifestID string) error
}

// SeenStore 是 event_id 幂等账本接缝。Claim 原子记账并报告是否首次；
// Release 在该次尝试失败后撤销记账，使事件可重试。内存实现见 MemSeen，
// 生产形态可换持久化实现。
type SeenStore interface {
	Claim(eventID string) bool
	Release(eventID string)
}

// StructureSource 是可选的结构树接缝：按 structure_ref 取结构树 JSON 字节
// （evalseed TreeJSON 契约，镜像见 structure.go）。nil 时跳过校验（dev
// 形态默认）。返回的错误按可重试处理。
type StructureSource interface {
	Structure(ctx context.Context, ref string) ([]byte, error)
}

// 错误哨兵。
var (
	// ErrInvalidEvent 标记事件字段级契约违规。
	ErrInvalidEvent = errors.New("invalid release event")
	// ErrInvalidRepr 标记 Encoder 输出不可量化（维度/行数/encoder_id）。
	ErrInvalidRepr = errors.New("invalid encoder repr")
)

// 工件对象命名：doc 载荷键 = manifestID + "/" + 内容寻址 ref；
// manifest 与树是固定名对象。给定 manifest_id 后，ref → 对象键是纯函数。
const (
	manifestObject = "manifest.v1.json"
	treeObject     = "tree.v1.json"
)

// Pipeline 是三段 worker 骨架：事件 → 编码量化/组 manifest → 工件落位 +
// 树构建 → READY 回执。字段即装配点，全部为可替换接缝。
type Pipeline struct {
	Source     EventSource
	Encoder    Encoder
	Sink       Sink
	Receipts   ReceiptSink
	Seen       SeenStore
	Structures StructureSource // 可选：nil 跳过结构树校验
	// Summarizer 可选：树构建摘要接缝，nil 用 tree.DeterministicSummarizer
	// （真实 LLM 摘要后续接入）。
	Summarizer tree.Summarizer
}

// Run 消费事件流直到 io.EOF。任一事件处理失败即返回（该事件可重试），
// 生产形态的重试策略由外层装配决定。
func (p *Pipeline) Run(ctx context.Context) error {
	for {
		ev, err := p.Source.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("event source: %w", err)
		}
		if err := p.HandleEvent(ctx, ev); err != nil {
			return fmt.Errorf("event %s: %w", ev.EventID, err)
		}
	}
}

// HandleEvent 处理单个事件。已记账（处理过或在处理中）的 event_id 直接
// 返回 nil（幂等重放）；处理失败则撤销记账并返回错误，事件可重试。
func (p *Pipeline) HandleEvent(ctx context.Context, ev ReleaseEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEvent(ev); err != nil {
		return err
	}
	if !p.Seen.Claim(ev.EventID) {
		return nil
	}
	if err := p.process(ctx, ev); err != nil {
		p.Seen.Release(ev.EventID)
		return err
	}
	return nil
}

// docPayload 暂存单文档量化后的三路字节，等 manifest_id 定键后统一落位。
type docPayload struct {
	dense  []byte
	sparse []byte
	multi  []byte
}

// process 执行一次事件的完整管线。阶段顺序保证不半提交：先全部编码量化，
// 再统一落位工件（键带 manifest_id 前缀），最后发射 READY——因此任何失败
// 都不会 emit READY，重放同一前缀即覆盖补齐。
func (p *Pipeline) process(ctx context.Context, ev ReleaseEvent) error {
	entries := make([]artifact.DocEntry, len(ev.Docs))
	payloads := make([]docPayload, len(ev.Docs))
	vecs := make([]tree.DocVec, len(ev.Docs))
	encoderID := ""
	for i, doc := range ev.Docs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.checkStructure(ctx, doc); err != nil {
			return fmt.Errorf("doc %d (%s): structure: %w", i, doc.DocKey, err)
		}
		repr, err := p.Encoder.Encode(ctx, doc)
		if err != nil {
			return fmt.Errorf("doc %d (%s): encode: %w", i, doc.DocKey, err)
		}
		if err := validateRepr(repr); err != nil {
			return fmt.Errorf("doc %d (%s): %w", i, doc.DocKey, err)
		}
		if encoderID == "" {
			encoderID = repr.EncoderID
		} else if encoderID != repr.EncoderID {
			return fmt.Errorf("%w: doc %d (%s) encoder_id %q differs from %q within one release",
				ErrInvalidRepr, i, doc.DocKey, repr.EncoderID, encoderID)
		}
		dense := artifact.QuantizeF32(repr.Dense)
		sparse := artifact.EncodeImpact(repr.Sparse)
		multi := artifact.QuantizeMulti(repr.Multi, repr.MultiDim)
		entries[i] = artifact.DocEntry{
			DocKey:       doc.DocKey,
			StructureRef: doc.StructureRef,
			DenseRef:     payloadRef("dense", dense),
			SparseRef:    payloadRef("sparse", sparse),
			MultiRef:     payloadRef("multi", multi),
			MultiTokens:  len(repr.Multi) / repr.MultiDim,
			EncoderID:    repr.EncoderID,
			SourceChars:  doc.CharLen,
			BudgetBytes:  len(dense) + len(sparse) + len(multi),
		}
		payloads[i] = docPayload{dense: dense, sparse: sparse, multi: multi}
		vecs[i] = tree.DocVec{DocKey: doc.DocKey, Vec: repr.Dense}
	}

	m := artifact.WholeDocIndexManifest{
		ModuleID:  ev.ModuleID,
		ReleaseID: ev.ReleaseID,
		Docs:      entries,
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	artifact.AssignID(&m)

	// 工件落位：键 = manifestID + "/" + ref（或固定名对象），重试覆盖同键。
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := wholeindex.ObjectKey(m.ManifestID, entries[i].DenseRef)
		if err := p.Sink.Put(ctx, key, payloads[i].dense); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
		key = wholeindex.ObjectKey(m.ManifestID, entries[i].SparseRef)
		if err := p.Sink.Put(ctx, key, payloads[i].sparse); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
		key = wholeindex.ObjectKey(m.ManifestID, entries[i].MultiRef)
		if err := p.Sink.Put(ctx, key, payloads[i].multi); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
	}
	manifestJSON, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := p.put(ctx, wholeindex.ManifestKey(m.ManifestID), manifestJSON); err != nil {
		return err
	}

	// 检索树：DeterministicSummarizer stub（可经 Summarizer 接缝替换为真实
	// LLM），树工件与 manifest 同前缀落位。
	summarizer := p.Summarizer
	if summarizer == nil {
		summarizer = tree.DeterministicSummarizer{}
	}
	tr, err := tree.Build(ev.ModuleID, encoderID, vecs, summarizer, nil)
	if err != nil {
		return fmt.Errorf("tree build: %w", err)
	}
	treeJSON, err := json.Marshal(tr)
	if err != nil {
		return fmt.Errorf("marshal tree: %w", err)
	}
	if err := p.put(ctx, wholeindex.TreeKey(m.ManifestID), treeJSON); err != nil {
		return err
	}

	// 最终提交点：全部工件（含树）落位后才 emit C-2 READY。失败不半提交。
	if err := p.Receipts.Ready(ctx, m.ManifestID); err != nil {
		return fmt.Errorf("emit READY: %w", err)
	}
	return nil
}

func (p *Pipeline) put(ctx context.Context, key string, b []byte) error {
	if err := p.Sink.Put(ctx, key, b); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// checkStructure 在注入 StructureSource 时校验结构树 revision 一致性：
// 按 structure_ref 取 TreeJSON，要求其 revision_id 与事件的 doc.revision_id
// 一致（结构树是冻结修订的派生物，见 evalseed 契约）。
func (p *Pipeline) checkStructure(ctx context.Context, doc ReleaseDoc) error {
	if p.Structures == nil {
		return nil
	}
	if doc.StructureRef == "" {
		return fmt.Errorf("%w: empty structure_ref while StructureSource is set", ErrInvalidEvent)
	}
	b, err := p.Structures.Structure(ctx, doc.StructureRef)
	if err != nil {
		return fmt.Errorf("fetch structure %s: %w", doc.StructureRef, err)
	}
	st, err := wholeindex.ParseStructureTree(b)
	if err != nil {
		return fmt.Errorf("parse structure %s: %w", doc.StructureRef, err)
	}
	if st.RevisionID != doc.RevisionID {
		return fmt.Errorf("%w: structure %s revision %q differs from event revision %q",
			ErrInvalidEvent, doc.StructureRef, st.RevisionID, doc.RevisionID)
	}
	return nil
}

// validateEvent 强制 C-1 字段级契约：三个 ID 非空、docs 非空、每文档
// doc_key/revision_id/content_sha256 非空且 char_len>0。structure_ref 允许
// 为空（无结构树的文档）。
func validateEvent(ev ReleaseEvent) error {
	if ev.EventID == "" {
		return fmt.Errorf("%w: event_id required", ErrInvalidEvent)
	}
	if ev.ModuleID == "" {
		return fmt.Errorf("%w: module_id required", ErrInvalidEvent)
	}
	if ev.ReleaseID == "" {
		return fmt.Errorf("%w: release_id required", ErrInvalidEvent)
	}
	if len(ev.Docs) < 1 {
		return fmt.Errorf("%w: docs must not be empty", ErrInvalidEvent)
	}
	for i, d := range ev.Docs {
		if d.DocKey == "" {
			return fmt.Errorf("%w: docs[%d].doc_key required", ErrInvalidEvent, i)
		}
		if d.RevisionID == "" {
			return fmt.Errorf("%w: docs[%d].revision_id required", ErrInvalidEvent, i)
		}
		if d.ContentSHA256 == "" {
			return fmt.Errorf("%w: docs[%d].content_sha256 required", ErrInvalidEvent, i)
		}
		if d.CharLen <= 0 {
			return fmt.Errorf("%w: docs[%d].char_len=%d must be > 0", ErrInvalidEvent, i, d.CharLen)
		}
	}
	return nil
}

// validateRepr 强制 Repr → 量化输入的形状契约：dense 非空、multi 是
// MultiDim 的整倍数且行数在 [1, MaxMultiTokens]、encoder_id 非空。
func validateRepr(r Repr) error {
	if len(r.Dense) == 0 {
		return fmt.Errorf("%w: dense lane empty", ErrInvalidRepr)
	}
	if r.MultiDim <= 0 || len(r.Multi)%r.MultiDim != 0 {
		return fmt.Errorf("%w: multi lane len %d not a multiple of multi_dim %d", ErrInvalidRepr, len(r.Multi), r.MultiDim)
	}
	rows := len(r.Multi) / r.MultiDim
	if rows < 1 || rows > artifact.MaxMultiTokens {
		return fmt.Errorf("%w: multi rows %d out of [1,%d]", ErrInvalidRepr, rows, artifact.MaxMultiTokens)
	}
	if r.EncoderID == "" {
		return fmt.Errorf("%w: encoder_id required", ErrInvalidRepr)
	}
	return nil
}

// payloadRef 生成内容寻址 ref：lane.v1:hex(sha256(b))[:32]。
func payloadRef(lane string, b []byte) string {
	sum := sha256.Sum256(b)
	return lane + ".v1:" + hex.EncodeToString(sum[:16])
}

// 工件对象键（ObjectKey/ManifestKey/TreeKey）的唯一实现：
// service/common/wholeindex/objectkey.go。
