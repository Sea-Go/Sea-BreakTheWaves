package items

import (
	"context"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/inference"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/item"
	"github.com/google/uuid"
	mc "github.com/milvus-io/milvus/client/v2/milvusclient"
	"os"
	"strings"
	"testing"
)

func TestSparseInnerProductAndIndependentMultiVector(t *testing.T) {
	ctx := context.Background()
	r := item.Retrieval{Dimensions: 2}
	index := NewExact()
	index.Ensure(ctx, r)
	a := Record{ID: "a", ItemID: "a", RootID: "a", RootKind: "article", RevisionID: "r1", Encoding: inference.Segment{Dense: []float32{1, 0}, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{10}}, Multi: [][]float32{{0, 1}}}}
	b := Record{ID: "b", ItemID: "b", RootID: "b", RootKind: "article", RevisionID: "r1", Encoding: inference.Segment{Dense: []float32{0, 1}, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{9}}, Multi: [][]float32{{1, 0}}}}
	index.Upsert(ctx, r, []Record{a, b})
	query := inference.Encoding{Segments: []inference.Segment{{Dense: []float32{1, 0}, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{1}}, Multi: [][]float32{{1, 0}}}}}
	dense, _ := index.Search(ctx, r, "dense", query, []string{"article"}, 1)
	sparse, _ := index.Search(ctx, r, "sparse", query, []string{"article"}, 1)
	multi, _ := index.Search(ctx, r, "multi", query, []string{"article"}, 1)
	if dense[0].ItemID != "a" || sparse[0].Score != 10 || multi[0].ItemID != "b" {
		t.Fatalf("lane semantics lost: %v %v %v", dense, sparse, multi)
	}
}
func TestMilvusDiskAndSparsePhysicalContract(t *testing.T) {
	address := os.Getenv("BTW_TEST_MILVUS_ADDRESS")
	if address == "" {
		t.Skip("requires isolated Milvus with DISKANN enabled")
	}
	ctx := context.Background()
	client, e := mc.New(ctx, &mc.ClientConfig{Address: address, APIKey: os.Getenv("BTW_TEST_MILVUS_TOKEN")})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close(ctx)
	prefix := "btwtest_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	r := item.Retrieval{Dimensions: 32, Dense: prefix + "_d", Sparse: prefix + "_s", Multi: prefix + "_m"}
	index := &Milvus{Client: client, BatchSize: 128, QueryTokenLimit: 64}
	defer func() {
		for _, lane := range lanes(r) {
			_ = client.DropCollection(ctx, mc.NewDropCollectionOption(collection(r, lane)))
		}
	}()
	if e = index.Ensure(ctx, r); e != nil {
		t.Fatal(e)
	}
	vector := make([]float32, 32)
	vector[0] = 1
	rows := []Record{}
	for i, id := range []string{"a", "b", "book-child"} {
		kind := "article"
		if i == 2 {
			kind = "book"
		}
		rows = append(rows, Record{ID: id, ItemID: id, RootID: id, RootKind: kind, RevisionID: "r1", Location: item.Location{SectionID: "s", End: 10}, Encoding: inference.Segment{Dense: vector, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{float32(10 - i)}}, Multi: [][]float32{vector}}})
	}
	if e = index.Upsert(ctx, r, rows); e != nil {
		t.Fatal(e)
	}
	q := inference.Encoding{Segments: []inference.Segment{{Dense: vector, Sparse: inference.Sparse{Indices: []uint32{1}, Values: []float32{1}}, Multi: [][]float32{vector}}}}
	for _, lane := range []string{"dense", "sparse", "multi"} {
		hits, e := index.Search(ctx, r, lane, q, []string{"article"}, 10)
		if e != nil {
			t.Fatal(e)
		}
		if len(hits) != 2 {
			t.Fatalf("%s scope/visibility returned %d", lane, len(hits))
		}
		if lane == "sparse" && (hits[0].ItemID != "a" || hits[0].Score != 10) {
			t.Fatal("learned sparse IP changed")
		}
	}
	if e = index.Delete(ctx, r, "a"); e != nil {
		t.Fatal(e)
	}
	hits, e := index.Search(ctx, r, "dense", q, []string{"article"}, 10)
	if e != nil || len(hits) != 1 {
		t.Fatal("physical delete not visible")
	}
}
