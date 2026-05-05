package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent_v2/config"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestZhihuSearchToolCallsSkillScript(t *testing.T) {
	skillDir := writeZhihuTestSkill(t)
	runtime := &zhihuRuntime{
		cfg: config.ZhihuConfig{
			AccessSecret:   "test-secret",
			OpenAPIBaseURL: "https://developer.example.com",
			ZhihuSearchURL: "https://developer.example.com/custom",
		},
		skillDir:      skillDir,
		pythonCommand: "python",
		timeout:       5 * time.Second,
	}

	out, err := runtime.Search(context.Background(), ZhihuSearchInput{
		Query: "AI Agent",
		Count: 2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if out.Code != 0 || out.Message != "success" || out.ItemCount != 1 {
		t.Fatalf("unexpected result: %+v", out)
	}
	if len(out.Items) != 1 || out.Items[0].Title != "AI Agent|2|test-secret|https://developer.example.com|https://developer.example.com/custom" {
		t.Fatalf("unexpected item: %+v", out.Items)
	}
}

func TestZhihuToolSetDeclaration(t *testing.T) {
	set := NewZhihuToolSet(config.ZhihuConfig{})
	got := set.Tools(context.Background())
	if len(got) != 1 {
		t.Fatalf("tool count = %d, want 1", len(got))
	}
	names := map[string]bool{}
	for _, item := range got {
		if item.Declaration() != nil {
			names[item.Declaration().Name] = true
		}
	}
	if !names["zhihu_guide_material"] || names["zhihu_search"] {
		t.Fatalf("unexpected tool declarations: %+v", names)
	}
	if set.Name() != "zhihu" {
		t.Fatalf("toolset name = %q, want zhihu", set.Name())
	}
}

func TestClampZhihuSearchCount(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{in: 0, want: 5},
		{in: 1, want: 1},
		{in: 11, want: 10},
	}
	for _, tc := range cases {
		if got := clampZhihuSearchCount(tc.in); got != tc.want {
			t.Fatalf("clampZhihuSearchCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestResolveZhihuPythonCommandFallsBackToPython3(t *testing.T) {
	binDir := t.TempDir()
	python3Path := filepath.Join(binDir, "python3")
	if err := os.WriteFile(python3Path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("PATH", binDir)

	runtime := &zhihuRuntime{pythonCommand: defaultPythonCommand}
	if got := runtime.resolvePythonCommand(); got != python3Path {
		t.Fatalf("resolvePythonCommand() = %q, want %q", got, python3Path)
	}
}

func TestZhihuToolsCallable(t *testing.T) {
	toolMap := map[string]agenttool.Tool{}
	for _, tl := range NewZhihuTools(config.ZhihuConfig{}) {
		toolMap[tl.Declaration().Name] = tl
	}
	if _, ok := toolMap["zhihu_guide_material"].(agenttool.CallableTool); !ok {
		t.Fatalf("zhihu_guide_material is not callable")
	}
	if _, ok := toolMap["zhihu_search"]; ok {
		t.Fatalf("zhihu_search should not be exposed as an agent tool")
	}
}

func writeZhihuTestSkill(t *testing.T) string {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "zhihu-search")
	scriptsDir := filepath.Join(skillDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	script := `import json
import os
import sys

query = ""
count = ""
for i, arg in enumerate(sys.argv):
    if arg == "--query":
        query = sys.argv[i + 1]
    if arg == "--count":
        count = sys.argv[i + 1]

title = "|".join([
    query,
    count,
    os.getenv("ZHIHU_ACCESS_SECRET", ""),
    os.getenv("ZHIHU_OPENAPI_BASE_URL", ""),
    os.getenv("ZHIHU_ZHIHU_SEARCH_URL", ""),
])
print(json.dumps({
    "code": 0,
    "message": "success",
    "item_count": 1,
    "items": [{
        "title": title,
        "url": "https://example.com",
        "author_name": "tester",
        "summary": "ok",
        "vote_up_count": 1,
        "comment_count": 2,
        "edit_time": 3
    }]
}, ensure_ascii=False))
`
	if err := os.WriteFile(filepath.Join(scriptsDir, "zhihu-search.py"), []byte(script), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return skillDir
}
