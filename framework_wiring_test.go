package sea_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// C17 要求"云端搜广推必须基于 tRPC-Agent-Go"（见 Sea-Docs 规划决策表 C17）。
// 本测试把规范的边界判据变成可执行约束：承载 C17 职责的包必须存在框架装
// 配文件，且装配文件确实引用了框架；纯算法内核则不得引入框架依赖。
//
// 判据来源：《LLM-Wiki平台工程目录与工作规范》§3.2 第 6 条。
const frameworkImportPrefix = "trpc.group/trpc-go/trpc-agent-go"

// frameworkWiredPackages 列出 C17 要求必须经框架的包、装配文件，以及该
// 装配类型对应的**框架真实路径**（测试必须走这条路径才算有效验证）。
// 新增此类包时同步登记，使约束随代码演进保持有效。
var frameworkWiredPackages = map[string]struct {
	File string
	// TestPath 是测试必须导入的框架子包之一（装配类型决定走哪条路径）：
	// Graph 类装配 → runner/graphagent；Tool 类 → tool/function；模型类 → model。
	TestPath []string
}{
	// C4/C5 编制编排：四阶段状态机必须落框架 Graph。
	"service/async/rpc/internal/compile": {
		File:     "graph.go",
		TestPath: []string{frameworkImportPrefix + "/runner", frameworkImportPrefix + "/agent/graphagent"},
	},
	// B2 查询规划：规划能力必须作为框架 Tool 暴露给 Agent。
	"service/search/rpc/internal/planner": {
		File:     "tool.go",
		TestPath: []string{frameworkImportPrefix + "/tool", frameworkImportPrefix + "/tool/function"},
	},
	// B6 摘要交付：模型调用必须经框架 model.Model（C31 再由 DC 注入）。
	"service/search/rpc/internal/summary": {
		File:     "model.go",
		TestPath: []string{frameworkImportPrefix + "/model"},
	},
	// 检索装配：C17 原文点名"检索装配以框架公开 API 为基础"。
	"service/search/rpc/internal/pipeline": {
		File:     "graph.go",
		TestPath: []string{frameworkImportPrefix + "/runner", frameworkImportPrefix + "/agent/graphagent"},
	},
}

// pureKernelPackages 列出允许不依赖框架的纯算法内核包。
// 约束其"确实是纯内核"——一旦出现框架引用，说明边界被判据要求之外地打破。
var pureKernelPackages = []string{
	"service/search/rpc/internal/retrieval",
	"service/search/rpc/internal/evidence",
	"service/search/rpc/internal/evalseed",
	"service/async/rpc/internal/artifact",
	"service/async/rpc/internal/tree",
	"service/search/rpc/internal/devseed",
	"service/search/rpc/internal/fakerepr",
}

// TestFrameworkWiredPackagesCarryFrameworkImports 装配文件必须真实引用框架。
// 仅存在文件而内容不接框架（假装配）会被此测试拦下。
func TestFrameworkWiredPackagesCarryFrameworkImports(t *testing.T) {
	for pkg, spec := range frameworkWiredPackages {
		path := filepath.Join(pkg, spec.File)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("C17 assembly file missing: %s (%v)", path, err)
		}
		imports := goImports(t, path)
		found := false
		for _, imp := range imports {
			if strings.HasPrefix(imp, frameworkImportPrefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s must import %s (C17 assembly), got imports: %v",
				path, frameworkImportPrefix, imports)
		}
	}
}

// TestPureKernelPackagesStayFrameworkFree 纯算法内核不得引入框架依赖，
// 保证"框架只管运行时装配"的边界不被侵蚀。
func TestPureKernelPackagesStayFrameworkFree(t *testing.T) {
	for _, pkg := range pureKernelPackages {
		entries, err := os.ReadDir(pkg)
		if err != nil {
			t.Fatalf("read %s: %v", pkg, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(pkg, entry.Name())
			for _, imp := range goImports(t, path) {
				if strings.HasPrefix(imp, frameworkImportPrefix) {
					t.Fatalf("pure kernel %s must not import the agent framework (found in %s)", pkg, path)
				}
			}
		}
	}
}

// TestFrameworkWiredAssemblyFilesAreTested 装配文件必须配套测试，
// 且测试需经框架真实路径（至少引用框架的 runner/agent/graph 之一）。
func TestFrameworkWiredAssemblyFilesAreTested(t *testing.T) {
	for pkg, spec := range frameworkWiredPackages {
		testPath := filepath.Join(pkg, strings.TrimSuffix(spec.File, ".go")+"_test.go")
		if _, err := os.Stat(testPath); err != nil {
			t.Fatalf("assembly %s has no companion test %s (%v)", spec.File, testPath, err)
		}
		imports := goImports(t, testPath)
		runsFramework := false
		for _, imp := range imports {
			for _, required := range spec.TestPath {
				if imp == required {
					runsFramework = true
				}
			}
		}
		if !runsFramework {
			t.Fatalf("%s must exercise a real framework path %v, imports: %v",
				testPath, spec.TestPath, imports)
		}
	}
}

// goImports 解析单个 Go 文件的 import 路径列表。
func goImports(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, `"`))
	}
	return imports
}
