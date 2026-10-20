package sea_test

// C45 唯一正名（Sea-Docs《规划决策与待对齐事项》C45 与《工程目录与工作规范》§3.7）：
// 目录名、Go 包名不得携带 v2/v3 等代次后缀；同一能力同一时刻只存在一份实现，
// 演进在 main 原地替换，旧版本只活在 git 历史、tag 与归档分支。
// 协议版本（sea.search.v1、desktop-event.v1 等）是数据契约标识，不属于本测试范围。
// 白名单见 naming_allowlist.json，每条豁免必须含路径/理由/收敛事件，收敛后删除条目即恢复检查。

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var generationSuffixRe = regexp.MustCompile(`v[2-9][0-9]*$`)

// 只拦"形如版本常量"的标识符；协议字符串（如 "desktop-event.v1"）不在其列。
var hardVersionConstRe = regexp.MustCompile(`(?m)^\s*(?:var|const)\s+(?:\w+\s+)?(Version|AppVersion|BuildVersion|BinaryVersion|ReleaseVersion)\s*=\s*"[0-9v]`)

var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "pb": true,
	"generated": true, ".wrangler": true, ".native": true,
	"dist": true, "target": true, "build": true,
}

type allowlistEntry struct {
	Path          string `json:"path"`
	Package       string `json:"package"`
	Reason        string `json:"reason"`
	ConvergeEvent string `json:"converge_event"`
}

type allowlist struct {
	Entries []allowlistEntry `json:"entries"`
}

type namingViolation struct {
	Path string
	Kind string
	Msg  string
}

func loadAllowlist(root string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "naming_allowlist.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var al allowlist
	if err := json.Unmarshal(raw, &al); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, e := range al.Entries {
		if e.Path == "" || e.Reason == "" || e.ConvergeEvent == "" {
			return nil, os.ErrInvalid
		}
		allowed[e.Path] = true
	}
	return allowed, nil
}

func isAllowed(allowed map[string]bool, path string) bool {
	if allowed[path] {
		return true
	}
	for p := filepath.Dir(path); p != "." && p != string(filepath.Separator); p = filepath.Dir(p) {
		if allowed[p] {
			return true
		}
	}
	return false
}

func scanNamingRoot(root string) ([]namingViolation, error) {
	allowed, err := loadAllowlist(root)
	if err != nil {
		return nil, err
	}
	var violations []namingViolation
	seenPkgs := map[string]string{}

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			if skippedDirs[info.Name()] {
				return filepath.SkipDir
			}
			if generationSuffixRe.MatchString(info.Name()) {
				violations = append(violations, namingViolation{rel, "dir", "目录名携带代次后缀（C45）"})
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.Contains(filepath.ToSlash(rel), "/pb/") {
			return nil
		}
		fset := token.NewFileSet()
		astFile, pErr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if pErr != nil {
			return nil
		}
		pkg := astFile.Name.Name
		if strings.HasSuffix(pkg, "_test") {
			pkg = strings.TrimSuffix(pkg, "_test")
		}
		if generationSuffixRe.MatchString(pkg) {
			if first, ok := seenPkgs[pkg]; !ok {
				seenPkgs[pkg] = rel
				violations = append(violations, namingViolation{rel, "package", "包名携带代次后缀（C45）: " + pkg})
			} else {
				violations[len(violations)-1] = namingViolation{first, "package", "包名携带代次后缀（C45）: " + pkg}
			}
		}
		if !strings.HasSuffix(path, "_test.go") {
			src, rErr := os.ReadFile(path)
			if rErr == nil && hardVersionConstRe.Match(src) {
				violations = append(violations, namingViolation{rel, "const", "硬编码版本常量（版本应由构建注入，C45）"})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	filtered := violations[:0]
	for _, v := range violations {
		if !isAllowed(allowed, filepath.ToSlash(v.Path)) {
			filtered = append(filtered, v)
		}
	}
	return filtered, nil
}

func TestNamingRepoHasNoGenerationSuffix(t *testing.T) {
	violations, err := scanNamingRoot(".")
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for _, v := range violations {
		t.Errorf("[%s] %s: %s", v.Kind, v.Path, v.Msg)
	}
	if len(violations) > 0 {
		t.Logf("如属活跃迁移等已知豁免，请在 naming_allowlist.json 登记 {path, reason, converge_event}")
	}
}

func TestNamingDetector(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"recommendationv2", true},
		{"product_v2", true},
		{"timeline-v2", true},
		{"v2", true},
		{"gatewayv9", true},
		{"product", false},
		{"sea", false},
		{"beanstalkd2", false},
		{"sha256", false},
		{"v1", false},
	}
	for _, c := range cases {
		if got := generationSuffixRe.MatchString(c.name); got != c.want {
			t.Errorf("detect(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// 反向验证（§3.7 第 4 条）：无白名单时违例必须全部现形；白名单条目只压制其路径前缀下的违例。
func TestNamingWhitelistSuppressesAndReverseVerifies(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "foo_v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bar", "x.go"), []byte("package bar_v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	without, err := scanNamingRootWithoutAllowlist(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(without) != 2 {
		t.Fatalf("反向验证失败：无白名单时应检出 2 处违例，实际 %d: %+v", len(without), without)
	}

	if err := os.WriteFile(filepath.Join(root, "naming_allowlist.json"), []byte(`{"entries":[{"path":"foo_v2","reason":"r","converge_event":"e"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	with, err := scanNamingRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(with) != 1 || !strings.HasSuffix(with[0].Path, "bar/x.go") {
		t.Fatalf("白名单应只压制 foo_v2 前缀，实际剩余: %+v", with)
	}
}

func scanNamingRootWithoutAllowlist(root string) ([]namingViolation, error) {
	alPath := filepath.Join(root, "naming_allowlist.json")
	if _, err := os.Stat(alPath); err == nil {
		if rmErr := os.Remove(alPath); rmErr != nil {
			return nil, rmErr
		}
	}
	return scanNamingRoot(root)
}
