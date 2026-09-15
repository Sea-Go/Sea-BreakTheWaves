package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBackendRequiresExactBuildProjectionSettingsAndKeepsOldModeOff(t *testing.T) {
	values := testEnv(t)
	if cfg, err := loadConfig(envMap(values)); err != nil || cfg.Mode != "local-exact" ||
		cfg.Native != nil || cfg.MilvusAddress != "" {
		t.Fatalf("old local-exact mode acquired a physical backend: %+v %v", cfg, err)
	}
	values["BTW_SEARCH_MODE"] = "native-milvus"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("physical mode started with no address or immutable build settings")
	}
	values["BTW_SEARCH_MILVUS_ADDRESS"] = "127.0.0.1:19530"
	path := filepath.Join(t.TempDir(), "native.json")
	values["BTW_SEARCH_NATIVE_FILE"] = path
	valid := `{"schema_version":"sea.search.native-milvus.v1","engine":"lite","namespace":"release_fixed","dense":{"m":16,"ef_construction":128,"ef_search":64},"multivector":{"m":16,"ef_construction":128,"ef_search":64}}`
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"fixed physical release parameters", valid, true},
		{"missing multivector", strings.Replace(valid, `,"multivector":{"m":16,"ef_construction":128,"ef_search":64}`, "", 1), false},
		{"wrong engine", strings.Replace(valid, `"engine":"lite"`, `"engine":"exact"`, 1), false},
		{"wrong namespace", strings.Replace(valid, `"namespace":"release_fixed"`, `"namespace":"../latest"`, 1), false},
		{"dense HNSW bound", strings.Replace(valid, `"dense":{"m":16,"ef_construction":128`, `"dense":{"m":16,"ef_construction":8`, 1), false},
		{"duplicate engine", strings.Replace(valid, `"engine":"lite"`, `"engine":"lite","engine":"milvus"`, 1), false},
		{"escaped engine alias", strings.Replace(valid, `"engine":"lite"`, `"engine":"lite","\u0065ngine":"milvus"`, 1), false},
		{"duplicate token budget", strings.Replace(valid, `"multivector":{"m":16`, `"multivector":{"m":16,"m":32`, 1), false},
		{"unknown fallback", valid[:len(valid)-1] + `,"allow_exact_fallback":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(envMap(values))
			if tc.valid {
				if err != nil || cfg.Mode != "native-milvus" || cfg.Native == nil ||
					cfg.Native.Engine != "lite" || cfg.Native.Namespace != "release_fixed" ||
					cfg.Native.Dense.M != 16 || cfg.Native.MultiVector.EFSearch != 64 {
					t.Fatalf("fixed physical settings were not selected: %+v %v", cfg.Native, err)
				}
			} else if err == nil {
				t.Fatal("malformed collection identity was accepted before any SDK call")
			}
		})
	}
	values["BTW_SEARCH_MODE"] = "local-exact"
	if _, err := loadConfig(envMap(values)); err == nil {
		t.Fatal("old exact mode accepted native settings as an implicit backend switch")
	}
}
