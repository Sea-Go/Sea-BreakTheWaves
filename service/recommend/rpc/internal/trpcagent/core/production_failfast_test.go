package recommendationv2_test

import (
	"testing"

	core "github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/trpcagent/core"
)

// 危险默认修复回归（全仓评审 BTW Top1）：生产装配不得再静默落入
// 内存伪造召回。WithProductionProviders(nil, nil) 必须启动即失败。
func TestWithProductionProvidersPanicsWithoutRepos(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("双 nil 仓储必须 panic（旧实现会保留伪造 provider 继续服务）")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic 应携带可读信息，实际 %T", r)
		}
		t.Logf("装配失败信息: %s", msg)
	}()
	core.NewRecommendationRuntime(core.WithProductionProviders(nil, nil))
}
