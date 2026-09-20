// ============================================================================
// 该文件为 Registry 的单元测试，覆盖注册/获取/列表/Invoke/重复注册等场景。
// ============================================================================

package skill

import (
	"context"
	"errors"
	"testing"

	"sea/internal/domain"
)

// TestNewRegistry 验证 NewRegistry 创建空注册中心。
func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry 返回 nil")
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("新建 Registry 应为空，实际 %d 项", len(got))
	}
}

// TestRegisterAndGet 验证注册后可获取。
func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	s := domain.Skill{
		Name:        "recall.content",
		Description: "向量+BM25 内容召回",
		Tools:       []string{"milvus_search", "bm25_search"},
		Resources:   []string{"/skills/recall"},
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	got, err := r.Get("recall.content")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got.Name != s.Name {
		t.Errorf("Name = %q, want %q", got.Name, s.Name)
	}
	if got.Description != s.Description {
		t.Errorf("Description = %q, want %q", got.Description, s.Description)
	}
	if len(got.Tools) != 2 {
		t.Errorf("Tools 长度 = %d, want 2", len(got.Tools))
	}
}

// TestGetNotFound 验证获取不存在的 Skill 返回错误。
func TestGetNotFound(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("not.exist"); err == nil {
		t.Fatal("Get 不存在的 Skill 应返回错误")
	}
}

// TestRegisterEmptyName 验证空名称注册报错。
func TestRegisterEmptyName(t *testing.T) {
	r := NewRegistry()
	err := r.Register(domain.Skill{Name: ""})
	if err == nil {
		t.Fatal("空名称注册应返回错误")
	}
}

// TestDuplicateRegister 验证重复注册同名 Skill 报错。
func TestDuplicateRegister(t *testing.T) {
	r := NewRegistry()
	s := domain.Skill{Name: "rank.weighted", Description: "加权排序"}
	if err := r.Register(s); err != nil {
		t.Fatalf("首次 Register 失败: %v", err)
	}
	err := r.Register(s)
	if err == nil {
		t.Fatal("重复注册应返回错误")
	}
}

// TestList 验证列出所有 Skill。
func TestList(t *testing.T) {
	r := NewRegistry()
	skills := []domain.Skill{
		{Name: "recall.content", Description: "内容召回"},
		{Name: "recall.cf", Description: "协同过滤召回"},
		{Name: "rank.weighted", Description: "加权排序"},
	}
	for _, s := range skills {
		if err := r.Register(s); err != nil {
			t.Fatalf("Register 失败: %v", err)
		}
	}
	got := r.List()
	if len(got) != len(skills) {
		t.Fatalf("List 长度 = %d, want %d", len(got), len(skills))
	}
	// 验证所有 Skill 均可获取。
	names := make(map[string]bool, len(got))
	for _, s := range got {
		names[s.Name] = true
	}
	for _, s := range skills {
		if !names[s.Name] {
			t.Errorf("List 缺少 Skill: %s", s.Name)
		}
	}
}

// TestInvokeNotFound 验证调用不存在的 Skill 返回错误。
func TestInvokeNotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Invoke(context.Background(), "not.exist", domain.SkillInput{})
	if err == nil {
		t.Fatal("Invoke 不存在的 Skill 应返回错误")
	}
}

// TestInvokeWithoutExecute 验证无 Execute 时返回 Description（物化到 tool result）。
func TestInvokeWithoutExecute(t *testing.T) {
	r := NewRegistry()
	s := domain.Skill{
		Name:        "recall.content",
		Description: "向量+BM25 内容召回",
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	out, err := r.Invoke(context.Background(), "recall.content", domain.SkillInput{
		Args: map[string]any{"query": "AI"},
	})
	if err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	if out.Result != s.Description {
		t.Errorf("Result = %v, want %q（Description 物化为 tool result）", out.Result, s.Description)
	}
}

// TestInvokeWithExecute 验证含 Execute 时调用执行函数。
func TestInvokeWithExecute(t *testing.T) {
	r := NewRegistry()
	want := domain.SkillOutput{Result: "召回结果:123"}
	s := Skill{
		Skill: domain.Skill{
			Name:        "recall.content",
			Description: "内容召回",
		},
		Category: "recall",
		Execute: func(ctx context.Context, input domain.SkillInput) (domain.SkillOutput, error) {
			if q, ok := input.Args["query"].(string); !ok || q != "AI" {
				return domain.SkillOutput{}, errors.New("参数错误")
			}
			return want, nil
		},
	}
	if err := r.RegisterSkill(s); err != nil {
		t.Fatalf("RegisterSkill 失败: %v", err)
	}
	// 验证 Get 仍返回 domain.Skill（不含 Category/Execute）。
	got, err := r.Get("recall.content")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got.Description != "内容召回" {
		t.Errorf("Get Description = %q, want %q", got.Description, "内容召回")
	}
	// 验证 Invoke 调用 Execute。
	out, err := r.Invoke(context.Background(), "recall.content", domain.SkillInput{
		Args: map[string]any{"query": "AI"},
	})
	if err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	if out.Result != want.Result {
		t.Errorf("Invoke Result = %v, want %v", out.Result, want.Result)
	}
}

// TestInvokeExecuteError 验证 Execute 返回错误时透传。
func TestInvokeExecuteError(t *testing.T) {
	r := NewRegistry()
	wantErr := errors.New("执行失败")
	s := Skill{
		Skill: domain.Skill{Name: "recall.error"},
		Execute: func(ctx context.Context, input domain.SkillInput) (domain.SkillOutput, error) {
			return domain.SkillOutput{}, wantErr
		},
	}
	if err := r.RegisterSkill(s); err != nil {
		t.Fatalf("RegisterSkill 失败: %v", err)
	}
	_, err := r.Invoke(context.Background(), "recall.error", domain.SkillInput{})
	if !errors.Is(err, wantErr) {
		t.Errorf("Invoke 错误 = %v, want %v", err, wantErr)
	}
}

// TestRegisterSkillDuplicate 验证 RegisterSkill 重复注册报错。
func TestRegisterSkillDuplicate(t *testing.T) {
	r := NewRegistry()
	s := Skill{Skill: domain.Skill{Name: "rank.lr"}, Category: "rank"}
	if err := r.RegisterSkill(s); err != nil {
		t.Fatalf("首次 RegisterSkill 失败: %v", err)
	}
	if err := r.RegisterSkill(s); err == nil {
		t.Fatal("重复 RegisterSkill 应返回错误")
	}
}

// TestRegistryImplementsInterface 编译期验证 Registry 实现 domain.SkillRegistry。
// （var _ domain.SkillRegistry = (*Registry)(nil) 已在 registry.go 中声明，
// 此处仅为运行时占位，确保测试文件参与编译。）
func TestRegistryImplementsInterface(t *testing.T) {
	var _ domain.SkillRegistry = NewRegistry()
}
