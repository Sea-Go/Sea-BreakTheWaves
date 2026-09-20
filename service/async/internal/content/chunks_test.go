package content

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/artifacts"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/corpus"
)

func testRevision(id, body string) corpus.Revision {
	return corpus.Revision{RevisionID: id, ModuleID: "module", EntityID: id, Kind: "source", Title: id,
		MediaType: "text/markdown", Object: artifacts.Reference([]byte(body)), Content: body}
}

func testInput(revisions ...corpus.Revision) ChunkInput {
	return ChunkInput{ModuleID: "module", ReleaseID: "release", InputManifestHash: artifacts.Hash([]byte("fixed input")), Revisions: revisions}
}

func TestChunkIdentityLocationsAndReplay(t *testing.T) {
	c, err := NewChunker(ChunkConfig{ID: "paragraph-v1", Size: 4, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	revision := testRevision("r1", "  同一段落同一段落  \r\n\r\n尾部。")
	m, err := c.Build(context.Background(), testInput(revision))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Chunks) != 3 {
		t.Fatalf("chunks: %+v", m.Chunks)
	}
	if m.Chunks[1].Location.NormalizedRuneStart != 3 || m.Chunks[1].Location.NormalizedRuneEnd != 8 {
		t.Fatalf("overlap span: %+v", m.Chunks[1].Location)
	}
	last := m.Chunks[2]
	if last.Location.Locator != "paragraph:2" || revision.Content[last.Location.OriginalByteStart:last.Location.OriginalByteEnd] != "尾部。" {
		t.Fatalf("original location: %+v", last)
	}
	replay, err := c.Build(context.Background(), testInput(revision))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatal("replay changed immutable manifest bytes")
	}
	if m.Chunks[0].NextID != m.Chunks[1].ID || m.Chunks[1].PreviousID != m.Chunks[0].ID {
		t.Fatal("adjacency lost")
	}
}

func TestDuplicateTextRetainsRevisionIdentity(t *testing.T) {
	c, _ := NewChunker(ChunkConfig{ID: "paragraph-v1", Size: 100})
	a, b := testRevision("a", "same text"), testRevision("b", "same text")
	m, err := c.Build(context.Background(), testInput(b, a))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Inputs) != 2 || len(m.Chunks) != 2 || m.Chunks[0].ID == m.Chunks[1].ID || m.Chunks[0].EncodingKey != m.Chunks[1].EncodingKey || m.Chunks[1].DuplicateOf != m.Chunks[0].ID {
		t.Fatalf("dedup erased provenance: %+v", m)
	}
	reverse, err := c.Build(context.Background(), testInput(a, b))
	if err != nil || !reflect.DeepEqual(m, reverse) {
		t.Fatal("caller ordering changed manifest")
	}
	if b.RevisionID != "b" {
		t.Fatal("mutated caller input")
	}
}

func TestRepeatedChunkTextUsesPositionNotFirstSubstring(t *testing.T) {
	c, _ := NewChunker(ChunkConfig{ID: "repeated-v1", Size: 4, Overlap: 1})
	m, err := c.Build(context.Background(), testInput(testRevision("a", "aaaaaaaaaaaa")))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, 3, 7} {
		if m.Chunks[i].Location.NormalizedRuneStart != want {
			t.Fatalf("chunk %d span=%+v", i, m.Chunks[i].Location)
		}
	}
}

func TestRequiredBadRevisionNeverShrinksCoverage(t *testing.T) {
	c, _ := NewChunker(ChunkConfig{ID: "p", Size: 16})
	valid := testRevision("valid", "content")
	for name, bad := range map[string]corpus.Revision{
		"empty":     testRevision("bad", "  \n\n"),
		"nul":       testRevision("bad", "a\x00b"),
		"utf8":      testRevision("bad", string([]byte{0xff})),
		"duplicate": valid,
	} {
		t.Run(name, func(t *testing.T) {
			m, err := c.Build(context.Background(), testInput(valid, bad))
			if !errors.Is(err, ErrInvalid) || len(m.Chunks) != 0 {
				t.Fatalf("invalid required input accepted: %v %+v", err, m)
			}
		})
	}
	bad := valid
	bad.Content += "changed"
	if _, err := c.Build(context.Background(), testInput(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatal("hash mismatch accepted")
	}
	bad = valid
	bad.ModuleID = "other"
	if _, err := c.Build(context.Background(), testInput(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatal("wrong module accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Build(ctx, testInput(valid)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
