package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndSaveContentStrategyMemory(t *testing.T) {
	oldDir := contentStrategyMemoryDir
	contentStrategyMemoryDir = t.TempDir()
	defer func() { contentStrategyMemoryDir = oldDir }()

	input := ContentStrategyMemory{
		PreferredStructure: []string{"先给总览，再按天展开", "先给总览，再按天展开"},
		DoMore:             []string{"多写交通衔接"},
		Avoid:              []string{"少写空泛形容词"},
	}

	path, err := SaveContentStrategyMemory("Travel Article/Default", input)
	if err != nil {
		t.Fatalf("SaveContentStrategyMemory() error = %v", err)
	}
	if path == "" {
		t.Fatalf("expected path to be returned")
	}

	got, err := LoadContentStrategyMemory("Travel Article/Default")
	if err != nil {
		t.Fatalf("LoadContentStrategyMemory() error = %v", err)
	}
	if len(got.PreferredStructure) != 1 {
		t.Fatalf("preferred structure = %#v", got.PreferredStructure)
	}
	if got.DoMore[0] != "多写交通衔接" {
		t.Fatalf("do_more = %#v", got.DoMore)
	}
}

func TestMergeContentStrategyMemory(t *testing.T) {
	base := ContentStrategyMemory{
		PreferredStructure: []string{"先给总览，再按天展开"},
		DoMore:             []string{"多写交通衔接"},
	}
	update := ContentStrategyMemory{
		PreferredStructure: []string{"先给总览，再按天展开"},
		Avoid:              []string{"少写空泛形容词"},
	}

	got := MergeContentStrategyMemory(base, update)
	if len(got.PreferredStructure) != 1 {
		t.Fatalf("preferred structure = %#v", got.PreferredStructure)
	}
	if len(got.Avoid) != 1 {
		t.Fatalf("avoid = %#v", got.Avoid)
	}
}

func TestSanitizeContentKeyDistinguishesChineseKeys(t *testing.T) {
	a := sanitizeContentKey("成都三天慢旅行")
	b := sanitizeContentKey("杭州周末亲子")
	if a == b {
		t.Fatalf("expected distinct keys, got same value %q", a)
	}
	if a == "default" || b == "default" {
		t.Fatalf("expected hashed keys instead of default fallback: %q %q", a, b)
	}
}

func TestLoadContentStrategyMemorySupportsLegacyFilename(t *testing.T) {
	oldDir := contentStrategyMemoryDir
	contentStrategyMemoryDir = t.TempDir()
	defer func() { contentStrategyMemoryDir = oldDir }()

	legacyPath := filepath.Join(contentStrategyMemoryDir, "default.json")
	data := []byte(`{
  "preferred_structure": ["先给总览，再按天展开"],
  "do_more": ["多写交通衔接"],
  "avoid": ["少写空泛形容词"],
  "unanswered_questions": []
}`)
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatalf("write legacy memory: %v", err)
	}

	got, err := LoadContentStrategyMemory("default")
	if err != nil {
		t.Fatalf("LoadContentStrategyMemory() error = %v", err)
	}
	if len(got.PreferredStructure) != 1 {
		t.Fatalf("preferred structure = %#v", got.PreferredStructure)
	}
}

func TestSaveContentStrategyMemoryPrefersExistingLegacyFile(t *testing.T) {
	oldDir := contentStrategyMemoryDir
	contentStrategyMemoryDir = t.TempDir()
	defer func() { contentStrategyMemoryDir = oldDir }()

	legacyPath := filepath.Join(contentStrategyMemoryDir, "default.json")
	if err := os.WriteFile(legacyPath, []byte(`{"preferred_structure":["old"]}`), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	path, err := SaveContentStrategyMemory("default", ContentStrategyMemory{
		PreferredStructure: []string{"new"},
	})
	if err != nil {
		t.Fatalf("SaveContentStrategyMemory() error = %v", err)
	}
	if path != legacyPath {
		t.Fatalf("expected legacy path %q, got %q", legacyPath, path)
	}

	got, err := LoadContentStrategyMemory("default")
	if err != nil {
		t.Fatalf("LoadContentStrategyMemory() error = %v", err)
	}
	if len(got.PreferredStructure) != 1 || got.PreferredStructure[0] != "new" {
		t.Fatalf("preferred structure = %#v", got.PreferredStructure)
	}
}

func TestLegacyFileStemPreventsPathTraversal(t *testing.T) {
	got := legacyFileStem("../../secrets/default")
	if strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Fatalf("unsafe legacy file stem: %q", got)
	}
}
