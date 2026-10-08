package wholeindex

import (
	"encoding/json"
	"strings"
	"testing"
)

func validManifest() WholeDocIndexManifest {
	m := WholeDocIndexManifest{
		ModuleID:  "mod-7",
		ReleaseID: "rel-20261006",
		Docs: []DocEntry{
			{
				DocKey:       "wiki/en/GhostNet",
				StructureRef: "s3://btw-index/mod-7/rel-20261006/structure/ghostnet.bin",
				DenseRef:     "s3://btw-index/mod-7/rel-20261006/dense/ghostnet.i8",
				SparseRef:    "s3://btw-index/mod-7/rel-20261006/sparse/ghostnet.imp",
				MultiRef:     "s3://btw-index/mod-7/rel-20261006/multi/ghostnet.i8",
				MultiTokens:  64,
				EncoderID:    "dc-encoder/dense-v3",
				SourceChars:  12034,
				BudgetBytes:  8192,
			},
			{
				DocKey:       "wiki/en/CoralBleaching",
				StructureRef: "s3://btw-index/mod-7/rel-20261006/structure/coral.bin",
				DenseRef:     "s3://btw-index/mod-7/rel-20261006/dense/coral.i8",
				SparseRef:    "s3://btw-index/mod-7/rel-20261006/sparse/coral.imp",
				MultiRef:     "s3://btw-index/mod-7/rel-20261006/multi/coral.i8",
				MultiTokens:  2048,
				EncoderID:    "dc-encoder/dense-v3",
				SourceChars:  90422,
				BudgetBytes:  65536,
			},
		},
	}
	AssignID(&m)
	return m
}

