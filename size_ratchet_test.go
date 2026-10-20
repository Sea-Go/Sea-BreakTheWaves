package sea_test

// G6 巨型文件 ratchet（薄装配）：扫描器单源在 sea-guards 库。
// 白名单 size_allowlist.json 本仓自持，只减不增。

import (
	"testing"

	"github.com/Sea-Go/sea-guards/guards"
)

func TestSizeRatchetFilesOnlyShrink(t *testing.T) {
	violations, err := guards.ScanSize(".", 800)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for _, v := range violations {
		t.Errorf("[%s] %s: %s", v.Kind, v.Path, v.Msg)
	}
}
