// Package content prepares immutable knowledge build inputs and reconciles
// independent retrieval outputs. Formal revisions and activation belong to RTW.
package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/corpus"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/chunking"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/document"
)

var ErrInvalid = errors.New("invalid content input")

type ChunkConfig struct {
	// ID is fixed in the RTW release; changing any configuration needs a new ID.
	ID      string
	Size    int
	Overlap int
}

type ChunkInput struct {
	ModuleID          string
	ReleaseID         string
	InputManifestHash string
	Revisions         []corpus.Revision
}

type Chunker struct {
	config     ChunkConfig
	strategy   chunking.Strategy
	normalizer chunking.Strategy
}

func NewChunker(config ChunkConfig) (*Chunker, error) {
	if config.ID == "" || config.Size <= 0 || config.Size > 32768 || config.Overlap < 0 || config.Overlap >= config.Size {
		return nil, fmt.Errorf("%w: explicit chunk profile and bounded size/overlap required", ErrInvalid)
	}
	return &Chunker{config: config,
		strategy:   chunking.NewFixedSizeChunking(chunking.WithChunkSize(config.Size), chunking.WithOverlap(config.Overlap)),
		normalizer: chunking.NewFixedSizeChunking(chunking.WithChunkSize(artifacts.MaxBytes), chunking.WithOverlap(0)),
	}, nil
}

// Build preserves every revision identity, even when encoding text can be
// deduplicated. A bad required input rejects the entire build; no smaller
// denominator is silently substituted for the fixed release.
func (c *Chunker) Build(ctx context.Context, input ChunkInput) (corpus.ChunkManifest, error) {
	m := corpus.ChunkManifest{SchemaVersion: 1, ModuleID: input.ModuleID, ReleaseID: input.ReleaseID,
		InputManifestHash: input.InputManifestHash, Profile: c.config.ID, ParserVersion: "sea.paragraph.v1",
		ChunkerVersion: "trpc.fixed.v1.8.1", ChunkSize: c.config.Size, Overlap: c.config.Overlap,
		Inputs: []corpus.Input{}, Chunks: []corpus.Chunk{}}
	if input.ModuleID == "" || input.ReleaseID == "" || !artifacts.ValidHash(input.InputManifestHash) || len(input.Revisions) == 0 {
		return m, fmt.Errorf("%w: fixed release and nonempty revisions required", ErrInvalid)
	}
	revisions := append([]corpus.Revision(nil), input.Revisions...)
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].RevisionID < revisions[j].RevisionID })
	seen, encodings := map[string]bool{}, map[string]string{}
	for _, revision := range revisions {
		if err := ctx.Err(); err != nil {
			return corpus.ChunkManifest{}, err
		}
		if revision.RevisionID == "" || revision.EntityID == "" || revision.ModuleID != input.ModuleID || seen[revision.RevisionID] ||
			(revision.Kind != "source" && revision.Kind != "wiki") ||
			(revision.MediaType != "text/plain" && revision.MediaType != "text/markdown") ||
			!utf8.ValidString(revision.Content) || strings.ContainsRune(revision.Content, 0) ||
			len(revision.Content) > artifacts.MaxBytes || revision.Object != artifacts.Reference([]byte(revision.Content)) {
			return corpus.ChunkManifest{}, fmt.Errorf("%w: revision identity, format or hash mismatch", ErrInvalid)
		}
		seen[revision.RevisionID] = true
		start := len(m.Chunks)
		for _, block := range paragraphs(revision.Content) {
			if err := ctx.Err(); err != nil {
				return corpus.ChunkManifest{}, err
			}
			doc := &document.Document{ID: revision.RevisionID, Name: revision.Title, Content: block.text}
			normalized, err := c.normalizer.Chunk(doc)
			if err != nil || len(normalized) != 1 || strings.TrimSpace(normalized[0].Content) == "" {
				return corpus.ChunkManifest{}, fmt.Errorf("%w: paragraph normalization failed", ErrInvalid)
			}
			pieces, err := c.strategy.Chunk(normalized[0])
			if err != nil {
				return corpus.ChunkManifest{}, fmt.Errorf("split paragraph: %w", err)
			}
			previousEnd := 0
			runes := []rune(normalized[0].Content)
			for i, piece := range pieces {
				if piece == nil || piece.Content == "" {
					return corpus.ChunkManifest{}, fmt.Errorf("%w: empty chunk", ErrInvalid)
				}
				runeStart := previousEnd
				if i > 0 {
					runeStart -= c.config.Overlap
				}
				runeEnd := runeStart + utf8.RuneCountInString(piece.Content)
				if runeStart < 0 || runeEnd > len(runes) || string(runes[runeStart:runeEnd]) != piece.Content {
					return corpus.ChunkManifest{}, fmt.Errorf("%w: chunk has no source span", ErrInvalid)
				}
				textHash := artifacts.Hash([]byte(piece.Content))
				identity, _ := json.Marshal([]any{revision.RevisionID, c.config.ID, block.number, runeStart, textHash})
				encoding, _ := json.Marshal([]any{c.config.ID, piece.Content})
				id, encodingKey := artifacts.Hash(identity), artifacts.Hash(encoding)
				chunk := corpus.Chunk{ID: id, RevisionID: revision.RevisionID, ContentID: revision.EntityID, SourceKind: revision.Kind,
					Original: revision.Object, Location: corpus.Location{Locator: fmt.Sprintf("paragraph:%d", block.number),
						OriginalByteStart: block.start, OriginalByteEnd: block.end, NormalizedRuneStart: runeStart,
						NormalizedRuneEnd: runeEnd},
					Text: piece.Content, TextHash: textHash, EncodingKey: encodingKey, DuplicateOf: encodings[encodingKey], Required: true}
				if encodings[encodingKey] == "" {
					encodings[encodingKey] = id
				}
				m.Chunks = append(m.Chunks, chunk)
				previousEnd = runeEnd
			}
		}
		if start == len(m.Chunks) {
			return corpus.ChunkManifest{}, fmt.Errorf("%w: required revision contains no paragraphs", ErrInvalid)
		}
		for i := start; i < len(m.Chunks); i++ {
			if i > start {
				m.Chunks[i].PreviousID = m.Chunks[i-1].ID
			}
			if i+1 < len(m.Chunks) {
				m.Chunks[i].NextID = m.Chunks[i+1].ID
			}
		}
		m.Inputs = append(m.Inputs, corpus.Input{RevisionID: revision.RevisionID, ContentID: revision.EntityID,
			SourceKind: revision.Kind, Original: revision.Object, ChunkCount: len(m.Chunks) - start})
	}
	return m, nil
}

type paragraph struct {
	text               string
	start, end, number int
}

// RTW locators split CRLF-normalized text on blank lines. The offset map retains
// original byte positions, so CRLF and multibyte UTF-8 never drift the citation.
func paragraphs(original string) []paragraph {
	var text strings.Builder
	positions := make([]int, 0, len(original)+1)
	for i := 0; i < len(original); i++ {
		positions = append(positions, i)
		if original[i] == '\r' && i+1 < len(original) && original[i+1] == '\n' {
			i++
			text.WriteByte('\n')
		} else {
			text.WriteByte(original[i])
		}
	}
	positions = append(positions, len(original))
	normalized, start := text.String(), 0
	var result []paragraph
	for _, block := range strings.Split(normalized, "\n\n") {
		end := start + len(block)
		if strings.TrimSpace(block) != "" {
			result = append(result, paragraph{block, positions[start], positions[end], len(result) + 1})
		}
		start = end + 2
	}
	return result
}
