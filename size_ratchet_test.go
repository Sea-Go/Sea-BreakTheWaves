package sea_test

// G6 巨型文件 ratchet（治理基线 G6）：手写 .go 文件默认上限 800 行（生成物 pb/ 与镜像目录排除）。
// 超限文件必须登记在 size_allowlist.json（记录当前行数）——只减不增：
//   - 新文件超限且未登记 → 红
//   - 已登记文件行数超过记录值 → 红
//   - 行数低于记录值 → 通过（建议顺手收紧白名单）
//   - 白名单指向已不存在的文件 → 红（防止白名单变垃圾场）
// 反向验证见 TestSizeRatchetDetector。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sizeLimit = 800

var sizeSkippedDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "pb": true,
	"generated": true, "dist": true, "target": true, "build": true,
}

type sizeAllowlistFile struct {
	Limit int            `json:"limit"`
	Files map[string]int `json:"files"`
	Note  string         `json:"note,omitempty"`
}

func loadSizeAllowlist(root string) (map[string]int, error) {
	raw, err := os.ReadFile(filepath.Join(root, "size_allowlist.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, err
	}
	var al sizeAllowlistFile
	if err := json.Unmarshal(raw, &al); err != nil {
		return nil, err
	}
	return al.Files, nil
}

func countLines(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strings.Count(string(raw), "\n") + 1, nil
}

func scanSizeRoot(root string, limit int, allowed map[string]int) (over []string, stale []string, grew []string, err error) {
	seen := map[string]bool{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		relSlash := filepath.ToSlash(rel)
		if info.IsDir() {
			if rel == "." {
				return nil
			}
			if sizeSkippedDirs[info.Name()] || strings.HasSuffix(info.Name(), ".pb.go") {
				if info.IsDir() && sizeSkippedDirs[info.Name()] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".pb.go") {
			return nil
		}
		if strings.Contains(relSlash, "/pb/") || strings.Contains(relSlash, "/generated/") {
			return nil
		}
		n, cerr := countLines(path)
		if cerr != nil {
			return nil
		}
		if recorded, ok := allowed[relSlash]; ok {
			seen[relSlash] = true
			if n > recorded {
				grew = append(grew, relSlash)
			}
			return nil
		}
		if n > limit {
			over = append(over, relSlash)
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	for p := range allowed {
		if !seen[p] {
			stale = append(stale, p)
		}
	}
	return over, stale, grew, nil
}

func TestSizeRatchetFilesOnlyShrink(t *testing.T) {
	allowed, err := loadSizeAllowlist(".")
	if err != nil {
		t.Fatalf("读取白名单失败: %v", err)
	}
	over, stale, grew, err := scanSizeRoot(".", sizeLimit, allowed)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for _, p := range over {
		t.Errorf("[新增超限] %s 超过 %d 行且未登记白名单（G6）", p, sizeLimit)
	}
	for _, p := range stale {
		t.Errorf("[白名单过期] %s 已不存在，请删除条目（G6）", p)
	}
	for _, p := range grew {
		t.Errorf("[只增不减] %s 行数超过白名单记录 %d，超限新增代码请拆分文件（G6）", p, allowed[p])
	}
}

func TestSizeRatchetDetector(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.go")
	os.WriteFile(big, []byte(strings.Repeat("x\n", 801)), 0o644)
	normal := filepath.Join(root, "normal.go")
	os.WriteFile(normal, []byte(strings.Repeat("x\n", 10)), 0o644)

	over, stale, grew, err := scanSizeRoot(root, 800, map[string]int{})
	if err != nil || len(over) != 1 || len(stale) != 0 || len(grew) != 0 {
		t.Fatalf("反向验证失败（未登记超限应检出）: over=%v err=%v", over, err)
	}

	allowed := map[string]int{"big.go": 802, "ghost.go": 5}
	over, stale, grew, err = scanSizeRoot(root, 800, allowed)
	if err != nil || len(over) != 0 || len(stale) != 1 || len(grew) != 0 {
		t.Fatalf("反向验证失败（幽灵条目应检出）: stale=%v err=%v", stale, err)
	}

	allowed = map[string]int{"big.go": 500}
	over, stale, grew, err = scanSizeRoot(root, 800, allowed)
	if err != nil || len(grew) != 1 {
		t.Fatalf("反向验证失败（超过记录值应检出）: grew=%v err=%v", grew, err)
	}
	_ = normal
}
