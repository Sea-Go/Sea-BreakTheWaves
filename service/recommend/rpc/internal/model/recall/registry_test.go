package recall

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件测试 Registry 注册表的注册/获取/列表/覆盖/并发安全行为。
// ============================================================================

// stubRecaller 用于测试的轻量 Recaller 桩。
type stubRecaller struct {
	name string
}

func (s *stubRecaller) Recall(ctx context.Context, req domain.RecallRequest) (domain.RecallResult, error) {
	return domain.RecallResult{Source: s.name}, nil
}

func (s *stubRecaller) Name() string { return s.name }

// TestRegistry_Empty 验证空注册表的 Get/List 行为。
func TestRegistry_Empty(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("none"); ok {
		t.Errorf("空注册表 Get 应未命中")
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("空注册表 List 长度 = %d, 期望 0", len(got))
	}
}

// TestRegistry_RegisterAndGet 验证注册后可按名称获取。
func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	rc := &stubRecaller{name: "rule"}
	r.Register("rule", rc)

	got, ok := r.Get("rule")
	if !ok {
		t.Fatalf("Get(rule) 未命中")
	}
	if got.Name() != "rule" {
		t.Errorf("获取的召回器 Name = %q, 期望 rule", got.Name())
	}
}

// TestRegistry_Get_NotFound 验证未注册名称返回 ok=false。
func TestRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	r.Register("rule", &stubRecaller{name: "rule"})
	if _, ok := r.Get("content"); ok {
		t.Errorf("未注册的 content 应返回 false")
	}
}

// TestRegistry_List 验证 List 返回所有已注册名称。
func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	r.Register("rule", &stubRecaller{name: "rule"})
	r.Register("content", &stubRecaller{name: "content"})
	r.Register("cf", &stubRecaller{name: "cf"})

	names := r.List()
	if len(names) != 3 {
		t.Fatalf("List 长度 = %d, 期望 3", len(names))
	}
	// 转为 set 校验（顺序无保证）。
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	for _, want := range []string{"rule", "content", "cf"} {
		if !set[want] {
			t.Errorf("List 缺少 %q", want)
		}
	}
}

// TestRegistry_Overwrite 验证同名注册覆盖旧实现（二开替换场景）。
func TestRegistry_Overwrite(t *testing.T) {
	r := NewRegistry()
	r.Register("rule", &stubRecaller{name: "old"})
	r.Register("rule", &stubRecaller{name: "new"})

	got, ok := r.Get("rule")
	if !ok {
		t.Fatalf("Get(rule) 未命中")
	}
	if got.Name() != "new" {
		t.Errorf("覆盖后 Name = %q, 期望 new", got.Name())
	}
	if len(r.List()) != 1 {
		t.Errorf("覆盖后注册表大小 = %d, 期望 1", len(r.List()))
	}
}

// TestRegistry_ConcurrentSafe 验证并发注册与获取不触发 race（需配合 -race 运行）。
func TestRegistry_ConcurrentSafe(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	// 并发写入。
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Register("n", &stubRecaller{name: "n"})
		}(i)
	}
	// 并发读取。
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Get("n")
			_ = r.List()
		}()
	}
	wg.Wait()
}

// TestRegistry_RegisterRealRecallers 验证真实召回器可注册并通过 interface 契约。
func TestRegistry_RegisterRealRecallers(t *testing.T) {
	r := NewRegistry()
	r.Register("rule", NewRuleRecaller(nil, 10))
	r.Register("content", NewContentRecaller(nil, nil, nil, 10))
	r.Register("cf", NewCFRecaller(nil, nil, nil, nil, 10))

	for _, name := range []string{"rule", "content", "cf"} {
		got, ok := r.Get(name)
		if !ok {
			t.Errorf("Get(%q) 未命中", name)
			continue
		}
		if got.Name() != name {
			t.Errorf("%q 的 Name = %q", name, got.Name())
		}
	}
}

// 编译期断言：各召回器实现 domain.Recaller interface。
var (
	_ domain.Recaller = (*RuleRecaller)(nil)
	_ domain.Recaller = (*ContentRecaller)(nil)
)

// 确保 stubRecaller 实现 domain.Recaller（避免误删方法）。
var _ domain.Recaller = (*stubRecaller)(nil)

// errSentinel 用于错误传播测试。
var errSentinel = errors.New("sentinel")
