package parity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func pinnedBGEPaths() (string, string) {
	root := filepath.Join("..", "..", "..", "training", "serving", "bge_m3")
	return filepath.Join(root, "profiles.json"), filepath.Join(root, "model.lock.json")
}

func TestLoadFrozenRealBGE(t *testing.T) {
	path := os.Getenv("SEA_BGE_THREE_LANE_MANIFEST")
	if path == "" {
		t.Skip("set SEA_BGE_THREE_LANE_MANIFEST to locked encoder artifact")
	}
	profiles, modelLock := pinnedBGEPaths()
	frozen, err := LoadFrozen(path, profiles, modelLock)
	if err != nil || len(frozen.Queries) == 0 || len(frozen.Chunks) == 0 || frozen.Manifest.ModelRevision == "" {
		t.Fatalf("real BGE-M3 frozen manifest: queries=%d chunks=%d err=%v", len(frozen.Queries), len(frozen.Chunks), err)
	}
	for _, bad := range []struct {
		manifest, profiles, lock string
	}{
		{path, filepath.Join(t.TempDir(), "missing-profiles.json"), modelLock},
		{path, profiles, filepath.Join(t.TempDir(), "missing-model.lock.json")},
		{filepath.Join(t.TempDir(), "manifest", "missing.json"), profiles, modelLock},
	} {
		if _, err := LoadFrozen(bad.manifest, bad.profiles, bad.lock); !errors.Is(err, ErrArtifact) {
			t.Fatalf("missing/misbound frozen artifact accepted: %v", err)
		}
	}
}
