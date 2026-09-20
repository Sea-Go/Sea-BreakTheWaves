package cf

import (
	"math"
	"testing"
)

func TestCoOccurrenceMatrix(t *testing.T) {
	m := NewCoOccurrenceMatrix()
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "c")
	m.AddCoOccurrence("a", "a") // 自身不计共现
	if got := m.GetCoOccurrence("a", "b"); got != 2 {
		t.Fatalf("expected a-b co-occurrence 2, got %d", got)
	}
	if got := m.GetCoOccurrence("b", "a"); got != 2 {
		t.Fatalf("matrix should be symmetric, got %d", got)
	}
	if got := m.GetCoOccurrence("a", "c"); got != 1 {
		t.Fatalf("expected a-c co-occurrence 1, got %d", got)
	}
	if got := m.GetCoOccurrence("a", "a"); got != 0 {
		t.Fatalf("self co-occurrence should be 0, got %d", got)
	}
	if got := m.GetCoOccurrence("x", "y"); got != 0 {
		t.Fatalf("unknown pair should be 0, got %d", got)
	}
}

func TestItemCFComputeSimilarityJaccard(t *testing.T) {
	icf := NewItemCF(1, "jaccard")
	m := NewCoOccurrenceMatrix()
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "c")
	// countA=3, countB=2, co=2 -> jaccard = 2/(3+2-2) = 2/3
	got := icf.ComputeSimilarity(m, "a", "b")
	want := 2.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("jaccard expected %v, got %v", want, got)
	}
}

func TestItemCFComputeSimilarityCosine(t *testing.T) {
	icf := NewItemCF(1, "cosine")
	m := NewCoOccurrenceMatrix()
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "c")
	// countA=3, countB=2, co=2 -> cosine = 2/sqrt(3*2)
	got := icf.ComputeSimilarity(m, "a", "b")
	want := 2.0 / math.Sqrt(6.0)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("cosine expected %v, got %v", want, got)
	}
}

func TestItemCFMinCoOccurrence(t *testing.T) {
	icf := NewItemCF(5, "jaccard") // 阈值 5
	m := NewCoOccurrenceMatrix()
	m.AddCoOccurrence("a", "b") // 共现 1，低于阈值
	if got := icf.ComputeSimilarity(m, "a", "b"); got != 0 {
		t.Fatalf("below minCoOccurrence should be 0, got %v", got)
	}
}

func TestItemCFFindSimilarArticles(t *testing.T) {
	icf := NewItemCF(1, "cosine")
	m := NewCoOccurrenceMatrix()
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "b")
	m.AddCoOccurrence("a", "c")
	items := icf.FindSimilarArticles("a", m, 2)
	if len(items) != 2 {
		t.Fatalf("expected 2 similar articles, got %d", len(items))
	}
	if items[0].ArticleID != "b" {
		t.Fatalf("expected top similar b, got %s", items[0].ArticleID)
	}
	if items[0].Similarity < items[1].Similarity {
		t.Fatalf("should be sorted desc")
	}
}

func TestItemCFRecommend(t *testing.T) {
	icf := NewItemCF(1, "cosine")
	m := NewCoOccurrenceMatrix()
	// 用户看过 a, b；a~c 共现强，b~d 共现
	m.AddCoOccurrence("a", "c")
	m.AddCoOccurrence("a", "c")
	m.AddCoOccurrence("b", "d")
	cands := icf.Recommend([]string{"a", "b"}, m, 5)
	seen := map[string]bool{"a": true, "b": true}
	for _, c := range cands {
		if seen[c.ArticleID] {
			t.Fatalf("should not recommend seen article %s", c.ArticleID)
		}
	}
	// c 应排第一（共现更强，累加分更高）
	if len(cands) == 0 || cands[0].ArticleID != "c" {
		t.Fatalf("expected top recommendation c, got %v", cands)
	}
	if cands[0].Source != "cf" {
		t.Fatalf("expected source cf, got %s", cands[0].Source)
	}
}