func TestValidate(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	mut := func(f func(*WholeDocIndexManifest)) WholeDocIndexManifest {
		m := validManifest()
		f(&m)
		return m
	}
	cases := []struct {
		name string
		m    WholeDocIndexManifest
		want string
	}{
		{"no docs", mut(func(m *WholeDocIndexManifest) { m.Docs = nil }), "docs"},
		{"empty docs", mut(func(m *WholeDocIndexManifest) { m.Docs = []DocEntry{} }), "docs"},
		{"multi_tokens zero", mut(func(m *WholeDocIndexManifest) { m.Docs[0].MultiTokens = 0 }), "multi_tokens"},
		{"multi_tokens negative", mut(func(m *WholeDocIndexManifest) { m.Docs[1].MultiTokens = -1 }), "multi_tokens"},
		{"multi_tokens too large", mut(func(m *WholeDocIndexManifest) { m.Docs[1].MultiTokens = MaxMultiTokens + 1 }), "multi_tokens"},
		{"multi_tokens upper bound ok at 2048", mut(func(m *WholeDocIndexManifest) { m.Docs[0].MultiTokens = 2048 }), ""},
		{"budget_bytes zero", mut(func(m *WholeDocIndexManifest) { m.Docs[0].BudgetBytes = 0 }), "budget_bytes"},
		{"budget_bytes negative", mut(func(m *WholeDocIndexManifest) { m.Docs[0].BudgetBytes = -8 }), "budget_bytes"},
		{"encoder_id empty", mut(func(m *WholeDocIndexManifest) { m.Docs[0].EncoderID = "" }), "encoder_id"},
	}
	for _, c := range cases {
		err := c.m.Validate()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: rejected: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
}

func TestCanonicalJSONShape(t *testing.T) {
	m := WholeDocIndexManifest{
		ModuleID:  "mod",
		ReleaseID: "rel",
		Docs: []DocEntry{{
			DocKey: "k", StructureRef: "st", DenseRef: "de", SparseRef: "sp", MultiRef: "mu",
			MultiTokens: 8, EncoderID: "enc", SourceChars: 10, BudgetBytes: 32,
		}},
	}
	want := `{"module_id":"mod","release_id":"rel","docs":[{` +
		`"doc_key":"k","structure_ref":"st","dense_ref":"de","sparse_ref":"sp","multi_ref":"mu",` +
		`"multi_tokens":8,"encoder_id":"enc","source_chars":10,"budget_bytes":32}]}`
	got := string(CanonicalJSON(m))
	if got != want {
		t.Fatalf("canonical JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
	if strings.ContainsAny(got, " \t\n\r") {
		t.Fatalf("canonical JSON must not contain indentation or whitespace: %s", got)
	}
	if strings.Contains(got, "manifest_id") {
		t.Fatalf("canonical JSON must not embed the derived manifest_id: %s", got)
	}
}

func TestCanonicalJSONEscaping(t *testing.T) {
	m := WholeDocIndexManifest{
		ModuleID:  "mo\"d\\d",
		ReleaseID: "a\x01b\nc\td",
		Docs: []DocEntry{{
			DocKey: "中文/ключ&<>&", MultiTokens: 1, EncoderID: "e", BudgetBytes: 1,
		}},
	}
	raw := CanonicalJSON(m)
	var back WholeDocIndexManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("canonical JSON is not valid JSON: %v\n%s", err, raw)
	}
	if back.ModuleID != m.ModuleID || back.ReleaseID != m.ReleaseID || back.Docs[0].DocKey != m.Docs[0].DocKey {
		t.Fatalf("canonical JSON did not round-trip values: %s", raw)
	}
	if id1, id2 := ManifestID(m), ManifestID(back); id1 != id2 {
		t.Fatalf("id drifted over JSON re-parse: %s vs %s", id1, id2)
	}
}

func TestManifestIDDeterministic(t *testing.T) {
	a, b := validManifest(), validManifest()
	a.Docs[0].DocKey = "x"
	b.Docs[0].DocKey = "x" // rebuild identically
	if string(CanonicalJSON(a)) != string(CanonicalJSON(b)) {
		t.Fatal("same content must yield identical canonical bytes")
	}
	if ManifestID(a) != ManifestID(b) {
		t.Fatal("same content must yield identical manifest id")
	}
	if len(ManifestID(a)) != 32 {
		t.Fatalf("manifest id must be 32 hex chars, got %d", len(ManifestID(a)))
	}

	// Any content change, including docs order, must change the id.
	for _, mut := range []func(*WholeDocIndexManifest){
		func(m *WholeDocIndexManifest) { m.Docs[0].BudgetBytes++ },
		func(m *WholeDocIndexManifest) { m.ReleaseID = "other" },
		func(m *WholeDocIndexManifest) { m.Docs[0], m.Docs[1] = m.Docs[1], m.Docs[0] },
		func(m *WholeDocIndexManifest) { m.Docs[0].MultiTokens++ },
	} {
		c := validManifest()
		mut(&c)
		if ManifestID(c) == ManifestID(a) {
			t.Fatal("different content must yield a different manifest id")
		}
	}
}

// TestManifestRoundTripNoDrift guards the serialization contract: after a
// regular JSON marshal/unmarshal round trip, the recomputed content-addressed
// id must equal the stored manifest_id, and the canonical bytes must be
// byte-identical.
func TestManifestRoundTripNoDrift(t *testing.T) {
	m := validManifest()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back WholeDocIndexManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ManifestID != m.ManifestID {
		t.Fatalf("stored manifest_id drifted: %s vs %s", back.ManifestID, m.ManifestID)
	}
	if recomputed := ManifestID(back); recomputed != back.ManifestID {
		t.Fatalf("recomputed id %s != stored id %s", recomputed, back.ManifestID)
	}
	if string(CanonicalJSON(back)) != string(CanonicalJSON(m)) {
		t.Fatal("canonical bytes drifted over round trip")
	}

	// A body mutation must break the stored id.
	back.Docs[0].SourceChars++
	if ManifestID(back) == back.ManifestID {
		t.Fatal("mutated body must not verify against the old id")
	}
}
