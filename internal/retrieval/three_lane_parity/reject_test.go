package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func copiedFrozen(t *testing.T, source string) (string, Manifest, string) {
	t.Helper()
	profiles, lock := pinnedBGEPaths()
	original, err := LoadFrozen(source, profiles, lock)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifestDir := filepath.Join(root, "manifest")
	if err := os.MkdirAll(manifestDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(manifestDir, filepath.Base(source))
	if err := os.WriteFile(manifest, manifestRaw, 0600); err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Dir(filepath.Dir(source))
	for _, shard := range original.Manifest.Shards {
		body, err := os.ReadFile(filepath.Join(oldRoot, shard.Path))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, shard.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return manifest, original.Manifest, root
}

func rewrittenManifest(t *testing.T, root string, manifest Manifest) string {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifest", digest(body)+".json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func rewrittenChunkShard(t *testing.T, root string, manifest *Manifest, mutate func(*ChunkRow)) {
	t.Helper()
	shard := &manifest.Shards[1]
	body, err := os.ReadFile(filepath.Join(root, shard.Path))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'})
	var first ChunkRow
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	mutate(&first)
	lines[0], err = json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(bytes.Join(lines, []byte{'\n'}), '\n')
	shard.SHA256 = digest(changed)
	shard.Path = filepath.Join("shards", "chunks", shard.SHA256+".jsonl")
	shard.SizeBytes = int64(len(changed))
	if err := os.WriteFile(filepath.Join(root, shard.Path), changed, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRejectMalformedFrozenRepresentations(t *testing.T) {
	source := os.Getenv("SEA_BGE_THREE_LANE_MANIFEST")
	if source == "" {
		t.Skip("set real BGE encoder manifest for mutation rejection")
	}
	profiles, lock := pinnedBGEPaths()
	t.Run("wrong-space", func(t *testing.T) {
		_, m, root := copiedFrozen(t, source)
		p := m.Lanes["dense"]
		p.RepresentationSpace = "wrong-space"
		m.Lanes["dense"] = p
		if _, err := LoadFrozen(rewrittenManifest(t, root, m), profiles, lock); !errors.Is(err, ErrArtifact) {
			t.Fatalf("wrong BGE representation space loaded: %v", err)
		}
	})
	t.Run("missing-shard", func(t *testing.T) {
		manifest, m, root := copiedFrozen(t, source)
		if err := os.Remove(filepath.Join(root, m.Shards[1].Path)); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFrozen(manifest, profiles, lock); !errors.Is(err, ErrArtifact) {
			t.Fatalf("missing document vector shard loaded: %v", err)
		}
	})
	for name, mutate := range map[string]func(*ChunkRow){
		"wrong-dimension":   func(row *ChunkRow) { row.Representations.Dense.Values = row.Representations.Dense.Values[:1023] },
		"wrong-mask":        func(row *ChunkRow) { row.Representations.TokenMatrix.Mask[0] = false },
		"missing-head":      func(row *ChunkRow) { row.Representations.Sparse = nil },
		"changed-text-hash": func(row *ChunkRow) { row.TextSHA256 = digest([]byte("different")) },
	} {
		t.Run(name, func(t *testing.T) {
			_, m, root := copiedFrozen(t, source)
			rewrittenChunkShard(t, root, &m, mutate)
			if _, err := LoadFrozen(rewrittenManifest(t, root, m), profiles, lock); !errors.Is(err, ErrArtifact) {
				t.Fatalf("%s loaded despite content-addressed mutation: %v", name, err)
			}
		})
	}
	t.Run("non-finite", func(t *testing.T) {
		frozen, err := LoadFrozen(source, profiles, lock)
		if err != nil {
			t.Fatal(err)
		}
		frozen.Chunks[0].Representations.Dense.Values[0] = math.NaN()
		if err := frozen.validateRows(); !errors.Is(err, ErrArtifact) {
			t.Fatalf("non-finite model value accepted: %v", err)
		}
	})
}

func TestCompositeTieBreakPreservesTupleOrder(t *testing.T) {
	type tuple [3]string
	values := []tuple{{"a", "r1", "x"}, {"aa", "r1", "x"}, {"a", "r2", "x"},
		{"a", "r1", "xy"}, {"a", "r1", "x1"}, {"a!", "r1", "x"}}
	keys := make([]string, len(values))
	for i, v := range values {
		var err error
		keys[i], err = composite(v[:]...)
		if err != nil {
			t.Fatal(err)
		}
	}
	got := append([]string(nil), keys...)
	sort.Strings(got)
	sort.Slice(values, func(i, j int) bool {
		for p := range values[i] {
			if values[i][p] != values[j][p] {
				return values[i][p] < values[j][p]
			}
		}
		return false
	})
	for i, v := range values {
		want, _ := composite(v[:]...)
		if got[i] != want {
			t.Fatalf("composite chunk ID changed tuple tie-break at %d", i)
		}
	}
	if _, err := composite("", "r1", "c1"); !errors.Is(err, ErrArtifact) {
		t.Fatalf("empty tuple segment was accepted: %v", err)
	}
}
