// Package artifact is the pure-logic domain for whole-doc index artifacts:
// manifest canonicalization and content-addressed IDs, symmetric int8
// quantization codecs for dense and multi-token-matrix payloads, impact
// encoding for sparse payloads, and the double-buffered manifest switcher.
//
// The package performs no object-storage IO and invokes no encoder; fetching
// and producing the referenced bytes is the caller's responsibility.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// MaxMultiTokens bounds the per-doc multi-token matrix row count.
const MaxMultiTokens = 2048

// WholeDocIndexManifest describes one whole-doc index release: the artifact
// references for every document, in docs order. Refs point at objects in an
// external store; this package never fetches them.
type WholeDocIndexManifest struct {
	ManifestID string     `json:"manifest_id"`
	ModuleID   string     `json:"module_id"`
	ReleaseID  string     `json:"release_id"`
	Docs       []DocEntry `json:"docs"`
}

// DocEntry references all retrieval-lane artifacts of a single document.
type DocEntry struct {
	DocKey       string `json:"doc_key"`
	StructureRef string `json:"structure_ref"`
	DenseRef     string `json:"dense_ref"`
	SparseRef    string `json:"sparse_ref"`
	MultiRef     string `json:"multi_ref"`
	MultiTokens  int    `json:"multi_tokens"`
	EncoderID    string `json:"encoder_id"`
	SourceChars  int    `json:"source_chars"`
	BudgetBytes  int    `json:"budget_bytes"`
}

// Validate enforces the field-level manifest contract: at least one doc,
// multi_tokens within [1, MaxMultiTokens], budget_bytes > 0 and a non-empty
// encoder_id on every entry.
func (m WholeDocIndexManifest) Validate() error {
	if len(m.Docs) < 1 {
		return fmt.Errorf("%w: docs must not be empty", ErrInvalidManifest)
	}
	for i, d := range m.Docs {
		if d.MultiTokens < 1 || d.MultiTokens > MaxMultiTokens {
			return fmt.Errorf("%w: docs[%d].multi_tokens=%d out of [1,%d]", ErrInvalidManifest, i, d.MultiTokens, MaxMultiTokens)
		}
		if d.BudgetBytes <= 0 {
			return fmt.Errorf("%w: docs[%d].budget_bytes=%d must be > 0", ErrInvalidManifest, i, d.BudgetBytes)
		}
		if d.EncoderID == "" {
			return fmt.Errorf("%w: docs[%d].encoder_id must not be empty", ErrInvalidManifest, i)
		}
	}
	return nil
}

// ErrInvalidManifest marks manifest contract violations.
var ErrInvalidManifest = errors.New("invalid manifest")

// CanonicalJSON renders the manifest as canonical JSON bytes: fixed field
// order (module_id, release_id, docs; within each doc entry doc_key,
// structure_ref, dense_ref, sparse_ref, multi_ref, multi_tokens, encoder_id,
// source_chars, budget_bytes), docs in original order, no indentation or
// extra whitespace, minimal string escaping.
//
// The manifest_id field itself is NOT part of the canonical bytes: it is
// derived from them (see ManifestID), so including it would be
// self-referential. The same manifest content therefore always yields the
// same canonical bytes and the same ID.
func CanonicalJSON(m WholeDocIndexManifest) []byte {
	b := make([]byte, 0, 128+len(m.Docs)*160)
	b = append(b, '{')
	b = appendJSONKey(b, "module_id")
	b = appendJSONString(b, m.ModuleID)
	b = append(b, ',')
	b = appendJSONKey(b, "release_id")
	b = appendJSONString(b, m.ReleaseID)
	b = append(b, ',')
	b = appendJSONKey(b, "docs")
	b = append(b, '[')
	for i, d := range m.Docs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '{')
		b = appendJSONKey(b, "doc_key")
		b = appendJSONString(b, d.DocKey)
		b = append(b, ',')
		b = appendJSONKey(b, "structure_ref")
		b = appendJSONString(b, d.StructureRef)
		b = append(b, ',')
		b = appendJSONKey(b, "dense_ref")
		b = appendJSONString(b, d.DenseRef)
		b = append(b, ',')
		b = appendJSONKey(b, "sparse_ref")
		b = appendJSONString(b, d.SparseRef)
		b = append(b, ',')
		b = appendJSONKey(b, "multi_ref")
		b = appendJSONString(b, d.MultiRef)
		b = append(b, ',')
		b = appendJSONKey(b, "multi_tokens")
		b = strconv.AppendInt(b, int64(d.MultiTokens), 10)
		b = append(b, ',')
		b = appendJSONKey(b, "encoder_id")
		b = appendJSONString(b, d.EncoderID)
		b = append(b, ',')
		b = appendJSONKey(b, "source_chars")
		b = strconv.AppendInt(b, int64(d.SourceChars), 10)
		b = append(b, ',')
		b = appendJSONKey(b, "budget_bytes")
		b = strconv.AppendInt(b, int64(d.BudgetBytes), 10)
		b = append(b, '}')
	}
	b = append(b, ']')
	b = append(b, '}')
	return b
}

// ManifestID returns hex(sha256(CanonicalJSON(m)))[0:32], the content-addressed
// manifest identity. It is deterministic: identical manifest content yields an
// identical ID. The stored m.ManifestID field does not participate in the hash.
func ManifestID(m WholeDocIndexManifest) string {
	sum := sha256.Sum256(CanonicalJSON(m))
	return hex.EncodeToString(sum[:16])
}

// AssignID sets m.ManifestID to the content-addressed ManifestID(m), making the
// manifest self-consistent for Load.
func AssignID(m *WholeDocIndexManifest) {
	m.ManifestID = ManifestID(*m)
}

func appendJSONKey(b []byte, key string) []byte {
	b = append(b, '"')
	b = append(b, key...)
	b = append(b, '"', ':')
	return b
}

// appendJSONString escapes only what minimal canonical JSON requires: the
// quote and backslash and control characters below 0x20. UTF-8 passes through
// unmodified; callers must supply valid UTF-8 strings.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigit(c>>4), hexDigit(c&0xf))
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

func hexDigit(v byte) byte {
	if v < 10 {
		return '0' + v
	}
	return 'a' + v - 10
}
