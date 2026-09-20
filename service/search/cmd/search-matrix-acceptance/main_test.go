package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaseExportCannotPromoteIncompleteQrels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "case-export.json")
	fixture := `{"schema_version":"sea.search.matrix-source.v1","dataset_manifest_sha256":"snapshot",` +
		`"data_kind":"synthetic","reader_status":"validated_synthetic_fixture","split":"test",` +
		`"split_windows":[{"name":"train"},{"name":"validation"},{"name":"test"}],` +
		`"evaluation_cutoff":"2026-09-09T00:00:00Z","judgment_scope_complete":false,` +
		`"cases":[{"query_id":"q","query_family_id":"family","near_duplicate_cluster_id":"cluster",` +
		`"query_text":"why","query_time":"2026-09-06T00:00:00Z","judgments":[]}]}`
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSource(path, hash([]byte(fixture))); err != nil {
		t.Fatalf("valid incomplete synthetic export rejected: %v", err)
	}
	promoted := strings.Replace(fixture, `"judgment_scope_complete":false`,
		`"judgment_scope_complete":true`, 1)
	if err := os.WriteFile(path, []byte(promoted), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSource(path, hash([]byte(fixture))); err == nil {
		t.Fatal("changed source bytes passed original SHA")
	}
	if _, err := readSource(path, hash([]byte(promoted))); err == nil {
		t.Fatal("complete judgment scope passed without a receipt")
	}
}
