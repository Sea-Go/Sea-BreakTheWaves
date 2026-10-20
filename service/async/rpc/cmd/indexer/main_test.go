package main

import (
	"bytes"
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/wholeindex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/rpc/internal/indexer"
)

func readyIDs(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "READY manifest_id="); ok && rest != "" {
			ids = append(ids, rest)
		}
	}
	return ids
}

func artifactLines(out string) []string {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "artifact" {
			keys = append(keys, fields[1])
		}
	}
	return keys
}

// TestRunExampleEvents：README 示例文件跑通完整管线——2 个 READY 回执、
// 3+1 文档 × 3 路 + 2 manifest + 2 tree = 16 个工件，键全部带各自
// manifest_id 前缀且含 manifest/tree 固定名对象。
func TestRunExampleEvents(t *testing.T) {
	var buf bytes.Buffer
	if err := runAll(context.Background(), filepath.Join("testdata", "example-events.jsonl"), &buf); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	out := buf.String()
	ids := readyIDs(out)
	if len(ids) != 2 {
		t.Fatalf("READY receipts = %d, want 2\noutput:\n%s", len(ids), out)
	}
	keys := artifactLines(out)
	if len(keys) != 16 {
		t.Fatalf("artifact lines = %d, want 16\noutput:\n%s", len(keys), out)
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		seen[k] = true
	}
	for _, id := range ids {
		if !seen[wholeindex.ManifestKey(id)] {
			t.Errorf("manifest object %s missing", wholeindex.ManifestKey(id))
		}
		if !seen[wholeindex.TreeKey(id)] {
			t.Errorf("tree object %s missing", wholeindex.TreeKey(id))
		}
	}
	if !strings.Contains(out, "indexer dev: done") {
		t.Errorf("missing done summary:\n%s", out)
	}
}

// TestRunDeterministic：同一事件文件跑两次，READY manifest_id 逐行一致。
func TestRunDeterministic(t *testing.T) {
	path := filepath.Join("testdata", "example-events.jsonl")
	var first, second bytes.Buffer
	if err := runAll(context.Background(), path, &first); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if err := runAll(context.Background(), path, &second); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	a, b := readyIDs(first.String()), readyIDs(second.String())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("manifest ids differ across runs: %v vs %v", a, b)
	}
}

// TestRunDuplicateEventIdSkipped：文件内重复 event_id 只处理一次。
func TestRunDuplicateEventIdSkipped(t *testing.T) {
	line := `{"event_id":"evt-dup","module_id":"m","release_id":"r","published_at":"t","docs":[{"doc_key":"wiki/a","revision_id":"rev-a","content_sha256":"sha-a","char_len":10,"structure_ref":"s/a"}]}`
	path := filepath.Join(t.TempDir(), "dup.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := runAll(context.Background(), path, &buf); err != nil {
		t.Fatalf("runAll: %v", err)
	}
	if ids := readyIDs(buf.String()); len(ids) != 1 {
		t.Fatalf("READY receipts = %d, want 1（幂等重放）\noutput:\n%s", len(ids), buf.String())
	}
	if keys := artifactLines(buf.String()); len(keys) != 5 {
		t.Fatalf("artifacts = %d, want 5（1 文档 × 3 路 + manifest + tree）", len(keys))
	}
}

func TestRunBadInput(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing path", "", "--events is required"},
		{"bad json", "{not-json}\n", "line 1"},
		{"empty file", "", ""},
	}
	for _, tc := range cases {
		var path string
		if tc.name == "missing path" {
			path = ""
		} else {
			path = filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var buf bytes.Buffer
		err := runAll(context.Background(), path, &buf)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: err = %v, want nil", tc.name, err)
			}
			if ids := readyIDs(buf.String()); len(ids) != 0 {
				t.Errorf("%s: unexpected READY %v", tc.name, ids)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestRunCancelled：取消的 context 干净退出，无 READY。
func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	err := runAll(ctx, filepath.Join("testdata", "example-events.jsonl"), &buf)
	if err == nil {
		t.Fatal("cancelled run must fail")
	}
	if ids := readyIDs(buf.String()); len(ids) != 0 {
		t.Fatalf("cancelled run emitted READY %v", ids)
	}
}

// TestFakeEncoderDeterministicAndShaped：假编码器同输入同输出、维度契约满足。
func TestFakeEncoderDeterministicAndShaped(t *testing.T) {
	doc := indexer.ReleaseDoc{
		DocKey: "wiki/a", RevisionID: "rev-a", ContentSHA256: "sha-a",
		CharLen: 10, StructureRef: "s/a",
	}
	r1, err := fakeEncoder{}.Encode(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := fakeEncoder{}.Encode(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r1, r2) {
		t.Fatal("fake encoder is not deterministic")
	}
	if len(r1.Dense) != fakeDenseDim {
		t.Fatalf("dense dim = %d, want %d", len(r1.Dense), fakeDenseDim)
	}
	if r1.EncoderID != fakeEncoderID || r1.EncoderID == "" {
		t.Fatalf("encoder id = %q", r1.EncoderID)
	}
	if rows := len(r1.Multi) / r1.MultiDim; rows < 1 || rows > fakeMultiRows {
		t.Fatalf("multi rows = %d, want [1,%d]", rows, fakeMultiRows)
	}
	if len(r1.Sparse) == 0 {
		t.Fatal("sparse lane empty for tokenized input")
	}
}
