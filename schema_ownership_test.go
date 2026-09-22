package sea_test

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationSchemaStaysInDomainGORMOwners(t *testing.T) {
	if _, err := os.Stat("migrations"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root migrations directory must stay absent (err=%v)", err)
	}
	err := filepath.WalkDir("service", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, imported := range file.Imports {
			if strings.Contains(imported.Path.Value, "github.com/Sea-Go/Sea-BreakTheWaves/migrations") {
				t.Errorf("%s imports deleted SQL migration package %s", path, imported.Path.Value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
