// ============================================================================
// 该文件为 Loader 的单元测试，覆盖 LoadAll 加载、SKILL.md 解析等场景。
// ============================================================================

package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sea/internal/domain"
)

// writeSkillMD 在 dir 下创建 SKILL.md，内容为 content。
func writeSkillMD(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll 失败 %s: %v", dir, err)
	}
	path := filepath.Join(dir, skillFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile 失败 %s: %v", path, err)
	}
}

// TestLoadAll 验证加载多个技能目录。
func TestLoadAll(t *testing.T) {
	root := t.TempDir()
	// 技能 1：内容召回。
	writeSkillMD(t, filepath.Join(root, "recall_content"), `---
name: recall.content
category: recall
description: 向量+BM25 内容召回
tools:
  - milvus_search
  - bm25_search
---
# 内容召回技能
基于 Milvus 向量检索与 BM25 稀疏检索融合召回候选文章。
`)
	// 技能 2：加权排序。
	writeSkillMD(t, filepath.Join(root, "rank_weighted"), `---
name: rank.weighted
category: rank
description: 多特征加权排序
tools:
  - feature_compute
---
# 加权排序技能
按特征权重加权打分排序候选文章。
`)

	r := NewRegistry()
	l := NewLoader(root, r)
	if err := l.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	// 验证加载了 2 个 Skill。
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("List 长度 = %d, want 2", len(got))
	}
	// 验证 recall.content。
	s, err := r.Get("recall.content")
	if err != nil {
		t.Fatalf("Get recall.content 失败: %v", err)
	}
	if s.Description == "" {
		t.Error("Description 为空")
	}
	if !strings.Contains(s.Description, "向量+BM25 内容召回") {
		t.Errorf("Description 缺少 front matter 摘要: %q", s.Description)
	}
	if !strings.Contains(s.Description, "基于 Milvus 向量检索") {
		t.Errorf("Description 缺少 body 内容: %q", s.Description)
	}
	if len(s.Tools) != 2 {
		t.Errorf("Tools 长度 = %d, want 2", len(s.Tools))
	}
	if len(s.Resources) != 1 {
		t.Errorf("Resources 长度 = %d, want 1（技能目录）", len(s.Resources))
	}
	// 验证 rank.weighted。
	if _, err := r.Get("rank.weighted"); err != nil {
		t.Fatalf("Get rank.weighted 失败: %v", err)
	}
	// 验证 Invoke 无 Execute 时返回 Description。
	out, err := r.Invoke(context.Background(), "recall.content", domain.SkillInput{})
	if err != nil {
		t.Fatalf("Invoke 失败: %v", err)
	}
	if out.Result != s.Description {
		t.Errorf("Invoke Result 未物化 Description")
	}
}

// TestLoadAllEmptyDir 验证空根目录不报错。
func TestLoadAllEmptyDir(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry()
	l := NewLoader(root, r)
	if err := l.LoadAll(context.Background()); err != nil {
		t.Fatalf("空目录 LoadAll 应返回 nil，实际: %v", err)
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("空目录加载后 List 应为空，实际 %d 项", len(got))
	}
}

// TestLoadAllNoSkillMD 验证无 SKILL.md 的子目录被跳过。
func TestLoadAllNoSkillMD(t *testing.T) {
	root := t.TempDir()
	// 子目录 1：含 SKILL.md。
	writeSkillMD(t, filepath.Join(root, "has_skill"), `---
name: recall.has
category: recall
description: 有技能
---
body
`)
	// 子目录 2：无 SKILL.md（仅含其他文件）。
	otherDir := filepath.Join(root, "no_skill")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "README.md"), []byte("无 SKILL.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 普通文件（非目录）应被跳过。
	if err := os.WriteFile(filepath.Join(root, "flat.txt"), []byte("flat"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry()
	l := NewLoader(root, r)
	if err := l.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List 长度 = %d, want 1（仅含 SKILL.md 的目录）", len(got))
	}
	if got[0].Name != "recall.has" {
		t.Errorf("Name = %q, want %q", got[0].Name, "recall.has")
	}
}

// TestLoadAllMissingName 验证 front matter 缺少 name 字段报错。
func TestLoadAllMissingName(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, filepath.Join(root, "bad"), `---
category: recall
description: 缺少 name
---
body
`)
	r := NewRegistry()
	l := NewLoader(root, r)
	if err := l.LoadAll(context.Background()); err == nil {
		t.Fatal("缺少 name 字段应返回错误")
	}
}

// TestLoadAllNotExists 验证根目录不存在时报错。
func TestLoadAllNotExists(t *testing.T) {
	r := NewRegistry()
	l := NewLoader("/not/exists/path", r)
	if err := l.LoadAll(context.Background()); err == nil {
		t.Fatal("根目录不存在应返回错误")
	}
}

// TestParseFrontMatter 验证 front matter 分离。
func TestParseFrontMatter(t *testing.T) {
	content := `---
name: recall.content
category: recall
description: 内容召回
tools:
  - milvus
---
# Skill Body
技能详细说明。
`
	fm, body, err := parseFrontMatter(content)
	if err != nil {
		t.Fatalf("parseFrontMatter 失败: %v", err)
	}
	if !strings.Contains(fm, "name: recall.content") {
		t.Errorf("front matter 缺少 name: %q", fm)
	}
	if !strings.Contains(body, "# Skill Body") {
		t.Errorf("body 缺少正文: %q", body)
	}
}

// TestParseFrontMatterMissingStart 验证缺少起始分隔符报错。
func TestParseFrontMatterMissingStart(t *testing.T) {
	_, _, err := parseFrontMatter("name: test\n---\nbody")
	if err == nil {
		t.Fatal("缺少起始 --- 应返回错误")
	}
}

// TestParseFrontMatterMissingEnd 验证缺少结束分隔符报错。
func TestParseFrontMatterMissingEnd(t *testing.T) {
	_, _, err := parseFrontMatter("---\nname: test\nbody")
	if err == nil {
		t.Fatal("缺少结束 --- 应返回错误")
	}
}

// TestLoadAllBodyAsDescription 验证无 front matter description 时 body 作为 Description。
func TestLoadAllBodyAsDescription(t *testing.T) {
	root := t.TempDir()
	writeSkillMD(t, filepath.Join(root, "skill1"), `---
name: recall.body
category: recall
---
这是 body 内容。
`)
	r := NewRegistry()
	l := NewLoader(root, r)
	if err := l.LoadAll(context.Background()); err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	s, err := r.Get("recall.body")
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if !strings.Contains(s.Description, "这是 body 内容。") {
		t.Errorf("Description 应含 body: %q", s.Description)
	}
}
