package app

import (
	"encoding/json"
	"os"
	"testing"
)

// The vector file is shared byte-identical with Sea-RideTheWind
// service/common/communityfact and Sea-DataCenter internal/platformjson.
// Any drift between the three canonicalization implementations must surface
// here, not in production receipts.
func TestJCSGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcs_golden_vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var doc struct {
		Schema string `json:"schema"`
		Cases  []struct {
			Name         string `json:"name"`
			Input        string `json:"input"`
			ExpectedHash string `json:"expected_hash"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if doc.Schema != "sea.jcs-golden-vectors.v1" {
		t.Fatalf("unexpected vector schema %q", doc.Schema)
	}
	for _, c := range doc.Cases {
		hash, err := canonicalInputHash([]byte(c.Input))
		if err != nil {
			t.Errorf("%s: canonicalize: %v", c.Name, err)
			continue
		}
		if hash != c.ExpectedHash {
			t.Errorf("%s: hash mismatch\n got %s\nwant %s", c.Name, hash, c.ExpectedHash)
		}
	}
}
