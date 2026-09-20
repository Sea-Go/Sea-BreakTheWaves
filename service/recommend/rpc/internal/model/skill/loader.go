// ============================================================================
// 该文件实现 Skill 加载器 Loader。
//
// Loader 扫描技能根目录下的所有子目录，每个含 SKILL.md 的目录加载为一个
// Skill。SKILL.md 格式为 YAML front matter + markdown body：
//
//	---
//	name: recall.content
//	category: recall
//	description: 向量+BM25 内容召回
//	tools:
//	  - milvus_search
//	  - bm25_search
//	---
//	# Skill Body
//	技能详细说明...
//
// 解析 front matter 得到 name/category/description/tools，body 作为
// Skill.Description（技能内容物化到 tool result，不污染 Prompt Cache 前缀）。
//
// 二开扩展点：业务方放置技能目录（SKILL.md + 工具 + 资源），框架自动加载。
// ============================================================================

package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/recommend/rpc/internal/domain"
)

// skillFileName 技能描述文件名。
const skillFileName = "SKILL.md"

// Loader Skill 加载器，扫描技能目录加载 SKILL.md。
type Loader struct {
	// rootDir 技能根目录，其下每个子目录可含一个 SKILL.md。
	rootDir string
	// registry 注册中心，加载的 Skill 注册到此。
	registry *Registry
}

// NewLoader 创建加载器。
//
// rootDir 技能根目录；registry 注册中心。
func NewLoader(rootDir string, registry *Registry) *Loader {
	return &Loader{
		rootDir:  rootDir,
		registry: registry,
	}
}

// LoadAll 遍历 rootDir 下所有子目录，每个含 SKILL.md 的目录加载为一个 Skill。
//
// 加载顺序按目录名字典序，确保可复现。任一技能加载或注册失败立即返回错误。
// 支持 ctx 取消。
func (l *Loader) LoadAll(ctx context.Context) error {
	entries, err := os.ReadDir(l.rootDir)
	if err != nil {
		return fmt.Errorf("skill: 读取技能根目录失败 %s: %w", l.rootDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// 响应 ctx 取消。
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(l.rootDir, entry.Name())
		skillPath := filepath.Join(dir, skillFileName)
		if _, err := os.Stat(skillPath); err != nil {
			// 无 SKILL.md，跳过该子目录。
			continue
		}
		s, err := l.loadSkill(ctx, dir)
		if err != nil {
			return fmt.Errorf("skill: 加载技能失败 %s: %w", dir, err)
		}
		if err := l.registry.RegisterSkill(s); err != nil {
			return fmt.Errorf("skill: 注册技能失败 %s: %w", s.Name, err)
		}
	}
	return nil
}

// loadSkill 解析 SKILL.md（YAML front matter + markdown body）。
//
// dir 技能目录（含 SKILL.md）。返回扩展 Skill（含 Category）。
func (l *Loader) loadSkill(ctx context.Context, dir string) (Skill, error) {
	skillPath := filepath.Join(dir, skillFileName)
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Skill{}, fmt.Errorf("读取 SKILL.md 失败: %w", err)
	}
	frontMatter, body, err := parseFrontMatter(string(data))
	if err != nil {
		return Skill{}, err
	}
	// 解析 YAML front matter。
	var meta struct {
		Name        string   `yaml:"name"`
		Category    string   `yaml:"category"`
		Description string   `yaml:"description"`
		Tools       []string `yaml:"tools"`
	}
	if err := yaml.Unmarshal([]byte(frontMatter), &meta); err != nil {
		return Skill{}, fmt.Errorf("解析 YAML front matter 失败: %w", err)
	}
	if meta.Name == "" {
		return Skill{}, errors.New("skill: SKILL.md front matter 缺少 name 字段")
	}
	// body 作为 Skill.Description（技能内容物化到 tool result）。
	// 若 front matter 含 description，作为摘要前缀拼接。
	description := strings.TrimSpace(body)
	if meta.Description != "" {
		description = meta.Description + "\n\n" + description
	}
	return Skill{
		Skill: domain.Skill{
			Name:        meta.Name,
			Description: description,
			Tools:       meta.Tools,
			Resources:   []string{dir},
		},
		Category: meta.Category,
	}, nil
}

// parseFrontMatter 分离 YAML front matter 与 markdown body。
//
// SKILL.md 格式：
//
//	---
//	yaml 内容
//	---
//	markdown body
//
// 返回 front matter 文本（不含分隔符）与 body 文本。
func parseFrontMatter(content string) (frontMatter string, body string, err error) {
	content = strings.TrimSpace(content)
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("skill: SKILL.md 缺少 YAML front matter 起始分隔符 ---")
	}
	// 查找结束分隔符 ---。
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return "", "", errors.New("skill: SKILL.md 缺少 YAML front matter 结束分隔符 ---")
	}
	frontMatter = strings.Join(lines[1:endIdx], "\n")
	if endIdx+1 < len(lines) {
		body = strings.Join(lines[endIdx+1:], "\n")
	}
	return frontMatter, body, nil
}
