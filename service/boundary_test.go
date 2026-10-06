package service

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Check all matching rules. Paths are relative to this test's service directory.
func TestServiceImportBoundaries(t *testing.T) {
	const module = "github.com/Sea-Go/Sea-BreakTheWaves/"
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		source := filepath.ToSlash(path)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			imported, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(imported, module) {
				continue
			}
			target := strings.TrimPrefix(imported, module)
			forbidden := strings.HasPrefix(target, "internal/")
			if strings.HasPrefix(source, "common/") && strings.HasPrefix(target, "service/") && !strings.HasPrefix(target, "service/common/") {
				forbidden = true
			}
			if strings.HasPrefix(source, "search/") && (strings.HasPrefix(target, "service/recommend/") || strings.HasPrefix(target, "service/async/")) {
				forbidden = true
			}
			if strings.HasPrefix(source, "async/") && (strings.HasPrefix(target, "service/search/") || strings.HasPrefix(target, "service/recommend/")) {
				forbidden = true
			}
			if strings.HasPrefix(source, "recommend/") && strings.Contains(target, "/internal/") && !strings.HasPrefix(target, "service/recommend/") {
				forbidden = true
			}
			if !strings.HasPrefix(source, "btw/") && strings.HasPrefix(target, "service/btw/") {
				forbidden = true
			}
			if forbidden {
				t.Errorf("%s imports forbidden boundary %q", source, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
