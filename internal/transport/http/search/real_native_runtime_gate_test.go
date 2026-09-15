package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/artifacts"
)

func TestNativeProjectionRejectsUnownedOrWrongEngineRuntimeBeforeSDK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	base := realNativeRuntime{SchemaVersion: "sea.search.native-lite-runtime.v1",
		Owner: realNativeOwner, Endpoint: "127.0.0.1:19530", Engine: "lite",
		MilvusLite: "3.2.1", BinarySHA256: strings.Repeat("a", 64),
		Directory: dir, ReleasePath: filepath.Join(dir, "release")}
	write := func(value realNativeRuntime, perm os.FileMode) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
	}
	write(base, 0600)
	if got, sha, err := readRealNativeRuntime(path); err != nil || got != base ||
		!artifacts.ValidHash(sha) {
		t.Fatalf("task-owned pinned Lite runtime rejected before projection: %+v %s %v", got, sha, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*realNativeRuntime)
		perm   os.FileMode
	}{
		{"unowned process", func(r *realNativeRuntime) { r.Owner = "old Sparse process" }, 0600},
		{"Sparse-only old version", func(r *realNativeRuntime) { r.MilvusLite = "2.5.1" }, 0600},
		{"nonlocal engine", func(r *realNativeRuntime) { r.Endpoint = "production:19530" }, 0600},
		{"missing binary identity", func(r *realNativeRuntime) { r.BinarySHA256 = "" }, 0600},
		{"different release path", func(r *realNativeRuntime) { r.ReleasePath = "/tmp/another/release" }, 0600},
		{"public fixture", func(*realNativeRuntime) {}, 0644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			tc.mutate(&candidate)
			write(candidate, tc.perm)
			if _, _, err := readRealNativeRuntime(path); err == nil {
				t.Fatal("an old/mismatched physical engine crossed the native builder boundary")
			}
		})
	}
}

func TestNativeProjectionReceiptUsesFormalAPIPhysicalConfigKeys(t *testing.T) {
	p := &realNativeProjector{runtime: realNativeRuntime{Endpoint: "127.0.0.1:19530",
		BinarySHA256: strings.Repeat("a", 64)},
		setting: realNativeSettings{SchemaVersion: "sea.search.native-milvus.v1",
			Engine: "lite", Namespace: "native_fixed",
			Dense:       realNativeHNSW{M: 16, EFConstruction: 128, EFSearch: 64},
			MultiVector: realNativeHNSW{M: 16, EFConstruction: 128, EFSearch: 64}}}
	receipt := p.Receipt()
	var root map[string]json.RawMessage
	if json.Unmarshal(receipt.Settings, &root) != nil || len(root) != 5 {
		t.Fatal("native projection produced a different formal API physical config root")
	}
	for _, key := range []string{"schema_version", "engine", "namespace", "dense", "multivector"} {
		if len(root[key]) == 0 {
			t.Fatalf("formal native config literal %s missing", key)
		}
	}
	for _, lane := range []string{"dense", "multivector"} {
		var hnsw map[string]json.RawMessage
		if json.Unmarshal(root[lane], &hnsw) != nil || len(hnsw) != 3 {
			t.Fatalf("%s physical HNSW settings differ from formal API", lane)
		}
		for _, key := range []string{"m", "ef_construction", "ef_search"} {
			if len(hnsw[key]) == 0 {
				t.Fatalf("%s missing fixed %s HNSW setting", lane, key)
			}
		}
	}
	if receipt.PhysicalQualified || receipt.Status != "test_projection_before_RTW_READY" {
		t.Fatal("test-only projection receipt falsely certified final physical publication")
	}
}
