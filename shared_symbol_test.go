package sea_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalSharedDirs 是共享算法的唯一实现点（2026-10-08 复用整改后确立）。
var canonicalSharedDirs = []string{
	"service/common/wholeindex",
	"service/common/retrieval/rrf",
}

// exportedFuncNames 返回目录下所有非测试 Go 文件的导出函数名（含接收者
// 归一化标记，方法与包级函数分开计）。
func exportedFuncNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			// 方法（有接收者）不算：不同类型可以有同名方法。
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				continue
			}
			names[fn.Name.Name] = true
		}
	}
	return names
}

// TestSharedAlgorithmSymbolsStayUnique 强制"同一算法只有一处实现"：
// 唯一实现点的导出包级函数不得在其他 service/ 包里再出现同名定义
// （防止镜像回潮——2026-10-08 复用审计前 artifactmirror.go 正是这种镜像；
// 别名转发 `var X = pkg.X` 不是函数定义，不受本测试约束）。
func TestSharedAlgorithmSymbolsStayUnique(t *testing.T) {
	// 收集唯一实现点的导出函数名 → 归属目录。
	canonical := map[string]string{}
	for _, dir := range canonicalSharedDirs {
		for name := range exportedFuncNames(t, dir) {
			if prev, dup := canonical[name]; dup {
				t.Fatalf("canonical symbol %q defined in both %s and %s", name, prev, dir)
			}
			canonical[name] = dir
		}
	}
	if len(canonical) == 0 {
		t.Fatal("no canonical symbols found — canonical dirs misconfigured?")
	}

	// 扫描 service/ 下所有非唯一实现点的非测试 Go 文件。
	root := "service"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		for _, c := range canonicalSharedDirs {
			if dir == c {
				return nil // 唯一实现点自身跳过
			}
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				continue
			}
			if owner, dup := canonical[fn.Name.Name]; dup {
				t.Errorf("%s re-defines shared symbol %q (canonical owner: %s) — 镜像回潮，应改为导入共享包或别名转发",
					path, fn.Name.Name, owner)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// TestMirrorFilesStayAbsent 已删除的镜像文件不得回潮。
func TestMirrorFilesStayAbsent(t *testing.T) {
	for _, path := range []string{
		"service/search/rpc/internal/retrieval/artifactmirror.go",
		"service/async/rpc/internal/indexer/structure.go",
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s must stay absent (mirror was lifted to common/ in 2026-10-08 dedup)", path)
		}
	}
}
