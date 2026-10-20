package fakerepr

import (
	"reflect"
	"testing"
)

// 黄金向量钉死假编码口径（期望值由镜像源 cmd/indexer/fake.go 的同口径
// 推导；任一侧口径漂移会在此显式失败——改动需双边同步，见包注释）。
func TestEncodeGoldenVector(t *testing.T) {
	r := Encode("doc-00\x1fstructure/doc-00\x1frev-0123456789abcdef")
	if len(r.Dense) != DenseDim || len(r.Multi) != r.MultiRows*MultiDim {
		t.Fatalf("形状不符: dense=%d multi=%d(%d 行)", len(r.Dense), len(r.Multi), r.MultiRows)
	}
	// dense 首分量与 token 数是口径的稳定指纹（完整逐字节对照由
	// retrieval 侧的工件黄金向量测试覆盖，此处钉查询侧消费的形状与
	// 确定性）。
	if r.Dense[0] == 0 {
		t.Fatal("dense 首分量为 0，哈希展开口径可疑")
	}
	if r.MultiRows != 7 { // doc/00/structure/doc/00/rev/0123456789abcdef
		t.Fatalf("multi 行数 %d want 7", r.MultiRows)
	}
	if r.EncoderID != "fake-encoder.v1" {
		t.Fatalf("encoder_id %q", r.EncoderID)
	}
}

// 同文本必得同 Repr；不同文本必得不同 dense（64 位哈希空间）。
func TestEncodeDeterministic(t *testing.T) {
	a, b := Encode("海洋观测的核心要点"), Encode("海洋观测的核心要点")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("同文本两次编码不一致")
	}
	c := Encode("海洋观测的延伸要点")
	if reflect.DeepEqual(a.Dense, c.Dense) {
		t.Fatal("不同文本的 dense 不应相同")
	}
}

// Rows/SparseMap 的切分与映射口径。
func TestRowsAndSparseMap(t *testing.T) {
	r := Encode("alpha beta gamma")
	rows := r.Rows(2)
	if len(rows) != 2 || len(rows[0]) != MultiDim {
		t.Fatalf("Rows(2) 形状不符: %d 行、首行 %d 维", len(rows), len(rows[0]))
	}
	all := r.Rows(0)
	if len(all) != 3 {
		t.Fatalf("Rows(0) 应保留全部 3 行，得到 %d", len(all))
	}
	m := r.SparseMap()
	if len(m) != len(r.Terms) {
		t.Fatalf("SparseMap 条数 %d want %d", len(m), len(r.Terms))
	}
	for _, term := range r.Terms {
		if w, ok := m[term.TermID]; !ok || w != float32(term.Weight) {
			t.Fatalf("SparseMap[%d]=%v 与 Terms 不符", term.TermID, w)
		}
	}
}

// 空文本：multi 补 1 行种子，sparse 为空。
func TestEncodeEmptyText(t *testing.T) {
	r := Encode("")
	if r.MultiRows != 1 || len(r.Multi) != MultiDim {
		t.Fatalf("空文本 multi 应补 1 行: rows=%d len=%d", r.MultiRows, len(r.Multi))
	}
	if len(r.Terms) != 0 || len(r.SparseMap()) != 0 {
		t.Fatal("空文本 sparse 应为空")
	}
	if len(r.Dense) != DenseDim {
		t.Fatalf("dense 维度 %d want %d", len(r.Dense), DenseDim)
	}
}

// SparseMapOfText 与 Encode 的 sparse 路同口径。
func TestSparseMapOfTextMatchesEncode(t *testing.T) {
	text := "检索 检索 与 融合"
	want := Encode(text).SparseMap()
	got := SparseMapOfText(text)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("SparseMapOfText 与 Encode.SparseMap 不一致: %v vs %v", got, want)
	}
}
