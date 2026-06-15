package bilibili

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent_v3/internal/config"

	agenttool "trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestBilibiliSearchToolCallsSkillScript(t *testing.T) {
	skillDir := writeBilibiliTestSkill(t)
	runtime := &bilibiliRuntime{
		cfg: config.BilibiliConfig{
			Cookie: "SESSDATA=test-cookie",
		},
		skillDir:      skillDir,
		pythonCommand: "python",
		timeout:       5 * time.Second,
	}

	out, err := runtime.Search(context.Background(), BilibiliSearchInput{
		Query: "chengdu travel guide",
		Count: 2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if out.Code != 0 || out.Message != "success" || out.ItemCount != 1 {
		t.Fatalf("unexpected result: %+v", out)
	}
	if len(out.Items) != 1 || out.Items[0].Title != "chengdu travel guide|2|SESSDATA=test-cookie" {
		t.Fatalf("unexpected item: %+v", out.Items)
	}
}

func TestBilibiliToolSetDeclaration(t *testing.T) {
	set := NewBilibiliToolSet(config.BilibiliConfig{})
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
	if !names["bilibili_guide_material"] || names["bilibili_search"] {
		t.Fatalf("unexpected tool declarations: %+v", names)
	}
	if set.Name() != "bilibili" {
		t.Fatalf("toolset name = %q, want bilibili", set.Name())
	}
}

func TestClampBilibiliSearchCount(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{in: 0, want: 10},
		{in: 1, want: 1},
		{in: 21, want: 20},
	}
	for _, tc := range cases {
		if got := clampBilibiliSearchCount(tc.in); got != tc.want {
			t.Fatalf("clampBilibiliSearchCount(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestBilibiliScriptEnvIncludesCookie(t *testing.T) {
	runtime := &bilibiliRuntime{cfg: config.BilibiliConfig{Cookie: "SESSDATA=test-cookie"}}
	env := runtime.bilibiliScriptEnv()
	found := false
	for _, item := range env {
		if item == "BILIBILI_COOKIE=SESSDATA=test-cookie" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("BILIBILI_COOKIE missing from env: %+v", env)
	}
}

func TestResolveBilibiliPythonCommandFallsBackToPython3(t *testing.T) {
	binDir := t.TempDir()
	python3Path := filepath.Join(binDir, "python3")
	if err := os.WriteFile(python3Path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("PATH", binDir)

	runtime := &bilibiliRuntime{pythonCommand: defaultBilibiliPythonCommand}
	if got := runtime.resolvePythonCommand(); got != python3Path {
		t.Fatalf("resolvePythonCommand() = %q, want %q", got, python3Path)
	}
}

func TestBilibiliSearchRejectsEmptyQuery(t *testing.T) {
	runtime := &bilibiliRuntime{}
	_, err := runtime.Search(context.Background(), BilibiliSearchInput{Query: "   "})
	if err == nil || !strings.Contains(err.Error(), "query cannot be empty") {
		t.Fatalf("Search() error = %v, want empty query error", err)
	}
}

func TestBilibiliToolsCallable(t *testing.T) {
	toolMap := map[string]agenttool.Tool{}
	for _, tl := range NewBilibiliTools(config.BilibiliConfig{}) {
		toolMap[tl.Declaration().Name] = tl
	}
	if _, ok := toolMap["bilibili_guide_material"].(agenttool.CallableTool); !ok {
		t.Fatalf("bilibili_guide_material is not callable")
	}
	if _, ok := toolMap["bilibili_search"]; ok {
		t.Fatalf("bilibili_search should not be exposed as an agent tool")
	}
}

func writeBilibiliTestSkill(t *testing.T) string {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "bilibili-search")
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
    os.getenv("BILIBILI_COOKIE", ""),
])
print(json.dumps({
    "code": 0,
    "message": "success",
    "item_count": 1,
    "items": [{
        "title": title,
        "url": "https://www.bilibili.com/video/BV1xx411c7mD",
        "bvid": "BV1xx411c7mD",
        "author_name": "tester",
        "summary": "ok",
        "view_count": 100,
        "danmaku_count": 2,
        "like_count": 3,
        "favorite_count": 4,
        "publish_time": 1893456000
    }]
}, ensure_ascii=False))
`
	if err := os.WriteFile(filepath.Join(scriptsDir, "bilibili-search.py"), []byte(script), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return skillDir
}
