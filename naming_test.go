package sea_test

// C45 唯一正名守护（薄装配）：扫描器单源在 sea-guards 库（C46 第三批收敛，
// 此前四仓各持一份同构拷贝）。白名单 naming_allowlist.json 本仓自持。

import (
	"testing"

	"github.com/Sea-Go/sea-guards/guards"
)

func TestNamingRepoHasNoGenerationSuffix(t *testing.T) {
	violations, err := guards.ScanNaming(".")
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for _, v := range violations {
		t.Errorf("[%s] %s: %s", v.Kind, v.Path, v.Msg)
	}
}
